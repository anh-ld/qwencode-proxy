package main

import (
	"bytes"
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
}

func newProxy(cfg Config) *proxy {
	return &proxy{
		upstream: cfg.Upstream,
		token:    cfg.Token,
		pipe:     pipeline{rules: compile(cfg)},
		client:   &http.Client{Transport: &http.Transport{DisableCompression: true}},
	}
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

	up, err := url.Parse(p.upstream)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	target := *up
	target.Path = strings.TrimRight(up.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	isChat := strings.HasSuffix(r.URL.Path, "/chat/completions")
	var bodyReader io.Reader = r.Body // stream through untouched unless a request rule applies
	if isChat && p.pipe.hasRequest() {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body: "+err.Error(), http.StatusBadGateway)
			return
		}
		bodyReader = bytes.NewReader(p.pipe.ApplyRequest(raw))
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bodyReader)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadGateway)
		return
	}
	copyHeaders(req.Header, r.Header, "Accept-Encoding", "Content-Length", "Host")
	req.Header.Set("Accept-Encoding", "identity") // force plain so content rules can match
	req.Host = up.Host

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

	switch {
	case !isChat || !p.pipe.hasResponse():
		_, _ = io.Copy(w, resp.Body)
		flush()
	case strings.Contains(resp.Header.Get("Content-Type"), "event-stream"):
		p.pipe.Stream(w, resp.Body, flush)
	default:
		raw, _ := io.ReadAll(resp.Body)
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
	srv := &http.Server{Handler: newProxy(cfg), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }, nil
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
