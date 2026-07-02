package main

import (
	"bytes"
	"encoding/json"
	"io"
)

// pipeline transforms chat traffic via a ruleSet; HTTP-free, tested reader->writer.
type pipeline struct {
	rules ruleSet
}

func (p pipeline) hasRequest() bool  { return p.rules.hasRequest() }
func (p pipeline) hasResponse() bool { return p.rules.hasResponse() }

// ApplyRequest runs request rules over a chat body; fail-open on parse error.
func (p pipeline) ApplyRequest(body []byte) []byte {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	p.rules.applyRequest(m)
	nb, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return nb
}

// Stream runs each SSE data line's content through response rules, flushing as it goes.
func (p pipeline) Stream(dst io.Writer, src io.Reader, flush func()) {
	proc := &respProc{ts: p.rules.newTransducers()}
	var buf []byte
	tmp := make([]byte, 8192)
	for {
		n, err := src.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				i := bytes.IndexByte(buf, '\n')
				if i < 0 {
					break
				}
				line := buf[:i+1]
				buf = buf[i+1:]
				_, _ = dst.Write(proc.line(line))
				flush()
			}
		}
		if err != nil {
			break
		}
	}
	if len(buf) > 0 {
		_, _ = dst.Write(proc.line(buf))
	}
	if tail := proc.finalFlush(); len(tail) > 0 {
		_, _ = dst.Write(tail)
	}
	flush()
}

// Body transforms a non-streamed chat response (message.content per choice).
func (p pipeline) Body(body []byte) []byte {
	var j map[string]any
	if json.Unmarshal(body, &j) != nil {
		return body
	}
	changed := false
	for _, c := range asSlice(j["choices"]) {
		msg := asMap(asMap(c)["message"])
		ts := p.rules.newTransducers()
		if mapContent(msg, func(s string) string { return chainFeed(ts, s) + chainEnd(ts) }) {
			changed = true
		}
	}
	if !changed {
		return body
	}
	nb, err := json.Marshal(j)
	if err != nil {
		return body
	}
	return nb
}

// respProc rewrites content across one SSE response, holding shared transducer state.
type respProc struct {
	ts   []Transducer
	done bool
}

func (p *respProc) line(line []byte) []byte {
	trimmed := bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return line
	}
	payload := bytes.TrimPrefix(bytes.TrimPrefix(trimmed, []byte("data:")), []byte(" ")) // optional single space
	if bytes.Equal(payload, []byte("[DONE]")) {
		if tail := p.finalFlush(); len(tail) > 0 {
			return append(tail, line...)
		}
		return line
	}
	var j map[string]any
	if json.Unmarshal(payload, &j) != nil {
		return line
	}
	choices := asSlice(j["choices"])
	if len(choices) == 0 {
		return line
	}
	c0 := asMap(choices[0])
	delta := asMap(c0["delta"])
	if delta == nil {
		return line
	}
	content, hasContent := delta["content"].(string)
	finish := c0["finish_reason"] != nil
	if !hasContent && !finish {
		return line // no content (e.g. tool_calls / role-only) — pass through
	}
	out := ""
	if hasContent {
		out = chainFeed(p.ts, content)
	}
	if finish && !p.done {
		out += chainEnd(p.ts) // flush any held tail INTO the finishing chunk, even if its delta was empty
		p.done = true
	}
	if !hasContent && out == "" {
		return line // empty finish with nothing held — leave untouched
	}
	delta["content"] = out
	nb, err := json.Marshal(j)
	if err != nil {
		return line
	}
	return []byte("data: " + string(nb) + "\n")
}

// finalFlush emits leftover tail as a synthetic delta when no finish_reason flushed it.
func (p *respProc) finalFlush() []byte {
	if p.done {
		return nil
	}
	p.done = true
	tail := chainEnd(p.ts)
	if tail == "" {
		return nil
	}
	j := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": tail}}}}
	b, _ := json.Marshal(j)
	return []byte("data: " + string(b) + "\n\n")
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asSlice(v any) []any        { s, _ := v.([]any); return s }

// mapContent applies fn to holder["content"] when a string; returns true if found.
func mapContent(holder map[string]any, fn func(string) string) bool {
	if holder == nil {
		return false
	}
	if s, ok := holder["content"].(string); ok {
		holder["content"] = fn(s)
		return true
	}
	return false
}
