package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/dtest"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/report"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// newExec builds a *ccpoolRun with injected clock/tick + fakes, mirroring the
// orchestrator's newOrch but for the executor seam.
func newExec(cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config) *ccpoolRun {
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	return &ccpoolRun{deps: Deps{
		CC: cc, BD: bd, Cfg: cfg,
		Now: clk.Now, Tick: clk.TickAdvancing(),
	}}
}

func fastCfg() config.Config {
	c := config.Default()
	c.MaxWait = 50 * time.Millisecond
	c.PollInterval = time.Millisecond
	return c
}

func testRoleSet(cfg config.Config) roles.RoleSet {
	return roles.BuiltinRoleSet(roles.BuiltinParams{
		WorktreeDir: cfg.WorktreeDir, SkillMD: cfg.SkillMD, WorkerSkillMD: cfg.WorkerSkillMD,
		MaxFeedback: cfg.MaxFeedback, MaxWorker: cfg.MaxWorker, WorkerBudget: cfg.WorkerBudget(),
	})
}

func roleByName(cfg config.Config, name string) roles.Role {
	for _, r := range testRoleSet(cfg) {
		if r.Name == name {
			return r
		}
	}
	panic("test: role not found: " + name)
}
func feedbackRole(cfg config.Config) roles.Role { return roleByName(cfg, "feedback") }
func workerRole(cfg config.Config) roles.Role   { return roleByName(cfg, "worker") }

// --- waitDone scenarios (ports bats: wait_done cases) ---

func TestWaitDone_workerCloses(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("expected success, got %v; updates=%v", err, bd.Updates)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("success must not unclaim/human; updates=%v", bd.Updates)
	}
}

func TestWaitDone_workerHandbackToOpen(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("handback after seen_claimed should be success, got %v", err)
	}
}

func TestWaitDone_workerTimeoutAddsHumanNoUnclaim(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("timeout should be failure")
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label human") || dtest.HasUpdate(bd, "--status=open") {
		t.Errorf("worker timeout must add human and not unclaim; updates=%v", bd.Updates)
	}
}

func TestWaitDone_paneDiesAsBeadCloses_success(t *testing.T) {
	cfg := fastCfg()
	// first poll: in_progress + live; second poll: status reads closed AND session not live.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateIdle}},
	}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("bead closed as pane died = success, got %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("must not flag on race-success; updates=%v", bd.Updates)
	}
}

func TestWaitDone_paneDiesStillInProgress_failure(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("dead session + in_progress = failure")
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("must add human; updates=%v", bd.Updates)
	}
}

func TestWaitDone_feedbackTimeoutUnclaims(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err == nil {
		t.Fatal("timeout should fail")
	}
	if !dtest.HasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback timeout must unclaim; updates=%v", bd.Updates)
	}
}

func TestWaitDone_transientStatusErrorKeepsPolling(t *testing.T) {
	cfg := fastCfg()
	// First show call for "zr-w" errors; subsequent calls return "closed".
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{"zr-w": {"closed"}},
		ShowErrOnce: map[string]error{"zr-w": errors.New("bd: transient error")},
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("transient status error should not flag bead; got err=%v; updates=%v", err, bd.Updates)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("transient error must not trigger human/unclaim; updates=%v", bd.Updates)
	}
}

func TestWaitDone_ctxCancelDoesNotFail(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true}}}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	err := e.waitDone(ctx, nil, d, "pr-pool-worker-zr-w")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("cancellation must NOT run a failure action; updates=%v", bd.Updates)
	}
}

// TestWaitDone_ctxCancelledBeforeDeathPathNoFail covers the structural
// single-terminal guard (Fix 2): when ctx is already cancelled AND the session
// is reported NOT live AND bead status is in_progress (the death path), waitDone
// must return ctx.Err() and run NO failure action (no bead update).
func TestWaitDone_ctxCancelledBeforeDeathPathNoFail(t *testing.T) {
	cfg := fastCfg()
	// Session is not live on the very first List call — the death path triggers.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before waitDone is called (watchdog already won)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	err := e.waitDone(ctx, nil, d, "pr-pool-worker-zr-w")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled on cancelled-ctx death path, got %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("cancelled-ctx death path must NOT run o.fail; updates=%v", bd.Updates)
	}
}

