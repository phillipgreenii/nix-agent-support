package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/budget"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// The lease refresh and the bead read are decoupled from the 10 s wait poll: the
// lease is written every LeaseTTL / 4, and while the session row is active the
// bead is read at most every 30 s. All tests here run on fake clocks and an
// injected ticker; nothing sleeps.

// manualTicker is an injected lease ticker the test fires by hand. The channel
// is unbuffered, so a send returns once the refresh loop has taken the tick and
// the NEXT send (or stop) returns only after that tick's refresh finished.
type manualTicker struct {
	ch    chan time.Time
	ready chan struct{} // closed once the loop has built its ticker
	every time.Duration // the period the handler asked for (valid after ready)
}

func newManualTicker() *manualTicker {
	return &manualTicker{ch: make(chan time.Time), ready: make(chan struct{})}
}

func (m *manualTicker) factory(d time.Duration) (<-chan time.Time, func()) {
	m.every = d
	close(m.ready)
	return m.ch, func() {}
}

// leaseProbeCC records the clock reading of every lease write and can fail
// chosen ones (by 0-based write index).
type leaseProbeCC struct {
	*dtest.FakeCC
	clk  *dtest.ManualClock
	mu   sync.Mutex
	at   []time.Time
	vals []time.Time // the lease_until each write carried
	fail map[int]bool
}

func (c *leaseProbeCC) SetMeta(ctx context.Context, id, key, value string) error {
	c.mu.Lock()
	n := len(c.at)
	c.at = append(c.at, c.clk.Now())
	until, _ := ccpool.ParseMetaTime(value)
	c.vals = append(c.vals, until)
	failing := c.fail[n]
	c.mu.Unlock()
	if failing {
		return errors.New("ccpool meta set: disk I/O error")
	}
	return c.FakeCC.SetMeta(ctx, id, key, value)
}

// renewProbe is a checkoutpool.Client double that records the clock of every
// Renew.
type renewProbe struct {
	*fakePool
	clk *dtest.ManualClock
	mu  sync.Mutex
	at  []time.Time
}

func (p *renewProbe) Renew(ctx context.Context, id string) error {
	p.mu.Lock()
	p.at = append(p.at, p.clk.Now())
	p.mu.Unlock()
	return p.fakePool.Renew(ctx, id)
}

// runLease starts the lease refresh on a fake clock, fires ticks every `step`
// until `total` has elapsed, stops it and returns the period the handler asked
// for plus what was written and renewed.
func runLease(t *testing.T, cc *leaseProbeCC, clk *dtest.ManualClock, total time.Duration) (every time.Duration, writes, renews []time.Time) {
	t.Helper()
	cfg := leaseCfg()
	mt := newManualTicker()
	pool := &renewProbe{fakePool: &fakePool{}, clk: clk}
	r := &ccpoolRun{deps: Deps{CC: cc, Cfg: cfg, Now: clk.Now, NewTicker: mt.factory, Pool: pool}, lease: "lease-1"}
	stop := r.startLease(context.Background(), leaseSess)
	// The first write happens inline on the loop goroutine before it reads ticks:
	// the first send below synchronizes with it.
	<-mt.ready
	start := clk.Now()
	advance := clk.TickAdvancing()
	for clk.Now().Sub(start) < total {
		_ = advance(context.Background(), mt.every)
		mt.ch <- clk.Now()
	}
	stop()
	cc.mu.Lock()
	writes = append([]time.Time(nil), cc.at...)
	cc.mu.Unlock()
	pool.mu.Lock()
	renews = append([]time.Time(nil), pool.at...)
	pool.mu.Unlock()
	return mt.every, writes, renews
}

// refreshedWithin reports whether, over [start, start+total], no gap between
// consecutive refreshes (nor from start to the first, nor from the last to the
// end) exceeds window.
func refreshedWithin(times []time.Time, start time.Time, total, window time.Duration) bool {
	prev := start
	for _, ts := range times {
		if ts.Sub(prev) > window {
			return false
		}
		prev = ts
	}
	return start.Add(total).Sub(prev) <= window
}

