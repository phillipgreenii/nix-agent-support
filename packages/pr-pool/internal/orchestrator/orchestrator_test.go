package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/dtest"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

func newOrch(cc ccpool.Runner, bd *dtest.ScriptBD, cfg config.Config) *Orchestrator {
	o := &Orchestrator{CC: cc, BD: bd, Reg: testRoleSet(cfg), Cfg: cfg}
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	o.now = clk.Now
	o.tick = clk.TickAdvancing()
	o.stamp = func() string { return dtest.TestStamp }
	// SAFETY: a no-op git so the per-bead worktree.Ensure never shells out to real
	// git against the real repo (pg2-yukh #2). Combined with fastCfg's tempdir
	// WorktreeDir, dispatch tests leave no worktree/branch state behind.
	o.git = &dtest.NoopGit{}
	return o
}

// testRoleSet builds the built-in role set for tests (feedback then worker), with
// caps/budget derived from cfg — so the workerRole/feedbackRole helpers resolve the
// same roles the orchestrator drains.
func testRoleSet(cfg config.Config) roles.RoleSet {
	return roles.BuiltinRoleSet(roles.BuiltinParams{
		WorktreeDir:   cfg.WorktreeDir,
		SkillMD:       cfg.SkillMD,
		WorkerSkillMD: cfg.WorkerSkillMD,
		MaxFeedback:   cfg.MaxFeedback,
		MaxWorker:     cfg.MaxWorker,
		WorkerBudget:  cfg.WorkerBudget(),
	})
}

func roleByName(o *Orchestrator, name string) roles.Role {
	for _, r := range o.Reg {
		if r.Name == name {
			return r
		}
	}
	panic("test: role not found: " + name)
}

func feedbackRole(o *Orchestrator) roles.Role { return roleByName(o, "feedback") }
func workerRole(o *Orchestrator) roles.Role   { return roleByName(o, "worker") }

// testStamp is a package-level alias for dtest.TestStamp so golden_test.go
// (which is "Unchanged" per spec) can reference it without modification.
const testStamp = dtest.TestStamp

func fastCfg() config.Config {
	c := config.Default()
	c.MaxWait = 50 * time.Millisecond
	c.PollInterval = time.Millisecond
	// SAFETY: never the real ~/.local/state worktree dir — keep worktree.Ensure's
	// MkdirAll inside an isolated throwaway path (the no-op git in newOrch already
	// prevents any real `git worktree add`). os.MkdirTemp is used (not t.TempDir)
	// to preserve fastCfg's no-arg signature; the OS reaps it.
	if d, err := os.MkdirTemp("", "pr-pool-orch-wt-"); err == nil {
		c.WorktreeDir = d
	}
	return c
}

// writeTemp creates a sentinel file and returns its path (+ a no-op cleanup;
// t.TempDir() handles removal). Used by the gated-pass test.
func writeTemp(t *testing.T) (string, func()) {
	t.Helper()
	p := t.TempDir() + "/sentinel"
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, func() {}
}

// --- DrainOnce scenarios (ports bats: drain_once cases) ---

func TestDrainOnce_gatedNoTeardown(t *testing.T) {
	f, _ := writeTemp(t) // creates a sentinel file
	cfg := fastCfg()
	cfg.QuotaPaused = f
	bd := &dtest.ScriptBD{Ready: map[string]string{"feedback": "[]", "worker": "[]"}}
	cc := &dtest.FakeCC{}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cc.Ensured) != 0 || len(cc.Closed) != 0 {
		t.Errorf("gated pass must not dispatch or teardown; ensured=%v closed=%v", cc.Ensured, cc.Closed)
	}
}

func TestDrainOnce_workerCapZeroSkips(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxWorker = 0
	bd := &dtest.ScriptBD{Ready: map[string]string{"feedback": "[]", "worker": `[{"id":"zr-w"}]`}}
	cc := &dtest.FakeCC{}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range cc.Ensured {
		if n == "pr-pool-worker-zr-w" {
			t.Errorf("cap=0 should skip worker; ensured=%v", cc.Ensured)
		}
	}
}

func TestDrainOnce_capStopsAtOne(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		Ready:     map[string]string{"feedback": "[]", "worker": `[{"id":"zr-w1"},{"id":"zr-w2"}]`},
		StatusSeq: map[string][]string{"zr-w1": {"in_progress", "closed"}, "zr-w2": {"in_progress", "closed"}},
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-w1", Live: true}, {ExternalID: "pr-pool-worker-zr-w2", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background())
	if len(cc.Sent) != 1 {
		t.Errorf("MAX_WORKER=1 should dispatch one worker, sent=%v", cc.Sent)
	}
}

