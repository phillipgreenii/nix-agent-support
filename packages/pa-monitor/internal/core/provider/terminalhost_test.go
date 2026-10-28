package provider

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

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
	now := time.Unix(1_000_000, 0)
	c := New(func() time.Time { return now })
	seq := []string{"unknown", "cmux"}
	i := 0
	c.FetchTerminalHost = func(int) string { v := seq[i]; i++; return v }
	if got := c.TerminalHost("s", 42); got != "unknown" {
		t.Fatalf("first: got %q want unknown", got)
	}
	now = now.Add(UnknownProbeMin)                     // the first backoff window (5s) has elapsed
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

// unknownProbeFixture drives the unknown-host backoff with an injected clock.
// fetches records the clock time of every real probe.
type unknownProbeFixture struct {
	c       *Cache
	now     time.Time
	fetches []time.Time
	host    string // what the next probe returns
	ppid    int
}

func newUnknownProbeFixture() *unknownProbeFixture {
	f := &unknownProbeFixture{now: time.Unix(1_000_000, 0), host: "unknown", ppid: 100}
	f.c = New(func() time.Time { return f.now })
	f.c.FetchTerminalHost = func(int) string {
		f.fetches = append(f.fetches, f.now)
		return f.host
	}
	f.c.FetchParentPID = func(int) (int, bool) { return f.ppid, true }
	return f
}

