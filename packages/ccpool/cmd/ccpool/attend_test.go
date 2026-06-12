package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/store"
)

func TestAttendCandidates(t *testing.T) {
	rows := []store.Session{
		{Name: "wait1", State: store.NeedsInput, TmuxSession: "cc-wait1"},
		{Name: "wait2", State: store.NeedsInput, TmuxSession: "cc-wait2"}, // dead
		{Name: "busy", State: store.Working, TmuxSession: "cc-busy"},
		{Name: "done1", State: store.Done, TmuxSession: "cc-done1"},
	}
	live := map[string]bool{"cc-wait1": true, "cc-wait2": false, "cc-busy": true, "cc-done1": true}
	liveFn := func(_, target string) bool { return live[target] }

	// needs_input only, dead one filtered out
	got := attendCandidates(rows, false, liveFn, "ccpool")
	if len(got) != 1 || got[0].Name != "wait1" {
		t.Fatalf("default: got %v, want [wait1]", names(got))
	}
	// --include-done adds live done rows
	got = attendCandidates(rows, true, liveFn, "ccpool")
	if len(got) != 2 || got[0].Name != "wait1" || got[1].Name != "done1" {
		t.Fatalf("include-done: got %v, want [wait1 done1]", names(got))
	}
}

func names(rows []store.Session) []string {
	var n []string
	for _, r := range rows {
		n = append(n, r.Name)
	}
	return n
}

// attendFixtures returns a small, deterministic set of NeedsInput candidates
// with the fields candidateLine renders (Name/State/CWD/LastActivityAt).
func attendFixtures() []store.Session {
	return []store.Session{
		{Name: "alpha", State: store.NeedsInput, CWD: "/tmp/alpha", LastActivityAt: 1_700_000_000},
		{Name: "bravo", State: store.NeedsInput, CWD: "/tmp/bravo", LastActivityAt: 1_700_000_100},
	}
}

// testPicker builds a picker driven entirely by in-memory fakes: closures for
// the predicates, a strings.Reader for stdin, and a bytes.Buffer sink for the
// listing/prompt output. pickFzfFn defaults to a stub that records invocation
// (callers that exercise the fzf branch override it). Returns the picker, its
// output buffer, and a pointer that reports whether the (default) fzf stub ran.
func testPicker(isTTY, hasFzf bool, stdin string) (picker, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return picker{
		isTerminal: func() bool { return isTTY },
		hasFzf:     func() bool { return hasFzf },
		pickFzfFn:  func(_ []store.Session) (string, bool) { return "", false },
		in:         strings.NewReader(stdin),
		out:        out,
	}, out
}

func TestPickCandidate_NoTTY_ListsAndReturnsFalse(t *testing.T) {
	cands := attendFixtures()
	p, out := testPicker(false, false, "")
	name, ok := p.pickCandidate(cands)
	if ok || name != "" {
		t.Fatalf("no-TTY: got (%q,%v), want (\"\",false)", name, ok)
	}
	s := out.String()
	if !strings.Contains(s, "no TTY to pick") {
		t.Errorf("no-TTY: listing header missing; out=%q", s)
	}
	for _, c := range cands {
		if !strings.Contains(s, c.Name) {
			t.Errorf("no-TTY: candidate %q not listed; out=%q", c.Name, s)
		}
	}
}

func TestPickCandidate_TTYWithFzf_InvokesFzfFn(t *testing.T) {
	cands := attendFixtures()
	p, out := testPicker(true, true, "")
	invoked := false
	p.pickFzfFn = func(got []store.Session) (string, bool) {
		invoked = true
		if len(got) != len(cands) {
			t.Errorf("fzf branch: got %d cands, want %d", len(got), len(cands))
		}
		return "STUB", true
	}
	name, ok := p.pickCandidate(cands)
	if !invoked {
		t.Fatalf("fzf branch: pickFzfFn was not invoked")
	}
	if name != "STUB" || !ok {
		t.Errorf("fzf branch: got (%q,%v), want (\"STUB\",true)", name, ok)
	}
	if strings.Contains(out.String(), "pick>") {
		t.Errorf("fzf branch: numbered prompt was written but should not be; out=%q", out.String())
	}
}

func TestPickCandidate_TTYNoFzf_SelectsNumberedBranch(t *testing.T) {
	cands := attendFixtures()
	p, out := testPicker(true, false, "1\n")
	name, ok := p.pickCandidate(cands)
	if name != cands[0].Name || !ok {
		t.Fatalf("numbered branch: got (%q,%v), want (%q,true)", name, ok, cands[0].Name)
	}
	if !strings.Contains(out.String(), "pick>") {
		t.Errorf("numbered branch: pick> prompt not written; out=%q", out.String())
	}
}

func TestPickNumbered_Parse(t *testing.T) {
	cands := attendFixtures() // 2 candidates: alpha, bravo
	cases := []struct {
		name     string
		stdin    string
		wantName string
		wantOK   bool
	}{
		{"valid first", "1\n", "alpha", true},
		{"valid last", "2\n", "bravo", true},
		{"zero below range", "0\n", "", false},
		{"over range", "3\n", "", false},
		{"negative", "-1\n", "", false},
		{"non-numeric", "abc\n", "", false},
		{"whitespace trimmed", " 2 \n", "bravo", true},
		{"eof empty", "", "", false},
		{"no trailing newline", "2", "bravo", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := testPicker(true, false, tc.stdin)
			name, ok := p.pickNumbered(cands)
			if name != tc.wantName || ok != tc.wantOK {
				t.Errorf("stdin=%q: got (%q,%v), want (%q,%v)",
					tc.stdin, name, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}
