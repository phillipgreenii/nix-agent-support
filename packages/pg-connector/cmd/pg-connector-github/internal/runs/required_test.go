package runs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// rulesGH answers the runs, jobs and branch-rules endpoints and records the
// rules calls.
type rulesGH struct {
	runs, jobs, rules string
	rulesErr          error
	rulesCalls        []string
}

func (g *rulesGH) Run(_ context.Context, args ...string) ([]byte, error) {
	endpoint := args[len(args)-1]
	switch {
	case strings.Contains(endpoint, "/rules/branches/"):
		g.rulesCalls = append(g.rulesCalls, endpoint)
		if g.rulesErr != nil {
			return nil, g.rulesErr
		}
		return ok(g.rules), nil
	case strings.Contains(endpoint, "/jobs"):
		return ok(g.jobs), nil
	default:
		return ok(g.runs), nil
	}
}

const rulesBody = `[{"type":"pull_request","parameters":{}},
 {"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"gate"},{"context":"unit"}]}}]`

func required(r schema.CIRun) string {
	if r.Required == nil {
		return "nil"
	}
	if *r.Required {
		return "true"
	}
	return "false"
}

func fetchRuns(t *testing.T, gh *rulesGH, base string) (schema.CIRun, schema.CIRun, groups.Outcome) {
	t.Helper()
	// fakeGH's interface is the same Run signature.
	e := newEnvFor(t, gh)
	tg := target(t, "feat/x")
	tg.BaseRefName = base
	o := e.fetch(t, tg)
	if o.Err != nil {
		t.Fatalf("fetch: %v", o.Err)
	}
	c, err := Decode(o.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Runs) != 2 {
		t.Fatalf("%d runs, want 2", len(c.Runs))
	}
	return c.Runs[0], c.Runs[1], o
}

func TestFetch_MarksRequiredFromTheBaseBranchRules(t *testing.T) {
	gh := &rulesGH{
		runs: runsBody(
			run(11, "deploy-preview", head, "completed", "cancelled", 1),
			run(12, "gate", head, "completed", "failure", 1),
		),
		jobs:  jobsBody(job(1, "optional-job", "completed", "cancelled")),
		rules: rulesBody,
	}
	first, second, o := fetchRuns(t, gh, "main")
	if len(gh.rulesCalls) != 1 || gh.rulesCalls[0] != "repos/phillipgreenii/widgets/rules/branches/main?per_page=100" {
		t.Errorf("rules calls = %v", gh.rulesCalls)
	}
	if got := required(first); got != "false" {
		t.Errorf("run with fetched jobs matching no context: required = %s, want false", got)
	}
	if got := required(second); got != "true" {
		t.Errorf("run named like a context: required = %s, want true", got)
	}
	if o.Cost != 4 {
		t.Errorf("cost = %d, want runs + 2 jobs + rules = 4", o.Cost)
	}
}

func TestFetch_RequiredUnknownWhenRulesHaveNoStatusCheckRule(t *testing.T) {
	gh := &rulesGH{
		runs: runsBody(
			run(11, "a", head, "completed", "failure", 1),
			run(12, "b", head, "completed", "failure", 1),
		),
		jobs:  jobsBody(job(1, "j", "completed", "failure")),
		rules: `[{"type":"pull_request","parameters":{}}]`,
	}
	first, second, _ := fetchRuns(t, gh, "main")
	if required(first) != "nil" || required(second) != "nil" {
		t.Errorf("required = %s, %s, want nil, nil (no required_status_checks rule says nothing)", required(first), required(second))
	}
}

func TestFetch_RequiredUnknownWhenRulesUnreadable(t *testing.T) {
	gh := &rulesGH{
		runs: runsBody(
			run(11, "a", head, "completed", "failure", 1),
			run(12, "b", head, "completed", "failure", 1),
		),
		jobs:     jobsBody(job(1, "j", "completed", "failure")),
		rulesErr: errors.New("HTTP 403"),
	}
	first, second, o := fetchRuns(t, gh, "main")
	if required(first) != "nil" || required(second) != "nil" {
		t.Errorf("required = %s, %s, want nil, nil", required(first), required(second))
	}
	if o.Err != nil {
		t.Errorf("an unreadable rules answer must not fail the runs fetch: %v", o.Err)
	}
}

func TestFetch_NoBaseBranchMeansNoRulesLookup(t *testing.T) {
	gh := &rulesGH{
		runs:  runsBody(run(11, "a", head, "completed", "failure", 1), run(12, "b", head, "completed", "success", 1)),
		jobs:  jobsBody(job(1, "j", "completed", "failure")),
		rules: rulesBody,
	}
	first, _, _ := fetchRuns(t, gh, "")
	if len(gh.rulesCalls) != 0 || required(first) != "nil" {
		t.Errorf("rules calls %v, required %s; want none and nil", gh.rulesCalls, required(first))
	}
}

func TestMarkRequired_JobMatchesAndUnfetchedJobsStayUnknown(t *testing.T) {
	ctxs := map[string]bool{"unit": true, "wf / reusable": true}
	runs := []schema.CIRun{
		{Name: "ci", Jobs: []schema.CIJob{{Name: "unit"}}},
		{Name: "wf", Jobs: []schema.CIJob{{Name: "reusable"}}},
		{Name: "other", Jobs: []schema.CIJob{{Name: "lint"}}},
		{Name: "no-jobs"},
	}
	markRequired(runs, ctxs)
	got := []string{required(runs[0]), required(runs[1]), required(runs[2]), required(runs[3])}
	if strings.Join(got, ",") != "true,true,false,nil" {
		t.Errorf("required = %v, want true,true,false,nil", got)
	}
	markRequired(runs[2:], nil)
	if required(runs[2]) != "false" {
		t.Errorf("no contexts must leave marks untouched")
	}
}

func TestFingerprint_IgnoresRequired(t *testing.T) {
	tr, fl := true, false
	mk := func(r *bool) Content {
		return Content{Runs: []schema.CIRun{{ID: "1", Name: "a", Required: r}}}
	}
	// A change of the branch rules is no CI event: the stored content carries
	// the latest value, but the fingerprint (and so change detection) does not.
	if Fingerprint(mk(nil)) != Fingerprint(mk(&tr)) || Fingerprint(mk(&tr)) != Fingerprint(mk(&fl)) {
		t.Error("the fingerprint must not read required")
	}
}