// pg2-c1vp: when the watchdog wins the single-terminal race, waitDone (the
// loser) must run NO failure action — otherwise the bead ends up both unclaimed
// (watchdog) AND human-labeled (waitDone). An injected claimTerminal that always
// loses drives the death path; it must mutate nothing and exit with ctx.Err().
func TestWaitDone_lostRace_deathPathNoFail(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	claimed := make(chan struct{}, 1)
	claim := func() bool { claimed <- struct{}{}; return false } // always lose the claim
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	resCh := make(chan error, 1)
	go func() { resCh <- e.waitDone(ctx, claim, d, "pr-pool-worker-zr-w") }()
	<-claimed // waitDone reached its terminal decision and lost
	cancel()  // release the loser
	if err := <-resCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("loser should return ctx.Err(), got %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("loser must not mutate the bead; updates=%v", bd.Updates)
	}
}

// pg2-c1vp: the watchdog's terminal unclaim sets the bead to open; waitDone must
// NOT misread that as a successful "open" hand-back when it lost the race (else a
// budget hard-stop is reported as success). A lost "open" must yield ctx.Err().
func TestWaitDone_lostRace_openNotReportedSuccess(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	claimed := make(chan struct{}, 1)
	claim := func() bool { claimed <- struct{}{}; return false }
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	resCh := make(chan error, 1)
	go func() { resCh <- e.waitDone(ctx, claim, d, "pr-pool-worker-zr-w") }()
	<-claimed
	cancel()
	if err := <-resCh; err == nil {
		t.Fatal("a lost 'open' hand-back must NOT be reported as success (nil)")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("loser should return ctx.Err(), got %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("loser must not mutate the bead; updates=%v", bd.Updates)
	}
}

func TestWaitDone_workerDoneStopsFast_failure(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateIdle}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("worker done-without-close must add human; updates=%v", bd.Updates)
	}
	// sessionState (edge check) + active() each call List once on the single
	// stopping poll, so 2 List calls proves the loop stopped immediately.
	if cc.ListIdx != 2 {
		t.Errorf("done must stop on first poll (listIdx=2: sessionState+active), got %d (looped to MaxWait?)", cc.ListIdx)
	}
}

func TestWaitDone_feedbackDoneStopsFast_unclaims(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateIdle}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !dtest.HasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback done-without-close must unclaim; updates=%v", bd.Updates)
	}
	if cc.ListIdx != 2 {
		t.Errorf("done must stop on first poll (listIdx=2), got %d", cc.ListIdx)
	}
}

// Regression guard (passes before AND after the fix): a session that reaches done
// in the same instant its bead closes must still be a SUCCESS via the re-check.
func TestWaitDone_doneStopsFast_successRace(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateIdle}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err != nil {
		t.Fatalf("bead closed as the turn ended = success, got %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("success must not unclaim/flag; updates=%v", bd.Updates)
	}
}

// Lock-in (passes before AND after): needs_input is NOT terminal — a human may
// attach. The loop must keep waiting to MaxWait, then time out and apply OnFailure.
func TestWaitDone_needsInputWaitsUntilMaxWait(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	e := newExec(cc, bd, cfg)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err == nil {
		t.Fatal("needs_input that never resolves should time out (failure)")
	}
	if !dtest.HasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("needs_input timeout must apply OnFailure (feedback unclaim); updates=%v", bd.Updates)
	}
	if cc.ListIdx < 10 {
		t.Errorf("needs_input must keep waiting to MaxWait; listIdx=%d (stopped early?)", cc.ListIdx)
	}
}

