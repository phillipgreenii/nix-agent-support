package ciops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ciprovider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func listRuns(t *testing.T, e *env, args any) []schema.CIRun {
	t.Helper()
	got, err := e.call(OpListRuns, args)
	if err != nil {
		t.Fatalf("list_runs: %v", err)
	}
	rs, ok := got.([]schema.CIRun)
	if !ok {
		t.Fatalf("list_runs result is %T, want []schema.CIRun", got)
	}
	return rs
}

func TestHandlersCarryTheCISchemaVersionAndTheThreeOps(t *testing.T) {
	h := Handlers(Deps{})
	if len(h) != 3 || !reflect.DeepEqual(Ops, []string{"list_runs", "get_logs", "rerun_failed"}) {
		t.Fatalf("handlers %v, ops %v", h, Ops)
	}
	for _, op := range Ops {
		if h[op].SchemaVersion != schema.CISchemaVersion || h[op].Handle == nil {
			t.Errorf("%s: schema version %d (want %d), handle nil %v", op, h[op].SchemaVersion, schema.CISchemaVersion, h[op].Handle == nil)
		}
	}
}

func TestListRunsAllGroupsFreshIsALocalAnswerFromTheCache(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.seedPR(node1)
	e.seedRuns(node1, false,
		ciRun("12", "ci", head1, "completed", "failure"),
		ciRun("11", "lint", head1, "completed", "success"))
	originBefore := e.rig.Origin.CallCount()

	rs := listRuns(t, e, map[string]any{"pr_id": ref1})
	if len(rs) != 2 || rs[0].ID != "12" || rs[1].ID != "11" {
		t.Fatalf("runs = %+v, want the stored order (newest first)", rs)
	}
	if e.gh.runsCount() != 0 || e.rig.Origin.CallCount() != originBefore {
		t.Errorf("a fresh answer made %d runs requests and %d summary calls", e.gh.runsCount(), e.rig.Origin.CallCount()-originBefore)
	}
	for i, r := range rs {
		if r.ServedFrom != "cache" || r.Stale || r.AgeSeconds == nil || r.PRID != ref1 {
			t.Errorf("run %d annotations: served_from %q stale %v age %v pr_id %q", i, r.ServedFrom, r.Stale, r.AgeSeconds, r.PRID)
		}
		if len(r.Groups) != 2 || r.Groups[0].Group != "summary" || r.Groups[1].Group != "runs" {
			t.Errorf("run %d groups = %+v, want summary and runs", i, r.Groups)
		}
		if want := e.clk.Now().Add(-time.Minute).UTC().Format(time.RFC3339); r.AsOf != want {
			t.Errorf("run %d as_of = %q, want the runs group's fetched_at %q", i, r.AsOf, want)
		}
	}
	// Each element owns its annotations: changing one never changes another.
	rs[0].Groups[0].Group = "changed"
	if rs[1].Groups[0].Group != "summary" {
		t.Error("two elements share one groups slice")
	}
	if age := *rs[0].AgeSeconds; age != 60 {
		t.Errorf("age_seconds = %d, want 60 (the oldest group served: runs, fetched a minute ago)", age)
	}
}

func TestListRunsFetchesStaleRunsAtInteractivePriority(t *testing.T) {
	gh := &fakeGH{runs: func(string, int) ghAnswer {
		return ok(runsBody(runJSON(21, "ci", head1, "completed", "success", 1)))
	}}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	e.seedRuns(node1, true, ciRun("old", "ci", head1, "completed", "failure"))

	rs := listRuns(t, e, map[string]any{"pr_id": ref1})
	if len(rs) != 1 || rs[0].ID != "21" || rs[0].Conclusion != "success" {
		t.Fatalf("runs = %+v, want the freshly fetched run", rs)
	}
	if rs[0].ServedFrom != "origin" || rs[0].Stale {
		t.Errorf("served_from %q stale %v, want origin and not stale", rs[0].ServedFrom, rs[0].Stale)
	}
	if gh.runsCount() != 1 {
		t.Errorf("%d runs requests, want 1", gh.runsCount())
	}
	// The fetch was admitted as a class 1 call a caller waits on.
	var interactive int
	for _, ev := range e.spends(governor.BucketRESTCore) {
		if ev.Class == governor.ClassInteractive && ev.Purpose == governor.PurposeInteractive && ev.Group == groups.Runs {
			interactive++
		}
	}
	if interactive != 1 {
		t.Errorf("%d class 1 interactive runs spends, want 1: %+v", interactive, e.spends(governor.BucketRESTCore))
	}
}

