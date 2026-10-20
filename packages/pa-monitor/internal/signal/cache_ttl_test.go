package signal_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// fakeClock is a controllable clock for the TTL-cache seam; advancing it
// crosses a TTL without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// psCallCounter wraps a RunCmd and counts invocations by kind: the whole-table
// `ps -A ...` enumerations versus every other ps (the uncached ancestry walks).
type psCallCounter struct {
	inner      func(context.Context, string, ...string) ([]byte, error)
	psAll      int
	psOther    int
	nonPsCalls int
}

func (p *psCallCounter) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch {
	case name == "ps" && len(args) >= 1 && args[0] == "-A":
		p.psAll++
	case name == "ps":
		p.psOther++
	default:
		p.nonPsCalls++
	}
	return p.inner(ctx, name, args...)
}

func (p *psCallCounter) total() int { return p.psAll + p.psOther + p.nonPsCalls }

// TestTTLConstantsStaySplit guards against re-merging the two TTLs: the
// surface map must stay short (a stale map drops a nudge for a newly created
// surface), while both whole-table ps enumerations use the long TTL.
func TestTTLConstantsStaySplit(t *testing.T) {
	surface, serverPID, tmux := signal.TTLs()
	if surface != 2*time.Second {
		t.Errorf("surface cache TTL = %v, want 2s (stale surface map drops nudges)", surface)
	}
	if serverPID != 10*time.Second {
		t.Errorf("server-PID cache TTL = %v, want 10s", serverPID)
	}
	if tmux != 10*time.Second {
		t.Errorf("tmux pane cache TTL = %v, want 10s", tmux)
	}
	if surface >= serverPID {
		t.Errorf("surface TTL %v must be shorter than server-PID TTL %v", surface, serverPID)
	}
}

func TestCmuxServerPIDCacheOnePsPerTTLWindow(t *testing.T) {
	procs := []fakeProc{{pid: 5000, comm: "cmux", parent: 1}}
	counter := &psCallCounter{inner: fakePsRun(procs)}
	clock := newFakeClock()
	sig := &signal.CmuxSignaler{RunCmd: counter.run}
	signal.SetClockForTest(sig, clock.Now)

	for i := range 5 {
		pids, err := signal.CachedCmuxServerPIDsForTest(sig)
		if err != nil || len(pids) != 1 || pids[0] != 5000 {
			t.Fatalf("call #%d: pids=%v err=%v", i, pids, err)
		}
		clock.Advance(time.Second) // 5 calls span 0..4s, inside the 10s TTL
	}
	if counter.psAll != 1 {
		t.Fatalf("ps -A ran %d times within the TTL window; want 1", counter.psAll)
	}

	// Still inside the window just before the TTL elapses...
	clock.Advance(4 * time.Second) // loop left the clock at 5s; now 9s after the first fill
	if _, err := signal.CachedCmuxServerPIDsForTest(sig); err != nil {
		t.Fatal(err)
	}
	if counter.psAll != 1 {
		t.Fatalf("ps -A ran %d times at 9s; want still 1", counter.psAll)
	}
	// ...and one more after it elapses.
	clock.Advance(2 * time.Second) // 11s after the first fill
	if _, err := signal.CachedCmuxServerPIDsForTest(sig); err != nil {
		t.Fatal(err)
	}
	if counter.psAll != 2 {
		t.Fatalf("ps -A ran %d times after the TTL; want 2", counter.psAll)
	}
}

func TestCmuxFindServerAncestorReusesServerPIDCache(t *testing.T) {
	// FindCmuxServerAncestor issues uncached `ps -o ppid=` walks; only the -A
	// call is cached, so count ONLY -A.
	procs := []fakeProc{
		{pid: 5000, comm: "cmux", parent: 1},
		{pid: 5001, comm: "zsh", parent: 5000},
		{pid: 5002, comm: "claude", parent: 5001},
	}
	counter := &psCallCounter{inner: fakePsRun(procs)}
	clock := newFakeClock()
	sig := &signal.CmuxSignaler{RunCmd: counter.run}
	signal.SetClockForTest(sig, clock.Now)

	for range 4 {
		if _, ok := sig.FindCmuxServerAncestor(5002); !ok {
			t.Fatal("FindCmuxServerAncestor(5002) = false, want true")
		}
		clock.Advance(3 * time.Second)
	}
	// Calls at t=0,3,6,9 -> one fill; the walks themselves are not cached.
	if counter.psAll != 1 {
		t.Errorf("ps -A ran %d times across 9s; want 1", counter.psAll)
	}
	if counter.psOther == 0 {
		t.Error("expected the uncached ancestry walk to still run (out of scope for this cache)")
	}
}