func TestDrainOnce_noStarvation(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		Ready: map[string]string{
			"feedback": `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x","parent":"zr-p"}]`,
			"worker":   `[{"id":"zr-w"}]`,
		},
		Show:      map[string]string{"zr-p": `{"id":"zr-p","metadata":{"author":"phillipg"}}`},
		StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}, "zr-w": {"in_progress", "closed"}},
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-feedback-zr-c", Live: true}, {ExternalID: "pr-pool-worker-zr-w", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background())
	if len(cc.Sent) != 2 {
		t.Errorf("one of each role should be worked, sent=%v", cc.Sent)
	}
}

func TestDrainOnce_teardownReapsStrays(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Ready: map[string]string{"feedback": "[]", "worker": "[]"}}
	// a stray session from a prior crashed run remains in the list
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-stray", Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background())
	if !dtest.Contains(cc.Closed, "pr-pool-worker-zr-stray") {
		t.Errorf("teardown must reap pr-pool- strays; closed=%v", cc.Closed)
	}
	if dtest.Contains(cc.Closed, "cc-unrelated") {
		t.Errorf("teardown must NOT close non-pr-pool sessions; closed=%v", cc.Closed)
	}
}

func TestWorkOne_sendFailFeedbackUnclaims(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: feedbackRole(o), Item: item.Item{ID: "zr-c"}}
	_, _ = o.workOne(context.Background(), d)
	if !dtest.HasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback send-fail must unclaim; updates=%v", bd.Updates)
	}
}

func TestWorkOne_sendFailWorkerNotUnclaimed(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	_, _ = o.workOne(context.Background(), d)
	if dtest.HasUpdate(bd, "--status=open") {
		t.Errorf("worker send-fail must NOT unclaim; updates=%v", bd.Updates)
	}
}

// pg2-c1vp / review follow-up: a worker that completes successfully while the
// budget watchdog is armed (but never trips) must return success with NO bead
// mutation — waitDone wins the terminal claim and the watchdog is the loser via
// cancellation, touching nothing. Exercises the workerWaitWithWatchdog happy
// path (the prior watchdog test only covered the watchdog-WINS case).
func TestWorkOne_workerSuccessWithWatchdogArmed(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1_000_000 // armed, but usage stays far below the cap
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking, TranscriptPath: "/t"}}}}
	o := newOrch(cc, bd, cfg)
	o.usageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 10}}} // nowhere near 1,000,000
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	if _, err := o.workOne(context.Background(), d); err != nil {
		t.Fatalf("worker that closes its bead should succeed, got %v; updates=%v", err, bd.Updates)
	}
	// Success clears any pool-launch-fail (ADR 0015) — that is the only permitted
	// update. It must NOT unclaim, add human, or comment.
	for _, u := range bd.Updates {
		if u != "update zr-w --remove-label pool-launch-fail" {
			t.Errorf("success must not unclaim/human/comment; unexpected update %q; updates=%v", u, bd.Updates)
		}
	}
}

func TestDrainOnce_teardownRunsOnDiscoverError(t *testing.T) {
	cfg := fastCfg()
	// a bd ready failure makes Discover return an error
	bd := &dtest.ScriptBD{ReadyErr: errors.New("bd ready failed")}
	// a stray pr-pool session exists from a prior run
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-stray", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background()); err == nil {
		t.Fatal("a bd ready failure should return an error from DrainOnce")
	}
	if !dtest.Contains(cc.Closed, "pr-pool-worker-zr-stray") {
		t.Errorf("teardown must run even on discover error; closed=%v", cc.Closed)
	}
}