func TestListRunsServesStaleContentInsideExpiryWhenTheOriginFails(t *testing.T) {
	gh := &fakeGH{runs: func(string, int) ghAnswer {
		return ghAnswer{status: 502, body: `{"message":"Bad Gateway"}`}
	}}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	e.seedRuns(node1, true, ciRun("old", "ci", head1, "completed", "failure"))

	rs := listRuns(t, e, map[string]any{"pr_id": ref1})
	if len(rs) != 1 || rs[0].ID != "old" {
		t.Fatalf("runs = %+v, want the stored run", rs)
	}
	if !rs[0].Stale || rs[0].ServedFrom != "cache" {
		t.Errorf("stale %v served_from %q, want stale: true from the cache", rs[0].Stale, rs[0].ServedFrom)
	}
	if rs[0].AsOf != e.clk.Now().Add(-time.Hour).UTC().Format(time.RFC3339) {
		t.Errorf("as_of = %q, want the stored fetched_at", rs[0].AsOf)
	}
}

func TestListRunsFreshTrueForcesTheSummaryAndTheRunsToOrigin(t *testing.T) {
	gh := &fakeGH{runs: func(string, int) ghAnswer {
		return ok(runsBody(runJSON(31, "ci", head1, "completed", "success", 1)))
	}}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	e.seedRuns(node1, false, ciRun("old", "ci", head1, "completed", "failure"))
	originBefore := e.rig.Origin.CallCount()

	rs := listRuns(t, e, map[string]any{"pr_id": ref1, "fresh": true})
	if len(rs) != 1 || rs[0].ID != "31" || rs[0].ServedFrom != "origin" {
		t.Fatalf("runs = %+v", rs)
	}
	if gh.runsCount() != 1 {
		t.Errorf("%d runs requests, want 1 (forced)", gh.runsCount())
	}
	if got := e.rig.Origin.CallCount() - originBefore; got != 1 {
		t.Errorf("%d summary calls, want 1 (forced)", got)
	}
	// Without fresh the same, now fresh, answer is local.
	rs = listRuns(t, e, map[string]any{"pr_id": ref1})
	if rs[0].ServedFrom != "cache" || gh.runsCount() != 1 {
		t.Errorf("a second read: served_from %q, %d runs requests", rs[0].ServedFrom, gh.runsCount())
	}
}

func TestListRunsAnAnswerForAPRWithNoRunsIsAnEmptyListLikeTheOldOp(t *testing.T) {
	e := newEnv(t, envOptions{}) // the default gh lists no runs
	e.seedPR(node1)
	rs := listRuns(t, e, map[string]any{"pr_id": ref1})
	if rs == nil || len(rs) != 0 {
		t.Fatalf("runs = %#v, want a non-nil empty list", rs)
	}
	if b, _ := json.Marshal(rs); string(b) != "[]" {
		t.Errorf("marshals as %s, want []", b)
	}

	// The old op marshals [] when gh lists no run.
	old := ciprovider.NewWithDeps(&oldGH{list: "[]"}, oldResolver{})
	oldRuns, err := old.ListRuns(context.Background(), ref1)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(oldRuns); string(b) != "[]" {
		t.Errorf("the old op marshals %s for no runs", b)
	}
}

// oldGH is the gh of the old backend: `gh run list --json` and `gh run view
// --json jobs`.
type oldGH struct {
	list string
	jobs string
}

func (g *oldGH) Run(_ context.Context, args ...string) ([]byte, error) {
	switch {
	case len(args) >= 2 && args[0] == "run" && args[1] == "list":
		return []byte(g.list), nil
	case len(args) >= 2 && args[0] == "run" && args[1] == "view":
		return []byte(g.jobs), nil
	}
	return nil, errors.New("oldGH: unexpected call " + strings.Join(args, " "))
}

type oldResolver struct{}

func (oldResolver) Resolve(context.Context, string) (string, string, error) {
	return repo1, "feature", nil
}

