package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/prompt"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
	"github.com/phillipgreenii/x/gitclient"
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

// feedbackPromptBody / workerPromptBody mirror the built-in role prompt
// bodies packages/pg-router/internal/roles/builtin.go authors (this module
// keeps no dependency on that package — Go's internal-package visibility
// rule, docs/adr/0065's Addendum — so these test fixtures build the SAME two
// roles by value instead of via a ported BuiltinRoleSet/BuiltinParams).
const (
	feedbackPromptBody = `Read {{.SkillMD}} and process process-feedback cycle {{.BeadID}}.`
	workerPromptBody   = `Read {{.SkillMD}} and implement work bead {{.BeadID}}.`
	reviewPromptBody   = `Review pull request {{index .Item.Metadata "repo"}}#{{index .Item.Metadata "pr_number"}}.`
)

// feedbackRole / workerRole build the two built-in ccpool roles this test
// file dispatches against, matching packages/pg-router/internal/roles.
// BuiltinRoleSet's "feedback"/"worker" entries field-for-field (Completion/
// OnFailure/OnDispatchFail/AuthorshipGuard) — only the prompt text itself is
// abbreviated, since these tests never render or assert on it.
func feedbackRole(config.Config) roles.Role {
	return roles.Role{
		Name: "feedback", Type: "ccpool",
		CCPool: &roles.CCPoolConfig{
			Actor: "pgii-pool__process-feedback", SkillMD: "",
			Completion: roles.CloseOnly, OnFailure: roles.Unclaim, OnDispatchFail: roles.DispatchUnclaim,
			AuthorshipGuard: false, PromptBody: feedbackPromptBody, Prompt: mustParsePrompt("feedback", feedbackPromptBody),
			Budget: budget.Budget{}, // unlimited => no watchdog
		},
	}
}

func workerRole(cfg config.Config) roles.Role {
	return roles.Role{
		Name: "worker", Type: "ccpool",
		CCPool: &roles.CCPoolConfig{
			Actor: "pgii-pool__worker", SkillMD: "",
			Completion: roles.CloseOrHandback, OnFailure: roles.AddHuman, OnDispatchFail: roles.DispatchLeave,
			AuthorshipGuard: true, PromptBody: workerPromptBody, Prompt: mustParsePrompt("worker", workerPromptBody),
			Budget: cfg.WorkerBudget(),
		},
	}
}

func reviewRole(cfg config.Config) roles.Role {
	return roles.Role{
		Name: "review", Type: "ccpool",
		CCPool: &roles.CCPoolConfig{
			Actor: "pgii-pool__review", SkillMD: "",
			Completion: roles.CloseOrHandback, OnFailure: roles.AddHuman, OnDispatchFail: roles.DispatchLeave,
			AuthorshipGuard: false, PromptBody: reviewPromptBody, Prompt: mustParsePrompt("review", reviewPromptBody),
			Budget: cfg.WorkerBudget(),
		},
	}
}

func mustParsePrompt(name, body string) *template.Template {
	t, err := prompt.Parse(name, body)
	if err != nil {
		panic("test: prompt failed to parse: " + err.Error())
	}
	return t
}

// --- waitDone scenarios (ports bats: wait_done cases) ---

func TestWaitDone_workerCloses(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err != nil {
		t.Fatalf("expected success, got %v; updates=%v", err, bd.Updates)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("success must not unclaim/human; updates=%v", bd.Updates)
	}
}

func TestWaitDone_workerHandbackToOpen(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err != nil {
		t.Fatalf("handback after seen_claimed should be success, got %v", err)
	}
}