func (f *unknownProbeFixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// TestTerminalHost_UnknownBackoffSchedule: an "unknown" host is re-probed at
// 5s, 10s, 20s, 40s then a 60s ceiling (intervals between probes), not every
// call. Polled every second so the schedule, not the poll cadence, drives it.
func TestTerminalHost_UnknownBackoffSchedule(t *testing.T) {
	f := newUnknownProbeFixture()
	start := f.now
	for range 200 {
		if got := f.c.TerminalHost("s", 42); got != "unknown" {
			t.Fatalf("got %q, want unknown", got)
		}
		f.advance(time.Second)
	}
	want := []time.Duration{0, 5 * time.Second, 15 * time.Second, 35 * time.Second, 75 * time.Second, 135 * time.Second, 195 * time.Second}
	if len(f.fetches) != len(want) {
		t.Fatalf("probes = %d (%v), want %d at offsets %v", len(f.fetches), offsets(start, f.fetches), len(want), want)
	}
	for i, at := range f.fetches {
		if got := at.Sub(start); got != want[i] {
			t.Errorf("probe %d at +%v, want +%v (all: %v)", i, got, want[i], offsets(start, f.fetches))
		}
	}
	// The ceiling is the documented 60s: the last gaps must not exceed it.
	for i := 1; i < len(f.fetches); i++ {
		if gap := f.fetches[i].Sub(f.fetches[i-1]); gap > UnknownProbeMax {
			t.Errorf("gap %v between probes %d and %d exceeds the %v ceiling", gap, i-1, i, UnknownProbeMax)
		}
	}
}

func offsets(start time.Time, ts []time.Time) []time.Duration {
	out := make([]time.Duration, len(ts))
	for i, at := range ts {
		out[i] = at.Sub(start)
	}
	return out
}

func TestTerminalHost_UnknownBackoffResetsWhenParentChanges(t *testing.T) {
	f := newUnknownProbeFixture()
	f.c.TerminalHost("s", 42) // probe 1 at +0
	f.advance(5 * time.Second)
	f.c.TerminalHost("s", 42) // probe 2 at +5 (next due +10 later)
	f.advance(time.Second)
	f.c.TerminalHost("s", 42) // backed off: no probe
	if len(f.fetches) != 2 {
		t.Fatalf("setup: probes = %d, want 2", len(f.fetches))
	}
	f.ppid = 101 // the pid was re-parented (e.g. moved under a terminal host)
	f.host = "cmux"
	if got := f.c.TerminalHost("s", 42); got != "cmux" {
		t.Fatalf("after parent change got %q, want an immediate re-probe returning cmux", got)
	}
	if len(f.fetches) != 3 {
		t.Fatalf("probes = %d, want 3 (parent change must bypass the backoff window)", len(f.fetches))
	}
}

func TestTerminalHost_UnknownBackoffResetsToMinimumAfterParentChange(t *testing.T) {
	f := newUnknownProbeFixture()
	f.c.TerminalHost("s", 42) // probe at +0
	f.advance(5 * time.Second)
	f.c.TerminalHost("s", 42) // probe at +5
	f.advance(10 * time.Second)
	f.c.TerminalHost("s", 42) // probe at +15: the window is now 40s
	f.advance(time.Second)
	f.ppid = 101
	f.c.TerminalHost("s", 42) // +16: parent changed, re-probe, window back to 5s
	n := len(f.fetches)
	f.advance(4 * time.Second)
	f.c.TerminalHost("s", 42) // +20: inside the reset 5s window
	if len(f.fetches) != n {
		t.Fatalf("probed again after 4s; want none inside the reset 5s window")
	}
	f.advance(time.Second)
	f.c.TerminalHost("s", 42) // +21: the 5s window elapsed
	if len(f.fetches) != n+1 {
		t.Fatalf("probes = %d, want %d (a 5s window after the reset, not the previous 40s)", len(f.fetches), n+1)
	}
}

func TestTerminalHost_UnknownBackoffResetsWhenSessionReappears(t *testing.T) {
	f := newUnknownProbeFixture()
	sess := &session.Session{SessionID: "s", PID: 42}
	f.c.TerminalHost("s", 42)
	f.advance(5 * time.Second)
	f.c.TerminalHost("s", 42) // probe 2; now inside a 10s window
	f.c.Reconcile(nil)        // session vanished: node evicted
	f.c.Reconcile([]*session.Session{sess})
	f.c.TerminalHost("s", 42) // reappears: fresh node, immediate probe
	if len(f.fetches) != 3 {
		t.Fatalf("probes = %d, want 3 (reappearing session probes immediately)", len(f.fetches))
	}
}

func TestTerminalHost_UnknownBackoffResetsWhenPIDChanges(t *testing.T) {
	f := newUnknownProbeFixture()
	f.c.TerminalHost("s", 42)
	f.c.TerminalHost("s", 43) // same session id, new pid: do not honour 42's window
	if len(f.fetches) != 2 {
		t.Fatalf("probes = %d, want 2", len(f.fetches))
	}
}

func TestTerminalHost_KnownHostStillNeverReprobedOrParentChecked(t *testing.T) {
	f := newUnknownProbeFixture()
	parentChecks := 0
	f.c.FetchParentPID = func(int) (int, bool) { parentChecks++; return f.ppid, true }
	f.host = "cmux"
	for range 10 {
		f.c.TerminalHost("s", 42)
		f.advance(5 * time.Second)
	}
	if len(f.fetches) != 1 || parentChecks > 1 {
		t.Fatalf("probes=%d parentChecks=%d, want 1 probe and no parent checks on a cached host", len(f.fetches), parentChecks)
	}
}

// TestTerminalHost_FailedProbeNeverStuck: a failed probe (the fetch boundary
// returns "" or "unknown") is never cached as a host, and once the probe can
// succeed it is picked up within the 60s ceiling, however long it had failed.
func TestTerminalHost_FailedProbeNeverStuck(t *testing.T) {
	for _, failure := range []string{"", "unknown"} {
		t.Run("failure="+failure, func(t *testing.T) {
			f := newUnknownProbeFixture()
			f.host = failure
			for range 100 { // 500s of failing probes at the 5s tick: backoff saturates
				if got := f.c.TerminalHost("s", 42); got != "unknown" {
					t.Fatalf("failed probe surfaced %q, want unknown", got)
				}
				f.advance(5 * time.Second)
			}
			if h := f.c.bySession["s"].terminalHost; h != "" && h != "unknown" {
				t.Fatalf("a failed probe cached %q as the host", h)
			}
			f.host = "tmux" // the cause clears
			recovered := time.Duration(-1)
			recoverStart := f.now
			for range 30 { // at most 150s at the 5s tick
				if f.c.TerminalHost("s", 42) == "tmux" {
					recovered = f.now.Sub(recoverStart)
					break
				}
				f.advance(5 * time.Second)
			}
			if recovered < 0 || recovered > UnknownProbeMax {
				t.Fatalf("recovered after %v, want within the %v ceiling", recovered, UnknownProbeMax)
			}
			n := len(f.fetches)
			f.advance(10 * time.Minute)
			if f.c.TerminalHost("s", 42) != "tmux" || len(f.fetches) != n {
				t.Fatal("a successful probe must be cached for the session's life")
			}
		})
	}
}
