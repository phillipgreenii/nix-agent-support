package changesop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// callCI dispatches one op of the ci instance through the router.
func (r *rig) callCI(op, args string) scriptout.Response {
	r.t.Helper()
	return r.rt.Dispatch(context.Background(), router.Call{Instance: "ci", Request: scriptout.Request{Op: op, Args: json.RawMessage(args)}})
}

func (r *rig) changesCI(args string) (schema.CIChangesResult, scriptout.Response) {
	r.t.Helper()
	resp := r.callCI(OpChanges, args)
	if resp.Error != nil {
		r.t.Fatalf("ci changes %s: %s: %s", args, resp.Error.Code, resp.Error.Message)
	}
	var res schema.CIChangesResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		r.t.Fatalf("decode %s: %v", resp.Result, err)
	}
	return res, resp
}

func (r *rig) ackCI(consumer, query string, seq int64) schema.ChangesAckResult {
	r.t.Helper()
	resp := r.callCI(OpChangesAck, ackArgs(consumer, query, seq))
	if resp.Error != nil {
		r.t.Fatalf("ci changes_ack: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	var res schema.ChangesAckResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		r.t.Fatal(err)
	}
	return res
}

func (r *rig) posKind(kind, consumer, query string) (int64, bool) {
	r.t.Helper()
	c, ok, err := r.st.GetConsumer(context.Background(), consumer, kind, query)
	if err != nil {
		r.t.Fatal(err)
	}
	return c.AckedSeq, ok
}

// putChecks sets the checks state of the stored summary of a PR put by putPR.
func (r *rig) putChecks(node, state string) {
	r.t.Helper()
	ctx := context.Background()
	g, ok, err := r.st.GetGroup(ctx, "pr", node, summary.GroupName)
	if err != nil || !ok {
		r.t.Fatalf("summary of %s: %v, found %v", node, err, ok)
	}
	var s summary.Summary
	if err := json.Unmarshal(g.Content, &s); err != nil {
		r.t.Fatal(err)
	}
	s.ChecksState = state
	if g.Content, err = json.Marshal(s); err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.PutGroup(ctx, g); err != nil {
		r.t.Fatal(err)
	}
}

// putRuns stores the runs group of a PR, fetched at the manual clock's now.
func (r *rig) putRuns(node string, list ...schema.CIRun) {
	r.t.Helper()
	raw, err := json.Marshal(runs.Content{Runs: list})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.PutGroup(context.Background(), store.GroupRow{Kind: "pr", ID: node, Group: "runs", Content: raw, FetchedAt: t0.Add(time.Minute)}); err != nil {
		r.t.Fatal(err)
	}
}

func testRun(id, status, conclusion, head string) schema.CIRun {
	return schema.CIRun{ID: id, Name: "build", Status: status, Conclusion: conclusion, URL: "https://example.invalid/runs/" + id, Provider: runs.ProviderName, HeadSHA: head}
}

func TestCIHandlersCarryTheCISchemaVersionAndTheTwoOps(t *testing.T) {
	r := newRig(t)
	for _, op := range []string{OpChanges, OpChangesAck} {
		h := Handlers(Deps{})["ci"][op]
		if h.Handle == nil || h.SchemaVersion != schema.CISchemaVersion {
			t.Errorf("%s: handle nil %t, schema version %d (want %d)", op, h.Handle == nil, h.SchemaVersion, schema.CISchemaVersion)
		}
	}
	if schema.CISchemaVersion != 7 {
		t.Errorf("CI schema version = %d, want 7", schema.CISchemaVersion)
	}
	if resp := r.callCI(OpChangesAck, ackArgs("c", "mine", 0)); resp.SchemaVersion != schema.CISchemaVersion || resp.Error != nil {
		t.Errorf("answer = schemaVersion %d, error %+v; want %d and none", resp.SchemaVersion, resp.Error, schema.CISchemaVersion)
	}
}

func TestCIFirstPollStartsAtTheTailAndAckAdvancesTheCIKey(t *testing.T) {
	r := newRig(t)
	r.add(refA, 1, "head-0", []string{classify.KindCIChanged})
	tail := r.add(refA, 2, "head-1", []string{classify.KindCIChanged})

	res, _ := r.changesCI(pollArgs("notifier", "mine"))
	if len(res.Changes) != 0 || len(res.Sources) != 1 {
		t.Fatalf("first poll = %+v, want one source and no rows", res)
	}
	if src := res.Sources[0]; src.Backend != status.RegistryNameCI || src.Status != "succeeded" || src.Truncated || src.NextSeq != tail || src.Version != tail {
		t.Errorf("source = %+v, want %s succeeded at the tail %d", src, status.RegistryNameCI, tail)
	}
	if p, ok := r.posKind("ci", "notifier", "mine"); !ok || p != tail {
		t.Fatalf("ci key = %d, %v; want created at the tail %d", p, ok, tail)
	}
	next := r.add(refA, 3, "head-2", []string{classify.KindCIChanged})
	if got := r.ackCI("notifier", "mine", next); got.AckedSeq != next || got.Consumer != "notifier" || got.Query != "mine" {
		t.Errorf("ack = %+v, want position %d", got, next)
	}
	if after, _ := r.changesCI(pollArgs("notifier", "mine")); len(after.Changes) != 0 {
		t.Errorf("after the ack, changes = %+v, want none", after.Changes)
	}
}

func TestCIChangesDeliverCIAndMembershipRowsNotContentOnlyRows(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-9", 9)
	r.changesCI(pollArgs("c", "mine"))

	r.add(refA, 1, "h1", []string{classify.KindFeedbackChanged}) // a comment or label: not CI
	r.add(refA, 2, "h2", []string{classify.KindHeadChanged})
	ci := r.add(refA, 3, "h3", []string{classify.KindCIChanged}, classify.Field{Name: "checks", Before: jsonOf("pending"), After: jsonOf("failure")})
	entered := r.add(refB, 1, "hb", []string{classify.KindEnteredQuery}, enteredField("mine"))
	left := r.add(refA, 4, "h4", []string{classify.KindLeftQuery}, leftField("mine"))
	removed := r.add(refA, 5, "h5", []string{classify.KindRemoved}, leftField("mine"))
	r.add(other, 1, "ho", []string{classify.KindCIChanged}) // not a member

	res, _ := r.changesCI(pollArgs("c", "mine"))
	want := []struct {
		seq    int64
		change string
		kinds  string
		ver    int64
		head   string
	}{
		{ci, "changed", classify.KindCIChanged, 3, "h3"},
		{entered, "added", classify.KindEnteredQuery, 1, "hb"},
		{left, "removed", classify.KindLeftQuery, 4, "h4"},
		{removed, "removed", classify.KindRemoved, 5, "h5"},
	}
	if len(res.Changes) != len(want) {
		t.Fatalf("changes = %+v, want %d", res.Changes, len(want))
	}
	for i, w := range want {
		c := res.Changes[i]
		if c.Seq != w.seq || c.Change != w.change || c.Source != status.RegistryNameCI || strings.Join(c.Kinds, ",") != w.kinds ||
			c.Entity.Version != w.ver || c.Entity.HeadSHA != w.head {
			t.Errorf("change %d = %+v, want seq %d %s kinds %s version %d head %s", i, c, w.seq, w.change, w.kinds, w.ver, w.head)
		}
	}
	var fields []classify.Field
	if err := json.Unmarshal(res.Changes[0].Fields, &fields); err != nil || len(fields) != 1 || fields[0].Name != "checks" {
		t.Errorf("fields = %s (%v), want the row's own checks field", res.Changes[0].Fields, err)
	}
	if got := res.Sources[0].NextSeq; got != r.head() {
		t.Errorf("next_seq = %d, want the head %d (the skipped rows are scanned past)", got, r.head())
	}
}

func TestCIEntityIsTheCurrentStoredCIStateWithTheRowsOwnVersionAndHead(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-9", 9)
	r.putChecks("PR_a", "FAILURE")
	r.putRuns("PR_a", testRun("22", "completed", "failure", "head-9"), testRun("21", "completed", "success", "head-8"))
	r.changesCI(pollArgs("c", "mine"))
	first := r.add(refA, 4, "head-4", []string{classify.KindCIChanged})
	second := r.add(refA, 5, "head-5", []string{classify.KindCIChanged})

	res, _ := r.changesCI(pollArgs("c", "mine"))
	if len(res.Changes) != 2 || res.Changes[0].Seq != first || res.Changes[1].Seq != second {
		t.Fatalf("changes = %+v, want the two rows", res.Changes)
	}
	for i, ver := range []int64{4, 5} {
		e := res.Changes[i].Entity
		if e.PRID != refA || e.Rollup != "failure" || e.Version != ver || e.HeadSHA != []string{"head-4", "head-5"}[i] {
			t.Errorf("entity %d = %+v, want %s failure version %d and the row's head", i, e, refA, ver)
		}
		if len(e.Runs) != 2 || e.Runs[0].ID != "22" || e.Runs[1].ID != "21" {
			t.Fatalf("entity %d runs = %+v, want the stored runs newest first", i, e.Runs)
		}
		for _, run := range e.Runs {
			if run.PRID != refA || run.AsOf != "2026-10-12T09:01:00Z" || run.Provider != runs.ProviderName {
				t.Errorf("run = %+v, want pr_id %s and as_of 2026-10-12T09:01:00Z", run, refA)
			}
		}
	}
	// The runs are as stored NOW: a later runs write shows in an older row's delivery.
	r.putChecks("PR_a", "SUCCESS")
	r.putRuns("PR_a", testRun("23", "completed", "success", "head-9"))
	r.ackCI("c", "mine", 0)
	again, _ := r.changesCI(pollArgs("c", "mine"))
	if len(again.Changes) != 2 || again.Changes[0].Entity.Rollup != "success" || len(again.Changes[0].Entity.Runs) != 1 || again.Changes[0].Entity.Runs[0].ID != "23" || again.Changes[0].Entity.Version != 4 {
		t.Errorf("redelivery = %+v, want the current state with the row's own version 4", again.Changes)
	}
}

func TestCIEntityWithoutSummaryOrRunsIsNoneAndEmpty(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-1", 1) // a summary, no checks, no runs group
	r.changesCI(pollArgs("c", "mine"))
	r.add(refA, 1, "h1", []string{classify.KindCIChanged})
	r.add(refB, 2, "hb", []string{classify.KindEnteredQuery}, enteredField("mine")) // no summary stored
	r.add(refA, 3, "h3", []string{classify.KindRemoved}, leftField("mine"))

	res, resp := r.changesCI(pollArgs("c", "mine"))
	if len(res.Changes) != 3 {
		t.Fatalf("changes = %+v, want 3", res.Changes)
	}
	for i, ver := range []int64{1, 2, 3} {
		e := res.Changes[i].Entity
		if e.Rollup != "none" || e.Runs == nil || len(e.Runs) != 0 || e.Version != ver {
			t.Errorf("entity %d = %+v, want rollup none, empty runs, version %d", i, e, ver)
		}
	}
	if got := strings.Count(string(resp.Result), `"runs":[]`); got != 3 || strings.Contains(string(resp.Result), `"runs":null`) {
		t.Errorf("answer = %s, want three empty run arrays", resp.Result)
	}
}

func TestCIRollupFoldsTheChecksStateAsPRShowDoes(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-1", 1)
	r.changesCI(pollArgs("c", "mine"))
	r.add(refA, 1, "h1", []string{classify.KindCIChanged})
	for state, want := range map[string]string{"SUCCESS": "success", "FAILURE": "failure", "ERROR": "failure", "PENDING": "pending", "EXPECTED": "pending", "": "none"} {
		r.putChecks("PR_a", state)
		r.ackCI("c", "mine", 0)
		res, _ := r.changesCI(pollArgs("c", "mine"))
		if len(res.Changes) != 1 || res.Changes[0].Entity.Rollup != want {
			t.Errorf("checks state %q: changes = %+v, want rollup %s", state, res.Changes, want)
		}
	}
}

func TestCIResetReplaysTheCurrentMembersAsAddedAndMovesThePosition(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-a", 6)
	r.putChecks("PR_a", "PENDING")
	r.putRuns("PR_a", testRun("5", "in_progress", "", "head-a"))
	r.changesCI(pollArgs("c", "team"))
	r.add(refA, 1, "h1", []string{classify.KindCIChanged})
	head := r.add(refB, 1, "h2", []string{classify.KindCIChanged})

	res, resp := r.changesCI(`{"consumer":"c","query":"team","reset":true}`)
	if len(res.Changes) != 2 {
		t.Fatalf("reset changes = %+v, want the two members", res.Changes)
	}
	for i, ref := range []string{refA, refB} {
		c := res.Changes[i]
		if c.Entity.PRID != ref || c.Change != "added" || c.Seq != 0 || c.Source != status.RegistryNameCI || len(c.Kinds) != 0 {
			t.Errorf("replay %d = %+v, want %s added with seq 0 and no kinds", i, c, ref)
		}
	}
	if e := res.Changes[0].Entity; e.Version != 6 || e.HeadSHA != "head-a" || e.Rollup != "pending" || len(e.Runs) != 1 || e.Runs[0].ID != "5" {
		t.Errorf("replay entity = %+v, want the CI state as of now (version 6, head-a, pending, run 5)", e)
	}
	if e := res.Changes[1].Entity; e.Rollup != "none" || len(e.Runs) != 0 {
		t.Errorf("replay entity without a summary = %+v, want none and no runs", e)
	}
	if !strings.Contains(string(resp.Result), `"kinds":[]`) || !strings.Contains(string(resp.Result), `"fields":[]`) {
		t.Errorf("a replay entry has empty kinds and fields arrays: %s", resp.Result)
	}
	if res.Sources[0].NextSeq != head || res.Sources[0].Version != head {
		t.Errorf("source = %+v, want next_seq and version %d", res.Sources[0], head)
	}
	if p, _ := r.posKind("ci", "c", "team"); p != head {
		t.Errorf("position = %d, want the head %d", p, head)
	}
}

func TestCICachedNeverRunsTheQueryAndAnUncachedPollDoes(t *testing.T) {
	r := newRig(t)
	r.changesCI(`{"consumer":"c","query":"mine","cached":true}`)
	r.changesCI(`{"consumer":"c","query":"mine","cached":true}`)
	if n := r.q.runCount("mine"); n != 0 {
		t.Fatalf("cached polls ran the query %d times", n)
	}
	r.changesCI(pollArgs("c", "mine"))
	if n := r.q.runCount("mine"); n != 1 {
		t.Errorf("an uncached poll ran the query %d times, want 1", n)
	}
}

func TestCICursorExpiredIsInvalidArgumentAndAResetRecovers(t *testing.T) {
	r := newRig(t, func(c *feed.Config) { c.ConsumerStaleAfter = 60 * day })
	r.changesCI(pollArgs("c", "mine"))
	r.add(refA, 1, "h1", []string{classify.KindCIChanged})
	r.add(refA, 2, "h2", []string{classify.KindCIChanged})
	r.clk.Advance(15 * day) // past the 14-day retention
	r.add(refA, 3, "h3", []string{classify.KindCIChanged})
	if rows, _, err := r.f.Prune(context.Background()); err != nil || rows != 2 {
		t.Fatalf("prune = %d, %v; want the two old rows", rows, err)
	}
	resp := r.callCI(OpChanges, pollArgs("c", "mine"))
	wantError(t, resp, "invalid_argument", "cursor_expired:")
	if resp.Error != nil && !strings.HasPrefix(resp.Error.Message, "cursor_expired:") {
		t.Errorf("message = %q, want it to begin cursor_expired:", resp.Error.Message)
	}
	res, _ := r.changesCI(`{"consumer":"c","query":"mine","reset":true}`)
	if len(res.Changes) != 1 || res.Changes[0].Change != "added" {
		t.Errorf("reset after cursor_expired = %+v, want the member as added", res.Changes)
	}
}

func TestCIAckMovesTheCIKeyOnlyAndLeavesAnotherConsumersPRKeyUntouched(t *testing.T) {
	r := newRig(t)
	r.changesCI(pollArgs("notifier", "mine"))
	r.changes(pollArgs("router", "mine"))
	head := r.add(refA, 1, "h", []string{classify.KindCIChanged})

	r.ackCI("notifier", "mine", head)
	if p, _ := r.posKind("ci", "notifier", "mine"); p != head {
		t.Errorf("ci key = %d, want %d", p, head)
	}
	if p, ok := r.posKind("pr", "router", "mine"); !ok || p != head-1 {
		t.Errorf("another consumer's pr key = %d, %v; want %d untouched", p, ok, head-1)
	}
	if _, ok := r.posKind("pr", "notifier", "mine"); ok {
		t.Error("the ci ack created a pr key")
	}
	// The pr feed still delivers the ci_changed row to its own consumer.
	if res, _ := r.changes(pollArgs("router", "mine")); len(res.Changes) != 1 || res.Changes[0].Seq != head {
		t.Errorf("pr feed = %+v, want the ci_changed row", res.Changes)
	}
}

func TestCIKeyConflictsWithThePRKeyBothWaysNamingTheExistingKey(t *testing.T) {
	r := newRig(t)
	r.changes(pollArgs("c", "mine"))
	wantError(t, r.callCI(OpChanges, pollArgs("c", "mine")), "invalid_argument", "(c, pr, mine)")
	wantError(t, r.callCI(OpChangesAck, ackArgs("c", "mine", 0)), "invalid_argument", "(c, pr, mine)")
	if _, ok := r.posKind("ci", "c", "mine"); ok {
		t.Fatal("a refused ci request created the ci key")
	}

	r.changesCI(pollArgs("c", "team"))
	wantError(t, r.call(OpChanges, pollArgs("c", "team")), "invalid_argument", "(c, ci, team)")
	wantError(t, r.call(OpChangesAck, ackArgs("c", "team", 0)), "invalid_argument", "(c, ci, team)")
	if _, ok := r.posKind("pr", "c", "team"); ok {
		t.Fatal("a refused pr request created the pr key")
	}
	// Another consumer, and another query of the same consumer, are not in conflict.
	r.changesCI(pollArgs("other-consumer", "mine"))
}

func TestCIUnknownQueryIsQueryNotRecognized(t *testing.T) {
	r := newRig(t)
	wantError(t, r.callCI(OpChanges, pollArgs("c", "nope")), "query_not_recognized", `"nope"`)
	wantError(t, r.callCI(OpChangesAck, ackArgs("c", "nope", 0)), "query_not_recognized", `"nope"`)
}

func TestCIArgumentErrorsAreInvalidArgument(t *testing.T) {
	r := newRig(t)
	for _, args := range []string{
		`{"query":"mine"}`,
		`{"consumer":"c"}`,
		``,
		`{"consumer":"c","query":"mine","since":"2026-10-01T00:00:00Z"}`,
		`{"consumer":"c","query":"mine","before":"2026-10-01T00:00:00Z"}`,
		`{"consumer":"c","query":"mine","cached":"yes"}`,
		`{"consumer":"c","query":"mine","reset":1}`,
	} {
		wantError(t, r.callCI(OpChanges, args), "invalid_argument", "")
	}
	wantError(t, r.callCI(OpChangesAck, `{"consumer":"c","query":"mine"}`), "invalid_argument", "next_seq")
	if _, ok := r.posKind("ci", "c", "mine"); ok {
		t.Error("a refused request created a key")
	}
}

func TestCIResultArraysAreEmptyArraysNeverNull(t *testing.T) {
	r := newRig(t)
	if err := r.st.DeleteQuery(context.Background(), "team"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.DeleteQuery(context.Background(), "mine"); err != nil {
		t.Fatal(err)
	}
	_, resp := r.changesCI(pollArgs("c", "mine"))
	for _, key := range []string{`"changes":[]`, `"sources_freshness":[]`} {
		if !strings.Contains(string(resp.Result), key) {
			t.Errorf("answer %s lacks %s", resp.Result, key)
		}
	}
	if strings.Contains(string(resp.Result), "null") {
		t.Errorf("answer contains null: %s", resp.Result)
	}
}

func TestCIMemberFlagIgnoresTheCaseOfOwnerAndRepo(t *testing.T) {
	r := newRig(t)
	r.setQuery("cased", true, "Acme-Org/Widget#1")
	r.changesCI(pollArgs("c", "cased"))
	seq := r.add("acme-org/widget#1", 1, "h", []string{classify.KindCIChanged})
	if res, _ := r.changesCI(pollArgs("c", "cased")); len(res.Changes) != 1 || res.Changes[0].Seq != seq {
		t.Errorf("changes = %+v, want the one member row", res.Changes)
	}
}
