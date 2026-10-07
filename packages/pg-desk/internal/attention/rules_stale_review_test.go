package attention

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func TestReviewStaleAfterPushRule(t *testing.T) {
	stale := func(mod func(*prSpec)) prSpec {
		p := prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingTeam, ci: "success"}
		p.approvals.SelfReviewStale = true
		mod(&p)
		return p
	}
	cases := []struct {
		name string
		spec prSpec
		want bool
	}{
		{"my review went stale after a push", stale(func(*prSpec) {}), true},
		{"it raises whatever the panel says", stale(func(p *prSpec) { p.panel = interpret.PanelTeamAwaitingOwner }), true},
		{"a standing teammate approval takes it off my plate", stale(func(p *prSpec) { p.approvals.HumanApprovalStanding = true }), false},
		{"a merge conflict dampens it", stale(func(p *prSpec) { p.conflict = true }), false},
		{"my review is not stale", stale(func(p *prSpec) { p.approvals.SelfReviewStale = false }), false},
		{"a closed PR", stale(func(p *prSpec) { p.state = "closed" }), false},
		{"a draft PR", stale(func(p *prSpec) { p.draft = true }), false},
		{"an own PR has no review of mine to go stale", stale(func(p *prSpec) { p.ownership = "mine" }), false},
		{"degraded interpretation", stale(func(p *prSpec) { p.degraded = true }), false},
		{"stored facts unavailable", stale(func(p *prSpec) { p.noFacts = true }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			put(t, st, tc.spec)
			tr := evaluate(t, st, baseConfig()).Traces[Ref("pr", tc.spec.id())]
			var got *Candidate
			for i := range tr.Survived {
				if tr.Survived[i].Kind == KindReviewStaleAfterPush {
					got = &tr.Survived[i]
				}
			}
			if !tc.want {
				if got != nil {
					t.Fatalf("raised %+v, want nothing", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("did not raise; trace %+v", tr)
			}
			if got.Severity != SeverityMedium || !strings.Contains(got.Reason, "since my review") {
				t.Errorf("candidate = %+v, want medium severity and a reason saying new commits landed since my review", *got)
			}
			if got.SelfBroken {
				t.Error("a stale review is not the entity being broken; it must not be held back by an open dependency")
			}
		})
	}
}

// A row stored before the staleness fields existed decodes with both false,
// so the rule stays quiet until the entity is hydrated again.
func TestReviewStaleAfterPushRule_QuietOnRowsStoredBeforeTheField(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingTeam, ci: "success"})
	if res := evaluate(t, st, baseConfig()); len(res.Items) != 0 {
		t.Fatalf("items = %+v, want none", res.Items)
	}
}

// The re-review of a PR the operator was also asked to review again is ONE
// item (INV-ATTNEVAL-3); the severity decides which reason leads.
func TestReviewStaleAfterPushRule_CollapsesWithReviewRequested(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	p := prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe, ci: "success"}
	p.approvals.SelfReviewStale = true
	put(t, st, p)
	res := evaluate(t, st, baseConfig())
	if len(res.Items) != 1 {
		t.Fatalf("items = %+v, want exactly one", res.Items)
	}
	if !strings.Contains(res.Items[0].Summary, "(+1 more)") {
		t.Errorf("summary %q should say the PR raised one more reason", res.Items[0].Summary)
	}
}