// TestWorkOne_workerBudgetHardStopUnclaimsNoHuman verifies that when the budget
// watchdog fires a hard stop for a worker dispatch: workOne returns a budget
// error, the bead is left open+unclaimed, and the human label is NOT added.
func TestWorkOne_workerBudgetHardStopUnclaimsNoHuman(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000                                                        // finite token cap so the ramp can trip it
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}} // never completes on its own
	// The session must be LIVE and addressed by the per-attempt stamped external_id
	// (the form workOne dispatches under) so waitDone's active() check sees a working
	// session — otherwise the session reads as absent and waitDone takes the
	// session-exited death path instead of the budget scenario this test covers.
	ext := "pr-pool-worker-zr-w-" + dtest.TestStamp
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: ext, Live: true, State: ccpool.StateWorking, TranscriptPath: "/t", CWD: "/repo"}}}}
	o := newOrch(cc, bd, cfg)
	o.usageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately over 100%
	// Deterministic race resolution (pg2-erfg). The budget watchdog reads over-budget
	// usage on its very first poll and must own the single terminal outcome (unclaim,
	// no human). In production waitDone only reaches its MaxWait deadline after a REAL
	// 50ms of polling, so the watchdog's microsecond-fast hard stop always wins. The
	// manualClock's TickAdvancing collapses that 50ms to a handful of microsecond
	// iterations, turning the deterministic production ordering into a scheduling
	// coin-flip (still ~0.7% under -race -count even after the live-session fix above).
	// Park waitDone's poll until ctx is cancelled so it cannot reach its deadline
	// before the watchdog fires — faithful to prod, where the deadline is genuinely far
	// away when the hard stop trips. (If the watchdog ever stopped firing, waitDone
	// would block and `go test -timeout` would surface it; the watchdog's own
	// fire-on-Hard contract is covered directly in watchdog_test.go.)
	o.tick = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	_, err := o.workOne(context.Background(), d)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	if !dtest.HasUpdate(bd, "update zr-w --status=open --assignee=") {
		t.Errorf("hard stop must unclaim; updates=%v", bd.Updates)
	}
	if dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Error("hard stop must NOT add human")
	}
}

func TestRunOne_feedbackClosesSession(t *testing.T) {
	ext := "pr-pool-feedback-zr-c-" + dtest.TestStamp
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: ext, Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: feedbackRole(o), Item: item.Item{ID: "zr-c"}}
	if err := o.RunOne(context.Background(), d); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !dtest.Contains(cc.Ensured, ext) {
		t.Errorf("RunOne must Ensure the stamped session; ensured=%v", cc.Ensured)
	}
	// RunOne must close the very session it launched (same stamped external_id).
	if len(cc.Closed) != 1 || cc.Closed[0] != ext || !cc.ClosedPurge[0] {
		t.Errorf("RunOne must purge-close its one session; closed=%v purge=%v", cc.Closed, cc.ClosedPurge)
	}
}

func TestRunOne_doneWithoutCloseFlagsAndCloses(t *testing.T) {
	ext := "pr-pool-feedback-zr-c-" + dtest.TestStamp
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: ext, Live: true, State: ccpool.StateIdle}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: feedbackRole(o), Item: item.Item{ID: "zr-c"}}
	if err := o.RunOne(context.Background(), d); err == nil {
		t.Fatal("idle-without-close should fail")
	}
	if !dtest.HasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback failure must unclaim; updates=%v", bd.Updates)
	}
	if !dtest.Contains(cc.Closed, ext) {
		t.Errorf("RunOne must still close its session on failure; closed=%v", cc.Closed)
	}
}

// --- E3: per-attempt external_id, purge teardown, stuck-bead escalation ---

// TestWorkOne_usesPerAttemptExternalID: Ensure is addressed by a stamped
// external_id (unique per attempt) while the ccpool --name display label is the
// stable per-bead DisplayName. The stamp is injected via o.stamp for determinism.
func TestWorkOne_usesPerAttemptExternalID(t *testing.T) {
	cfg := fastCfg()
	ext := "pr-pool-worker-zr-w-" + dtest.TestStamp
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: ext, Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	if _, err := o.workOne(context.Background(), d); err != nil {
		t.Fatalf("worker that closes its bead should succeed, got %v", err)
	}
	if !dtest.Contains(cc.Ensured, ext) {
		t.Errorf("Ensure must use the stamped external_id %q; ensured=%v", ext, cc.Ensured)
	}
	if !dtest.Contains(cc.EnsureNames, "pr-pool-worker-zr-w") {
		t.Errorf("Ensure must pass the stable DisplayName as --name; names=%v", cc.EnsureNames)
	}
	// Ensure must carry the prpool.* dispatch metadata (bead/role/pool) so ccpool
	// stamps it atomically via `new --meta` (pg2-5o5i).
	if cc.EnsuredMeta["prpool.bead"] != "zr-w" || cc.EnsuredMeta["prpool.role"] != "worker" || cc.EnsuredMeta["prpool.pool"] != "pr-pool" {
		t.Errorf("Ensure must pass prpool.* dispatch metadata; got %v", cc.EnsuredMeta)
	}
}