func TestActive_stateMapping(t *testing.T) {
	cases := []struct {
		name string
		sess []ccpool.Session
		want bool
	}{
		{"working-live", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateWorking}}, true},
		{"needs_input-live", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateNeedsInput}}, true},
		{"done-live", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateIdle}}, false},
		{"failed-live", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateErrored}}, false},
		{"working-not-live", []ccpool.Session{{ExternalID: "s", Live: false, State: ccpool.StateWorking}}, false},
		{"absent", []ccpool.Session{{ExternalID: "other", Live: true, State: ccpool.StateWorking}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{tc.sess}}, &dtest.ScriptBD{}, fastCfg())
			if got := e.active(context.Background(), "s"); got != tc.want {
				t.Errorf("active(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestSessionState_lookup(t *testing.T) {
	cases := []struct {
		name      string
		sess      []ccpool.Session
		wantState ccpool.SessionState
		wantOK    bool
	}{
		{"present-needs-input", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateNeedsInput}}, ccpool.StateNeedsInput, true},
		{"present-working", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateWorking}}, ccpool.StateWorking, true},
		{"absent", []ccpool.Session{{ExternalID: "other", Live: true, State: ccpool.StateWorking}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{tc.sess}}, &dtest.ScriptBD{}, fastCfg())
			gotState, gotOK := e.sessionState(context.Background(), "s")
			if gotState != tc.wantState || gotOK != tc.wantOK {
				t.Errorf("sessionState(%s) = (%q, %v), want (%q, %v)", tc.name, gotState, gotOK, tc.wantState, tc.wantOK)
			}
		})
	}
}

// readEventLog returns the parsed JSONL records written to path.
func readEventLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil // no file ⇒ no records emitted
	}
	defer func() { _ = f.Close() }()
	var recs []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", sc.Text(), err)
		}
		recs = append(recs, m)
	}
	return recs
}

// countKind returns how many records carry kind == k.
func countKind(recs []map[string]any, k string) int {
	n := 0
	for _, r := range recs {
		if r["kind"] == k {
			n++
		}
	}
	return n
}

// newExecWithLog is newExec plus an eventlog.Writer at logPath on deps.Log.
func newExecWithLog(cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config, logPath string) *ccpoolRun {
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	lw, err := eventlog.New(logPath)
	if err != nil {
		panic(err)
	}
	lw.Now = clk.Now
	return &ccpoolRun{deps: Deps{
		CC: cc, BD: bd, Cfg: cfg, Log: lw,
		Now: clk.Now, Tick: clk.TickAdvancing(),
	}}
}

// A session that sits in needs_input until MaxWait must emit EXACTLY ONE
// needs_input alert (edge-fire-once), naming the external_id, and must still
// run to MaxWait then time out (non-terminal semantics unchanged).
func TestWaitDone_needsInput_alertsOnceOnEdge(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err == nil {
		t.Fatal("needs_input that never resolves must still time out (non-terminal)")
	}
	if cc.ListIdx < 10 {
		t.Errorf("needs_input must keep polling to MaxWait; listIdx=%d", cc.ListIdx)
	}
	recs := readEventLog(t, logPath)
	if n := countKind(recs, "needs_input"); n != 1 {
		t.Fatalf("needs_input alert must fire exactly once on the edge; got %d records: %v", n, recs)
	}
	var alert map[string]any
	for _, r := range recs {
		if r["kind"] == "needs_input" {
			alert = r
		}
	}
	if got, _ := alert["session"].(string); got != "pr-pool-feedback-zr-c" {
		t.Errorf("alert must name the external_id session; session=%q rec=%v", got, alert)
	}
	if lvl, _ := alert["level"].(string); lvl != "warn" {
		t.Errorf("needs_input alert level = %q, want warn", lvl)
	}
}

// A session that is working first, THEN goes needs_input, THEN resolves to a
// closed bead must alert once (only on the working→needs_input edge) and end
// successfully.
func TestWaitDone_needsInput_edgeNotEveryPoll(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	// status stays in_progress, then closed on the last read.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "in_progress", "in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}},
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}},
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateWorking}},
	}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	_ = e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c")
	recs := readEventLog(t, logPath)
	if n := countKind(recs, "needs_input"); n != 1 {
		t.Fatalf("alert must fire once across two consecutive needs_input polls, got %d: %v", n, recs)
	}
}

// A session that NEVER reaches needs_input must emit NO needs_input alert.
func TestWaitDone_noNeedsInput_noAlert(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateWorking}}}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err != nil {
		t.Fatalf("working→closed should succeed, got %v", err)
	}
	if n := countKind(readEventLog(t, logPath), "needs_input"); n != 0 {
		t.Errorf("no needs_input ⇒ no alert; got %d", n)
	}
}

