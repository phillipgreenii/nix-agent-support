package executor

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// verbsOf lists every verb of a dispatch result's actions, in order.
func verbsOf(res report.Result) []report.Verb {
	var vs []report.Verb
	for _, a := range res.Actions {
		vs = append(vs, a.Verb)
	}
	return vs
}

// TestDispatch_deathReleasesClaim (pg2-0fsuu): a review session that dies
// holding its claim (the 2026-10-08 incident: an HTTP 429 spend limit errored
// the session and the handler closed its row) must leave the bead released
// (status open, assignee cleared) in the SAME single update that applies the
// role's add-human on_failure, and the dispatch result must carry the unclaim
// verb. Before the fix only `--add-label human` was written, so the bead stayed
// in_progress under the dead session until the exporter alert fired.
func TestDispatch_deathReleasesClaim(t *testing.T) {
	cases := []struct {
		name      string
		onFailure roles.FailureAction
		wantBD    string
		wantVerbs []report.Verb
	}{
		{
			"add-human", roles.AddHuman, "update bead-1 --add-label human --status=open --assignee=",
			[]report.Verb{report.Escalated, report.Unclaimed},
		},
		{
			"unclaim", roles.Unclaim, "update bead-1 --status=open --assignee=",
			[]report.Verb{report.Unclaimed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastCfg()
			cfg.WorktreeDir = t.TempDir()
			role := noBudget(reviewRole(cfg))
			role.CCPool.OnFailure = tc.onFailure
			display := role.DisplayName(cfg.SessionPrefix, "bead-1")
			dead := ccpool.Session{
				ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateErrored, CloseReason: "handler",
				Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:bead-1", ccpool.MetaKeyHeadSHA: "h1"},
			}
			// The bead stays claimed by the dead session until the handler acts.
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bead-1": {"in_progress"}}}
			fake := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{dead}}}
			d := DispatchContext{
				Role: role, EventID: "review.ready:bead-1",
				Item: item.Item{ID: "bead-1", Metadata: map[string]any{"repo": "example/repo", "pr_number": "7", "head_sha": "h1"}},
			}
			deps := newExec(fake, bd, cfg).deps
			deps.ExternalID = "att-2"
			deps.Git = &dtest.NoopGit{}
			deps.GitOpener = (&dtest.NoopGitOpener{}).Open

			res, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
			if err == nil || err.Error() != "bead-1: session exited before completing" {
				t.Fatalf("the failure text must be unchanged; err=%v", err)
			}
			if !dtest.HasUpdate(bd, tc.wantBD) {
				t.Errorf("the dead session's claim must be released in one update %q; updates=%v", tc.wantBD, bd.Updates)
			}
			for _, u := range bd.Updates {
				if u == "update bead-1 --add-label human" {
					t.Errorf("the human label must ride the same update as the release, not a separate one; updates=%v", bd.Updates)
				}
			}
			got := verbsOf(res)
			if len(got) != len(tc.wantVerbs) {
				t.Fatalf("result actions = %v, want %v", got, tc.wantVerbs)
			}
			for i := range got {
				if got[i] != tc.wantVerbs[i] {
					t.Fatalf("result actions = %v, want %v", got, tc.wantVerbs)
				}
			}
			for _, a := range res.Actions {
				if len(a.Refs) != 1 || a.Refs[0].ID != "bead-1" {
					t.Errorf("each action must reference the bead; got %+v", a)
				}
			}
		})
	}
}
