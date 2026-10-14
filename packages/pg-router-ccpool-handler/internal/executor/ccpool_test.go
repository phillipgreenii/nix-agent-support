package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/parknote"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/prompt"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
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
	// noBudget: a time budget would raise the wait to budget+10m (INV-CCH-27)
	// and make this loop to a 35m manual-clock deadline instead of MaxWait.
	d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}}
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
	if !dtest.HasUpdate(bd, "update zr-w --add-label human --status=open --assignee=") {
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
	if !dtest.HasUpdate(bd, "update zr-w --add-label human --status=open --assignee=") {
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

// INV-CCH-27: the wait deadline is max(MaxWait, budget.time + 10m) when the
// role has a time budget, else MaxWait.
func TestEffectiveMaxWait(t *testing.T) {
	cases := []struct {
		name       string
		maxWait    time.Duration
		budgetTime time.Duration
		want       time.Duration
	}{
		{"no time budget keeps MaxWait", 30 * time.Minute, 0, 30 * time.Minute},
		{"budget below MaxWait keeps MaxWait", 30 * time.Minute, 5 * time.Minute, 30 * time.Minute},
		{"budget+10m == MaxWait", 30 * time.Minute, 20 * time.Minute, 30 * time.Minute},
		{"budget+10m just past MaxWait", 30 * time.Minute, 20*time.Minute + time.Second, 30*time.Minute + time.Second},
		{"default review budget (25m)", 30 * time.Minute, 25 * time.Minute, 35 * time.Minute},
		{"default worker budget (30m)", 30 * time.Minute, 30 * time.Minute, 40 * time.Minute},
		{"budget above MaxWait", 30 * time.Minute, 2 * time.Hour, 2*time.Hour + 10*time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.MaxWait = tc.maxWait
			cc := &roles.CCPoolConfig{Budget: budget.Budget{Time: tc.budgetTime}}
			if got := effectiveMaxWait(cfg, cc); got != tc.want {
				t.Errorf("effectiveMaxWait(MaxWait=%s, budget.time=%s) = %s, want %s", tc.maxWait, tc.budgetTime, got, tc.want)
			}
		})
	}
	// Only the time dimension extends the wait: token/cost limits do not.
	cfg := config.Default()
	if got := effectiveMaxWait(cfg, &roles.CCPoolConfig{Budget: budget.Budget{Tokens: 1_000_000, Cost: 500}}); got != cfg.MaxWait {
		t.Errorf("a role with only token/cost budgets must keep MaxWait, got %s", got)
	}
}

// INV-CCH-27 through waitDone: the loop keeps polling to the derived deadline
// (not Cfg.MaxWait) and the failure text names the wait actually applied. The
// manual clock advances a poll interval per poll, so a one-minute interval
// reaches a 35m deadline in ~35 polls with no real sleeping.
func TestWaitDone_usesDerivedWaitAndReportsIt(t *testing.T) {
	cases := []struct {
		name       string
		budgetTime time.Duration
		wantText   string
		minPolls   int
		maxPolls   int
	}{
		{"budget beyond MaxWait waits budget+10m", 25 * time.Minute, "not complete within 35m0s", 35, 36},
		{"no time budget waits MaxWait", 0, "not complete within 30m0s", 30, 31},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastCfg()
			cfg.MaxWait = 30 * time.Minute
			cfg.PollInterval = time.Minute
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
			e := newExec(cc, bd, cfg)
			role := workerRole(cfg)
			role.CCPool.Budget = budget.Budget{Time: tc.budgetTime}
			d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
			err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w")
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("error must contain %q, got %v", tc.wantText, err)
			}
			// Each poll reads the session list twice (the liveness checks), so
			// the list count bounds the polls made before the deadline.
			if cc.ListIdx < 2*tc.minPolls || cc.ListIdx > 2*tc.maxPolls+2 {
				t.Errorf("listIdx=%d, want the loop to run to the derived deadline (%d-%d polls)", cc.ListIdx, tc.minPolls, tc.maxPolls)
			}
		})
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
		// A human death writes the park note (pg2-78mxs) before the update.
		{"handler is not external", "handler", false, true, true},
		{"unstamped death", "", false, true, true},
		{"absent row", "absent", false, true, true},
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
			if got := dtest.HasUpdate(bd, "update zr-w --add-label human --status=open --assignee="); got != tc.wantHuman {
				t.Errorf("human = %v, want %v; updates=%v", got, tc.wantHuman, bd.Updates)
			}
			if got := len(bd.Comments) > 0; got != tc.wantComment {
				t.Errorf("comment = %v, want %v; comments=%v", got, tc.wantComment, bd.Comments)
			}
			if tc.wantHuman {
				if len(bd.Comments) != 1 || !strings.Contains(bd.Comments[0], parknote.Marker) ||
					!strings.Contains(bd.Comments[0], "No person-question was identified") {
					t.Errorf("a human death must write exactly one park note; comments=%v", bd.Comments)
				}
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

// --- pg2-qmltm: a same-bead external reopen racing session death is a
// handback, not an unexplained death ---

// TestWaitDone_reopenRacesDeath_recognizedAsHandback reproduces the exact race
// pg2-qmltm documents (observed live against zr-n1abo.2, a review dispatch):
// the bead is claimed (in_progress, latching seenClaimed), then a same-bead
// external mutation reopens it (status -> open, e.g. pg-router's ACL
// head-advance reopen, docs/pr-review-flow.md JR4) in the SAME instant the
// ccpool session goes idle. The immediate re-check-after-death still observes
// the stale "in_progress" read (the second StatusSeq entry below); only the
// bounded retry's second read observes the reopen (the third entry, "open").
// Before this bead's fix this fell into "unexplained death" and, for the
// review role's on_failure=add-human, spuriously flagged the bead human.
func TestWaitDone_reopenRacesDeath_recognizedAsHandback(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-rv": {"in_progress", "in_progress", "open"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pg-router-review-zr-rv", Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "pg-router-review-zr-rv", Live: false, State: ccpool.StateIdle}},
	}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: reviewRole(cfg), Item: item.Item{ID: "zr-rv"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-review-zr-rv"); err != nil {
		t.Fatalf("a same-bead reopen racing session death must be a clean handback, not an unexplained death: got %v; updates=%v", err, bd.Updates)
	}
	if dtest.HasUpdate(bd, "update zr-rv --add-label human") {
		t.Errorf("must NOT spuriously add-human on a legitimate reopen; updates=%v", bd.Updates)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("a recognized handback is not waitDone's mutation to make (the reopener already made it); updates=%v", bd.Updates)
	}
}

// TestWaitDone_genuineDeathStillFailsAfterBoundedRetry is the negative-case
// lock alongside the fix above: when the bead never reopens (a genuine
// unexplained death — the session simply crashed), the new bounded retry must
// NOT turn that into a false success. It costs exactly one extra poll
// interval, then still classifies as death and applies the role's on_failure.
func TestWaitDone_genuineDeathStillFailsAfterBoundedRetry(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-rv": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pg-router-review-zr-rv", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: reviewRole(cfg), Item: item.Item{ID: "zr-rv"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-review-zr-rv"); err == nil {
		t.Fatal("a genuine unexplained death (no reopen ever arrives) must still fail")
	}
	if !dtest.HasUpdate(bd, "update zr-rv --add-label human --status=open --assignee=") {
		t.Errorf("review role's on_failure=add-human must still apply; updates=%v", bd.Updates)
	}
}

// TestWaitDone_neverClaimedDeath_stillFails locks in DoneSignal's own
// seenClaimed guard against the new bounded retry: a session that dies before
// ever claiming the bead (the "startup race" DoneSignal's seenClaimed exists
// to reject) must still fail even though the bead is sitting at status=open
// with no assignee — the exact shape a legitimate handback also has. Without
// seenClaimed gating the retry the same way it gates the ordinary DoneSignal
// check, this would be misread as success.
func TestWaitDone_neverClaimedDeath_stillFails(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-rv": {"open"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pg-router-review-zr-rv", Live: false, State: ccpool.StateErrored}},
	}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: reviewRole(cfg), Item: item.Item{ID: "zr-rv"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-review-zr-rv"); err == nil {
		t.Fatal("dying before ever claiming (never-seen in_progress) must still fail, per DoneSignal's own startup-race guard")
	}
	if !dtest.HasUpdate(bd, "update zr-rv --add-label human --status=open --assignee=") {
		t.Errorf("review role's on_failure=add-human must still apply; updates=%v", bd.Updates)
	}
}

