package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
)

// Settled-session close (INV-CCH-17, ADR 0082): after a dispatch reaches a
// terminal outcome the handler closes, non-purge, the session it launched or
// absorbed -- but only when it has settled and is quiet.

// settleCC is a dtest.FakeCC whose single session row follows the test's
// script and whose Close (when it succeeds) turns the row into the closed
// state real ccpool would report (Live=false, CloseReason "handler"), which
// dtest.FakeCC.Close alone does not mutate.
type settleCC struct {
	*dtest.FakeCC
	id         string
	name       string
	transcript string
	state      func() ccpool.SessionState // state of the still-open row
	preClosed  string                     // CloseReason the row already carries ("" = open)

	mu     sync.Mutex
	closed bool
}

func (c *settleCC) Close(ctx context.Context, externalID string, purge bool) error {
	err := c.FakeCC.Close(ctx, externalID, purge)
	if err == nil {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	}
	return err
}

func (c *settleCC) List(context.Context) ([]ccpool.Session, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	row := ccpool.Session{ExternalID: c.id, Name: c.name, TranscriptPath: c.transcript, State: c.state(), Live: true, CWD: "/repo"}
	switch {
	case closed:
		row.Live, row.CloseReason = false, "handler"
	case c.preClosed != "":
		row.Live, row.CloseReason = false, c.preClosed
	}
	return []ccpool.Session{row}, nil
}

func fixedState(st ccpool.SessionState) func() ccpool.SessionState {
	return func() ccpool.SessionState { return st }
}

