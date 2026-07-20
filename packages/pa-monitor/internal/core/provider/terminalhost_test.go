package provider

import "testing"

func TestTerminalHost_CachesNonUnknown(t *testing.T) {
	c := New(nil)
	calls := 0
	c.FetchTerminalHost = func(int) string { calls++; return "tmux" }
	if got := c.TerminalHost("s", 42); got != "tmux" {
		t.Fatalf("first: got %q", got)
	}
	if got := c.TerminalHost("s", 42); got != "tmux" {
		t.Fatalf("cached: got %q", got)
	}
	if calls != 1 {
		t.Fatalf("non-unknown must cache: got %d fetches", calls)
	}
}

func TestTerminalHost_ReprobesUnknown(t *testing.T) {
	c := New(nil)
	seq := []string{"unknown", "cmux"}
	i := 0
	c.FetchTerminalHost = func(int) string { v := seq[i]; i++; return v }
	if got := c.TerminalHost("s", 42); got != "unknown" {
		t.Fatalf("first: got %q want unknown", got)
	}
	if got := c.TerminalHost("s", 42); got != "cmux" { // unknown re-probes
		t.Fatalf("re-probe: got %q want cmux", got)
	}
}

func TestTerminalHost_RecordsOnlyOnFetch(t *testing.T) {
	c := New(nil)
	fr := &fakeRec{}
	c.SetRecorder(fr)
	c.FetchTerminalHost = func(int) string { return "tmux" }
	c.TerminalHost("s", 42)
	c.TerminalHost("s", 42) // cache hit
	if n := countKind(fr, "terminal_host"); n != 1 {
		t.Fatalf("terminal_host should fire only on fetch: got %d", n)
	}
}

func TestTerminalHost_PidReuseNoCrossContamination(t *testing.T) {
	c := New(nil)
	c.FetchTerminalHost = func(int) string { return "tmux" }
	if got := c.TerminalHost("dead", 42); got != "tmux" {
		t.Fatalf("dead session: got %q", got)
	}
	// New session reuses pid 42 but detects cmux.
	c.FetchTerminalHost = func(int) string { return "cmux" }
	if got := c.TerminalHost("new", 42); got != "cmux" {
		t.Fatalf("new session (reused pid): got %q want cmux", got)
	}
	if got := c.bySession["dead"].terminalHost; got != "tmux" {
		t.Fatalf("dead session's cached host was contaminated: got %q", got)
	}
}

func TestTerminalHost_NilFetchUnknownNoMetric(t *testing.T) {
	c := New(nil)
	fr := &fakeRec{}
	c.SetRecorder(fr)
	if got := c.TerminalHost("s", 42); got != "unknown" {
		t.Fatalf("nil fetch: got %q want unknown", got)
	}
	if n := countKind(fr, "terminal_host"); n != 0 {
		t.Fatalf("nil fetch must record nothing: got %d", n)
	}
}