// --- pg2-04bf7: dead ccpool sessions must not block retries forever ---

// TestWaitDone_deathClosesDeadRow proves waitDone's unexplained-death branch
// (not an external close) actually issues a Close call against the dead row
// it just identified — mirroring run()'s own not-ingested cleanup — so the
// same stale row cannot collide with the next dispatch attempt for this
// (bead, role) pair. This is fix (b) of the zombie-session incident.
func TestWaitDone_deathClosesDeadRow(t *testing.T) {
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
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-w" {
		t.Errorf("death branch must Close the dead row it identified; Closed=%v", cc.Closed)
	}
	if len(cc.ClosedPurge) != 1 || cc.ClosedPurge[0] != false {
		t.Errorf("death-branch Close must mirror run()'s own not-ingested Close (purge=false); ClosedPurge=%v", cc.ClosedPurge)
	}
}

// TestWaitDone_externalCloseDoesNotDoubleClose proves the new Close call is
// scoped to the unexplained-death path only: an EXTERNAL close (ccpool's own
// reaper already ended the session) must NOT be closed again by waitDone —
// that row's closure is already owned by ccpool itself.
func TestWaitDone_externalCloseDoesNotDoubleClose(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "in_progress", "in_progress"}}}
	row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateWorking, CloseReason: "cap_eviction"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}, {row}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err == nil {
		t.Fatal("external close must still return an error (unclaimed)")
	}
	if len(cc.Closed) != 0 {
		t.Errorf("an externally-closed row must not be Closed again by waitDone; Closed=%v", cc.Closed)
	}
}

// --- prior sessions of the bead and role (INV-CCH-30, ADR 0094) ---
//
// These fake-ccpool tests pin the decision table fast. The same rules are
// proven against the REAL ccpool binary in prior_sessions_realccpool_test.go
// (global rule V-3).

var planNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// planClock is planNow with fastCfg's launch window (ccpool.EnsureTimeout plus
// the default two-minute lease TTL).
var planClock = priorClock{now: planNow, window: ccpool.EnsureTimeout + 2*time.Minute}

// planMeta builds a row's dispatch meta: its event id ("" omits it) and a lease
// that ends at planNow+leaseIn (a zero leaseIn omits the lease entirely).
func planMeta(eventID string, leaseIn time.Duration) map[string]string {
	m := map[string]string{}
	if eventID != "" {
		m[ccpool.MetaKeyEventID] = eventID
	}
	if leaseIn != 0 {
		m[ccpool.MetaKeyLeaseUntil] = ccpool.FormatMetaTime(planNow.Add(leaseIn))
	}
	return m
}

func TestPriorSessionPredicates(t *testing.T) {
	held, expired := planMeta("evt-1", time.Minute), planMeta("evt-1", -time.Minute)
	cases := []struct {
		name                                    string
		s                                       ccpool.Session
		inProgress, inFlight, leftover, absorbs bool
	}{
		{"live working, same event, lease held", ccpool.Session{Live: true, State: ccpool.StateWorking, Meta: held}, true, true, false, true},
		{"live needs_input, same event, lease expired (a dead handler's turn)", ccpool.Session{Live: true, State: ccpool.StateNeedsInput, Meta: expired}, true, true, false, true},
		{"live working, no lease at all", ccpool.Session{Live: true, State: ccpool.StateWorking, Meta: planMeta("evt-1", 0)}, true, true, false, true},
		{"dead working row (crashed), lease expired", ccpool.Session{State: ccpool.StateWorking, Meta: expired}, false, false, false, false},
		{"dead working row, lease still held (handler handling its death)", ccpool.Session{State: ccpool.StateWorking, Meta: held}, false, true, false, false},
		{"live idle, lease held (handler settling it)", ccpool.Session{Live: true, State: ccpool.StateIdle, Meta: held}, false, true, false, false},
		{"live idle, lease expired (settled, nobody closed it)", ccpool.Session{Live: true, State: ccpool.StateIdle, Meta: expired}, false, false, false, false},
		{"handler-closed idle row, lease expired (the settled success row)", ccpool.Session{State: ccpool.StateIdle, CloseReason: "handler", Meta: expired}, false, false, false, false},
		{"handler-closed errored row, lease held", ccpool.Session{State: ccpool.StateErrored, CloseReason: "handler", Meta: held}, false, true, false, false},
		{"live ready, lease held (being launched)", ccpool.Session{Live: true, State: ccpool.StateReady, Meta: held}, false, true, false, false},
		{"live starting, lease held (being launched)", ccpool.Session{Live: true, State: ccpool.StateStarting, Meta: held}, false, true, false, false},
		{"live ready, lease expired: a leftover", ccpool.Session{Live: true, State: ccpool.StateReady, Meta: expired}, false, false, true, false},
		{"live starting, lease expired: a leftover", ccpool.Session{Live: true, State: ccpool.StateStarting, Meta: expired}, false, false, true, false},
		{"live ready, no lease, age unknown: may still be launching", ccpool.Session{Live: true, State: ccpool.StateReady}, false, true, false, false},
		{"live starting, no lease yet, last activity a minute ago: still launching (ccpool stamps meta only at ready)", ccpool.Session{Live: true, State: ccpool.StateStarting, LastActivityAt: planNow.Add(-time.Minute).Unix()}, false, true, false, false},
		{"live starting, no lease, outlived the launch window: a leftover", ccpool.Session{Live: true, State: ccpool.StateStarting, LastActivityAt: planNow.Add(-time.Hour).Unix()}, false, false, true, false},
		{"live ready, no lease, ready an hour ago (late ready after a failed launch): a leftover", ccpool.Session{Live: true, State: ccpool.StateReady, LastActivityAt: planNow.Add(-time.Hour).Unix()}, false, false, true, false},
		{"live idle, no lease: settled, nobody's lease to hold", ccpool.Session{Live: true, State: ccpool.StateIdle, LastActivityAt: planNow.Unix()}, false, false, false, false},
		{"dead ready row, lease expired: not live, not a leftover", ccpool.Session{State: ccpool.StateReady, Meta: expired}, false, false, false, false},
		{"live ready under two-phase teardown: the purge retry's", ccpool.Session{Live: true, State: ccpool.StateReady, Meta: map[string]string{ccpool.MetaKeyPurgePending: "1", ccpool.MetaKeyLeaseUntil: expired[ccpool.MetaKeyLeaseUntil]}}, false, false, false, false},
		{"live working, closed while it runs", ccpool.Session{Live: true, State: ccpool.StateWorking, CloseReason: "operator", Meta: held}, true, true, false, false},
		{"live working under two-phase teardown", ccpool.Session{Live: true, State: ccpool.StateWorking, Meta: map[string]string{ccpool.MetaKeyEventID: "evt-1", ccpool.MetaKeyPurgePending: "1"}}, true, true, false, false},
		{"live working, orphan reclaim begun", ccpool.Session{Live: true, State: ccpool.StateWorking, Meta: map[string]string{ccpool.MetaKeyEventID: "evt-1", ccpool.MetaKeyOrphanReclaimed: "2026-10-13T11:00:00Z"}}, true, true, false, false},
		{"live working, another event", ccpool.Session{Live: true, State: ccpool.StateWorking, Meta: planMeta("evt-0", time.Minute)}, true, true, false, false},
		{"live working, no event id recorded", ccpool.Session{Live: true, State: ccpool.StateWorking, Meta: planMeta("", time.Minute)}, true, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := turnInProgress(tc.s); got != tc.inProgress {
				t.Errorf("turnInProgress = %v, want %v", got, tc.inProgress)
			}
			if got := inFlight(tc.s, planClock); got != tc.inFlight {
				t.Errorf("inFlight = %v, want %v", got, tc.inFlight)
			}
			if got := leftover(tc.s, planClock); got != tc.leftover {
				t.Errorf("leftover = %v, want %v", got, tc.leftover)
			}
			if got := absorbable(tc.s, "evt-1"); got != tc.absorbs {
				t.Errorf("absorbable(evt-1) = %v, want %v", got, tc.absorbs)
			}
		})
	}
	if absorbable(ccpool.Session{Live: true, State: ccpool.StateWorking, Meta: planMeta("", time.Minute)}, "") {
		t.Error("a dispatch with no event id can compare nothing, so it absorbs nothing")
	}
}