func TestWaitDone_workerTimeoutAddsHumanNoUnclaim(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err == nil {
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
		{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateIdle}},
	}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err != nil {
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
		{{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err == nil {
		t.Fatal("dead session + in_progress = failure")
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("must add human; updates=%v", bd.Updates)
	}
}

func TestWaitDone_feedbackTimeoutUnclaims(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-feedback-zr-c"); err == nil {
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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err != nil {
		t.Fatalf("transient status error should not flag bead; got err=%v; updates=%v", err, bd.Updates)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("transient error must not trigger human/unclaim; updates=%v", bd.Updates)
	}
}

func TestWaitDone_ctxCancelDoesNotFail(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true}}}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	err := e.waitDone(ctx, nil, d, "pg-router-worker-zr-w")
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
		{{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before waitDone is called (watchdog already won)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	err := e.waitDone(ctx, nil, d, "pg-router-worker-zr-w")
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
		{{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	claimed := make(chan struct{}, 1)
	claim := func() bool { claimed <- struct{}{}; return false } // always lose the claim
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	resCh := make(chan error, 1)
	go func() { resCh <- e.waitDone(ctx, claim, d, "pg-router-worker-zr-w") }()
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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	claimed := make(chan struct{}, 1)
	claim := func() bool { claimed <- struct{}{}; return false }
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	resCh := make(chan error, 1)
	go func() { resCh <- e.waitDone(ctx, claim, d, "pg-router-worker-zr-w") }()
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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateIdle}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("worker done-without-close must add human; updates=%v", bd.Updates)
	}
	// sessionState (edge check) + active() each call List once, then closeReason
	// (INV-CCH-7) calls List twice more (its own bounded re-read, since this
	// session's CloseReason is unset/present) — 4 List calls on the single
	// stopping poll proves the loop stopped immediately (not looped to MaxWait).
	if cc.ListIdx != 4 {
		t.Errorf("done must stop on first poll (listIdx=4: sessionState+active+closeReason's 2), got %d (looped to MaxWait?)", cc.ListIdx)
	}
}

func TestWaitDone_feedbackDoneStopsFast_unclaims(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateIdle}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-feedback-zr-c"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !dtest.HasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback done-without-close must unclaim; updates=%v", bd.Updates)
	}
	// See TestWaitDone_workerDoneStopsFast_failure's comment: sessionState +
	// active() + closeReason's own bounded re-read = 4 List calls.
	if cc.ListIdx != 4 {
		t.Errorf("done must stop on first poll (listIdx=4), got %d", cc.ListIdx)
	}
}

// Regression guard (passes before AND after the fix): a session that reaches done
// in the same instant its bead closes must still be a SUCCESS via the re-check.
func TestWaitDone_doneStopsFast_successRace(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateIdle}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-feedback-zr-c"); err != nil {
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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-feedback-zr-c"); err == nil {
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

// --- INV-CCH-7: eviction-aware death branch, breadcrumb + two-strike escalation ---

// TestWaitDone_deathByCloseReason covers waitDone's death branch across every
// close-reason shape: the three external reasons (idle_ttl, cap_eviction,
// operator) must release the bead with a comment and NOT apply the role's
// on_failure; a "handler" close, an unstamped ("") reason, and an absent row
// must all fall through to the role's normal on_failure (human, for the
// worker role used here) — unchanged from before this packet.
func TestWaitDone_deathByCloseReason(t *testing.T) {
	cases := []struct {
		name        string
		reason      string // "" = unstamped; "absent" = row missing from list
		wantUnclaim bool
		wantHuman   bool
		wantComment bool
	}{
		{"cap_eviction", "cap_eviction", true, false, true},
		{"idle_ttl", "idle_ttl", true, false, true},
		{"operator", "operator", true, false, true},
		{"handler is not external", "handler", false, true, false},
		{"unstamped death", "", false, true, false},
		{"absent row", "absent", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastCfg()
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "in_progress", "in_progress"}}}
			row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateWorking, CloseReason: tc.reason}
			var seq [][]ccpool.Session
			if tc.reason == "absent" {
				seq = [][]ccpool.Session{{}, {}, {}}
			} else {
				seq = [][]ccpool.Session{{row}, {row}, {row}}
			}
			cc := &dtest.FakeCC{ListSeq: seq}
			e := newExec(cc, bd, cfg)
			d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}} // workerRole's OnFailure is add-human
			err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w")
			if err == nil {
				t.Fatal("death must return an error")
			}
			if got := dtest.HasUpdate(bd, "update zr-w --status=open --assignee="); got != tc.wantUnclaim {
				t.Errorf("unclaimed = %v, want %v; updates=%v", got, tc.wantUnclaim, bd.Updates)
			}
			if got := dtest.HasUpdate(bd, "update zr-w --add-label human"); got != tc.wantHuman {
				t.Errorf("human = %v, want %v; updates=%v", got, tc.wantHuman, bd.Updates)
			}
			if got := len(bd.Comments) > 0; got != tc.wantComment {
				t.Errorf("comment = %v, want %v; comments=%v", got, tc.wantComment, bd.Comments)
			}
		})
	}
}