func newSettleCC(id string, state func() ccpool.SessionState) *settleCC {
	return &settleCC{FakeCC: &dtest.FakeCC{}, id: id, name: "disp-" + id, state: state}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// noBudget returns role with its budget stripped, so no watchdog goroutine
// races waitDone and the outcome is deterministic.
func noBudget(role roles.Role) roles.Role {
	role.CCPool.Budget = budget.Budget{}
	return role
}

// dispatchWith runs a full ccpoolExecutor.Dispatch of role against cc/bd with
// a manual clock the caller can also read.
func dispatchWith(t *testing.T, cc ccpool.Runner, bd *dtest.ScriptBD, cfg config.Config, role roles.Role, ext string, clk *dtest.ManualClock) (report.Result, error) {
	t.Helper()
	cfg.WorktreeDir = t.TempDir()
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
	deps := newExec(&dtest.FakeCC{}, bd, cfg).deps
	deps.CC = cc
	deps.ExternalID = ext
	deps.Now, deps.Tick = clk.Now, clk.TickAdvancing()
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	return ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
}

func assertClosedOnceNonPurge(t *testing.T, cc *dtest.FakeCC, id string) {
	t.Helper()
	if len(cc.Closed) != 1 || cc.Closed[0] != id {
		t.Errorf("want exactly one close of own session %q, got Closed=%v", id, cc.Closed)
	}
	if len(cc.ClosedPurge) != 1 || cc.ClosedPurge[0] {
		t.Errorf("settled-session close must be non-purge, got ClosedPurge=%v", cc.ClosedPurge)
	}
}

// --- one close per terminal outcome ---

func TestSettledClose_successBeadClosed(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := newSettleCC("sess-1", func() ccpool.SessionState {
		if bd.Idx["zr-w"] >= 2 { // bead close observed => the turn has ended
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	logs := captureLog(t)
	res, err := dispatchWith(t, cc, bd, cfg, noBudget(workerRole(cfg)), "sess-1", &dtest.ManualClock{T: time.Unix(0, 0)})
	if err != nil || verbOf(res) != "" {
		t.Fatalf("success must be unchanged: res=%v err=%v", res, err)
	}
	assertClosedOnceNonPurge(t, cc.FakeCC, "sess-1")
	for _, want := range []string{"settled session closed", "session=sess-1", "bead=zr-w", "state=idle", "outcome=success"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("INFO close line must carry %q; logs=%s", want, logs.String())
		}
	}
}

func TestSettledClose_successHandbackToOpen(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := newSettleCC("sess-1", func() ccpool.SessionState {
		if bd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	_, err := dispatchWith(t, cc, bd, cfg, noBudget(workerRole(cfg)), "sess-1", &dtest.ManualClock{T: time.Unix(0, 0)})
	if err != nil {
		t.Fatalf("handback is a success: %v", err)
	}
	assertClosedOnceNonPurge(t, cc.FakeCC, "sess-1")
}

// onFailure outcomes: the worker never completes within MaxWait while still
// working; by the time finishWait runs the session has settled (idle).
func TestSettledClose_onFailureOutcomes(t *testing.T) {
	cfg := fastCfg()
	cases := []struct {
		name string
		role roles.Role
		want report.Verb
	}{
		{"add-human", noBudget(workerRole(cfg)), report.Escalated},
		{"unclaim", noBudget(feedbackRole(cfg)), report.Unclaimed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
			clk := &dtest.ManualClock{T: time.Unix(0, 0)}
			cc := newSettleCC("sess-1", func() ccpool.SessionState {
				if failureApplied(bd) { // the failure action has been applied: the dispatch gave up
					return ccpool.StateIdle // ... and the session has settled by now
				}
				return ccpool.StateWorking
			})
			logs := captureLog(t)
			res, err := dispatchWith(t, cc, bd, cfg, tc.role, "sess-1", clk)
			if err == nil {
				t.Fatal("expected a not-complete failure")
			}
			if v := verbOf(res); v != tc.want {
				t.Errorf("failure verb must be unchanged: got %q want %q", v, tc.want)
			}
			assertClosedOnceNonPurge(t, cc.FakeCC, "sess-1")
			if !strings.Contains(logs.String(), "outcome=failure") {
				t.Errorf("INFO close line must name the failure outcome; logs=%s", logs.String())
			}
		})
	}
}

// failureApplied reports whether complete.OnFailure's add-human or unclaim
// update has reached bd (as opposed to run()'s unrelated label cleanup).
func failureApplied(bd *dtest.ScriptBD) bool {
	for _, u := range bd.Updates {
		if strings.Contains(u, "--add-label human") || strings.Contains(u, "--status=open") {
			return true
		}
	}
	return false
}

// Budget stop: the watchdog hard stop already closed the session (once), so
// finishWait must add no second close.
func TestSettledClose_budgetStopAddsNoSecondClose(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
	// While open the row is mid-turn (so waitDone keeps waiting and the watchdog
	// wins); once closed it reads CloseReason "handler" + idle, the very row a
	// guard-less finishWait would close a second time.
	cc.state = func() ccpool.SessionState {
		cc.mu.Lock()
		defer cc.mu.Unlock()
		if cc.closed {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	}
	cc.transcript = "/t"
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	cfg.WorktreeDir = t.TempDir()
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExecCC(cc, bd, cfg).deps
	deps.ExternalID = "sess-1"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	origTick := clk.TickAdvancing()
	deps.Now = clk.Now
	deps.Tick = func(ctx context.Context, d time.Duration) error { // same wall-clock pacing as TestDispatch_watchdogHardStop_unclaimed
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		return origTick(ctx, d)
	}
	deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}}
	res, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("budget stop must still report Unclaimed, got %q", v)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "sess-1" {
		t.Errorf("budget stop must close the session exactly once (the watchdog's); Closed=%v", cc.Closed)
	}
}

// --- negative cases (closeSettledSession directly) ---

func settledRun(cc ccpool.Runner, cfg config.Config) *ccpoolRun {
	return newExecCC(cc, &dtest.ScriptBD{}, cfg)
}

// newExecCC is newExec for a ccpool.Runner other than *dtest.FakeCC.
func newExecCC(cc ccpool.Runner, bd *dtest.ScriptBD, cfg config.Config) *ccpoolRun {
	r := newExec(&dtest.FakeCC{}, bd, cfg)
	r.deps.CC = cc
	return r
}

func TestSettledClose_skips(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *settleCC)
	}{
		{"needs_input is exempt", func(c *settleCC) { c.state = fixedState(ccpool.StateNeedsInput) }},
		{"working again", func(c *settleCC) { c.state = fixedState(ccpool.StateWorking) }},
		{"starting", func(c *settleCC) { c.state = fixedState(ccpool.StateStarting) }},
		{"ready", func(c *settleCC) { c.state = fixedState(ccpool.StateReady) }},
		{"already closed idle_ttl", func(c *settleCC) { c.preClosed = "idle_ttl" }},
		{"already closed cap_eviction", func(c *settleCC) { c.preClosed = "cap_eviction" }},
		{"already closed operator", func(c *settleCC) { c.preClosed = "operator" }},
		{"already closed handler", func(c *settleCC) { c.preClosed = "handler" }},
		{"row absent", func(c *settleCC) { c.id = "someone-else" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
			tc.mutate(cc)
			r := settledRun(cc, fastCfg())
			r.closeSettledSession(context.Background(), "zr-w", "sess-1", nil, quietResult{})
			if len(cc.Closed) != 0 {
				t.Errorf("must not close; Closed=%v", cc.Closed)
			}
		})
	}
}

func TestSettledClose_closesOnlyItsOwnRow(t *testing.T) {
	// Two rows idle; only name's own may be closed.
	cc := &twoRowCC{FakeCC: &dtest.FakeCC{}}
	r := settledRun(cc, fastCfg())
	r.closeSettledSession(context.Background(), "zr-w", "mine", nil, quietResult{})
	if len(cc.Closed) != 1 || cc.Closed[0] != "mine" {
		t.Errorf("must close only its own row; Closed=%v", cc.Closed)
	}
}

type twoRowCC struct{ *dtest.FakeCC }

func (c *twoRowCC) List(context.Context) ([]ccpool.Session, error) {
	return []ccpool.Session{
		{ExternalID: "peer", Name: "peer", State: ccpool.StateIdle, Live: true},
		{ExternalID: "mine", Name: "mine", State: ccpool.StateIdle, Live: true},
	}, nil
}

func TestSettledClose_skipsOnCancellation(t *testing.T) {
	t.Run("ctx cancelled", func(t *testing.T) {
		cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		settledRun(cc, fastCfg()).closeSettledSession(ctx, "zr-w", "sess-1", nil, quietResult{})
		if len(cc.Closed) != 0 {
			t.Errorf("a cancelled ctx (daemon shutdown) must not close; Closed=%v", cc.Closed)
		}
	})
	for _, werr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(werr.Error(), func(t *testing.T) {
			cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
			settledRun(cc, fastCfg()).closeSettledSession(context.Background(), "zr-w", "sess-1", werr, quietResult{})
			if len(cc.Closed) != 0 {
				t.Errorf("a cancellation werr must not close; Closed=%v", cc.Closed)
			}
		})
	}
}