// TestPlanPriorSessions is the decision table of INV-CCH-30 over a whole pool
// listing: absorb only the same event's turn in progress on an open bead,
// decline while any other attempt is in flight, close and replace leftovers,
// and launch fresh otherwise.
func TestPlanPriorSessions(t *testing.T) {
	const name = "pg-router-review-zr-c"
	row := func(id string, live bool, st ccpool.SessionState, meta map[string]string) ccpool.Session {
		return ccpool.Session{ExternalID: id, Name: name, Live: live, State: st, Meta: meta}
	}
	settledSuccess := row("old", false, ccpool.StateIdle, map[string]string{
		ccpool.MetaKeyEventID: "evt-1", ccpool.MetaKeyHeadSHA: "h1",
		ccpool.MetaKeyLeaseUntil: ccpool.FormatMetaTime(planNow.Add(-time.Hour)),
	})
	settledSuccess.CloseReason = "handler"
	cases := []struct {
		name       string
		sessions   []ccpool.Session
		listErr    error
		eventID    string
		bead       string // "" = unreadable
		want       priorVerdict
		wantID     string // absorb target, or the in-flight session of a decline
		wantLefts  []string
		wantBDRead bool
	}{
		{name: "empty pool: launch", want: verdictLaunch},
		{name: "settled success row, same event and head (the rejected candidate): launch", sessions: []ccpool.Session{settledSuccess}, eventID: "evt-1", bead: "open", want: verdictLaunch},
		{name: "settled row closed by idle_ttl: launch", sessions: []ccpool.Session{{ExternalID: "old", Name: name, State: ccpool.StateIdle, CloseReason: "idle_ttl"}}, eventID: "evt-1", want: verdictLaunch},
		{name: "crashed row, lease expired: launch", sessions: []ccpool.Session{row("old", false, ccpool.StateWorking, planMeta("evt-1", -time.Minute))}, eventID: "evt-1", want: verdictLaunch},
		{name: "live idle nobody closed, lease expired: launch, never absorbed", sessions: []ccpool.Session{row("old", true, ccpool.StateIdle, planMeta("evt-1", -time.Minute))}, eventID: "evt-1", want: verdictLaunch},
		{name: "live idle, handler still settling it: decline", sessions: []ccpool.Session{row("old", true, ccpool.StateIdle, planMeta("evt-1", time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "old"},
		{name: "same event's turn in progress, bead open: absorb", sessions: []ccpool.Session{row("cur", true, ccpool.StateWorking, planMeta("evt-1", time.Minute))}, eventID: "evt-1", bead: "in_progress", want: verdictAbsorb, wantID: "cur", wantBDRead: true},
		{name: "same event's turn paused for a person: absorb", sessions: []ccpool.Session{row("cur", true, ccpool.StateNeedsInput, planMeta("evt-1", -time.Minute))}, eventID: "evt-1", bead: "in_progress", want: verdictAbsorb, wantID: "cur", wantBDRead: true},
		{name: "same event's turn, bead already closed: decline", sessions: []ccpool.Session{row("cur", true, ccpool.StateWorking, planMeta("evt-1", time.Minute))}, eventID: "evt-1", bead: "closed", want: verdictDecline, wantID: "cur", wantBDRead: true},
		{name: "same event's turn, bead unreadable: decline", sessions: []ccpool.Session{row("cur", true, ccpool.StateWorking, planMeta("evt-1", time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "cur", wantBDRead: true},
		{name: "another event's turn in progress: decline", sessions: []ccpool.Session{row("cur", true, ccpool.StateWorking, planMeta("evt-0", time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "cur"},
		{name: "another event's turn paused for a person: decline", sessions: []ccpool.Session{row("cur", true, ccpool.StateNeedsInput, planMeta("evt-0", -time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "cur"},
		{name: "a turn that records no event id: decline", sessions: []ccpool.Session{row("cur", true, ccpool.StateWorking, planMeta("", time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "cur"},
		{name: "a dispatch with no event id: decline", sessions: []ccpool.Session{row("cur", true, ccpool.StateWorking, planMeta("evt-1", time.Minute))}, want: verdictDecline, wantID: "cur"},
		{name: "session being launched (ready, lease held): decline", sessions: []ccpool.Session{row("cur", true, ccpool.StateReady, planMeta("evt-1", time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "cur"},
		{name: "a launch still in progress (starting, no metadata yet): decline", sessions: []ccpool.Session{{ExternalID: "cur", Name: name, Live: true, State: ccpool.StateStarting, LastActivityAt: planNow.Add(-30 * time.Second).Unix()}}, eventID: "evt-1", want: verdictDecline, wantID: "cur"},
		{name: "a late-ready leftover with no metadata: launch after closing it", sessions: []ccpool.Session{{ExternalID: "late", Name: name, Live: true, State: ccpool.StateReady, LastActivityAt: planNow.Add(-time.Hour).Unix()}}, eventID: "evt-1", want: verdictLaunch, wantLefts: []string{"late"}},
		{name: "leftover ready and starting: launch after closing both", sessions: []ccpool.Session{row("l1", true, ccpool.StateReady, planMeta("evt-0", -time.Minute)), row("l2", true, ccpool.StateStarting, planMeta("evt-1", -time.Minute))}, eventID: "evt-1", want: verdictLaunch, wantLefts: []string{"l1", "l2"}},
		{name: "a leftover beside an attempt in flight: decline, the leftover untouched", sessions: []ccpool.Session{row("l1", true, ccpool.StateReady, planMeta("evt-0", -time.Minute)), row("cur", true, ccpool.StateWorking, planMeta("evt-0", time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "cur"},
		{name: "every session counts, not only the first: decline", sessions: []ccpool.Session{settledSuccess, row("cur", true, ccpool.StateWorking, planMeta("evt-0", time.Minute))}, eventID: "evt-1", want: verdictDecline, wantID: "cur"},
		{name: "the same event's turn is preferred over another attempt in flight", sessions: []ccpool.Session{row("other", true, ccpool.StateIdle, planMeta("evt-0", time.Minute)), row("cur", true, ccpool.StateWorking, planMeta("evt-1", time.Minute))}, eventID: "evt-1", bead: "in_progress", want: verdictAbsorb, wantID: "cur", wantBDRead: true},
		{name: "another bead's session is not this bead's: launch", sessions: []ccpool.Session{{ExternalID: "x", Name: "pg-router-review-zr-d", Live: true, State: ccpool.StateWorking, Meta: planMeta("evt-9", time.Minute)}}, eventID: "evt-1", want: verdictLaunch},
		{name: "a session found by its dispatch tags, not its name: decline", sessions: []ccpool.Session{{ExternalID: "tagged", Live: true, State: ccpool.StateWorking, Meta: map[string]string{
			ccpool.MetaKeyPool: ccpool.PoolName, ccpool.MetaKeyBead: "zr-c", ccpool.MetaKeyRole: "review", ccpool.MetaKeyEventID: "evt-0",
		}}}, eventID: "evt-1", want: verdictDecline, wantID: "tagged"},
		{name: "the pool cannot be read: decline", listErr: errors.New("ccpool list: boom"), eventID: "evt-1", want: verdictDecline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sbd := &dtest.ScriptBD{}
			if tc.bead != "" {
				sbd.StatusSeq = map[string][]string{"zr-c": {tc.bead}}
			}
			bd := &showCountBD{ScriptBD: sbd}
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{tc.sessions}, ListErr: tc.listErr}
			e := newExec(cc, sbd, fastCfg())
			e.deps.BD = bd
			e.deps.Now = func() time.Time { return planNow }
			d := DispatchContext{Role: reviewRole(fastCfg()), Item: item.Item{ID: "zr-c"}, EventID: tc.eventID}
			got := e.planPriorSessions(context.Background(), d, name)
			if got.verdict != tc.want {
				t.Fatalf("verdict = %v, want %v (plan %+v)", got.verdict, tc.want, got)
			}
			switch tc.want {
			case verdictAbsorb:
				if got.absorb.ExternalID != tc.wantID {
					t.Errorf("absorbs %q, want %q", got.absorb.ExternalID, tc.wantID)
				}
			case verdictDecline:
				if got.session != tc.wantID {
					t.Errorf("declines over session %q, want %q (why %q)", got.session, tc.wantID, got.why)
				}
				if got.why == "" {
					t.Error("a decline must say why")
				}
			}
			var lefts []string
			for _, s := range got.leftovers {
				lefts = append(lefts, s.ExternalID)
			}
			if !slices.Equal(lefts, tc.wantLefts) {
				t.Errorf("leftovers = %v, want %v", lefts, tc.wantLefts)
			}
			if read := bd.shows > 0; read != tc.wantBDRead {
				t.Errorf("bead read = %v, want %v (the bead is read only for an absorb candidate)", read, tc.wantBDRead)
			}
		})
	}
}

// TestDispatch_sessionInFlight_declinesWithoutMutation: a dispatch that finds
// another event's turn in progress declines with ErrSessionInFlight and touches
// nothing: no launch, no send, no close, no session metadata, no bead write.
func TestDispatch_sessionInFlight_declinesWithoutMutation(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := reviewRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	other := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking,
		Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:zr-c-earlier"},
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{other}}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}, EventID: "review.ready:zr-c"}
	res, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
	if !errors.Is(err, ErrSessionInFlight) {
		t.Fatalf("err = %v, want ErrSessionInFlight", err)
	}
	if errors.Is(err, ErrPoolAtCapacity) || errors.Is(err, ErrPoolCapacityUnknown) {
		t.Fatalf("a session-in-flight decline must not also read as a capacity decline: %v", err)
	}
	if v := verbOf(res); v != "" {
		t.Errorf("a decline applies no verb, got %q", v)
	}
	if len(cc.Ensured)+len(cc.Sent)+len(cc.Closed)+len(cc.SetMetaCalls()) != 0 {
		t.Errorf("a decline mutates no session; Ensured=%v Sent=%v Closed=%v SetMeta=%v", cc.Ensured, cc.Sent, cc.Closed, cc.SetMetaCalls())
	}
	if len(bd.Updates)+len(bd.Comments) != 0 {
		t.Errorf("a decline writes nothing to the bead; updates=%v comments=%v", bd.Updates, bd.Comments)
	}
}

// TestDispatch_leftover_closedThenReplaced: a live ready session whose lease has
// expired (a launch whose handler died before its prompt was taken) is closed,
// without purging and under the per-session lock, and the dispatch then launches
// and prompts its OWN session. The leftover is never handed the prompt (the
// pg2-oq6cy behavior this replaces).
func TestDispatch_leftover_closedThenReplaced(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	stuck := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateReady, CWD: t.TempDir(),
		Meta: map[string]string{ccpool.MetaKeyLeaseUntil: "1969-12-31T23:00:00Z"}, // long expired
	}
	lockDir := t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{stuck}, // planPriorSessions
		{stuck}, // closeLeftover's re-read under the lock
		{{ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking}},
	}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.LockDir = lockDir
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}, EventID: "evt-1"}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch should succeed (bead closes), got %v", err)
	}
	if len(cc.Closed) == 0 || cc.Closed[0] != "att-1" || cc.ClosedPurge[0] {
		t.Errorf("the leftover must be closed first, non-purge; Closed=%v purge=%v", cc.Closed, cc.ClosedPurge)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("the dispatch must launch its own session; Ensured=%v", got)
	}
	if got := cc.Sent; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("only the fresh session is prompted, never the leftover; Sent=%v", got)
	}
	l, err := sessionlock.TryLock(lockDir, "att-1")
	if err != nil {
		t.Fatalf("the leftover's lock must be released once it is closed: %v", err)
	}
	l.Unlock()
}

// TestDispatch_leftover_reReadUnderLock: under the lock the row is read again.
// One that a handler has since taken up (lease refreshed) is in flight, so the
// dispatch declines without closing it; one that is gone needs nothing and the
// dispatch launches.
func TestDispatch_leftover_reReadUnderLock(t *testing.T) {
	cfg := fastCfg()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	stuck := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateReady,
		Meta: map[string]string{ccpool.MetaKeyLeaseUntil: "1969-12-31T23:00:00Z"},
	}
	retaken := stuck
	retaken.Meta = map[string]string{ccpool.MetaKeyLeaseUntil: "1970-01-01T01:00:00Z"} // the manual clock starts at the epoch
	for _, tc := range []struct {
		name       string
		reRead     []ccpool.Session
		wantErr    error
		wantLaunch bool
	}{
		{"now in flight: decline, nothing closed", []ccpool.Session{retaken}, ErrSessionInFlight, false},
		{"gone: nothing to close, launch", []ccpool.Session{}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfg
			cfg.WorktreeDir = t.TempDir()
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{stuck}, tc.reRead, {{ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking}}}}
			deps := newExec(cc, bd, cfg).deps
			deps.ExternalID = "att-2"
			deps.LockDir = t.TempDir()
			deps.Git = &dtest.NoopGit{}
			deps.GitOpener = (&dtest.NoopGitOpener{}).Open
			d := DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}, EventID: "evt-1"}
			_, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("err = %v, want success", err)
			}
			if slices.Contains(cc.Closed, "att-1") {
				t.Errorf("the re-read row must not be closed; Closed=%v", cc.Closed)
			}
			if launched := len(cc.Ensured) == 1; launched != tc.wantLaunch {
				t.Errorf("launched = %v, want %v; Ensured=%v", launched, tc.wantLaunch, cc.Ensured)
			}
		})
	}
}

// TestDispatch_leftover_closeFails_launchesNothing: a leftover that cannot be
// closed is an error of this dispatch, never a launch beside it.
func TestDispatch_leftover_closeFails_launchesNothing(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	stuck := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateStarting,
		Meta: map[string]string{ccpool.MetaKeyLeaseUntil: "1969-12-31T23:00:00Z"},
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{stuck}}, CloseErr: errors.New("ccpool close: boom")}
	deps := newExec(cc, &dtest.ScriptBD{}, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	_, err := (ccpoolExecutor{}).Dispatch(context.Background(), DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}}, deps)
	if err == nil || errors.Is(err, ErrSessionInFlight) || !strings.Contains(err.Error(), "close leftover att-1") {
		t.Fatalf("a failed leftover close must surface as an error naming it, got %v", err)
	}
	if len(cc.Ensured) != 0 {
		t.Errorf("nothing may launch beside an unclosed leftover; Ensured=%v", cc.Ensured)
	}
}