// TestWaitDone_secondExternalCloseEscalates proves the two-strike idiom: a bead
// that already carries the pool-evicted breadcrumb (from a PRIOR external
// close) must be escalated to human on THIS external close too, in addition
// to still being released.
func TestWaitDone_secondExternalCloseEscalates(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq: map[string][]string{"zr-w": {"in_progress", "in_progress"}},
		Labels:    map[string][]string{"zr-w": {"pool-evicted"}}, // seeded from a prior external close
	}
	row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateWorking, CloseReason: "cap_eviction"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_ = e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w")
	if !dtest.HasUpdate(bd, "update zr-w --status=open --assignee=") {
		t.Fatalf("still released; updates=%v", bd.Updates)
	}
	if !dtest.HasUpdate(bd, "update zr-w --add-label human") {
		t.Fatalf("second consecutive external close must add human; updates=%v", bd.Updates)
	}
}

// TestWaitFailureResult_externallyClosedMapsToUnclaimed proves the wait-failure
// mapping reports ErrExternallyClosed as Unclaimed even when the role's own
// OnFailure is AddHuman — the external-close branch must take priority over
// (not merge with) the role's configured on_failure.
func TestWaitFailureResult_externallyClosedMapsToUnclaimed(t *testing.T) {
	e := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, fastCfg())
	cc := workerRole(fastCfg()).CCPool // OnFailure: AddHuman
	err := fmt.Errorf("zr-w: %w", ErrExternallyClosed)
	got := e.waitFailureResult(cc, "zr-w", err)
	if got.Actions[0].Verb != report.Unclaimed {
		t.Fatalf("got %+v", got)
	}
}

// --- pg2-4roho: dispatch-completion worktree cleanup (decision items 1/2/5) ---

// TestNeedsInputAlive_cases mirrors TestSessionState_lookup's table shape,
// covering needsInputAlive's three outcomes: present-and-needs_input (true,
// skip cleanup), present-in-some-other-state (false), absent (false, gone —
// safe to clean up), and a List error (true — can't tell, so fail toward NOT
// deleting; the opposite bias from active()'s own can't-tell case).
func TestNeedsInputAlive_cases(t *testing.T) {
	cases := []struct {
		name    string
		sess    []ccpool.Session
		listErr error
		want    bool
	}{
		{"present-needs-input", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateNeedsInput}}, nil, true},
		{"present-working", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateWorking}}, nil, false},
		{"absent", []ccpool.Session{{ExternalID: "other", Live: true, State: ccpool.StateNeedsInput}}, nil, false},
		{"list-error-cant-tell", nil, errors.New("ccpool list: transient"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{tc.sess}, ListErr: tc.listErr}
			e := newExec(cc, &dtest.ScriptBD{}, fastCfg())
			if got := e.needsInputAlive(context.Background(), "s"); got != tc.want {
				t.Errorf("needsInputAlive(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestUsesWorktreeIsolation locks in newIsolation's own empty/"worktree"
// default against the other three strategies — the guard that keeps
// cleanupWorktree from ever removing RepoRoot ("none"), a fixed reused
// directory ("path"), or a workforest root ("workforest").
func TestUsesWorktreeIsolation(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"", true},
		{"worktree", true},
		{"none", false},
		{"path", false},
		{"workforest", false},
	}
	for _, tc := range cases {
		if got := usesWorktreeIsolation(roles.IsolationConfig{Type: tc.typ}); got != tc.want {
			t.Errorf("usesWorktreeIsolation(%q) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

// newExecWithOpener is newExec plus a NoopGitOpener wired onto deps.GitOpener,
// returning both so a test can inspect the opener's own WTM.Calls.
func newExecWithOpener(cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config) (*ccpoolRun, *dtest.NoopGitOpener) {
	e := newExec(cc, bd, cfg)
	opener := &dtest.NoopGitOpener{}
	e.deps.GitOpener = opener.Open
	return e, opener
}

func TestCleanupWorktree_emptyPathNoop(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "s", Live: true, State: ccpool.StateWorking}}}}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "")
	if len(opener.WTM.Calls) != 0 || cc.ListIdx != 0 {
		t.Errorf("empty wt must short-circuit before any List/RemoveWorktree call; listIdx=%d calls=%v", cc.ListIdx, opener.WTM.Calls)
	}
}