func TestSettledClose_listErrorSkips(t *testing.T) {
	cc := &dtest.FakeCC{ListErr: errors.New("ccpool list: transient")}
	r := settledRun(cc, fastCfg())
	r.closeSettledSession(context.Background(), "zr-w", "sess-1", nil, quietResult{checked: true, quiet: true})
	if len(cc.Closed) != 0 {
		t.Errorf("unreadable row => can't tell => no close; Closed=%v", cc.Closed)
	}
}

// --- quiet check, for every isolation type ---

func busyCfg() config.Config {
	cfg := fastCfg()
	cfg.WorktreeQuietWindow = time.Minute
	cfg.WorktreeQuietMax = 5 * time.Millisecond
	return cfg
}

func TestSettledClose_deferredWhileSubagentsActive_everyIsolationType(t *testing.T) {
	for _, typ := range []string{"", "worktree", "none", "path", "workforest"} {
		t.Run("isolation="+typ, func(t *testing.T) {
			cfg := busyCfg()
			cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
			cc.transcript = "/t/s.jsonl"
			bd := &dtest.ScriptBD{}
			e := newExecCC(cc, bd, cfg)
			e.deps.GitOpener = (&dtest.NoopGitOpener{}).Open
			probes := 0
			e.deps.LatestActivity = func(string) (time.Time, bool) { probes++; return e.deps.clock(), true } // always fresh
			logs := captureLog(t)
			role := noBudget(workerRole(cfg))
			role.CCPool.Isolation = roles.IsolationConfig{Type: typ}
			d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
			if _, err := e.finishWait(context.Background(), role.CCPool, d, "sess-1", "/tmp/wt/zr-w", nil); err != nil {
				t.Fatalf("finishWait must not fail: %v", err)
			}
			if probes == 0 {
				t.Errorf("waitSessionQuiet must run before the close for isolation %q", typ)
			}
			if len(cc.Closed) != 0 {
				t.Errorf("must not close while subagents are active; Closed=%v", cc.Closed)
			}
			for _, want := range []string{"settled-session close deferred -- subagents active", "session=sess-1", "bead=zr-w"} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("deferral INFO must carry %q; logs=%s", want, logs.String())
				}
			}
		})
	}
}