func TestLeaseCadence_quarterTTLOverTenMinutes(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clk := &dtest.ManualClock{T: t0}
	cc := &leaseProbeCC{FakeCC: &dtest.FakeCC{}, clk: clk}
	const total = 10 * time.Minute
	every, writes, renews := runLease(t, cc, clk, total)

	if every != leaseTTL/4 {
		t.Fatalf("ticker period = %v, want LeaseTTL/4 = %v", every, leaseTTL/4)
	}
	// once at start, then one per 30 s tick: 1 + 600/30 = 21.
	if want := 1 + int(total/every); len(writes) != want {
		t.Fatalf("lease writes over 10m = %d, want %d (1 + %v / %v)", len(writes), want, total, every)
	}
	if !writes[0].Equal(t0) {
		t.Errorf("first write at %v, want the start %v", writes[0], t0)
	}
	if len(renews) != len(writes) {
		t.Errorf("checkout-pool renews = %d, want one per lease write (%d)", len(renews), len(writes))
	}
	if !refreshedWithin(writes, t0, total, leaseTTL/2) {
		t.Errorf("lease must be refreshed at least once per LeaseTTL/2; writes=%v", writes)
	}
	if !refreshedWithin(renews, t0, total, leaseTTL/2) {
		t.Errorf("checkout lease must be renewed at least once per LeaseTTL/2; renews=%v", renews)
	}
}

// Negative control for the cadence assertion: a refresh period of one whole
// LeaseTTL (writes at 0, 2m, 4m, ...) leaves gaps wider than LeaseTTL/2, so the
// same check fails on it.
func TestLeaseCadence_negativeControlTTLCadenceFails(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	var writes []time.Time
	for ts := t0; !ts.After(t0.Add(10 * time.Minute)); ts = ts.Add(leaseTTL) {
		writes = append(writes, ts)
	}
	if refreshedWithin(writes, t0, 10*time.Minute, leaseTTL/2) {
		t.Fatal("a cadence of one LeaseTTL must fail the at-least-once-per-TTL/2 assertion")
	}
}

// A failed refresh is logged and retried on the next tick, and one missed tick
// never lets the lease lapse: the next successful write lands before the
// previous successful write's lease_until.
func TestLeaseCadence_failedTickRetriesAndLeaseNeverLapses(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clk := &dtest.ManualClock{T: t0}
	cc := &leaseProbeCC{FakeCC: &dtest.FakeCC{}, clk: clk, fail: map[int]bool{1: true}} // the first tick fails
	logs := captureLog(t)
	every, writes, _ := runLease(t, cc, clk, 3*leaseTTL/4) // ticks at +30s (fails), +60s, +90s
	if len(writes) != 4 {
		t.Fatalf("writes = %d, want 4 (start + 3 ticks, the failed one included)", len(writes))
	}
	if !strings.Contains(logs.String(), "level=WARN msg=\"lease refresh failed\"") {
		t.Errorf("a failed refresh must be logged at WARN; logs=%s", logs)
	}
	if !strings.Contains(logs.String(), "lease refresh recovered") {
		t.Errorf("the retry on the next tick must be logged as recovered; logs=%s", logs)
	}
	// The write at index 0 succeeded and promised lease_until = t0 + TTL; the
	// failed write (index 1) wrote nothing; the retry (index 2) lands at t0 + 2
	// ticks, inside that promise.
	cc.mu.Lock()
	promised := cc.vals[0]
	cc.mu.Unlock()
	if !writes[2].Before(promised) {
		t.Errorf("retry at %v is not inside the previous lease (until %v)", writes[2], promised)
	}
	if got := writes[2].Sub(t0); got != 2*every {
		t.Errorf("retry landed %v after start, want 2 ticks (%v)", got, 2*every)
	}
	// FakeCC recorded only the successful writes.
	if n := len(cc.FakeCC.SetMetaCalls()); n != 3 {
		t.Errorf("successful lease writes = %d, want 3", n)
	}
}

// --- bead read cadence in the wait loop ---

// timedShowBD counts `bd show` reads and notes the fake clock at each.
type timedShowBD struct {
	*dtest.ScriptBD
	clk *dtest.ManualClock
	mu  sync.Mutex
	at  []time.Time
}

func (b *timedShowBD) Run(ctx context.Context, args ...string) (string, error) {
	if args[0] == "show" {
		b.mu.Lock()
		b.at = append(b.at, b.clk.Now())
		b.mu.Unlock()
	}
	return b.ScriptBD.Run(ctx, args...)
}

func (b *timedShowBD) shows() []time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]time.Time(nil), b.at...)
}