func TestCleanupWorktree_nonWorktreeIsolationNoop(t *testing.T) {
	for _, typ := range []string{"none", "path", "workforest"} {
		t.Run(typ, func(t *testing.T) {
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "s", Live: true, State: ccpool.StateWorking}}}}
			e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
			e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{Isolation: roles.IsolationConfig{Type: typ}}, "s", "/some/shared/path")
			if len(opener.WTM.Calls) != 0 {
				t.Errorf("isolation %q must never be removed by cleanupWorktree; calls=%v", typ, opener.WTM.Calls)
			}
		})
	}
}

func TestCleanupWorktree_removesWhenSessionGone(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{}}} // no sessions at all — definitively gone
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	wt := "/tmp/pg2-4roho/zr-w"
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", wt)
	if len(opener.WTM.Calls) != 1 {
		t.Fatalf("expected exactly one RemoveWorktree call, got %v", opener.WTM.Calls)
	}
	got := opener.WTM.Calls[0]
	if got[0] != "remove" || got[1] != wt || got[2] != "false" {
		t.Errorf("RemoveWorktree call = %v, want [remove %s false] (force=false is decision item 5's own guard)", got, wt)
	}
}

// TestCleanupWorktree_skipsWhileNeedsInput is the regression guard for the
// exact bug pg2-lyriv reported (a needs_input session's worktree deleted out
// from under it): decision item 2 ties a needs_input worktree's lifetime to
// its SESSION's own close, not to this dispatch's own terminal outcome.
func TestCleanupWorktree_skipsWhileNeedsInput(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "s", Live: true, State: ccpool.StateNeedsInput}}}}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "/tmp/pg2-4roho/zr-w")
	if len(opener.WTM.Calls) != 0 {
		t.Errorf("needs_input session's worktree must NOT be removed; calls=%v", opener.WTM.Calls)
	}
}

func TestCleanupWorktree_skipsOnListError(t *testing.T) {
	cc := &dtest.FakeCC{ListErr: errors.New("ccpool list: transient")}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "/tmp/pg2-4roho/zr-w")
	if len(opener.WTM.Calls) != 0 {
		t.Errorf("a can't-tell List error must fail toward NOT deleting; calls=%v", opener.WTM.Calls)
	}
}

// TestCleanupWorktree_removeFailsSoft proves a RemoveWorktree failure (e.g.
// git itself refusing a dirty worktree, decision item 5's own native guard)
// neither panics nor is surfaced as an error — it is logged and left for the
// next sweep.
func TestCleanupWorktree_removeFailsSoft(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{}}}
	e := newExec(cc, &dtest.ScriptBD{}, fastCfg())
	e.deps.GitOpener = func(context.Context, string) (gitclient.WorktreeManager, error) {
		return nil, errors.New("open failed")
	}
	// Should not panic; failure is swallowed (fail-soft).
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "/tmp/pg2-4roho/zr-w")
}