// TestDispatch_absorb_turnEndedWhileTakingOver_declines: the row chosen for
// absorbing is read again under the lock; if its turn ended meanwhile it is no
// longer absorbable, and the dispatch declines rather than adopting a settled
// session or launching beside a handler that may still be settling it.
func TestDispatch_absorb_turnEndedWhileTakingOver_declines(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	turn := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking,
		Meta: map[string]string{ccpool.MetaKeyEventID: "evt-1"},
	}
	ended := turn
	ended.State = ccpool.StateIdle
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{turn}, {ended}}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.LockDir = t.TempDir()
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	_, err := (ccpoolExecutor{}).Dispatch(context.Background(), DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}, EventID: "evt-1"}, deps)
	if !errors.Is(err, ErrSessionInFlight) {
		t.Fatalf("err = %v, want ErrSessionInFlight", err)
	}
	if len(cc.Ensured)+len(cc.Sent)+len(cc.Closed)+len(cc.SetMetaCalls()) != 0 {
		t.Errorf("nothing may be written; Ensured=%v Sent=%v Closed=%v SetMeta=%v", cc.Ensured, cc.Sent, cc.Closed, cc.SetMetaCalls())
	}
}

// showCountBD counts the bead reads ("show") a dispatch makes.
type showCountBD struct {
	*dtest.ScriptBD
	shows int
}

