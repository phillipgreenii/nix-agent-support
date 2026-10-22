package executor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
)

// labelBD is a stateful bd fake for the dispatch path: it keeps the labels an
// `update --add-label` writes (serving them on `show`, as bd does) and records
// every call in order, so a dispatch's budget stop can be followed end to end.
type labelBD struct {
	inner *dtest.ScriptBD
	mu    sync.Mutex
	set   map[string]bool
	calls []string
}

func (b *labelBD) Run(ctx context.Context, args ...string) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, strings.Join(args, " "))
	if args[0] == "update" {
		for i, a := range args {
			if a == "--add-label" && i+1 < len(args) {
				if b.set == nil {
					b.set = map[string]bool{}
				}
				b.set[args[i+1]] = true
			}
		}
	}
	labels := make([]string, 0, len(b.set))
	for l := range b.set {
		labels = append(labels, l)
	}
	b.mu.Unlock()
	out, err := b.inner.Run(ctx, args...)
	if err != nil || args[0] != "show" {
		return out, err
	}
	var m map[string]any
	if json.Unmarshal([]byte(out), &m) != nil {
		return out, nil
	}
	m["labels"] = labels
	raw, _ := json.Marshal(m)
	return string(raw), nil
}

func (b *labelBD) idx(exact string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Index(b.calls, exact)
}

func (b *labelBD) has(prefix string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.ContainsFunc(b.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// drainLikeRole is a drain role as the deployment wires it: close-or-release,
// the 2h-style budget (a token cap here so the usage ramp trips it at once) and
// budgetStopEscalateAfter 1 (bead pg2-izvr8).
func drainLikeRole(name string, cfg config.Config) roles.Role {
	return roles.Role{
		Name: name, Type: "ccpool",
		CCPool: &roles.CCPoolConfig{
			Actor: "pgii-pool__" + name, Completion: roles.CloseOrRelease,
			OnFailure: roles.AddHuman, OnDispatchFail: roles.DispatchLeave,
			PromptBody: workerPromptBody, Prompt: mustParsePrompt(name, workerPromptBody),
			Budget: cfg.WorkerBudget(), BudgetStopEscalateAfter: 1,
		},
	}
}

// TestDispatch_budgetStopAtThresholdOne_escalatesBeforeUnclaim drives the real
// dispatch path (Dispatch -> watchdog -> terminal -> escalate) through the FIRST
// budget stop of a drain-style role (threshold 1), as the pg-router wiring rule
// asks for a changed handler path (bead pg2-cty80). The bead must end labelled
// escalated, never needs-split-review or human, with the label written before
// the release; a triage role's stop goes to human instead.
func TestDispatch_budgetStopAtThresholdOne_escalatesBeforeUnclaim(t *testing.T) {
	for _, tc := range []struct {
		role      string
		wantLabel string
		notLabels []string
	}{
		{"drain-pg2", "escalated", []string{"needs-split-review", "human"}},
		{"drain-zr", "escalated", []string{"needs-split-review", "human"}},
		{"pg2-escalation-triager", "human", []string{"escalated", "needs-split-review"}},
	} {
		t.Run(tc.role, func(t *testing.T) {
			cfg := fastCfg()
			cfg.BudgetTokens = 1000
			cfg.WorktreeDir = t.TempDir()
			bd := &labelBD{inner: &dtest.ScriptBD{StatusSeq: map[string][]string{"pg2-x": {"in_progress"}}}}
			ext := "pg-router-" + tc.role + "-pg2-x"
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: ext, Live: true, TranscriptPath: "/t", CWD: "/repo"}}}}
			d := DispatchContext{Role: drainLikeRole(tc.role, cfg), Item: item.Item{ID: "pg2-x"}}
			clk := &dtest.ManualClock{T: time.Unix(0, 0)}
			deps := Deps{CC: cc, BD: bd, Cfg: cfg, Now: clk.Now, Tick: clk.TickAdvancing()}
			deps.ExternalID = ext
			deps.Git = &dtest.NoopGit{}
			deps.GitOpener = (&dtest.NoopGitOpener{}).Open
			origTick := deps.Tick
			deps.Tick = func(ctx context.Context, dur time.Duration) error { // wall-clock pacing, as TestDispatch_watchdogHardStop_unclaimed
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(dur):
				}
				return origTick(ctx, dur)
			}
			deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}}

			res, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
			if !errors.Is(err, watchdog.ErrBudgetExceeded) {
				t.Fatalf("the dispatch must end in the watchdog's budget hard stop, got %v", err)
			}
			if v := verbOf(res); v != report.Unclaimed {
				t.Errorf("a budget hard stop unclaims its bead, got verb %q", v)
			}
			wantCall := "update pg2-x --add-label " + tc.wantLabel
			if tc.wantLabel == "human" {
				// A triage role's own stop is a hand-off to the operator: the strict
				// pair human + attention in ONE call that also drops escalated
				// (bead pg2-ikkpk).
				wantCall = "update pg2-x --add-label human --add-label attention --remove-label escalated"
			}
			lab := bd.idx(wantCall)
			un := bd.idx("update pg2-x --status=open --assignee=")
			if lab < 0 || un < 0 || lab > un {
				t.Fatalf("%s must be written BEFORE the release; label=%d unclaim=%d calls=%v", tc.wantLabel, lab, un, bd.calls)
			}
			for _, l := range tc.notLabels {
				if bd.has("update pg2-x --add-label " + l) {
					t.Errorf("must not add %q; calls=%v", l, bd.calls)
				}
			}
			if !bd.has("comment pg2-x budget stops reached threshold 1;") {
				t.Errorf("the explanatory comment is missing; calls=%v", bd.calls)
			}
		})
	}
}