// --- pg2-kj7j: Dispatch reports the failure verb actually taken ---

func dispatchWorker(t *testing.T, cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config, ext string) (report.Result, error) {
	t.Helper()
	cfg.WorktreeDir = t.TempDir() // isolate the per-bead worktree to a throwaway dir
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = ext
	deps.Git = &dtest.NoopGit{} // never shell out to real git in tests
	return ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
}

func verbOf(res report.Result) report.Verb {
	if len(res.Actions) == 0 {
		return ""
	}
	return res.Actions[0].Verb
}

// pg2-yukh #2: the worker session must launch in a FRESH per-bead worktree
// (<WorktreeDir>/<beadID>), never the shared monorepo at Cfg.RepoRoot.
func TestDispatch_launchesInFreshPerBeadWorktree(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	// Bead closes fast so run() returns cleanly (mirror TestWaitDone_workerCloses).
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pr-pool-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	_, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err != nil {
		t.Fatalf("dispatch should succeed (bead closed), got %v", err)
	}
	want := filepath.Join(cfg.WorktreeDir, "zr-w")
	if cc.EnsuredCwd != want {
		t.Errorf("session launched at %q, want fresh worktree %q (not RepoRoot %q)", cc.EnsuredCwd, want, cfg.RepoRoot)
	}
}

// TestDispatch_reviewRole_completeOnClose exercises the pg2-ynhr.3 review role
// end-to-end through the ccpool executor: a review-pr bead (task + "review-pr: "
// prefix + PR-coord metadata) is dispatched, the ported prompt renders, and the
// dispatch completes cleanly when the agent closes the bead (complete-on-close),
// with no unclaim/human on success.
func TestDispatch_reviewRole_completeOnClose(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-rv": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-review-zr-rv", Live: true, State: ccpool.StateWorking}}}}
	d := discover.DispatchContext{
		Role: roleByName(cfg, "review"),
		Item: item.Item{
			ID: "zr-rv", Type: "task", Title: "review-pr: o/r#7",
			Metadata: map[string]any{
				"repo": "o/r", "pr_number": float64(7), "branch": "feat/x", "head_sha": "abc123",
			},
		},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pr-pool-review-zr-rv"
	deps.Git = &dtest.NoopGit{}
	_, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err != nil {
		t.Fatalf("review dispatch should succeed on bead close, got %v; updates=%v", err, bd.Updates)
	}
	// complete-on-close: the bead closed, so no failure handling (unclaim / add-human).
	for _, u := range bd.Updates {
		if strings.Contains(u, "--status=open") || strings.Contains(u, "--add-label human") {
			t.Errorf("successful review must not unclaim/add-human; got %q", u)
		}
	}
}

func TestDispatch_ensureFailFirst_noVerb(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":[]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	res, err := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if err == nil {
		t.Fatal("ensure failure should error")
	}
	if v := verbOf(res); v != "" {
		t.Errorf("first launch-fail (label only) must report NO verb, got %q", v)
	}
}

func TestDispatch_ensureFailRepeat_escalated(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":["pool-launch-fail"]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	res, _ := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if v := verbOf(res); v != report.Escalated {
		t.Errorf("repeat launch-fail must report Escalated, got %q", v)
	}
}

func TestDispatch_sendFailWorkerLeave_noVerb(t *testing.T) {
	cfg := fastCfg() // worker on_dispatch_fail = leave
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend}
	res, err := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if err == nil {
		t.Fatal("send failure should error")
	}
	if v := verbOf(res); v != "" {
		t.Errorf("worker send-fail (leave) must report NO verb, got %q", v)
	}
}

func TestDispatch_sendFailFeedbackUnclaim_unclaimed(t *testing.T) {
	cfg := fastCfg() // feedback on_dispatch_fail = unclaim
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend}
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pr-pool-feedback-zr-c"
	deps.Git = &dtest.NoopGit{}
	res, _ := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("feedback send-fail (unclaim) must report Unclaimed, got %q", v)
	}
}

