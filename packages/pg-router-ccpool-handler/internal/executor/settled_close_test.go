package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
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
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
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
	rowMeta    map[string]string          // the row's meta, when a test wants List to carry it
	preClosed  string                     // CloseReason the row already carries ("" = open)

	mu     sync.Mutex
	closed bool
	ops    []string // "meta:<key>" / "close", in call order
}

func (c *settleCC) SetMeta(ctx context.Context, externalID, key, value string) error {
	c.mu.Lock()
	c.ops = append(c.ops, "meta:"+key)
	c.mu.Unlock()
	return c.FakeCC.SetMeta(ctx, externalID, key, value)
}

// closeOps is ops without the supervision-lease refreshes, which tick alongside
// every dispatch and are not what these tests pin.
func (c *settleCC) closeOps() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(c.ops), func(op string) bool { return op == "meta:"+ccpool.MetaKeyLeaseUntil })
}

func (c *settleCC) Close(ctx context.Context, externalID string, purge bool) error {
	err := c.FakeCC.Close(ctx, externalID, purge)
	c.mu.Lock()
	c.ops = append(c.ops, "close")
	if err == nil {
		c.closed = true
	}
	c.mu.Unlock()
	return err
}

func (c *settleCC) List(context.Context) ([]ccpool.Session, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	row := ccpool.Session{ExternalID: c.id, Name: c.name, TranscriptPath: c.transcript, State: c.state(), Live: true, CWD: "/repo", Meta: c.rowMeta}
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

// incompleteOps is the SetMeta/Close order closeSettledSession must produce for a
// dispatch that left its bead open: mark the row incomplete, THEN close it, so no
// same-event same-head re-request ever finds an unmarked handler-closed row of that
// attempt (pg2-tc9c3).
var incompleteOps = []string{"meta:" + ccpool.MetaKeyIncomplete, "close"}

// A hand-back (the session unclaimed the bead and went idle) leaves the bead open:
// the closed row is marked incomplete, and the marked row then reads as ABSENT to
// a same-event same-head re-request (crashOrphaned).
func TestSettledClose_handbackMarksRowIncomplete(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := newSettleCC("sess-1", func() ccpool.SessionState {
		if bd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	if _, err := dispatchWith(t, cc, bd, cfg, noBudget(workerRole(cfg)), "sess-1", &dtest.ManualClock{T: time.Unix(0, 0)}); err != nil {
		t.Fatalf("handback is a success: %v", err)
	}
	if !slices.Equal(cc.closeOps(), incompleteOps) {
		t.Fatalf("a hand-back must mark the row incomplete, then close it; ops=%v", cc.closeOps())
	}
	row := ccpool.Session{State: ccpool.StateIdle, CloseReason: "handler", Meta: map[string]string{}}
	for _, m := range cc.SetMetaCalls() {
		row.Meta[m.Key] = m.Value
	}
	if !crashOrphaned(row) {
		t.Errorf("the handed-back row, as the handler wrote it, must be ABSENT to a re-request; meta=%v", row.Meta)
	}
}

// A completed dispatch (bead closed) keeps its row absorbable: a crash-window
// redelivery of finished work must still find it (INV-EVT-2, INV-CCH-17).
func TestSettledClose_closedBeadLeavesRowAbsorbable(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := newSettleCC("sess-1", func() ccpool.SessionState {
		if bd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	if _, err := dispatchWith(t, cc, bd, cfg, noBudget(workerRole(cfg)), "sess-1", &dtest.ManualClock{T: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cc.closeOps(), []string{"close"}) {
		t.Errorf("a completed dispatch must close the row without marking it; ops=%v", cc.closeOps())
	}
}

// leftBeadOpen decides whether a settled row is marked incomplete.
func TestLeftBeadOpen_cases(t *testing.T) {
	cases := []struct {
		name       string
		werr       error
		completion roles.Completion
		status     string // "" = the bead cannot be read
		want       bool
	}{
		{"failure", errors.New("not complete"), roles.CloseOnly, "closed", true},
		{"budget stop", watchdog.ErrBudgetExceeded, roles.CloseOrHandback, "open", true},
		{"hand-back: close-or-handback, bead open", nil, roles.CloseOrHandback, "open", true},
		{"hand-back: close-or-handback, bead re-claimed", nil, roles.CloseOrHandback, "in_progress", true},
		{"success: close-or-handback, bead closed", nil, roles.CloseOrHandback, "closed", false},
		{"success: close-only (bead is closed by definition)", nil, roles.CloseOnly, "", false},
		{"success: triage leaves the bead open by design", nil, roles.CloseOrTriage, "open", false},
		{"success: split-triage leaves the bead open by design", nil, roles.CloseOrSplitTriage, "open", false},
		{"cannot read the bead: stays absorbable", nil, roles.CloseOrHandback, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bd := &dtest.ScriptBD{}
			if tc.status != "" {
				bd.StatusSeq = map[string][]string{"zr-w": {tc.status}}
			}
			r := newExec(&dtest.FakeCC{}, bd, fastCfg())
			if got := r.leftBeadOpen(context.Background(), &roles.CCPoolConfig{Completion: tc.completion}, "zr-w", tc.werr); got != tc.want {
				t.Errorf("leftBeadOpen = %v, want %v", got, tc.want)
			}
		})
	}
}

// A failed dispatch (here the add-human on_failure after not completing) is marked
// incomplete too, before its close.
func TestSettledClose_failureMarksRowIncomplete(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := newSettleCC("sess-1", func() ccpool.SessionState {
		if failureApplied(bd) {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	if _, err := dispatchWith(t, cc, bd, cfg, noBudget(workerRole(cfg)), "sess-1", &dtest.ManualClock{T: time.Unix(0, 0)}); err == nil {
		t.Fatal("expected a not-complete failure")
	}
	if !slices.Equal(cc.closeOps(), incompleteOps) {
		t.Errorf("a failed dispatch must mark the row incomplete, then close it; ops=%v", cc.closeOps())
	}
}

// The unexplained-death branch of waitDone (the session went idle while a
// close-only bead is still open) closes the row ITSELF, so closeSettledSession
// later finds it already closed and skips it. That Close must therefore stamp
// the row incomplete, or a same-event same-head redelivery re-absorbs it and
// hard-stops instantly on its original launch time (pg2-tc9c3). End to end: the
// first dispatch dies, then the SAME event at the SAME head arrives an hour
// after the budget ran out of the old launch time and must launch afresh.
func TestSettledClose_unexplainedDeathMarksRowIncomplete_redeliveryLaunchesFresh(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTime = time.Hour
	role := feedbackRole(cfg) // close-only, on_failure=unclaim
	display := role.DisplayName(cfg.SessionPrefix, "zr-w")
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle)) // idle, bead still open: waitDone sees an unexplained death
	// Dispatch 1 must LAUNCH (not absorb a row by name), so the row only carries the
	// stable display name once the redelivery arrives.
	it := item.Item{ID: "zr-w", Metadata: map[string]any{"head_sha": "h1"}}
	dispatch := func(role roles.Role, ext string, now time.Time) error {
		cfg := cfg
		cfg.WorktreeDir = t.TempDir()
		deps := newExec(&dtest.FakeCC{}, bd, cfg).deps
		deps.CC, deps.ExternalID = cc, ext
		clk := &dtest.ManualClock{T: now}
		deps.Now, deps.Tick = clk.Now, clk.TickAdvancing()
		deps.Git = &dtest.NoopGit{}
		deps.GitOpener = (&dtest.NoopGitOpener{}).Open
		_, err := ccpoolExecutor{}.Dispatch(context.Background(), DispatchContext{Role: role, Item: it, EventID: "review.ready:zr-w"}, deps)
		return err
	}

	first := noBudget(role)
	if err := dispatch(first, "sess-1", time.Unix(0, 0)); err == nil {
		t.Fatal("the first dispatch must fail: its session exited before completing")
	}
	if got := cc.closeOps(); !slices.Equal(got, incompleteOps) {
		t.Fatalf("waitDone's own close of the dead row must mark it incomplete first; ops=%v", got)
	}

	// The row as ccpool now reports it: handler-closed, idle, carrying the meta the
	// launch stamped plus whatever the handler marked since.
	meta := map[string]string{}
	for k, v := range cc.EnsuredMeta {
		meta[k] = v
	}
	for _, m := range cc.SetMetaCalls() {
		meta[m.Key] = m.Value
	}
	cc.rowMeta, cc.name = meta, display
	cc.Ensured = nil

	// Redelivery of the same event at the same head, five hours after the launch,
	// under a one-hour budget: absorbing the row would hard-stop instantly.
	err := dispatch(role, "sess-2", time.Unix(0, 0).Add(5*time.Hour))
	if errors.Is(err, watchdog.ErrBudgetExceeded) {
		t.Fatalf("a redelivery after an unexplained death must not hard-stop on the old row's launch time: %v", err)
	}
	if len(cc.Ensured) != 1 || cc.Ensured[0] != "sess-2" {
		t.Errorf("a same-event same-head redelivery after an unexplained death must launch a fresh session; Ensured=%v", cc.Ensured)
	}
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
			r.closeSettledSession(context.Background(), &roles.CCPoolConfig{}, "zr-w", "sess-1", nil, quietResult{})
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
	r.closeSettledSession(context.Background(), &roles.CCPoolConfig{}, "zr-w", "mine", nil, quietResult{})
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
		settledRun(cc, fastCfg()).closeSettledSession(ctx, &roles.CCPoolConfig{}, "zr-w", "sess-1", nil, quietResult{})
		if len(cc.Closed) != 0 {
			t.Errorf("a cancelled ctx (daemon shutdown) must not close; Closed=%v", cc.Closed)
		}
	})
	for _, werr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(werr.Error(), func(t *testing.T) {
			cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
			settledRun(cc, fastCfg()).closeSettledSession(context.Background(), &roles.CCPoolConfig{}, "zr-w", "sess-1", werr, quietResult{})
			if len(cc.Closed) != 0 {
				t.Errorf("a cancellation werr must not close; Closed=%v", cc.Closed)
			}
		})
	}
}

func TestSettledClose_listErrorSkips(t *testing.T) {
	cc := &dtest.FakeCC{ListErr: errors.New("ccpool list: transient")}
	r := settledRun(cc, fastCfg())
	r.closeSettledSession(context.Background(), &roles.CCPoolConfig{}, "zr-w", "sess-1", nil, quietResult{checked: true, quiet: true})
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