func (b *showCountBD) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "show" {
		b.shows++
	}
	return b.ScriptBD.Run(ctx, args...)
}

// TestDispatch_reDispatchAfterHandlerClose_launchesFreshSession (bead pg2-uprw5,
// INV-CCH-30): the handler-closed settled row of event evt-1 still exists when
// event evt-2 (a reopened review for the same bead and role) dispatches. It must
// Ensure a fresh session, stamped with evt-2, rather than absorb the dead row and
// fail with "session exited before completing".
func TestDispatch_reDispatchAfterHandlerClose_launchesFreshSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	old := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{ccpool.MetaKeyEventID: "evt-1"},
	}

	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{old}, // planPriorSessions: only the earlier event's settled row exists
		{old, {ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking}},
	}}
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}, EventID: "evt-2"}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("re-dispatch should succeed (bead closed), got %v", err)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("a re-dispatch of a later event must launch a fresh session; Ensured=%v", got)
	}
	if got := cc.EnsuredMeta[ccpool.MetaKeyEventID]; got != "evt-2" {
		t.Errorf("the fresh session must be stamped with its own event id; meta=%v", cc.EnsuredMeta)
	}
}

// TestDispatch_reDispatchSameEventAfterHeadAdvance_launchesFreshSession is the
// pg2-afre3 regression. pg-router's event id is deterministic per (type, bead)
// (event.FingerprintID: review.ready:<bead>), so a review re-dispatched after a
// head advance carries the SAME event id as the review that already settled. The
// handler-closed settled row of that earlier review (head h1) must therefore NOT
// be absorbed by the new dispatch (head h2): absorbing it measured the time
// budget from the ORIGINAL launch (an instant false "session budget exceeded")
// and reported the old outcome. A fresh session must launch, stamped with the new
// head. Under INV-CCH-30 a settled row is never absorbed at all, so the same
// event at the same head launches fresh too
// (TestDispatch_sameEventSameHead_settledRow_launchesFreshSession); this test
// keeps the head-advance case that bead pg2-pq2ej verifies.
func TestDispatch_reDispatchSameEventAfterHeadAdvance_launchesFreshSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := reviewRole(cfg)
	role.CCPool.Budget = budget.Budget{} // no watchdog: this test is about which row is chosen
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	old := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:zr-c", ccpool.MetaKeyHeadSHA: "h1"},
	}

	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{old}, // planPriorSessions: only the earlier head's settled row exists
		{old, {ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking}},
	}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:zr-c",
		Item: item.Item{ID: "zr-c", Metadata: map[string]any{"repo": "o/r", "pr_number": "7", "head_sha": "h2"}},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("re-dispatch should succeed (bead closed), got %v", err)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("a re-dispatch after a head advance must launch a fresh session; Ensured=%v", got)
	}
	if got := cc.EnsuredMeta[ccpool.MetaKeyHeadSHA]; got != "h2" {
		t.Errorf("the fresh session must be stamped with its own head; meta=%v", cc.EnsuredMeta)
	}
}

// TestDispatch_sameEventSameHead_settledRow_launchesFreshSession: the candidate
// ADR 0094 considered and rejected. A settled row of the SAME event at the SAME
// head, whose dispatch completed the bead (handler-closed, idle), is still never
// absorbed: once its handler's lease has expired the dispatch runs in a fresh
// session of its own (INV-CCH-17, INV-CCH-30).
func TestDispatch_sameEventSameHead_settledRow_launchesFreshSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := reviewRole(cfg)
	role.CCPool.Budget = budget.Budget{}
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	old := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:zr-c", ccpool.MetaKeyHeadSHA: "h1"},
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{old},
		{old, {ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking}},
	}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:zr-c",
		Item: item.Item{ID: "zr-c", Metadata: map[string]any{"repo": "o/r", "pr_number": "7", "head_sha": "h1"}},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("the fresh session should run to completion (bead closed), got %v", err)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("a settled row is never absorbed, even for the same event and head; Ensured=%v", got)
	}
}

// TestDispatch_sameEventSameHead_afterHandback_launchesFreshSession is the
// pg2-tc9c3 regression (the same-head variant of pg2-afre3). After a hand-back
// the review bead is open again at the SAME head, so review-source re-emits the
// SAME event (review.ready:<bead>) at the SAME head_sha. The handler-closed row of
// the handed-back attempt is settled and must NOT be re-absorbed (INV-CCH-30; it
// carries no outcome mark any more): absorbing it measured the budget from its
// original launch (five hours ago here) and hard-stopped instantly, forever. A
// fresh session must launch, inside a fresh budget, with no "session budget
// exceeded".
func TestDispatch_sameEventSameHead_afterHandback_launchesFreshSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	cfg.BudgetTime = time.Hour // finite: an absorbed row launched five hours ago is over budget
	role := reviewRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	launched := time.Unix(0, 0).Add(-5 * time.Hour)
	old := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{
			ccpool.MetaKeyEventID: "review.ready:zr-c", ccpool.MetaKeyHeadSHA: "h1",
			ccpool.MetaKeyLaunchedAt: ccpool.FormatMetaTime(launched),
		},
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{old}, // planPriorSessions: only the handed-back attempt's settled row exists
		{old, {ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateIdle}},
	}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:zr-c",
		Item: item.Item{ID: "zr-c", Metadata: map[string]any{"repo": "o/r", "pr_number": "7", "head_sha": "h1"}},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	_, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
	if errors.Is(err, watchdog.ErrBudgetExceeded) {
		t.Fatalf("a re-request after a hand-back must not hard-stop on the old row's launch time: %v", err)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("a same-event same-head re-request after a hand-back must launch a fresh session; Ensured=%v", got)
	}
}

// TestDispatch_deadNameMatch_createsFreshSession is the dispatch-level,
// end-to-end version of fix (a): a dispatch whose stable display name
// matches only a DEAD prior row must Ensure a brand-new session under its own
// fresh per-attempt ExternalID, rather than short-circuiting into
// absorbDuplicate against a row that will never complete (the exact zombie
// collision the incident reported — "session exited before completing" on
// every retry).
func TestDispatch_deadNameMatch_createsFreshSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")

	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		// planPriorSessions' own List call: only a dead row under the same
		// stable display name exists (the crash-orphaned zombie).
		{{ExternalID: "dead-1", Name: display, Live: false, State: ccpool.StateWorking}},
		// Every subsequent List call (sessionState/active) sees the fresh
		// session running alongside the still-dead old row.
		{
			{ExternalID: "dead-1", Name: display, Live: false, State: ccpool.StateWorking},
			{ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking},
		},
	}}
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	_, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err != nil {
		t.Fatalf("dispatch should succeed (bead closed), got %v", err)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("a dead name match must NOT short-circuit into absorbDuplicate; a fresh session must be Ensured; Ensured=%v", got)
	}
	if got := cc.Sent; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("the fresh session's own nudge must be sent; Sent=%v", got)
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
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "")
	if len(opener.WTM.Calls) != 0 || cc.ListIdx != 0 {
		t.Errorf("empty wt must short-circuit before any List/RemoveWorktree call; listIdx=%d calls=%v", cc.ListIdx, opener.WTM.Calls)
	}
}

func TestCleanupWorktree_nonWorktreeIsolationNoop(t *testing.T) {
	for _, typ := range []string{"none", "path", "workforest"} {
		t.Run(typ, func(t *testing.T) {
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "s", Live: true, State: ccpool.StateWorking}}}}
			e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
			e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{Isolation: roles.IsolationConfig{Type: typ}}, "s", "zr-w", "/some/shared/path")
			if len(opener.WTM.Calls) != 0 {
				t.Errorf("isolation %q must never be removed by cleanupWorktree; calls=%v", typ, opener.WTM.Calls)
			}
		})
	}
}