func TestTmuxPaneCacheOnePsPerTTLWindow(t *testing.T) {
	tree := map[int][2]string{}
	panes := map[string]string{"alt": "100 mayor:0.0\n"}
	counter := &psCallCounter{inner: fakeMultiSocketRun(psSampleSingleServer, tree, panes, nil)}
	clock := newFakeClock()
	sig := &signal.TmuxSignaler{RunCmd: counter.run}
	signal.SetClockForTest(sig, clock.Now)

	for i := range 5 {
		if _, err := signal.CachedPanesForTest(sig); err != nil {
			t.Fatalf("call #%d: %v", i, err)
		}
		clock.Advance(2 * time.Second) // 0..8s, inside the 10s TTL
	}
	if counter.psAll != 1 {
		t.Fatalf("ps -A ran %d times within the TTL window; want 1", counter.psAll)
	}
	clock.Advance(3 * time.Second) // 11s after the first fill
	if _, err := signal.CachedPanesForTest(sig); err != nil {
		t.Fatal(err)
	}
	if counter.psAll != 2 {
		t.Fatalf("ps -A ran %d times after the TTL; want 2", counter.psAll)
	}
}

// TestTmuxGateNoProcessesWhenTmuxAbsent: with tmux not resolvable on PATH the
// signaler makes ZERO invocations through RunCmd (no ps -A, no ancestor walk,
// no tmux), and Detect is false. This must hold for Send and the input guard
// too (the five DefaultSignalers() call sites reach those).
func TestTmuxGateNoProcessesWhenTmuxAbsent(t *testing.T) {
	tree := map[int][2]string{
		67890: {"12345", "claude"},
		12345: {"100", "zsh"},
	}
	panes := map[string]string{"alt": "100 mayor:0.0\n"}
	counter := &psCallCounter{inner: fakeMultiSocketRun(psSampleSingleServer, tree, panes, nil)}
	lookups := 0
	sig := &signal.TmuxSignaler{
		RunCmd: counter.run,
		LookPath: func(file string) (string, error) {
			lookups++
			if file != "tmux" {
				t.Errorf("LookPath(%q); want \"tmux\"", file)
			}
			return "", errors.New("not found")
		},
	}

	if sig.Detect(67890) {
		t.Error("Detect = true with tmux absent; want false")
	}
	if err := sig.Send(67890, "continue"); err == nil {
		t.Error("Send succeeded with tmux absent; want an error")
	}
	if !sig.HasUnsubmittedInput(67890) {
		t.Error("HasUnsubmittedInput = false with tmux absent; want fail-closed true")
	}
	if counter.total() != 0 {
		t.Errorf("RunCmd invoked %d times with tmux absent (ps -A=%d, other ps=%d, other=%d); want 0",
			counter.total(), counter.psAll, counter.psOther, counter.nonPsCalls)
	}
	if lookups == 0 {
		t.Error("gate never consulted LookPath")
	}
}

// TestTmuxGateOpenWhenTmuxResolvable: with tmux resolvable, Detect and Send
// behave exactly as before the gate existed.
func TestTmuxGateOpenWhenTmuxResolvable(t *testing.T) {
	tree := map[int][2]string{
		67890: {"12345", "claude"},
		12345: {"100", "zsh"},
	}
	panes := map[string]string{"alt": "100 mayor:0.0\n"}
	var sent []string
	counter := &psCallCounter{inner: fakeMultiSocketRun(psSampleSingleServer, tree, panes, &sent)}
	sig := &signal.TmuxSignaler{
		RunCmd:   counter.run,
		LookPath: func(string) (string, error) { return "/usr/bin/tmux", nil },
	}
	if !sig.Detect(67890) {
		t.Fatal("Detect = false with tmux resolvable; want true")
	}
	if err := sig.Send(67890, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("send-keys calls = %d (%v); want 1", len(sent), sent)
	}
	if counter.psAll != 1 {
		t.Errorf("ps -A ran %d times; want 1 (cached across Detect+Send)", counter.psAll)
	}
}

// TestTmuxGateAbsentIsNotCached: the gate reads PATH on every call, so a tmux
// installed later is picked up without waiting out a TTL.
func TestTmuxGateAbsentIsNotCached(t *testing.T) {
	tree := map[int][2]string{100: {"1", "zsh"}}
	panes := map[string]string{"alt": "100 mayor:0.0\n"}
	counter := &psCallCounter{inner: fakeMultiSocketRun(psSampleSingleServer, tree, panes, nil)}
	present := false
	sig := &signal.TmuxSignaler{
		RunCmd: counter.run,
		LookPath: func(string) (string, error) {
			if present {
				return "/usr/bin/tmux", nil
			}
			return "", fmt.Errorf("not found")
		},
	}
	if sig.Detect(100) {
		t.Fatal("Detect true while tmux absent")
	}
	present = true
	if !sig.Detect(100) {
		t.Error("Detect false right after tmux became resolvable; the absent state must not be cached")
	}
}
