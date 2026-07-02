package main

import (
	"cmp"
	"maps"
	"strings"
)

// Rule type identifiers — used for both config values and registry keys.
const (
	ruleStripPair    = "strip-pair"
	ruleReplace      = "replace"
	ruleInjectSystem = "inject-system"
	ruleSetParam     = "set-param"
)

// Stateful streaming content transform; Feed in order, End flushes held tail.
type Transducer interface {
	Feed(string) string
	End() string
}

// partialTail: length of the longest tag-prefix that is a suffix of s.
func partialTail(s, tag string) int {
	for k := min(len(tag)-1, len(s)); k > 0; k-- {
		if s[len(s)-k:] == tag[:k] {
			return k
		}
	}
	return 0
}

// stripPair removes open..close blocks (e.g. <think>..</think>), streaming-safe across chunks.
type stripPair struct {
	open, close string
	inThink     bool
	buf         string
}

func (t *stripPair) Feed(s string) string {
	t.buf += s
	out := ""
	for {
		if !t.inThink {
			i := strings.Index(t.buf, t.open)
			if i < 0 {
				keep := partialTail(t.buf, t.open)
				out += t.buf[:len(t.buf)-keep]
				t.buf = t.buf[len(t.buf)-keep:]
				return out
			}
			out += t.buf[:i]
			t.buf = t.buf[i+len(t.open):]
			t.inThink = true
		} else {
			j := strings.Index(t.buf, t.close)
			if j < 0 {
				keep := partialTail(t.buf, t.close)
				t.buf = t.buf[len(t.buf)-keep:] // drop think text, hold partial-close tail
				return out
			}
			t.buf = t.buf[j+len(t.close):]
			t.inThink = false
		}
	}
}

func (t *stripPair) End() string {
	if t.inThink {
		return "" // unterminated think block dropped
	}
	s := t.buf
	t.buf = ""
	return s
}

// replaceRule does literal find->replace on content, streaming-safe (holds partial-match tail).
type replaceRule struct {
	find, repl string
	buf        string
}

func (t *replaceRule) Feed(s string) string {
	t.buf += s
	out := ""
	for {
		i := strings.Index(t.buf, t.find)
		if i < 0 {
			keep := partialTail(t.buf, t.find)
			out += t.buf[:len(t.buf)-keep]
			t.buf = t.buf[len(t.buf)-keep:]
			return out
		}
		out += t.buf[:i] + t.repl
		t.buf = t.buf[i+len(t.find):]
	}
}

func (t *replaceRule) End() string { s := t.buf; t.buf = ""; return s }

func chainFeed(ts []Transducer, s string) string {
	for _, t := range ts {
		s = t.Feed(s)
	}
	return s
}

// chainEnd flushes each transducer's tail through downstream ones so composition holds at EOF.
func chainEnd(ts []Transducer) string {
	var out strings.Builder
	for i := range ts {
		left := ts[i].End()
		for j := i + 1; j < len(ts); j++ {
			left = ts[j].Feed(left)
		}
		out.WriteString(left)
	}
	return out.String()
}

func ruleEnabled(r Rule) bool { return r.Enabled == nil || *r.Enabled }

// --- rule registry --- each type registers one builder; adding a rule = one register() call.

type builtRule struct {
	// exactly one is non-nil
	newTransducer func() Transducer    // response rule
	applyRequest  func(map[string]any) // request rule (mutates the request JSON)
}

// ruleBuilder compiles a Rule; ok=false when inert (e.g. replace with empty find).
type ruleBuilder func(Rule) (b builtRule, ok bool)

var registry = map[string]ruleBuilder{}

func register(name string, b ruleBuilder) { registry[name] = b }

func init() {
	register(ruleStripPair, func(r Rule) (builtRule, bool) {
		open, close := cmp.Or(r.Open, "<think>"), cmp.Or(r.Close, "</think>")
		return builtRule{newTransducer: func() Transducer {
			return &stripPair{open: open, close: close}
		}}, true
	})
	register(ruleReplace, func(r Rule) (builtRule, bool) {
		if r.Find == "" {
			return builtRule{}, false
		}
		find, repl := r.Find, r.Replace
		return builtRule{newTransducer: func() Transducer {
			return &replaceRule{find: find, repl: repl}
		}}, true
	})
	register(ruleInjectSystem, func(r Rule) (builtRule, bool) {
		if r.Text == "" {
			return builtRule{}, false
		}
		text, pos := r.Text, r.Position
		return builtRule{applyRequest: func(m map[string]any) {
			msgs, _ := m["messages"].([]any)
			sys := map[string]any{"role": "system", "content": text}
			if pos == "append" {
				msgs = append(msgs, sys)
			} else {
				msgs = append([]any{sys}, msgs...)
			}
			m["messages"] = msgs
		}}, true
	})
	register(ruleSetParam, func(r Rule) (builtRule, bool) {
		if len(r.Params) == 0 {
			return builtRule{}, false
		}
		params := r.Params
		return builtRule{applyRequest: func(m map[string]any) {
			maps.Copy(m, params)
		}}, true
	})
}

// --- compiled rule set --- built once from Config; proxy never touches rule types again.

type ruleSet struct {
	responses []func() Transducer
	requests  []func(map[string]any)
}

func compile(cfg Config) ruleSet {
	var rs ruleSet
	for _, r := range cfg.Rules {
		if !ruleEnabled(r) {
			continue
		}
		build, known := registry[r.Type]
		if !known {
			continue // unknown type — fail-open skip
		}
		b, ok := build(r)
		if !ok {
			continue
		}
		if b.newTransducer != nil {
			rs.responses = append(rs.responses, b.newTransducer)
		}
		if b.applyRequest != nil {
			rs.requests = append(rs.requests, b.applyRequest)
		}
	}
	return rs
}

func (rs ruleSet) hasResponse() bool { return len(rs.responses) > 0 }
func (rs ruleSet) hasRequest() bool  { return len(rs.requests) > 0 }

// newTransducers builds fresh transducer instances for one response.
func (rs ruleSet) newTransducers() []Transducer {
	ts := make([]Transducer, len(rs.responses))
	for i, f := range rs.responses {
		ts[i] = f()
	}
	return ts
}

// applyRequest runs every request rule over the parsed request JSON in order.
func (rs ruleSet) applyRequest(m map[string]any) {
	for _, f := range rs.requests {
		f(m)
	}
}