// TestCleanupWorktree_removesWhenSessionGone covers pg2-ci75j's own fix on top
// of pg2-4roho's original worktree removal: once RemoveWorktree succeeds, the
// bead's pg-router/<beadID> anchor branch (worktree.Ensure's own naming
// convention) must ALSO be deleted, via a SECOND Open anchored at RepoRoot
// (not at wt, which no longer exists once removed) and force=true (these
// throwaway anchor branches are rarely merged into anything).
func TestCleanupWorktree_removesWhenSessionGone(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{}}} // no sessions at all — definitively gone
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	wt := "/tmp/pg2-4roho/zr-w"
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", wt)
	if len(opener.WTM.Calls) != 2 {
		t.Fatalf("expected a RemoveWorktree call followed by a DeleteBranch call, got %v", opener.WTM.Calls)
	}
	gotRemove := opener.WTM.Calls[0]
	if gotRemove[0] != "remove" || gotRemove[1] != wt || gotRemove[2] != "false" {
		t.Errorf("RemoveWorktree call = %v, want [remove %s false] (force=false is decision item 5's own guard)", gotRemove, wt)
	}
	gotDelete := opener.WTM.Calls[1]
	if gotDelete[0] != "branch-delete" || gotDelete[1] != "pg-router/zr-w" || gotDelete[2] != "true" {
		t.Errorf("DeleteBranch call = %v, want [branch-delete pg-router/zr-w true]", gotDelete)
	}
	if len(opener.Calls) != 2 || opener.Calls[0] != wt || opener.Calls[1] != e.deps.Cfg.RepoRoot {
		t.Errorf("expected Open(wt) then Open(RepoRoot), got %v", opener.Calls)
	}
}

// TestCleanupWorktree_skipsWhileNeedsInput is the regression guard for the
// exact bug pg2-lyriv reported (a needs_input session's worktree deleted out
// from under it): decision item 2 ties a needs_input worktree's lifetime to
// its SESSION's own close, not to this dispatch's own terminal outcome.
func TestCleanupWorktree_skipsWhileNeedsInput(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "s", Live: true, State: ccpool.StateNeedsInput}}}}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "/tmp/pg2-4roho/zr-w")
	if len(opener.WTM.Calls) != 0 {
		t.Errorf("needs_input session's worktree must NOT be removed; calls=%v", opener.WTM.Calls)
	}
}

func TestCleanupWorktree_skipsOnListError(t *testing.T) {
	cc := &dtest.FakeCC{ListErr: errors.New("ccpool list: transient")}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "/tmp/pg2-4roho/zr-w")
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
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "/tmp/pg2-4roho/zr-w")
}

// TestCleanupWorktree_removeFailureNeverDeletesBranch proves the branch-delete
// step never runs at all (no second Open) when RemoveWorktree itself fails —
// the worktree is presumably still there (e.g. dirty), so its anchor branch
// must not be deleted out from under it either.
func TestCleanupWorktree_removeFailureNeverDeletesBranch(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{}}}
	e := newExec(cc, &dtest.ScriptBD{}, fastCfg())
	var opens []string
	e.deps.GitOpener = func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		opens = append(opens, dir)
		return failingRemoveWTM{}, nil
	}
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "/tmp/pg2-4roho/zr-w")
	if len(opens) != 1 {
		t.Errorf("a failed RemoveWorktree must short-circuit before any branch-delete Open; opens=%v", opens)
	}
}

// failingRemoveWTM is a gitclient.WorktreeManager (deliberately NOT a
// BranchManager) whose RemoveWorktree always fails, for
// TestCleanupWorktree_removeFailureNeverDeletesBranch.
type failingRemoveWTM struct{}

func (failingRemoveWTM) CreateWorktree(context.Context, string, string, gitclient.CreateWorktreeOptions) error {
	return nil
}

func (failingRemoveWTM) RemoveWorktree(context.Context, string, bool) error {
	return errors.New("dirty worktree")
}

func (failingRemoveWTM) PruneWorktrees(context.Context) error { return nil }

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
	return dispatchWorkerRole(t, cc, bd, cfg, workerRole(cfg), ext)
}

// dispatchWorkerRole is dispatchWorker for a caller-supplied worker role.
func dispatchWorkerRole(t *testing.T, cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config, role roles.Role, ext string) (report.Result, error) {
	t.Helper()
	cfg.WorktreeDir = t.TempDir() // isolate the per-bead worktree to a throwaway dir
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
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

// pg2-lhi3b: the dispatched worker session must carry its role's actor as
// BEADS_ACTOR, so every `bd` it runs -- including an UNWRAPPED bd build with
// no claim guard -- resolves its actor to the role identity, never to git
// user.name (the operator).
func TestDispatch_exportsRoleActorAsBeadsActor(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch should succeed (bead closed), got %v", err)
	}
	if got, want := cc.EnsuredEnv["BEADS_ACTOR"], "pgii-pool__worker"; got != want {
		t.Errorf("session env BEADS_ACTOR = %q, want the role actor %q", got, want)
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
	// pg2-u2v2p regression: an ensure failure/timeout must purge-close the
	// abandoned external_id so the underlying ccpool session (possibly already
	// inserted as state=starting by `ccpool new` before it failed) never lingers
	// and leaks pool capacity.
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-w" {
		t.Errorf("ensure failure must close the abandoned session, got Closed=%v", cc.Closed)
	}
	if len(cc.ClosedPurge) != 1 || !cc.ClosedPurge[0] {
		t.Errorf("ensure failure's close must purge, got ClosedPurge=%v", cc.ClosedPurge)
	}
}

// TestDispatch_ensureFailCloseFails_loggedAndRetried is pg2-rkjbu's regression:
// a failing purge-close of the abandoned session must not be swallowed. It is
// retried once and the error is logged.
func TestDispatch_ensureFailCloseFails_loggedAndRetried(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":[]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready"), CloseErr: errors.New("ccpool close: boom")}
	if _, err := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w"); err == nil {
		t.Fatal("ensure failure should error")
	}
	if len(cc.Closed) != 2 {
		t.Errorf("failing close must be retried once (2 attempts), got Closed=%v", cc.Closed)
	}
	out := buf.String()
	if !strings.Contains(out, "purge-close of abandoned session failed after retry") || !strings.Contains(out, "ccpool close: boom") {
		t.Errorf("close failure must be logged with its error, got log:\n%s", out)
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
	// pg2-u2v2p regression: still purge-closes the abandoned session even on
	// the escalated (repeat-failure) path.
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-w" || len(cc.ClosedPurge) != 1 || !cc.ClosedPurge[0] {
		t.Errorf("repeat ensure failure must purge-close the abandoned session, got Closed=%v ClosedPurge=%v", cc.Closed, cc.ClosedPurge)
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
	if errors.Is(err, ErrPoolCapacityUnknown) {
		t.Fatalf("a healthy at-capacity decline must NOT also satisfy errors.Is(err, ErrPoolCapacityUnknown); err = %v", err)
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

// TestRun_usageLimitDeclinesBusyWithoutMutation: a hit account usage window
// (ccpool reports free=0 plus usage_limit) declines the dispatch exactly like a
// full pool — same ErrPoolAtCapacity sentinel (so the same at-capacity busy tag,
// the same exclusion from the failure-rate alert, the same re-offer with
// backoff) — with no Ensure, no worktree and no bead mutation, and the log says
// which window and until when.
func TestRun_usageLimitDeclinesBusyWithoutMutation(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	resets := time.Date(2026, 10, 6, 17, 0, 0, 0, time.UTC)
	cc := &dtest.FakeCC{Cap: ccpool.Capacity{
		MaxSessions: 6, Free: 0,
		UsageLimit: &ccpool.UsageLimit{Window: "five_hour", UsedPct: 100, ResetsAt: resets},
	}}
	e := newExec(cc, bd, cfg)
	g := &dtest.NoopGitOpener{}
	e.deps.GitOpener = g.Open
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("err = %v, want ErrPoolAtCapacity (a usage limit is the same busy decline)", err)
	}
	if errors.Is(err, ErrPoolCapacityUnknown) {
		t.Fatalf("a usage-limit decline must not satisfy ErrPoolCapacityUnknown; err = %v", err)
	}
	for _, want := range []string{"five_hour", "2026-10-06T17:00:00Z"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q must name %q", err, want)
		}
	}
	if logged := buf.String(); !strings.Contains(logged, "usage limit hit") || !strings.Contains(logged, "five_hour") {
		t.Errorf("log %q must say which usage window is hit", logged)
	}
	if len(cc.Ensured) != 0 || len(g.Calls) != 0 || len(bd.Updates) != 0 {
		t.Fatalf("launched, prepared a worktree, or mutated a bead: ensured=%v git=%v updates=%v", cc.Ensured, g.Calls, bd.Updates)
	}
}