// showWait is the 5-minute wait: poll 10 s, max wait 5 m, bead stays claimed.
// rowState reports the session row state at a clock reading; inFlight keeps a
// non-active close-or-release row from counting as ended.
func showWait(t *testing.T, release bool, rowState func(elapsed time.Duration) ccpool.SessionState) (*timedShowBD, time.Time, error) {
	t.Helper()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clk := &dtest.ManualClock{T: t0}
	cfg := leaseCfg()
	cfg.PollInterval = 10 * time.Second
	cfg.MaxWait = 5 * time.Minute
	role := workerRole(cfg)
	if release {
		role = releaseRole(cfg)
	}
	role.CCPool.Budget = budget.Budget{}
	role.CCPool.OnFailure = roles.Unclaim
	actor := role.CCPool.Actor
	sbd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{"zr-w": {"in_progress"}},
		AssigneeSeq: map[string][]string{"zr-w": {actor}},
	}
	bd := &timedShowBD{ScriptBD: sbd, clk: clk}
	cc := newSettleCC(leaseSess, func() ccpool.SessionState { return rowState(clk.Now().Sub(t0)) })
	cc.transcript = "/t"
	deps := leaseDeps(t, cc, bd, cfg, clk)
	deps.LatestActivity = func(string) (time.Time, bool) { return clk.Now(), true }
	deps.InFlight = func(string, time.Time) bool { return true }
	r := &ccpoolRun{deps: deps}
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
	err := r.waitDone(context.Background(), nil, d, leaseSess)
	return bd, t0, err
}

func TestWaitShow_windowIsThreePollsCappedAt30s(t *testing.T) {
	for poll, want := range map[time.Duration]time.Duration{
		10 * time.Second:      30 * time.Second,
		20 * time.Millisecond: 60 * time.Millisecond,
		time.Minute:           30 * time.Second,
		0:                     0,
	} {
		if got := beadShowWindow(poll); got != want {
			t.Errorf("beadShowWindow(%v) = %v, want %v", poll, got, want)
		}
	}
}

// maxActiveShows is the bound for an active row over the 5-minute run: one read
// per 30 s window, 5 m / 30 s = 10, plus the two reads of the deadline poll (its
// ordinary read and the final check).
const maxActiveShows = 10 + 2

func TestWaitShow_activeRowReadsAtMostEvery30s(t *testing.T) {
	bd, t0, err := showWait(t, false, func(time.Duration) ccpool.SessionState { return ccpool.StateWorking })
	if err == nil {
		t.Fatal("the bead never completes, so the wait must end at the deadline with an error")
	}
	shows := bd.shows()
	if len(shows) > maxActiveShows {
		t.Errorf("bd show count over 5m with an active row = %d, want <= %d", len(shows), maxActiveShows)
	}
	if len(shows) < 10 {
		t.Errorf("bd show count = %d: the bead must still be read about every 30 s (want >= 10)", len(shows))
	}
	for i := 1; i < len(shows)-2; i++ { // the last two are the deadline poll's
		if gap := shows[i].Sub(shows[i-1]); gap < 30*time.Second {
			t.Errorf("reads %d and %d are %v apart, want >= 30s while the row is active", i-1, i, gap)
		}
	}
	// The deadline's final check always runs.
	if last := shows[len(shows)-1]; last.Sub(t0) < 5*time.Minute {
		t.Errorf("last read at +%v, want the deadline final check at +5m", last.Sub(t0))
	}
}

// A row that is not active (idle, with its end test not yet met) is read every
// poll: 5 m / 10 s = 30 polls, plus the deadline poll's two reads. Its count is
// also the negative control for the active bound: the old every-poll behaviour
// exceeds maxActiveShows.
func TestWaitShow_nonActiveRowReadsEveryPoll(t *testing.T) {
	bd, _, err := showWait(t, true, func(time.Duration) ccpool.SessionState { return ccpool.StateIdle })
	if err == nil {
		t.Fatal("the bead never completes, so the wait must end at the deadline with an error")
	}
	n := len(bd.shows())
	if n < 30 {
		t.Errorf("bd show count over 5m with a non-active row = %d, want >= 30 (every poll)", n)
	}
	if n <= maxActiveShows {
		t.Errorf("negative control: every-poll reads (%d) must exceed the active bound (%d)", n, maxActiveShows)
	}
}

// The re-check after death always runs, even inside the 30 s window: the row
// is read at +0 and +30, goes idle at +40 (10 s after the last read), and both
// reads of the death branch still happen.
func TestWaitShow_recheckAfterDeathIgnoresTheWindow(t *testing.T) {
	bd, t0, err := showWait(t, false, func(el time.Duration) ccpool.SessionState {
		if el >= 40*time.Second {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	if err == nil {
		t.Fatal("a session that ended with the bead unfinished must fail the dispatch")
	}
	shows := bd.shows()
	// +0 and +30 are the windowed reads; +40 and +50 are the death branch's two
	// re-checks (more reads follow from the failure handling, not asserted).
	if len(shows) < 4 {
		t.Fatalf("reads = %v, want at least +0, +30, +40, +50", shows)
	}
	for i, want := range []time.Duration{0, 30 * time.Second, 40 * time.Second, 50 * time.Second} {
		if got := shows[i].Sub(t0); got != want {
			t.Errorf("read %d at +%v, want +%v (the re-check at +40 is inside the 30 s window and must still run)", i, got, want)
		}
	}
}
