package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// collectContent joins every choices[0].delta.content across an SSE body.
func collectContent(body string) string {
	out := ""
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var j map[string]any
		if json.Unmarshal([]byte(line[6:]), &j) != nil {
			continue
		}
		ch, _ := j["choices"].([]any)
		if len(ch) == 0 {
			continue
		}
		c0, _ := ch[0].(map[string]any)
		d, _ := c0["delta"].(map[string]any)
		if s, ok := d["content"].(string); ok {
			out += s
		}
	}
	return out
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	for _, c := range chunks {
		d, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": c}}}})
		_, _ = w.Write([]byte("data: " + string(d) + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}
	// final chunk with finish_reason then DONE
	d, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": ""}, "finish_reason": "stop"}}})
	w.Write([]byte("data: " + string(d) + "\n\n"))
	w.Write([]byte("data: [DONE]\n\n"))
}

func TestProxyStripsThinkStream(t *testing.T) {
	var gotAcceptEnc, gotAuth, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEnc = r.Header.Get("Accept-Encoding")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		// reasoning split across several deltas, then the real answer
		sse(w, "<thi", "nk>secret ", "reasoning</thi", "nk>Hello", " world")
	}))
	defer up.Close()

	on := true
	cfg := Config{Upstream: up.URL, Port: 0, Rules: []Rule{
		{Type: "strip-pair", Open: "<think>", Close: "</think>", Enabled: &on},
		{Type: "inject-system", Text: "Be terse.", Position: "append", Enabled: &on},
	}}
	p := newProxy(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	p.ServeHTTP(rec, req)

	if got := collectContent(rec.Body.String()); got != "Hello world" {
		t.Errorf("stripped content = %q, want %q", got, "Hello world")
	}
	if gotAcceptEnc != "identity" {
		t.Errorf("Accept-Encoding forwarded = %q, want identity", gotAcceptEnc)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization not forwarded: %q", gotAuth)
	}
	if !strings.Contains(gotBody, "Be terse.") {
		t.Errorf("inject-system not applied to request body: %s", gotBody)
	}
}

func TestProxyPassesNonChatUntouched(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path forwarded = %q, want /models", r.URL.Path)
		}
		w.Write([]byte(`{"data":["<think>not stripped</think>"]}`))
	}))
	defer up.Close()

	on := true
	cfg := Config{Upstream: up.URL, Rules: []Rule{{Type: "strip-pair", Enabled: &on}}}
	rec := httptest.NewRecorder()
	newProxy(cfg).ServeHTTP(rec, httptest.NewRequest("GET", "/models", nil))
	if !strings.Contains(rec.Body.String(), "<think>not stripped</think>") {
		t.Errorf("non-chat path was modified: %s", rec.Body.String())
	}
}

func TestProxyNonStreamStrips(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"<think>x</think>done"}}]}`))
	}))
	defer up.Close()

	on := true
	cfg := Config{Upstream: up.URL, Rules: []Rule{{Type: "strip-pair", Open: "<think>", Close: "</think>", Enabled: &on}}}
	rec := httptest.NewRecorder()
	newProxy(cfg).ServeHTTP(rec, httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`)))
	if !strings.Contains(rec.Body.String(), `"content":"done"`) {
		t.Errorf("non-stream strip failed: %s", rec.Body.String())
	}
}