// TestRun_usageLimitAtLaunchDeclinesBusyNoEscalation: the window can fill between
// the capacity pre-check and `ccpool new` (which then exits 8). That is a busy
// decline, never a launch failure: no pool-launch-fail label, no human escalation,
// and the abandoned session is still purged.
func TestRun_usageLimitAtLaunchDeclinesBusyNoEscalation(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":[]}`}}
	cc := &dtest.FakeCC{EnsureErr: fmt.Errorf("ccpool new: %w", ccpool.ErrUsageLimited)}
	_, err := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w")
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("err = %v, want ErrPoolAtCapacity (the busy decline)", err)
	}
	for _, u := range bd.Updates {
		if strings.Contains(u, "pool-launch-fail") || strings.Contains(u, "human") {
			t.Fatalf("a usage-limit refusal must not stamp or escalate the bead; updates=%v", bd.Updates)
		}
	}
	if len(cc.Closed) == 0 {
		t.Error("the abandoned session must still be purged")
	}
}

// TestRun_poolCapacityErrorDeclinesBusy proves an unreadable pool fails
// CLOSED as busy — never as "launch anyway" — and as its OWN sentinel
// (ErrPoolCapacityUnknown, bead pg2-j4uwg), NOT ErrPoolAtCapacity: before
// this bead the two branches were indistinguishable (both wrapped
// ErrPoolAtCapacity), which is exactly the confirmed-live incident this
// bead splits apart.
func TestRun_poolCapacityErrorDeclinesBusy(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{CapErr: errors.New("capacity: store: disk I/O error")}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolCapacityUnknown) {
		t.Fatalf("unknown pool must fail closed as busy via ErrPoolCapacityUnknown; err = %v", err)
	}
	if errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("unknown-capacity decline must NOT also satisfy errors.Is(err, ErrPoolAtCapacity) (the two are now distinct sentinels); err = %v", err)
	}
	if len(cc.Ensured) != 0 || len(bd.Updates) != 0 {
		t.Fatal("launched or mutated despite unknown capacity")
	}
}

// --- isolation low-disk (bead pg2-8vn8t) ---

// lowDiskWTM is a gitclient.WorktreeManager whose CreateWorktree always fails
// with the disk-full *gitclient.GitError shape observed in the 2026-09-23
// incident: exit 128, stderr naming the OS's out-of-space wording.
type lowDiskWTM struct{}

func (lowDiskWTM) CreateWorktree(context.Context, string, string, gitclient.CreateWorktreeOptions) error {
	return &gitclient.GitError{ExitCode: 128, Stderr: "fatal: Unable to create '/repo/.git/worktrees/zr-w/index.lock': No space left on device"}
}
func (lowDiskWTM) RemoveWorktree(context.Context, string, bool) error { return nil }
func (lowDiskWTM) PruneWorktrees(context.Context) error               { return nil }

// TestRun_isolationLowDiskDeclinesBusyNoEscalation proves a worktree-add
// failure caused by a full filesystem (bead pg2-8vn8t) is classified
// ErrLowDisk and mutates NO bead at all (no pool-launch-fail/human
// escalation — see ccpool.go's own doc comment at its isolation-Ensure
// error branch): before this fix, ANY isolation failure — including this
// one — escalated per-bead via escalateLaunchFailure, which is exactly what
// flooded several UNRELATED beads with `human` during the incident (each
// dispatch that happened to land while the volume was full got its own
// escalation for the SAME underlying cause). It must also stay distinct from
// either pool-capacity sentinel — a third, independent "decline and retry
// later" case, not a launch failure.
func TestRun_isolationLowDiskDeclinesBusyNoEscalation(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{}
	e := newExec(cc, bd, cfg)
	path := filepath.Join(cfg.WorktreeDir, "zr-w")
	e.deps.GitOpener = func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		if dir == path {
			return nil, gitclient.ErrNotARepository // path doesn't exist yet -> attempt create
		}
		return lowDiskWTM{}, nil
	}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrLowDisk) {
		t.Fatalf("err = %v, want errors.Is(err, ErrLowDisk)", err)
	}
	if errors.Is(err, ErrPoolAtCapacity) || errors.Is(err, ErrPoolCapacityUnknown) {
		t.Fatalf("a low-disk isolation failure must not also satisfy either pool-capacity sentinel; err = %v", err)
	}
	if len(cc.Ensured) != 0 {
		t.Fatal("no ccpool session may be launched when isolation itself failed")
	}
	if len(bd.Updates) != 0 {
		t.Fatalf("low disk must not mutate the bead (no pool-launch-fail/human escalation); updates=%v", bd.Updates)
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
	// noBudget: with the default time budget the watchdog's hard stop (25m)
	// fires before the derived wait (35m, INV-CCH-27) and reports Unclaimed.
	res, _ := dispatchWorkerRole(t, cc, bd, cfg, noBudget(workerRole(cfg)), "pg-router-worker-zr-w")
	if v := verbOf(res); v != report.Escalated {
		t.Errorf("worker timeout must report Escalated, got %q", v)
	}
}

// TestDispatch_redeliveryDuringTurn_absorbsIntoExistingSession is INV-EVT-2's
// duplicate tolerance as INV-CCH-30 keeps it: the SAME event redelivered (a
// crash window, or the re-offer after a daemon restart) while the first
// attempt's turn is still in progress must be absorbed into that session, not
// get a second one. The redelivery mints its OWN fresh per-attempt ExternalID,
// so the correlation is the stable per-bead DisplayName plus the event id the
// first launch stamped. The absorbing dispatch sends nothing and reports the
// session's own outcome.
func TestDispatch_redeliveryDuringTurn_absorbsIntoExistingSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")
	running := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking,
		Meta: map[string]string{ccpool.MetaKeyEventID: "evt-1"},
	}
	// The absorb decision reads the bead (open work), then the wait sees it close.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{running}}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	res, err := ccpoolExecutor{}.Dispatch(context.Background(), DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}, EventID: "evt-1"}, deps)
	if err != nil {
		t.Fatalf("the redelivery must be absorbed and report the session's success, got %v", err)
	}
	if v := verbOf(res); v != "" {
		t.Errorf("an absorbed success reports no verb, got %q", v)
	}
	if len(cc.Ensured)+len(cc.Sent) != 0 {
		t.Errorf("absorbing launches and sends nothing; Ensured=%v Sent=%v", cc.Ensured, cc.Sent)
	}
	refreshed := false
	for _, m := range cc.SetMetaCalls() {
		if m.Key == ccpool.MetaKeyLeaseUntil {
			if m.ExternalID != "att-1" {
				t.Errorf("only the absorbed session's lease is refreshed, got a write to %q", m.ExternalID)
			}
			refreshed = true
		}
	}
	if !refreshed {
		t.Error("the absorbing dispatch must take over the absorbed session's lease")
	}
}