func TestListRunsResultEqualsTheOldListRunsForEquivalentRuns(t *testing.T) {
	// The same three runs and the same failed job, as GitHub's REST API (the new
	// path) and as gh's JSON (the old one) report them. The first run is on the
	// PR's current head, which is where the old op takes the head from.
	gh := &fakeGH{
		runs: func(string, int) ghAnswer {
			return ok(runsBody(
				runJSON(41, "ci", head1, "completed", "FAILURE", 2),
				runJSON(40, "lint", head1, "COMPLETED", "Success", 1),
				runJSON(39, "ci", "head-old", "completed", "failure", 1),
			))
		},
		jobs: func(runID string, attempt int) ghAnswer {
			return ok(`{"total_count":2,"jobs":[` +
				`{"id":801,"name":"changes","status":"completed","conclusion":"success","html_url":"https://example.invalid/job/801"},` +
				`{"id":802,"name":"build","status":"completed","conclusion":"failure","html_url":"https://example.invalid/job/802"}]}`)
		},
	}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	got := listRuns(t, e, map[string]any{"pr_id": ref1})

	oldList := `[` +
		`{"databaseId":41,"name":"ci","status":"completed","conclusion":"FAILURE","url":"https://example.invalid/runs/41","headBranch":"feature","headSha":"` + head1 + `","attempt":2},` +
		`{"databaseId":40,"name":"lint","status":"COMPLETED","conclusion":"Success","url":"https://example.invalid/runs/40","headBranch":"feature","headSha":"` + head1 + `","attempt":1},` +
		`{"databaseId":39,"name":"ci","status":"completed","conclusion":"failure","url":"https://example.invalid/runs/39","headBranch":"feature","headSha":"head-old","attempt":1}]`
	oldJobs := `{"jobs":[` +
		`{"databaseId":801,"name":"changes","status":"completed","conclusion":"success","url":"https://example.invalid/job/801"},` +
		`{"databaseId":802,"name":"build","status":"completed","conclusion":"failure","url":"https://example.invalid/job/802"}]}`
	want, err := ciprovider.NewWithDeps(&oldGH{list: oldList, jobs: oldJobs}, oldResolver{}).ListRuns(context.Background(), ref1)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != len(want) {
		t.Fatalf("%d runs, the old op returns %d", len(got), len(want))
	}
	for i := range want {
		g := got[i]
		// Only the fields the new answer adds, and the old op's own as-of time
		// (its read's call time), differ.
		g.ServedFrom, g.AgeSeconds, g.Groups, g.AsOf = "", nil, nil, ""
		w := want[i]
		w.AsOf = ""
		if !reflect.DeepEqual(g, w) {
			t.Errorf("run %d\n got  %+v\n want %+v", i, g, w)
		}
	}
	if len(got[0].Jobs) != 2 || got[1].Jobs != nil || got[2].Jobs != nil {
		t.Errorf("jobs: %+v / %+v / %+v, want jobs only for the current head's failure", got[0].Jobs, got[1].Jobs, got[2].Jobs)
	}
	if got[0].Status != "completed" || got[0].Conclusion != "failure" || got[1].Conclusion != "success" {
		t.Errorf("status and conclusion must be lower-case: %+v %+v", got[0], got[1])
	}
}

func TestListRunsAnswersTheCallersSpellingOfThePRID(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.seedPR(node1)
	e.seedRuns(node1, false, ciRun("12", "ci", head1, "completed", "success"))
	for _, id := range []string{node1, "ACME-ORG/Widget#1"} {
		rs := listRuns(t, e, map[string]any{"pr_id": id})
		if len(rs) != 1 || rs[0].PRID != id {
			t.Errorf("pr_id %q: runs = %+v, want the id as written", id, rs)
		}
	}
}

func TestListRunsRefusesWhatItCannotAnswer(t *testing.T) {
	e := newEnv(t, envOptions{})
	cases := []struct {
		name string
		args any
		want string
	}{
		{"no id", map[string]any{}, "invalid_argument"},
		{"blank id", map[string]any{"pr_id": "  "}, "invalid_argument"},
		{"malformed id", map[string]any{"pr_id": "acme-org/widget"}, "invalid_argument"},
		{"unknown PR", map[string]any{"pr_id": "acme-org/nowhere#9"}, "not_found"},
		{"bad arguments", "not an object", "invalid_argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.call(OpListRuns, c.args)
			if code(err) != c.want {
				t.Errorf("err = %v (%s), want %s", err, code(err), c.want)
			}
		})
	}
}

func TestListRunsIsUnavailableNamingTheCapWhenTheGovernorRefusesAndNothingIsStored(t *testing.T) {
	cfg := governor.Config{
		GraphQL: governor.Limits{Cap: 1000}, RESTCore: governor.Limits{Cap: 1}, RESTSearch: governor.Limits{Cap: 1000},
		BackgroundShare: 1,
	}
	e := newEnv(t, envOptions{govConfig: &cfg})
	e.seedPR(node1)
	// Spend the one request the REST core bucket allows.
	tk, err := e.gov.Admit(governor.Request{Bucket: governor.BucketRESTCore, Class: governor.ClassInteractive, Cost: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tk.Settle(governor.Spend{Bucket: governor.BucketRESTCore, Cost: 1, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}

	_, err = e.call(OpListRuns, map[string]any{"pr_id": ref1})
	if code(err) != "unavailable" || !strings.Contains(err.Error(), "cap reached") {
		t.Fatalf("err = %v, want unavailable naming the cap", err)
	}
	if e.gh.runsCount() != 0 {
		t.Errorf("%d runs requests made past the cap", e.gh.runsCount())
	}
	if row, ok := e.rig.Group(node1, groups.Runs); ok && len(row.Content) > 0 {
		t.Errorf("a failed fetch stored %q", row.Content)
	}
}