// pg2-yukh #1: a CONFIRMED dropped nudge (ccpool.ErrPromptNotIngested) must hand
// the worker bead back UNCLAIMED even though the worker role's on_dispatch_fail is
// "leave" — leaving it claimed would let the budget watchdog later nudge a
// context-less model. The session did nothing, so NO other bead may be touched.
func TestDispatch_droppedNudge_handsBackNoOtherBeadTouched(t *testing.T) {
	cfg := fastCfg() // worker on_dispatch_fail = leave
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested}
	res, err := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if err == nil {
		t.Fatal("dropped nudge must return an error")
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("dropped nudge must hand the bead back unclaimed; verb=%q res=%+v", v, res)
	}
	if !dtest.HasUpdate(bd, "update zr-w --status=open --assignee=") {
		t.Errorf("dropped nudge must unclaim zr-w; updates=%v", bd.Updates)
	}
	// The ONLY bead mutation may be on zr-w (the unclaim). No comment, no update to
	// any other id.
	for _, c := range bd.Updates {
		if strings.Contains(c, "comment") || !strings.Contains(c, "zr-w") {
			t.Errorf("no other bead may be touched on a dropped nudge; got %q", c)
		}
	}
}

// pg2-yukh AC#4: the deterministic stand-in for the live lost-prompt repro. A
// worker dispatched for zr-6bq.3 whose initial nudge is dropped (zero model turns)
// must end with the bead handed back and NO write to any other bead (the incident
// wrote to zr-o8el2). The store is pre-loaded with tempting in-progress targets a
// context-less guess WOULD reach for; none may be touched.
func TestRegression_droppedNudge_noWriteToOtherBead_pg2yukh(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	// The incident's tempting targets — present in the store but must stay untouched.
	bd := &dtest.ScriptBD{
		StatusSeq: map[string][]string{
			"zr-o8el2": {"in_progress"},
			"zr-n6uo":  {"in_progress"},
			"zr-meaz":  {"in_progress"},
		},
	}
	cc := &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested}
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-6bq.3"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pr-pool-worker-zr-6bq.3"
	deps.Git = &dtest.NoopGit{}
	res, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("dropped nudge must fail the dispatch")
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("bead must be handed back unclaimed; verb=%q res=%+v", v, res)
	}
	if !dtest.HasUpdate(bd, "update zr-6bq.3 --status=open --assignee=") {
		t.Errorf("must unclaim zr-6bq.3; updates=%v", bd.Updates)
	}
	// The unclaim is the ONLY permitted update; no update may touch another id.
	for _, u := range bd.Updates {
		if !strings.Contains(u, "zr-6bq.3") {
			t.Errorf("no update may touch a bead other than zr-6bq.3; got %q", u)
		}
		for _, other := range []string{"zr-o8el2", "zr-n6uo", "zr-meaz"} {
			if strings.Contains(u, other) {
				t.Errorf("must not update unrelated bead %s; got %q", other, u)
			}
		}
	}
	// The incident shape: the lost-nudge worker wrote a wrap-up COMMENT to an
	// unrelated bead (zr-o8el2). ScriptBD now records comment calls, so this
	// assertion has teeth — it fails if ANY comment lands on a bead other than the
	// assigned zr-6bq.3 (and a dropped nudge should produce no comment at all).
	for _, c := range bd.Comments {
		if !strings.Contains(c, "zr-6bq.3") {
			t.Errorf("dropped nudge must write NO comment to any bead other than zr-6bq.3; got %q", c)
		}
		for _, other := range []string{"zr-o8el2", "zr-n6uo", "zr-meaz"} {
			if strings.Contains(c, other) {
				t.Errorf("must not comment on unrelated bead %s; got %q", other, c)
			}
		}
	}
}

func TestDispatch_waitFailWorkerTimeout_escalated(t *testing.T) {
	cfg := fastCfg() // worker on_failure = add-human
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	res, _ := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if v := verbOf(res); v != report.Escalated {
		t.Errorf("worker timeout must report Escalated, got %q", v)
	}
}

func TestDispatch_watchdogHardStop_unclaimed(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000 // finite cap so the ramp trips it
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, TranscriptPath: "/t", CWD: "/repo"}}}}
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pr-pool-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately >100%
	res, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("budget hard-stop unclaims => must report Unclaimed (NOT Escalated), got %q", v)
	}
}