// --- end pg2-4roho unit tests; dispatch-level end-to-end coverage lives near
// the other TestDispatch_* cases below (TestDispatch_success_removesWorktree,
// TestDispatch_needsInputTimeout_doesNotRemoveWorktree,
// TestDispatch_watchdogHardStop_removesWorktree). ---

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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-feedback-zr-c"); err == nil {
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
	if got, _ := alert["session"].(string); got != "pg-router-feedback-zr-c" {
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
		{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}},
		{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}},
		{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateWorking}},
	}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	_ = e.waitDone(context.Background(), nil, d, "pg-router-feedback-zr-c")
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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateWorking}}}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-feedback-zr-c"); err != nil {
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
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = ext
	deps.Git = &dtest.NoopGit{}                    // never shell out to real git in tests
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open // ditto, for per-bead worktree creation
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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
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
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-review-zr-rv", Live: true, State: ccpool.StateWorking}}}}
	d := DispatchContext{
		Role: reviewRole(cfg),
		Item: item.Item{
			ID: "zr-rv", Type: "task", Title: "review-pr: o/r#7",
			Metadata: map[string]any{
				"repo": "o/r", "pr_number": float64(7), "branch": "feat/x", "head_sha": "abc123",
			},
		},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-review-zr-rv"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
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
	res, err := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w")
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
	res, _ := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w")
	if v := verbOf(res); v != report.Escalated {
		t.Errorf("repeat launch-fail must report Escalated, got %q", v)
	}
}

func TestDispatch_sendFailWorkerLeave_noVerb(t *testing.T) {
	cfg := fastCfg() // worker on_dispatch_fail = leave
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend}
	res, err := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w")
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
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-feedback-zr-c"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
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
	res, err := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w")
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
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-6bq.3"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-worker-zr-6bq.3"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
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

// --- admission gate (ADR 0072's Decision item 3) ---

// TestRun_poolFullDeclinesBusy proves the gate declines busy — no Ensure, no
// worktree, no bead mutation — when the pool reports free == 0.
func TestRun_poolFullDeclinesBusy(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{Cap: ccpool.Capacity{MaxSessions: 6, Counted: 6, Free: 0}}
	e := newExec(cc, bd, cfg)
	g := &dtest.NoopGitOpener{}
	e.deps.GitOpener = g.Open
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("err = %v, want ErrPoolAtCapacity", err)
	}
	if len(cc.Ensured) != 0 {
		t.Fatal("Ensure must not be called when the pool is full")
	}
	if len(g.Calls) != 0 {
		t.Fatal("no worktree may be prepared when the pool is full")
	}
	if len(bd.Updates) != 0 {
		t.Fatalf("bead mutated: %v", bd.Updates)
	}
}

// TestRun_poolCapacityErrorDeclinesBusy proves an unreadable pool fails
// CLOSED as busy — never as "launch anyway".
func TestRun_poolCapacityErrorDeclinesBusy(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{CapErr: errors.New("capacity: store: disk I/O error")}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("unknown pool must fail closed as busy; err = %v", err)
	}
	if len(cc.Ensured) != 0 || len(bd.Updates) != 0 {
		t.Fatal("launched or mutated despite unknown capacity")
	}
}

// TestRun_notIngestedClosesSession proves a never-ingested session is closed
// (reason handler, via the CLI runner) before the bead is unclaimed, so a
// ready-but-empty row does not hold a counted slot until idle_ttl.
func TestRun_notIngestedClosesSession(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{Cap: ccpool.Capacity{Free: 3}, SendErr: ccpool.ErrPromptNotIngested}
	_, _ = dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w")
	if len(cc.Closed) != 1 {
		t.Fatalf("a never-ingested session must be closed so it does not occupy a counted slot; closes=%v", cc.Closed)
	}
	if !dtest.HasUpdate(bd, "update zr-w --status=open --assignee=") {
		t.Fatalf("bead must be unclaimed (existing behavior); updates=%v", bd.Updates)
	}
}

