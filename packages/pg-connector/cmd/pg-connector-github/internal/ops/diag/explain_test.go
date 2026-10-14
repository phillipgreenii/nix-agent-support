package diag

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// everyGroup stores a PR with every group present, each in a different state
// against the rig's clock:
//
//	summary       fresh
//	detail        fresh
//	conversation  past fresh_until, with a last error, and a task queued
//	files         inside its time window but fetched for another head
//	commits       at its hard age
//	pending       fresh
//	runs          a run in progress and a failed one on the current head, an old
//	              failed run on another head, two cached job rows
func (e *env) everyGroup() {
	e.t.Helper()
	now := e.rig.Clock.Now()
	e.seed(node, "detail", "pending")

	stale := e.row(node, "conversation")
	stale.FetchedAt, stale.FreshUntil, stale.MaxAgeAt = now.Add(-2*time.Hour), now.Add(-time.Hour), time.Time{}
	stale.LastError = "HTTP 502"
	e.rig.SeedGroup(stale)

	files := e.row(node, "files")
	files.FetchedForHead = "head-older"
	e.rig.SeedGroup(files)

	commits := e.row(node, "commits")
	commits.FetchedAt, commits.MaxAgeAt = now.Add(-48*time.Hour), now
	e.rig.SeedGroup(commits)

	runsRow := e.row(node, "runs")
	runsRow.Content = []byte(`{"runs":[` +
		`{"id":"101","attempt":1,"name":"build","head_sha":"head-1","status":"in_progress"},` +
		`{"id":"102","attempt":2,"name":"test","head_sha":"head-1","status":"completed","conclusion":"failure"},` +
		`{"id":"90","attempt":1,"name":"test","head_sha":"head-0","status":"completed","conclusion":"failure"}]}`)
	e.rig.SeedGroup(runsRow)
	for _, j := range []store.JobRow{
		{RunID: "102", Attempt: 2, FetchedAt: now, Jobs: []store.JobResult{{Name: "unit", Status: "completed", Conclusion: "failure"}}},
		{RunID: "90", Attempt: 1, FetchedAt: now, Jobs: []store.JobResult{{Name: "unit", Status: "completed", Conclusion: "failure"}}},
	} {
		if err := e.rig.Store.PutJobs(context.Background(), j); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.rig.Svc.QueueGroup(context.Background(), node, "conversation", governor.ClassInvalidated, time.Time{}); err != nil {
		e.t.Fatal(err)
	}
}

// goldenOf compares the answer's result document with testdata/<name>.
func goldenOf(t *testing.T, name string, raw json.RawMessage) {
	t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	var gotDoc, wantDoc any
	if err := json.Unmarshal(raw, &gotDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatalf("golden %s is not JSON: %v", name, err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("%s differs from the golden document.\ngot:\n%s\nwant:\n%s", name, pretty.Bytes(), want)
	}
}

func (e *env) explainResult(instance, id string) (explain.Result, json.RawMessage) {
	e.t.Helper()
	resp := e.call(instance, OpExplain, `{"id":"`+id+`"}`)
	if resp.Error != nil {
		e.t.Fatalf("explain %s %s answered an error: %+v", instance, id, resp.Error)
	}
	var res explain.Result
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		e.t.Fatal(err)
	}
	return res, resp.Result
}

func TestExplainOfAPRWithEveryGroupGivesEachGroupsVerdictsAndMakesNoOriginCall(t *testing.T) {
	e := newEnv(t)
	e.everyGroup()
	id := e.ref(node).String()
	calls, resolves := e.rig.Origin.CallCount(), len(e.rig.Resolver.Calls())

	res, raw := e.explainResult("pr", id)
	goldenOf(t, "explain_pr.golden.json", raw)

	by := map[string]explain.Group{}
	for _, g := range res.Groups {
		by[g.Group] = g
	}
	if _, ok := by["runs"]; ok || len(res.Groups) != 6 {
		t.Fatalf("the pr view is every stored group but runs: %+v", res.Groups)
	}
	check := func(group, status string, stamps *bool) {
		t.Helper()
		g := by[group]
		if g.Status != status {
			t.Errorf("%s: status %q (%s), want %q", group, g.Status, g.Reason, status)
		}
		if (stamps == nil) != (g.StampsMatch == nil) || (stamps != nil && *stamps != *g.StampsMatch) {
			t.Errorf("%s: stamps_match %v, want %v", group, g.StampsMatch, stamps)
		}
		if status != explain.StatusFresh && g.Reason == "" {
			t.Errorf("%s: a group that is not fresh says why", group)
		}
	}
	yes, no := true, false
	check("summary", "fresh", nil)
	check("detail", "fresh", nil)
	check("conversation", "stale", nil)
	check("files", "stale", &no)
	check("commits", "hard_stale", &yes)
	check("pending", "fresh", nil)
	if g := by["conversation"]; g.LastError != "HTTP 502" || g.QueuePosition == nil || *g.QueuePosition != 1 {
		t.Errorf("conversation: %+v, want its error and queue position 1", g)
	}
	if by["detail"].QueuePosition != nil {
		t.Error("a group with no queued task has no queue position")
	}

	if e.rig.Origin.CallCount() != calls || len(e.rig.Resolver.Calls()) != resolves {
		t.Error("explain made an origin call")
	}
	for g, f := range e.fetchers {
		if f.CallCount() != 0 {
			t.Errorf("explain fetched %s", g)
		}
	}
}

func TestExplainOfTheCIViewIsTheSummaryAndRunsWithTheActiveAndFailingVerdicts(t *testing.T) {
	e := newEnv(t)
	e.everyGroup()
	res, raw := e.explainResult("ci", e.ref(node).String())
	goldenOf(t, "explain_ci.golden.json", raw)

	if len(res.Groups) != 2 || res.Groups[0].Group != "summary" || res.Groups[1].Group != "runs" || res.Note != "" {
		t.Fatalf("the ci view is the summary and runs: %+v", res)
	}
	r := res.Groups[1]
	if r.Active == nil || !*r.Active || r.Failing == nil || !*r.Failing {
		t.Errorf("runs: active %v failing %v, want both (a run in progress and a failure on the current head)", r.Active, r.Failing)
	}
	if r.CachedJobs == nil || *r.CachedJobs != 1 {
		t.Errorf("runs: cached_jobs %v, want 1 (the old head's job row is not a current run's)", r.CachedJobs)
	}
	if r.StampsMatch == nil || !*r.StampsMatch || r.Status != explain.StatusFresh {
		t.Errorf("runs: %+v", r)
	}
	if res.Groups[0].Active != nil || res.Groups[0].CachedJobs != nil {
		t.Errorf("only the runs group carries the runs verdicts: %+v", res.Groups[0])
	}
}

func TestExplainOfAnUnseenEntityIsNotFoundWithoutAnOriginCall(t *testing.T) {
	e := newEnv(t)
	for _, inst := range []string{"pr", "ci"} {
		e.wantError(inst, OpExplain, `{"id":"acme-org/widget#999"}`, "not_found")
	}
	if len(e.rig.Resolver.Calls()) != 0 || e.rig.Origin.CallCount() != 0 {
		t.Error("explain resolved an unseen reference with an origin call")
	}
}

func TestExplainOfARunsGroupWithoutASummaryIsStaleAndCarriesNoVerdictOnTheHead(t *testing.T) {
	e := newEnv(t)
	e.seed(node)
	e.rig.SeedGroup(e.row(node, "runs"))
	if err := e.rig.Store.DeleteGroup(context.Background(), "pr", node, "summary"); err != nil {
		t.Fatal(err)
	}
	res, _ := e.explainResult("ci", e.ref(node).String())
	g := res.Groups[0]
	if g.Group != "runs" || g.Status != explain.StatusStale || g.StampsMatch == nil || *g.StampsMatch {
		t.Errorf("%+v", g)
	}
}

func TestExplainHandlersWithoutTheRulesAnswerThePhase2Document(t *testing.T) {
	e := newEnv(t)
	e.seed(node)
	h := ExplainHandlers(Deps{Store: e.rig.Store, Queue: e.rig.Sched})
	raw, err := h["pr"][OpExplain].Handle(context.Background(), json.RawMessage(`{"id":"`+e.ref(node).String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(raw)
	for _, field := range []string{`"status"`, `"reason"`, `"stamps_match"`} {
		if bytes.Contains(b, []byte(field)) {
			t.Errorf("a handler with no rules carries %s: %s", field, b)
		}
	}
	if h["pr"][OpExplain].SchemaVersion == 0 || h["ci"][OpExplain].SchemaVersion == 0 {
		t.Error("explain is stamped with its instance's schema version")
	}
}
