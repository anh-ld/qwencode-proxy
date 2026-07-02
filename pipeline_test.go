package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// sseLines builds an SSE body; the last content is tagged with finish_reason.
func sseLines(contents ...string) string {
	var b strings.Builder
	for i, c := range contents {
		choice := map[string]any{"delta": map[string]any{"content": c}}
		if i == len(contents)-1 {
			choice["finish_reason"] = "stop"
		}
		j, _ := json.Marshal(map[string]any{"choices": []any{choice}})
		b.WriteString("data: " + string(j) + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// Candidate B's payoff: the pipeline is tested reader->writer, no HTTP server.
func TestPipelineStreamNoHTTP(t *testing.T) {
	on := true
	p := pipeline{rules: compile(Config{Rules: []Rule{
		{Type: ruleStripPair, Open: "<think>", Close: "</think>", Enabled: &on},
	}})}
	var out bytes.Buffer
	p.Stream(&out, strings.NewReader(sseLines("<think>secret", " reasoning</think>Hello", " world")), func() {})
	if got := collectContent(out.String()); got != "Hello world" {
		t.Errorf("stream strip = %q, want %q", got, "Hello world")
	}
}

// #6 regression: when a held partial-tag tail is outstanding and finish_reason
// arrives in an EMPTY delta, the tail must ride out on the finish chunk itself,
// not a delta emitted after it (which finish-on-first clients would drop).
func TestPipelineFinishInEmptyDeltaCarriesTail(t *testing.T) {
	on := true
	p := pipeline{rules: compile(Config{Rules: []Rule{
		{Type: ruleStripPair, Open: "<think>", Close: "</think>", Enabled: &on},
	}})}
	body := `data: {"choices":[{"delta":{"content":"Hi<thi"}}]}` + "\n\n" + // "<thi" held as partial open
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" + // finish in empty delta
		"data: [DONE]\n\n"
	var out bytes.Buffer
	p.Stream(&out, strings.NewReader(body), func() {})

	var finishContent string
	for ln := range strings.SplitSeq(out.String(), "\n") {
		if !strings.HasPrefix(ln, "data: ") || !strings.Contains(ln, "finish_reason") {
			continue
		}
		var j map[string]any
		_ = json.Unmarshal([]byte(ln[6:]), &j)
		d := j["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		finishContent, _ = d["content"].(string)
	}
	if finishContent != "<thi" {
		t.Errorf("finish chunk should carry held tail; content = %q, want %q", finishContent, "<thi")
	}
}

func TestPipelineApplyRequest(t *testing.T) {
	on := true
	p := pipeline{rules: compile(Config{Rules: []Rule{
		{Type: ruleInjectSystem, Text: "Be terse.", Position: "append", Enabled: &on},
		{Type: ruleSetParam, Params: map[string]any{"temperature": 0.0}, Enabled: &on},
	}})}
	out := string(p.ApplyRequest([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
	if !strings.Contains(out, "Be terse.") {
		t.Errorf("inject-system missing: %s", out)
	}
	if !strings.Contains(out, `"temperature":0`) {
		t.Errorf("set-param missing: %s", out)
	}
}

// Candidate A's payoff: compile drives everything off the registry — unknown
// types and inert configs drop out with no scattered switch to maintain.
func TestCompileSkipsUnknownAndInert(t *testing.T) {
	on := true
	rs := compile(Config{Rules: []Rule{
		{Type: "nonexistent", Enabled: &on},      // unknown → skipped
		{Type: ruleReplace, Enabled: &on},        // empty find → inert
		{Type: ruleStripPair, Enabled: &on},      // real
		{Type: ruleSetParam, Enabled: new(bool)}, // disabled (enabled=false)
	}})
	if rs.hasRequest() {
		t.Error("expected no request rules")
	}
	if len(rs.responses) != 1 {
		t.Errorf("expected 1 response rule (strip-pair), got %d", len(rs.responses))
	}
}