// TestTeardownAll_purges: teardownAll closes pr-pool-prefixed sessions with purge=true.
func TestTeardownAll_purges(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-x-" + dtest.TestStamp, Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	o.teardownAll(context.Background())
	if len(cc.Closed) != 1 || cc.Closed[0] != "pr-pool-worker-zr-x-"+dtest.TestStamp {
		t.Fatalf("teardown must close only the pr-pool session; closed=%v", cc.Closed)
	}
	if len(cc.ClosedPurge) != 1 || !cc.ClosedPurge[0] {
		t.Errorf("teardown must purge; closedPurge=%v", cc.ClosedPurge)
	}
}

// TestStuckBead_firstEnsureFailureLabelsLaunchFail: the first time a worker bead
// fails to launch, the orchestrator stamps pool-launch-fail (and does NOT add
// human yet).
func TestStuckBead_firstEnsureFailureLabelsLaunchFail(t *testing.T) {
	cfg := fastCfg()
	// no pool-launch-fail label yet (show returns labels []).
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":[]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	if _, err := o.workOne(context.Background(), d); err == nil {
		t.Fatal("ensure failure should return an error")
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label pool-launch-fail") {
		t.Errorf("first launch failure must add pool-launch-fail; updates=%v", bd.Updates)
	}
	if dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("first launch failure must NOT add human; updates=%v", bd.Updates)
	}
}

// TestStuckBead_secondEnsureFailureEscalatesHuman: a bead already carrying
// pool-launch-fail that fails to launch again is escalated to human.
func TestStuckBead_secondEnsureFailureEscalatesHuman(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":["pool-launch-fail"]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	if _, err := o.workOne(context.Background(), d); err == nil {
		t.Fatal("ensure failure should return an error")
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("repeated launch failure must escalate to human; updates=%v", bd.Updates)
	}
}

// TestStuckBead_successClearsLaunchFailLabel: a SUCCESSFUL dispatch (Ensure
// succeeds and the bead completes) clears any pool-launch-fail label, so the
// escalation counts CONSECUTIVE launch failures, not lifetime ones. Without the
// clear, two non-consecutive failures (separated by a success) would escalate a
// healthy bead to human. (ADR 0015)
func TestStuckBead_successClearsLaunchFailLabel(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w-" + dtest.TestStamp, Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	if _, err := o.workOne(context.Background(), d); err != nil {
		t.Fatalf("worker that closes its bead should succeed, got %v; updates=%v", err, bd.Updates)
	}
	if !dtest.HasUpdate(bd, "update zr-w --remove-label pool-launch-fail") {
		t.Errorf("a successful dispatch must clear pool-launch-fail; updates=%v", bd.Updates)
	}
}

// TestStuckBead_launchFailDoesNotClearLabel: the clear fires ONLY on a
// successful Ensure. A launch failure must not remove pool-launch-fail (else the
// escalation would never trip). Guards the "clear on success only" placement.
func TestStuckBead_launchFailDoesNotClearLabel(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":[]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	if _, err := o.workOne(context.Background(), d); err == nil {
		t.Fatal("ensure failure should return an error")
	}
	if dtest.HasUpdate(bd, "update zr-w --remove-label pool-launch-fail") {
		t.Errorf("a launch failure must NOT clear pool-launch-fail; updates=%v", bd.Updates)
	}
}

// --- progress-marker support: computed values behind the new slog.Info markers ---

// TestDrain_returnsCompleteAndFlaggedCounts locks the per-role tally that feeds
// the "done" progress marker (complete=C flagged=F). One feedback bead closes
// (complete); the other never completes and times out (flagged).
func TestDrain_returnsCompleteAndFlaggedCounts(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxFeedback = 3 // allow all three feedback dispatches through the cap
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{
		"zr-ok":  {"closed"},      // DoneSignal on first poll => complete
		"zr-ok2": {"closed"},      // a second completion => asymmetric (2,1) catches a complete/flagged field swap
		"zr-bad": {"in_progress"}, // never completes => timeout => flagged
	}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-feedback-zr-ok", Live: true, State: ccpool.StateWorking},
		{ExternalID: "pr-pool-feedback-zr-ok2", Live: true, State: ccpool.StateWorking},
		{ExternalID: "pr-pool-feedback-zr-bad", Live: true, State: ccpool.StateWorking},
	}}}
	o := newOrch(cc, bd, cfg)
	ds := []discover.DispatchContext{
		{Role: feedbackRole(o), Item: item.Item{ID: "zr-ok"}},
		{Role: feedbackRole(o), Item: item.Item{ID: "zr-ok2"}},
		{Role: feedbackRole(o), Item: item.Item{ID: "zr-bad"}},
	}
	complete, flagged := o.drain(context.Background(), feedbackRole(o), ds)
	if complete != 2 || flagged != 1 {
		t.Errorf("drain counts = (complete=%d flagged=%d), want (2, 1); updates=%v", complete, flagged, bd.Updates)
	}
}