func TestDispatch_waitFailWorkerTimeout_escalated(t *testing.T) {
	cfg := fastCfg() // worker on_failure = add-human
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	res, _ := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w")
	if v := verbOf(res); v != report.Escalated {
		t.Errorf("worker timeout must report Escalated, got %q", v)
	}
}

// TestDispatch_crashWindowRedelivery_absorbsIntoExistingSession is Task 5.9's
// INV-EVT-2 test: the SAME event dispatched TWICE — as a crash-window
// redelivery would, each attempt minting its OWN fresh per-attempt
// ExternalID (Role.ExternalID's own stamp) — must launch exactly ONE ccpool
// session for the pair, correlated on the stable per-bead DisplayName
// (Role.DisplayName), not the per-attempt id (ADR 0065's "Register" section's
// INV-EVT-2 note). The second (redelivered) dispatch must be absorbed into
// the first session's own outcome rather than starting a second session.
func TestDispatch_crashWindowRedelivery_absorbsIntoExistingSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")

	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{}, // no session yet — the FIRST dispatch's own duplicate-absorption check
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking}},
		// att-1's own session has since settled by the time the redelivery lands.
		{{ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle}},
	}}
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}}

	dispatch := func(externalID string) (report.Result, error) {
		deps := newExec(cc, bd, cfg).deps
		deps.ExternalID = externalID
		deps.Git = &dtest.NoopGit{}
		deps.GitOpener = (&dtest.NoopGitOpener{}).Open
		return ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	}

	// First delivery: att-1's own session is created and runs to a normal close.
	res1, err1 := dispatch("att-1")
	if err1 != nil {
		t.Fatalf("first dispatch should succeed, got %v", err1)
	}
	if v := verbOf(res1); v != "" {
		t.Errorf("first dispatch success must report no verb, got %q", v)
	}

	// Redelivery: a fresh per-attempt ExternalID ("att-2"), same event/item/
	// role — so the same DisplayName. Must be absorbed, not re-dispatched.
	res2, err2 := dispatch("att-2")
	if err2 != nil {
		t.Fatalf("redelivery must be absorbed as success (existing outcome), got %v", err2)
	}
	if v := verbOf(res2); v != "" {
		t.Errorf("absorbed redelivery of an already-settled success must report no verb, got %q", v)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-1" {
		t.Errorf("exactly one ccpool session must ever be created for the pair; Ensured=%v", got)
	}
	if got := cc.Sent; len(got) != 1 || got[0] != "att-1" {
		t.Errorf("exactly one Send (the first attempt's own nudge) may ever happen; Sent=%v", got)
	}
}

func TestDispatch_watchdogHardStop_unclaimed(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000 // finite cap so the ramp trips it
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, TranscriptPath: "/t", CWD: "/repo"}}}}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	// This test exercises the real waitDone/watchdog race (workerWaitWithWatchdog),
	// not a single deterministic path, so it must not let the two racers run on
	// mismatched clocks: newExec's default Tick advances the manual clock with NO
	// real sleep, while the watchdog's own Poll wait is a real time.After. That
	// mismatch let waitDone's MaxWait deadline spin through all its iterations
	// (pure CPU, no real elapsed time) and occasionally win the terminal-claim
	// race before the watchdog's near-zero-cost first budget check ever got a
	// scheduler slice — reproduced at ~1/200 under GOMAXPROCS=1 (matching a
	// contended/single-core nix build sandbox), reporting Escalated instead of
	// Unclaimed. Wrapping Tick with a real sleep puts both racers on the same
	// wall-clock timeline (as in production), so the watchdog reliably wins by a
	// wide margin; the wrapped call still advances the manual clock afterward, so
	// a genuine regression in the watchdog still fails fast via the deadline path
	// instead of hanging.
	origTick := deps.Tick
	deps.Tick = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		return origTick(ctx, d)
	}
	deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately >100%
	res, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("budget hard-stop unclaims => must report Unclaimed (NOT Escalated), got %q", v)
	}
}

