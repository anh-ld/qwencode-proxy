package main

import "testing"

// feed drives chunks through a fresh transducer chain, joining Feed outputs + End.
func feedChain(newTs func() []Transducer, chunks []string) string {
	ts := newTs()
	out := ""
	for _, c := range chunks {
		out += chainFeed(ts, c)
	}
	return out + chainEnd(ts)
}

func TestStripPair(t *testing.T) {
	sp := func() []Transducer { return []Transducer{&stripPair{open: "<think>", close: "</think>"}} }
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"single chunk", []string{"<think>hidden</think>answer"}, "answer"},
		{"tags split across chunks", []string{"<thi", "nk>hid", "den</thi", "nk>vis"}, "vis"},
		{"text around block", []string{"before<think>x</think>after"}, "beforeafter"},
		{"passthrough", []string{"no tags here"}, "no tags here"},
		{"think spans many chunks", []string{"a<think>b", "c", "d</think>e"}, "ae"},
		{"unterminated think dropped", []string{"<think>only thinking, cut"}, ""},
		{"trailing partial-open flushed", []string{"hello<thi"}, "hello<thi"},
	}
	for _, c := range cases {
		if got := feedChain(sp, c.chunks); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestReplace(t *testing.T) {
	rp := func() []Transducer { return []Transducer{&replaceRule{find: "foo", repl: "bar"}} }
	cases := []struct {
		chunks []string
		want   string
	}{
		{[]string{"a foo b"}, "a bar b"},
		{[]string{"a fo", "o b"}, "a bar b"},   // match split across chunks
		{[]string{"foofoo"}, "barbar"},         // adjacent matches
		{[]string{"no match"}, "no match"},     // passthrough
		{[]string{"ends in fo"}, "ends in fo"}, // partial tail flushed at End
	}
	for _, c := range cases {
		if got := feedChain(rp, c.chunks); got != c.want {
			t.Errorf("%v: got %q want %q", c.chunks, got, c.want)
		}
	}
}

func TestChainOrder(t *testing.T) {
	// strip-pair then replace, composed over one stream
	newTs := func() []Transducer {
		return []Transducer{
			&stripPair{open: "<think>", close: "</think>"},
			&replaceRule{find: "world", repl: "there"},
		}
	}
	got := feedChain(newTs, []string{"<think>plan</think>hello world"})
	if got != "hello there" {
		t.Errorf("chain: got %q want %q", got, "hello there")
	}
}