// TestCreatedByActor locks the computed value behind the per-dispatch "created"
// marker: the bead IDs that appeared during a dispatch AND were created by the
// pool's own actor. Pre-existing beads (in the snapshot) and beads created by a
// different actor (e.g. the pg-pr daemon) during the window are both excluded.
func TestCreatedByActor(t *testing.T) {
	const actor = "pgii-pool__process-feedback"
	pre := map[string]struct{}{"zr-old1": {}, "zr-old2": {}}
	post := []beads.Issue{
		{ID: "zr-old1", CreatedBy: actor},            // pre-existing → excluded by snapshot diff
		{ID: "zr-old2", CreatedBy: "pg-pr daemon"},   // pre-existing → excluded
		{ID: "zr-new-b", CreatedBy: actor},           // new + mine → reported
		{ID: "zr-new-a", CreatedBy: actor},           // new + mine → reported (and sorted before zr-new-b)
		{ID: "zr-daemon", CreatedBy: "pg-pr daemon"}, // new but daemon → excluded
	}
	got := createdByActor(pre, post, actor)
	want := []string{"zr-new-a", "zr-new-b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("createdByActor = %v, want %v (sorted, actor-owned, new only)", got, want)
	}
}

// TestCreatedByActor_none locks the "created none" path: a dispatch where the
// actor created nothing new returns an empty slice (the marker prints "none").
func TestCreatedByActor_none(t *testing.T) {
	const actor = "pgii-pool__worker"
	pre := map[string]struct{}{"zr-x": {}}
	post := []beads.Issue{
		{ID: "zr-x", CreatedBy: actor},        // pre-existing
		{ID: "zr-daemon", CreatedBy: "other"}, // new but not mine
	}
	if got := createdByActor(pre, post, actor); len(got) != 0 {
		t.Errorf("createdByActor = %v, want empty (none created by actor)", got)
	}
}

// TestTeardownAll_returnsClosedCount locks the count that feeds the "teardown"
// progress marker (closed=N) — only pr-pool-prefixed sessions are counted.
func TestTeardownAll_returnsClosedCount(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-a", Live: true},
		{ExternalID: "pr-pool-feedback-zr-b", Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	o := newOrch(cc, &dtest.ScriptBD{}, fastCfg())
	n := o.teardownAll(context.Background())
	if n != 2 {
		t.Errorf("teardownAll closed count = %d, want 2 (pr-pool- sessions only); closed=%v", n, cc.Closed)
	}
}

// TestTeardownAll_preservesNeedsInput: a pr-pool session in needs_input is left
// alive (NOT closed) so the operator can still attach after the pass; other
// pr-pool sessions are still reaped, and the returned count excludes the
// preserved one. (pg2-th35)
func TestTeardownAll_preservesNeedsInput(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-need", Live: true, State: ccpool.StateNeedsInput},
		{ExternalID: "pr-pool-worker-zr-done", Live: true, State: ccpool.StateIdle},
		{ExternalID: "cc-unrelated", Live: true, State: ccpool.StateWorking},
	}}}
	o := newOrch(cc, &dtest.ScriptBD{}, fastCfg())
	n := o.teardownAll(context.Background())
	if n != 1 {
		t.Errorf("teardownAll closed count = %d, want 1 (needs_input preserved, stray excluded); closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pr-pool-worker-zr-done" {
		t.Fatalf("teardown must close the idle pr-pool session only; closed=%v", cc.Closed)
	}
	if dtest.Contains(cc.Closed, "pr-pool-worker-zr-need") {
		t.Errorf("teardown must NOT close a needs_input session; closed=%v", cc.Closed)
	}
}

// TestRunOne_preservesNeedsInputSession: a run-role dispatch that ends with its
// session in needs_input must NOT be purged — it is preserved (left alive) so the
// operator can `ccpool attach` (consistent with teardownAll; pg2-2yn2/pg2-th35).
func TestRunOne_preservesNeedsInputSession(t *testing.T) {
	ext := "pr-pool-feedback-zr-c-" + dtest.TestStamp
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: ext, Live: true, State: ccpool.StateNeedsInput}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: feedbackRole(o), Item: item.Item{ID: "zr-c"}}
	_ = o.RunOne(context.Background(), d) // ends in needs_input (never completes)
	if dtest.Contains(cc.Closed, ext) {
		t.Errorf("RunOne must PRESERVE a needs_input session (no close) so the operator can attach; closed=%v", cc.Closed)
	}
}