// --- pg2-4roho: dispatch-level end-to-end worktree cleanup coverage ---

// TestDispatch_success_removesWorktree confirms decision item 1's primary
// fix end-to-end: a normal successful dispatch removes its own per-bead
// worktree immediately (RemoveWorktree with force=false — decision item 5's
// own guard), rather than waiting on preshutdown.go's once-per-process sweep.
func TestDispatch_success_removesWorktree(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	opener := &dtest.NoopGitOpener{}
	deps.GitOpener = opener.Open
	_, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err != nil {
		t.Fatalf("dispatch should succeed (bead closed), got %v", err)
	}
	wantWT := filepath.Join(cfg.WorktreeDir, "zr-w")
	if !hasRemoveCall(opener, wantWT, false) {
		t.Errorf("expected a RemoveWorktree(force=false) call for %s; calls=%v", wantWT, opener.WTM.Calls)
	}
}

// TestDispatch_needsInputTimeout_doesNotRemoveWorktree is the pg2-lyriv
// regression guard at the Dispatch level: a session that stays needs_input
// through MaxWait is still reported as a failure (unaffected — matches
// TestWaitDone_needsInputWaitsUntilMaxWait), but its worktree must NOT be
// removed by this dispatch call — decision item 2 ties it to the SESSION's
// own eventual close instead, and reimplementing that tie is out of this
// bead's own scope, but this dispatch-level fix must not violate it.
func TestDispatch_needsInputTimeout_doesNotRemoveWorktree(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	d := DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-feedback-zr-c"
	deps.Git = &dtest.NoopGit{}
	opener := &dtest.NoopGitOpener{}
	deps.GitOpener = opener.Open
	_, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("needs_input that never resolves should still time out as a failure")
	}
	if anyRemoveCall(opener) {
		t.Errorf("needs_input session's worktree must NOT be removed; calls=%v", opener.WTM.Calls)
	}
}

// TestDispatch_watchdogHardStop_removesWorktree extends
// TestDispatch_watchdogHardStop_unclaimed with the worktree-cleanup
// assertion: a budget hard-stop (the "timeout" of decision item 1's three
// terminal outcomes) removes the worktree too — this fixture's session is
// not needs_input, so needsInputAlive must not block it.
func TestDispatch_watchdogHardStop_removesWorktree(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000 // finite cap so the ramp trips it
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, TranscriptPath: "/t", CWD: "/repo"}}}}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	opener := &dtest.NoopGitOpener{}
	deps.GitOpener = opener.Open
	// Same real-clock wrapping as TestDispatch_watchdogHardStop_unclaimed —
	// see that test's own comment for why the watchdog and waitDone's manual
	// clock must share a wall-clock timeline here.
	origTick := deps.Tick
	deps.Tick = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		return origTick(ctx, d)
	}
	deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately >100%
	_, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	wantWT := filepath.Join(cfg.WorktreeDir, "zr-w")
	if !hasRemoveCall(opener, wantWT, false) {
		t.Errorf("expected a RemoveWorktree(force=false) call for %s after budget hard-stop; calls=%v", wantWT, opener.WTM.Calls)
	}
}

// hasRemoveCall reports whether opener's WTM recorded a "remove" call for
// path with exactly the given force value.
func hasRemoveCall(opener *dtest.NoopGitOpener, path string, force bool) bool {
	want := strconv.FormatBool(force)
	for _, c := range opener.WTM.Calls {
		if len(c) == 3 && c[0] == "remove" && c[1] == path && c[2] == want {
			return true
		}
	}
	return false
}

// anyRemoveCall reports whether opener's WTM recorded any "remove" call at all.
func anyRemoveCall(opener *dtest.NoopGitOpener) bool {
	for _, c := range opener.WTM.Calls {
		if len(c) > 0 && c[0] == "remove" {
			return true
		}
	}
	return false
}
