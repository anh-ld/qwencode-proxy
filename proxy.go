package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const healthPath = "/__qphealth"
const healthMarker = "qwencode-proxy"

// proxy is pure transport; hands chat bodies to the pipeline, owns no rule logic.
type proxy struct {
	upstream string
	token    string
	pipe     pipeline
	client   *http.Client
	up       *url.URL
	baseRaw  string  // upstream path with trailing slashes trimmed
	dump     *dumper // nil unless dumping is enabled
}

func newProxy(cfg Config) *proxy {
	up, err := url.Parse(cfg.Upstream)
	if err != nil {
		fatalf("bad upstream %q: %v", cfg.Upstream, err)
	}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext:        idleConnDialer(streamIdleTimeout),
	}
	return &proxy{
		upstream: cfg.Upstream,
		token:    cfg.Token,
		pipe:     pipeline{rules: compile(cfg)},
		client:   &http.Client{Transport: transport},
		up:       up,
		baseRaw:  strings.TrimRight(up.Path, "/"),
		dump:     newDumper(cfg),
	}
}

// streamIdleTimeout caps the gap between upstream bytes; tune from the field.
const streamIdleTimeout = 30 * time.Second

// idleConnDialer wraps the default dialer so each returned conn aborts a read after `idle` of silence.
func idleConnDialer(idle time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dial := (&net.Dialer{}).DialContext
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &idleConn{Conn: c, idle: idle}, nil
	}
}

type idleConn struct {
	net.Conn
	idle time.Duration
}

// Read slides the idle deadline forward before each read, so the stream lives as long as bytes keep flowing.
func (c *idleConn) Read(b []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(b)
}

// healthValue: per-instance token (or marker) proving the listener is ours.
func healthValue(token string) string {
	if token != "" {
		return token
	}
	return healthMarker
}

func proxyAddr(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
	"Te": true, "Trailer": true, "Upgrade": true, "Proxy-Connection": true,
}

// copyHeaders forwards src->dst, dropping hop-by-hop headers and named skips.
func copyHeaders(dst, src http.Header, skip ...string) {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[s] = true
	}
	for k, vs := range src {
		if hopHeaders[k] || skipSet[k] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == healthPath {
		_, _ = fmt.Fprint(w, healthValue(p.token))
		return
	}

	target := *p.up
	base := p.baseRaw
	if (r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/")) && strings.HasSuffix(p.baseRaw, "/v1") {
		base = strings.TrimSuffix(p.baseRaw, "/v1")
	}
	target.Path = base + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	isChat := strings.HasSuffix(r.URL.Path, "/chat/completions")
	var bodyReader io.Reader = r.Body // stream through untouched unless a request rule or dump needs the bytes
	if isChat && (p.pipe.hasRequest() || p.dump != nil) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body: "+err.Error(), http.StatusBadGateway)
			return
		}
		if p.pipe.hasRequest() {
			raw = p.pipe.ApplyRequest(raw)
		}
		dumpURL := target
		dumpURL.RawQuery = ""                            // strip query — some providers put the api key there
		p.dump.section("REQUEST "+dumpURL.String(), raw) // no-op when dump is nil
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bodyReader)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadGateway)
		return
	}
	copyHeaders(req.Header, r.Header, "Accept-Encoding", "Content-Length", "Host")
	req.Header.Set("Accept-Encoding", "identity") // force plain so content rules can match
	req.Host = target.Host

	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copyHeaders(w.Header(), resp.Header, "Content-Length", "Content-Encoding")
	w.WriteHeader(resp.StatusCode)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	var respSrc io.Reader = resp.Body // raw upstream bytes, tee'd to the dump before any rule touches them
	if isChat && p.dump != nil {
		p.dump.header("RESPONSE " + resp.Status)
		respSrc = io.TeeReader(resp.Body, p.dump)
	}

	switch {
	case !isChat || !p.pipe.hasResponse():
		_, _ = io.Copy(w, respSrc)
		flush()
	case strings.Contains(resp.Header.Get("Content-Type"), "event-stream"):
		p.pipe.Stream(w, respSrc, flush)
	default:
		raw, _ := io.ReadAll(respSrc)
		_, _ = w.Write(p.pipe.Body(raw))
		flush()
	}
}

// ensureProxy starts the proxy unless ours is already listening; stop is a no-op on reuse.
func ensureProxy(cfg Config) (stop func(), err error) {
	addr := proxyAddr(cfg.Port)
	switch probe(cfg) {
	case probeOurs:
		return func() {}, nil // reuse — concurrency / crash-leftover
	case probeOther:
		return nil, fmt.Errorf("port %d is in use by another process — set a different port in %s", cfg.Port, configPath())
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	px := newProxy(cfg)
	srv := &http.Server{Handler: px, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close(); px.dump.close() }, nil
}

type probeResult int

const (
	probeFree probeResult = iota
	probeOurs
	probeOther
)

func probe(cfg Config) probeResult {
	addr := proxyAddr(cfg.Port)
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return probeFree
	}
	_ = c.Close()
	resp, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + healthPath)
	if err != nil {
		return probeOther
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(b)) == healthValue(cfg.Token) { // exact token match — a squatter can't forge it
		return probeOurs
	}
	return probeOther
}
