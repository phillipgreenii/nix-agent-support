package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// The "split-triage" precheck (INV-CCH-22): PrecheckTriage with
// needs-split-review in place of escalated.

func splitTriageRole() roles.Role {
	r := triageRole(fastCfg())
	r.CCPool.Completion = roles.CloseOrSplitTriage
	r.CCPool.Precheck = roles.PrecheckSplitTriage
	return r
}

// needsSplit is a bead the split triager's discovery query lists.
func needsSplit() beads.Issue {
	return beads.Issue{ID: "zr-t", Status: "open", Labels: []string{"needs-split-review", "agent-support"}}
}

func runSplitTriage(bd *recBD, iss *beads.Issue) (Skip, bool) {
	deps := Deps{Cfg: fastCfg(), BD: bd, Now: func() time.Time { return time.Unix(0, 0) }}
	return Precheck(context.Background(), DispatchContext{Role: splitTriageRole(), Item: item.Item{ID: "zr-t"}}, deps, iss)
}

func TestPrecheckSplitTriage_Dispositions(t *testing.T) {
	with := func(f func(*beads.Issue)) *beads.Issue { i := needsSplit(); f(&i); return &i }
	tests := []struct {
		name string
		bead *beads.Issue
		want string // "" = proceeds
	}{
		{"unreadable bead fails open", nil, ""},
		{"normal bead launches a session", with(func(*beads.Issue) {}), ""},
		{"assigned to a peer still launches", with(func(i *beads.Issue) { i.Assignee = "someone-else" }), ""},
		{"closed", with(func(i *beads.Issue) { i.Status = "closed" }), SkipBeadClosed},
		{"label gone", with(func(i *beads.Issue) { i.Labels = []string{"agent-support"} }), SkipBeadNotSplitReview},
		{"label gone, handed to a person", with(func(i *beads.Issue) { i.Labels = []string{"attention", "human"} }), SkipBeadNotSplitReview},
		{"escalated alone is not the split queue", with(func(i *beads.Issue) { i.Labels = []string{"escalated"} }), SkipBeadNotSplitReview},
		{"human with the label", with(func(i *beads.Issue) { i.Labels = append(i.Labels, "human") }), SkipBeadHumanSplitReview},
		{"attention with the label", with(func(i *beads.Issue) { i.Labels = append(i.Labels, "attention") }), SkipBeadHumanSplitReview},
		{"deferred status", with(func(i *beads.Issue) { i.Status = "deferred" }), SkipBeadDeferred},
		{"blocked status", with(func(i *beads.Issue) { i.Status = "blocked" }), SkipBeadBlocked},
		{"in_progress", with(func(i *beads.Issue) { i.Status = "in_progress" }), SkipBeadNotOpen},
		{"open blocks dependency", with(func(i *beads.Issue) {
			i.Dependencies = []beads.Dependency{{ID: "zr-x", Status: "open", DependencyType: "blocks"}}
		}), SkipBeadBlocked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, skipped := runSplitTriage(&recBD{}, tc.bead)
			if tc.want == "" {
				if skipped {
					t.Fatalf("must proceed, got skip %+v", got)
				}
				return
			}
			if !skipped || got.Reason != tc.want {
				t.Fatalf("got (%+v, %v), want skip %q", got, skipped, tc.want)
			}
			if Moot(tc.want) != got.Moot {
				t.Errorf("Moot flag %v disagrees with Moot(%q)", got.Moot, tc.want)
			}
		})
	}
}

func TestPrecheckSplitTriage_HumanLabelCommentsThenStrips(t *testing.T) {
	for _, l := range beads.HumanLabels {
		t.Run(l, func(t *testing.T) {
			iss := needsSplit()
			iss.Labels = append(iss.Labels, l)
			bd := &recBD{}
			got, skipped := runSplitTriage(bd, &iss)
			if !skipped || got.Reason != SkipBeadHumanSplitReview || !got.Moot || got.Retryable {
				t.Fatalf("got (%+v, %v), want a moot %s", got, skipped, SkipBeadHumanSplitReview)
			}
			if len(bd.calls) != 2 || !strings.HasPrefix(bd.calls[0], "comment zr-t ") || bd.calls[1] != "update zr-t --remove-label needs-split-review" {
				t.Fatalf("calls = %q, want a comment THEN removal of needs-split-review only", bd.calls)
			}
			if !strings.Contains(bd.calls[0], "Split triager precheck") || !strings.Contains(bd.calls[0], "needs-split-review") {
				t.Errorf("comment must say what was done: %q", bd.calls[0])
			}
		})
	}
}

func TestPrecheckSplitTriage_WriteFailureDeclinesRetryably(t *testing.T) {
	for _, failOn := range []string{"comment", "update"} {
		t.Run(failOn, func(t *testing.T) {
			iss := needsSplit()
			iss.Labels = append(iss.Labels, "human")
			got, skipped := runSplitTriage(&recBD{failOn: failOn}, &iss)
			if !skipped || got.Reason != SkipBeadHumanStripFailed || !got.Retryable || got.Moot {
				t.Fatalf("got (%+v, %v), want a retryable %s", got, skipped, SkipBeadHumanStripFailed)
			}
		})
	}
}

func TestPrecheckSplitTriage_RepeatIsMootNoWrites(t *testing.T) {
	iss := needsSplit()
	iss.Labels = []string{"human", "attention"}
	bd := &recBD{}
	got, skipped := runSplitTriage(bd, &iss)
	if !skipped || got.Reason != SkipBeadNotSplitReview || !got.Moot || len(bd.calls) != 0 {
		t.Fatalf("got (%+v, %v) calls=%q, want moot %s and no writes", got, skipped, bd.calls, SkipBeadNotSplitReview)
	}
}

func TestPrecheckSplitTriage_ParseAndNoCCPool(t *testing.T) {
	var p roles.Precheck
	if err := p.UnmarshalText([]byte("split-triage")); err != nil || p != roles.PrecheckSplitTriage {
		t.Fatalf("split-triage must parse: %v %q", err, p)
	}
	if _, skipped := PrecheckSplitTriage(context.Background(), DispatchContext{Role: roles.Role{Name: "t"}}, Deps{}, &beads.Issue{Status: "closed"}); skipped {
		t.Error("a role with no ccpool block is never prechecked")
	}
}