func TestSettledClose_noneIsolationClosesAfterQuiet(t *testing.T) {
	cfg := busyCfg()
	cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
	cc.transcript = "/t/s.jsonl"
	e := newExecCC(cc, &dtest.ScriptBD{}, cfg)
	e.deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	start := e.deps.clock()
	probes := 0
	// Busy at first; quiet once the manual clock advances past the window.
	e.deps.LatestActivity = func(string) (time.Time, bool) { probes++; return start, true }
	e.deps.Cfg.WorktreeQuietMax = time.Hour
	role := noBudget(workerRole(cfg))
	role.CCPool.Isolation = roles.IsolationConfig{Type: "none"}
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
	if _, err := e.finishWait(context.Background(), role.CCPool, d, "sess-1", "/repo", nil); err != nil {
		t.Fatal(err)
	}
	if probes == 0 {
		t.Error("the quiet check must run for none isolation")
	}
	assertClosedOnceNonPurge(t, cc.FakeCC, "sess-1")
}

// The quiet verdict cleanupWorktree already computed is reused, not re-polled.
func TestSettledClose_reusesCleanupQuietVerdict(t *testing.T) {
	cfg := busyCfg()
	cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
	cc.transcript = "/t/s.jsonl"
	e := newExecCC(cc, &dtest.ScriptBD{}, cfg)
	e.deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	probes := 0
	// Already quiet on the first probe: one waitSessionQuiet run == one probe.
	e.deps.LatestActivity = func(string) (time.Time, bool) {
		probes++
		return e.deps.clock().Add(-2 * cfg.WorktreeQuietWindow), true
	}
	role := noBudget(workerRole(cfg))
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
	if _, err := e.finishWait(context.Background(), role.CCPool, d, "sess-1", "/tmp/wt/zr-w", nil); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Errorf("worktree isolation must poll quiet exactly once across cleanup+close, got %d probes", probes)
	}
	assertClosedOnceNonPurge(t, cc.FakeCC, "sess-1")
}

// --- failure handling ---

func TestSettledClose_closeErrorWarnsAndLeavesResultUnchanged(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := newSettleCC("sess-1", func() ccpool.SessionState {
		if bd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	cc.CloseErr = errors.New("ccpool close: boom")
	logs := captureLog(t)
	res, err := dispatchWith(t, cc, bd, cfg, noBudget(workerRole(cfg)), "sess-1", &dtest.ManualClock{T: time.Unix(0, 0)})
	if err != nil || verbOf(res) != "" {
		t.Fatalf("a close failure must not change the dispatch result: res=%v err=%v", res, err)
	}
	if len(cc.Closed) != 1 {
		t.Fatalf("close must have been attempted once, Closed=%v", cc.Closed)
	}
	for _, want := range []string{"level=WARN", "settled-session close failed", "session=sess-1", "bead=zr-w", "ccpool close: boom"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("WARN must carry %q; logs=%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "settled session closed") {
		t.Errorf("a failed close must not log the success INFO line; logs=%s", logs.String())
	}
}

// A close failure on a failing dispatch keeps the original werr.
func TestSettledClose_closeErrorKeepsWerr(t *testing.T) {
	cfg := fastCfg()
	cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
	cc.CloseErr = errors.New("boom")
	e := newExecCC(cc, &dtest.ScriptBD{}, cfg)
	e.deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	role := noBudget(workerRole(cfg))
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
	want := errors.New("zr-w: not complete")
	res, err := e.finishWait(context.Background(), role.CCPool, d, "sess-1", "/tmp/wt/zr-w", want)
	if !errors.Is(err, want) {
		t.Errorf("werr must pass through unchanged, got %v", err)
	}
	if verbOf(res) != report.Escalated {
		t.Errorf("result must be the add-human verb, got %q", verbOf(res))
	}
}