// TestDispatch_redeliveryAfterSettle_launchesFreshSession is the consequence ADR
// 0094 accepts against ADR 0065's INV-EVT-2 closure: once the first attempt has
// settled (and its handler's lease is gone), a redelivery of the SAME event is a
// dispatch like any other and runs in a fresh session; the handler-closed settled
// row is never adopted.
func TestDispatch_redeliveryAfterSettle_launchesFreshSession(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := feedbackRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "zr-c")

	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	settled := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{ccpool.MetaKeyEventID: "evt-1"},
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{}, // the FIRST dispatch's prior-session check: nothing yet
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking}},
		// att-1 has settled (idle) when its own finishWait runs: cleanupWorktree's
		// needs_input, live-peer and quiet List reads, then closeSettledSession's
		// re-read (4 Lists, INV-CCH-17), which closes it (non-purge).
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateIdle}},
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateIdle}},
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateIdle}},
		{{ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateIdle}},
		// By the time the redelivery lands the handler's close has stamped the row
		// (dtest.FakeCC.Close does not mutate it, so it is scripted here).
		{settled},
		{settled, {ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking}},
	}}
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-c"}, EventID: "evt-1"}
	dispatch := func(externalID string) (report.Result, error) {
		deps := newExec(cc, bd, cfg).deps
		deps.ExternalID = externalID
		deps.Git = &dtest.NoopGit{}
		deps.GitOpener = (&dtest.NoopGitOpener{}).Open
		return ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	}
	if _, err := dispatch("att-1"); err != nil {
		t.Fatalf("first dispatch should succeed, got %v", err)
	}
	if _, err := dispatch("att-2"); err != nil {
		t.Fatalf("the redelivery must run its own session to completion, got %v", err)
	}
	if want := []string{"att-1", "att-2"}; !slices.Equal(cc.Ensured, want) {
		t.Errorf("a redelivery after the attempt settled launches a fresh session; Ensured=%v want %v", cc.Ensured, want)
	}
	if want := []string{"att-1", "att-2"}; !slices.Equal(cc.Sent, want) {
		t.Errorf("each session gets its own nudge and the settled one nothing; Sent=%v want %v", cc.Sent, want)
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
	if !hasDeleteBranchCall(opener, "pg-router/zr-w", true) {
		t.Errorf("expected a DeleteBranch(force=true) call for pg-router/zr-w (pg2-ci75j); calls=%v", opener.WTM.Calls)
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
	if anyDeleteBranchCall(opener) {
		t.Errorf("needs_input session's branch must NOT be deleted; calls=%v", opener.WTM.Calls)
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
	if !hasDeleteBranchCall(opener, "pg-router/zr-w", true) {
		t.Errorf("expected a DeleteBranch(force=true) call for pg-router/zr-w after budget hard-stop (pg2-ci75j); calls=%v", opener.WTM.Calls)
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

// hasDeleteBranchCall reports whether opener's WTM recorded a "branch-delete"
// call for branch with exactly the given force value (bead pg2-ci75j).
func hasDeleteBranchCall(opener *dtest.NoopGitOpener, branch string, force bool) bool {
	want := strconv.FormatBool(force)
	for _, c := range opener.WTM.Calls {
		if len(c) == 3 && c[0] == "branch-delete" && c[1] == branch && c[2] == want {
			return true
		}
	}
	return false
}

// anyDeleteBranchCall reports whether opener's WTM recorded any
// "branch-delete" call at all.
func anyDeleteBranchCall(opener *dtest.NoopGitOpener) bool {
	for _, c := range opener.WTM.Calls {
		if len(c) > 0 && c[0] == "branch-delete" {
			return true
		}
	}
	return false
}

// --- pg2-2grpj: close-or-triage completion (escalation triager) ---

func triagerRole(cfg config.Config) roles.Role {
	r := workerRole(cfg)
	r.Name = "pg2-escalation-triager"
	r.CCPool.Completion = roles.CloseOrTriage
	r.CCPool.BeadsDir = "/pg2"
	return r
}

func triagerRun(t *testing.T, bd *dtest.ScriptBD, live bool) error {
	t.Helper()
	cfg := fastCfg()
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-pg2-escalation-triager-pg2-x", Live: live, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: triagerRole(cfg), Item: item.Item{ID: "pg2-x"}}
	return e.waitDone(context.Background(), nil, d, "pg-router-pg2-escalation-triager-pg2-x")
}

func TestWaitDone_triager_closeIsSuccess(t *testing.T) {
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"pg2-x": {"open", "closed"}}, Labels: map[string][]string{"pg2-x": {"escalated"}}}
	if err := triagerRun(t, bd, true); err != nil {
		t.Fatalf("closed must succeed: %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("no on_failure effect expected; updates=%v", bd.Updates)
	}
}

func TestWaitDone_triager_triageCommentIsSuccessAndNotHumanLabeled(t *testing.T) {
	bd := &dtest.ScriptBD{
		StatusSeq:  map[string][]string{"pg2-x": {"open", "open", "open"}},
		CommentSeq: map[string][]int{"pg2-x": {4, 4, 5}},
		Labels:     map[string][]string{"pg2-x": {"escalated"}},
	}
	if err := triagerRun(t, bd, true); err != nil {
		t.Fatalf("Triage (comment, stays escalated) must succeed: %v", err)
	}
	if dtest.HasUpdate(bd, "add-label human") {
		t.Errorf("Triage must not be human-labeled; updates=%v", bd.Updates)
	}
}

func TestWaitDone_triager_escalateIsSuccess(t *testing.T) {
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"pg2-x": {"open"}}, Labels: map[string][]string{"pg2-x": {"human"}}}
	if err := triagerRun(t, bd, true); err != nil {
		t.Fatalf("Escalate (escalated removed, human added) must succeed: %v", err)
	}
}

func TestWaitDone_triager_exitWithoutChangeStillFails(t *testing.T) {
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"pg2-x": {"open"}}, Labels: map[string][]string{"pg2-x": {"escalated"}}}
	if err := triagerRun(t, bd, false); err == nil {
		t.Fatal("a session that exits without changing its bead must still fail")
	}
	if !dtest.HasUpdate(bd, "update pg2-x --add-label human --status=open --assignee=") {
		t.Errorf("on_failure=add-human must apply; updates=%v", bd.Updates)
	}
}

func TestBeadsDirFor(t *testing.T) {
	if got := beadsDirFor("/zr", &roles.CCPoolConfig{BeadsDir: "/pg2"}); got != "/pg2" {
		t.Errorf("got %q", got)
	}
	if got := beadsDirFor("/zr", &roles.CCPoolConfig{}); got != "/zr" {
		t.Errorf("got %q", got)
	}
}

// A dontAsk session is told up front that .claude/** writes are auto-denied and
// must be handed back (bead pg2-a4kzx); the notice sits before the task prompt
// and, when authorship_guard is on, after the authorship preamble.
func TestRenderNudge_dontAsk_includesProtectedPathNotice(t *testing.T) {
	cfg := fastCfg()
	cfg.PermissionMode = "dontAsk"
	r := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, cfg)
	role := workerRole(cfg)
	got := r.renderNudge(role.CCPool, DispatchContext{Role: role, Item: item.Item{ID: "pg2-x"}}, "/wt")
	notice := prompt.ProtectedPathNotice()
	if !strings.Contains(got, notice) {
		t.Fatalf("dontAsk nudge missing protected-path notice: %q", got)
	}
	ni, ai, bi := strings.Index(got, notice), strings.Index(got, prompt.AuthorshipPreamble()), strings.Index(got, "implement work bead pg2-x")
	if ai < 0 || ai >= ni || ni >= bi {
		t.Fatalf("order must be authorship preamble < notice < task body; got %d,%d,%d", ai, ni, bi)
	}
}

func TestRenderNudge_notDontAsk_omitsProtectedPathNotice(t *testing.T) {
	cfg := fastCfg()
	cfg.PermissionMode = "bypassPermissions"
	r := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, cfg)
	role := workerRole(cfg)
	got := r.renderNudge(role.CCPool, DispatchContext{Role: role, Item: item.Item{ID: "pg2-x"}}, "/wt")
	if strings.Contains(got, prompt.ProtectedPathNotice()) {
		t.Fatalf("non-dontAsk nudge must not carry the notice: %q", got)
	}
}
