package changesop

// The characterization golden of the change feed's wire answers (docket
// pg2-959y2, packet H0, oracle of INV-HOIST-1). One scripted scenario drives
// the pr and the ci instance through the router with the real store, feed and
// handlers, and records every answer (the whole response envelope, so the
// schema version is part of it) byte for byte, one file per view and op:
//
//	cmd/pg-connector-github/golden/changes_<view>_<op>.json
//
// with view in {pr, ci} and op in {changes, changes_ack}. Each file is a JSON
// array with one element per call, in call order:
// {"call": "<op and arguments>", "answer": <response envelope>}.
//
// The comparison is byte for byte over each answer in its compact form (the
// repo's formatter re-indents committed JSON, so the whitespace of the file is
// not part of the oracle; key order, values and every byte of a string are). A
// later packet of the hoist MUST leave these files untouched: a diff means the
// wire answer changed.
//
// To regenerate the files (only after an intended, reviewed wire change), run
// from the module root:
//
//	PG_CONNECTOR_GOLDEN_UPDATE=1 go test ./cmd/pg-connector-github/internal/ops/changesop -run TestChangesWireGolden
//
// The fixtures use example-* names only.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// goldenUpdateEnv is the environment variable that rewrites the golden files
// instead of comparing against them.
const goldenUpdateEnv = "PG_CONNECTOR_GOLDEN_UPDATE"

const (
	goldenRefOne   = "example-org/example-repo#1"
	goldenRefTwo   = "example-org/example-repo#2"
	goldenRefThree = "example-org/example-repo#3"
	goldenRefOther = "example-org/example-other#9"
)

// goldenStep is one recorded call of a view and op.
type goldenStep struct {
	Call   string          `json:"call"`
	Answer json.RawMessage `json:"answer"`
}

// goldenRecorder collects the steps of every view and op in call order.
type goldenRecorder struct {
	r     *rig
	steps map[string][]goldenStep
}

// call dispatches op of the view's instance, records the whole answer under
// the view and op, and returns the answer.
func (g *goldenRecorder) call(t *testing.T, view, op, args string) scriptout.Response {
	t.Helper()
	var resp scriptout.Response
	if view == "pr" {
		resp = g.r.call(op, args)
	} else {
		resp = g.r.callCI(op, args)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	key := "changes_" + view + "_" + op + ".json"
	g.steps[key] = append(g.steps[key], goldenStep{Call: op + " " + args, Answer: raw})
	return resp
}

// goldenPutPR stores the alias, the entity row and the summary of a pull
// request of the scripted scenario, with example-* values only.
func goldenPutPR(t *testing.T, r *rig, ref, node, title, head string, version int64) {
	t.Helper()
	ctx := context.Background()
	parsed, err := entity.ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entity.NewIdentity(r.st, r.clk).Put(ctx, node, parsed); err != nil {
		t.Fatal(err)
	}
	if err := r.st.PutEntity(ctx, store.EntityRow{Kind: "pr", ID: node, Version: version}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(summary.Summary{
		NodeID: node, Repo: parsed.NameWithOwner(), Number: parsed.Number, Title: title, State: "OPEN",
		Author: "example-author", URL: "https://example.invalid/" + ref, HeadRefOid: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.PutGroup(ctx, store.GroupRow{Kind: "pr", ID: node, Group: summary.GroupName, Content: raw, FetchedAt: t0}); err != nil {
		t.Fatal(err)
	}
}

// goldenScenario runs the scripted scenario and returns the recorded steps.
func goldenScenario(t *testing.T) map[string][]goldenStep {
	t.Helper()
	r := newRig(t)
	g := &goldenRecorder{r: r, steps: map[string][]goldenStep{}}

	r.setQuery("mine", true, goldenRefOne)
	r.setQuery("team", true, goldenRefOne, goldenRefTwo)
	r.putQueryRow(store.QueryRow{Key: "mine", Configured: true, IDs: []string{goldenRefOne}, BaselineAt: t0, LastSuccessAt: t0.Add(time.Hour)})
	r.putQueryRow(store.QueryRow{Key: "team", Configured: true, IDs: []string{goldenRefOne, goldenRefTwo}, BaselineAt: t0, LastError: "example error"})
	goldenPutPR(t, r, goldenRefOne, "PR_example_1", "Add the example widget", "example-head-9", 9)
	goldenPutPR(t, r, goldenRefThree, "PR_example_3", "Fix the example gadget", "example-head-3", 3)
	r.putChecks("PR_example_1", "FAILURE")
	r.putRuns("PR_example_1",
		testRun("22", "completed", "failure", "example-head-9"),
		testRun("21", "completed", "success", "example-head-8"))

	const (
		pollArgsMine = `{"consumer":"example-consumer","query":"mine"}`
		pollArgsCI   = `{"consumer":"example-ci-consumer","query":"mine"}`
	)

	// pr: the first poll of a new key starts at the tail.
	r.add(goldenRefOne, 1, "example-head-1", []string{classify.KindHeadChanged})
	g.call(t, "pr", OpChanges, pollArgsMine)

	title := classify.Field{Name: "title", Before: jsonOf("Old title"), After: jsonOf("Add the example widget")}
	changed := r.add(goldenRefOne, 4, "example-head-4", []string{classify.KindFeedbackChanged}, title)
	r.add(goldenRefOne, 5, "example-head-5", []string{classify.KindHeadChanged})
	r.add(goldenRefTwo, 2, "example-head-b", []string{classify.KindEnteredQuery}, enteredField("mine"))
	r.add(goldenRefOne, 6, "example-head-6", []string{classify.KindLeftQuery}, leftField("mine"))
	r.add(goldenRefThree, 7, "example-head-7", []string{classify.KindRemoved}, leftField("mine"))
	r.add(goldenRefOther, 1, "example-head-o", []string{classify.KindHeadChanged}) // not a member

	// pr: the five rows, then the same rows again (no ack), then the replay.
	first := g.call(t, "pr", OpChanges, pollArgsMine)
	g.call(t, "pr", OpChanges, pollArgsMine)
	g.call(t, "pr", OpChanges, `{"consumer":"example-consumer","query":"mine","cached":true}`)
	g.call(t, "pr", OpChanges, `{"consumer":"example-consumer","query":"team","reset":true}`)
	var firstRes schema.PRChangesResult
	if err := json.Unmarshal(first.Result, &firstRes); err != nil || len(firstRes.Sources) != 1 {
		t.Fatalf("first pr answer = %s (%v)", first.Result, err)
	}
	next := firstRes.Sources[0].NextSeq

	// pr: the ack, the empty poll after it, a new key's ack and the error answers.
	g.call(t, "pr", OpChangesAck, ackArgs("example-consumer", "mine", next))
	g.call(t, "pr", OpChanges, pollArgsMine)
	g.call(t, "pr", OpChangesAck, ackArgs("example-fresh", "mine", changed))
	g.call(t, "pr", OpChangesAck, ackArgs("example-consumer", "mine", next+50))
	g.call(t, "pr", OpChanges, `{"consumer":"example-consumer","query":"example-unknown"}`)

	// ci: a consumer holding the pr key of a query cannot create its ci key.
	g.call(t, "ci", OpChanges, `{"consumer":"example-consumer","query":"mine"}`)

	// ci: the first poll starts at the tail of the ci key.
	g.call(t, "ci", OpChanges, pollArgsCI)
	r.add(goldenRefOne, 11, "example-ci-1", []string{classify.KindFeedbackChanged}) // not a ci row
	r.add(goldenRefOne, 12, "example-ci-2", []string{classify.KindCIChanged}, classify.Field{Name: "checks", Before: jsonOf("pending"), After: jsonOf("failure")})
	r.add(goldenRefTwo, 3, "example-ci-b", []string{classify.KindEnteredQuery}, enteredField("mine"))
	r.add(goldenRefOne, 13, "example-ci-3", []string{classify.KindLeftQuery}, leftField("mine"))
	r.add(goldenRefThree, 8, "example-ci-4", []string{classify.KindRemoved}, leftField("mine"))
	r.add(goldenRefOther, 2, "example-ci-o", []string{classify.KindCIChanged}) // not a member

	ciFirst := g.call(t, "ci", OpChanges, pollArgsCI)
	g.call(t, "ci", OpChanges, pollArgsCI)
	g.call(t, "ci", OpChanges, `{"consumer":"example-ci-consumer","query":"team","reset":true}`)
	var ciRes schema.CIChangesResult
	if err := json.Unmarshal(ciFirst.Result, &ciRes); err != nil || len(ciRes.Sources) != 1 {
		t.Fatalf("first ci answer = %s %+v (%v)", ciFirst.Result, ciFirst.Error, err)
	}
	ciNext := ciRes.Sources[0].NextSeq

	g.call(t, "ci", OpChangesAck, ackArgs("example-ci-consumer", "mine", ciNext))
	g.call(t, "ci", OpChanges, pollArgsCI)
	g.call(t, "ci", OpChangesAck, ackArgs("example-ci-consumer", "mine", ciNext+50))
	g.call(t, "ci", OpChanges, `{"consumer":"example-ci-consumer","query":"example-unknown"}`)

	return g.steps
}

// goldenFile renders the steps of one file in the canonical form the comparison
// uses: every answer compacted, one element per line.
func goldenFile(t *testing.T, steps []goldenStep) []byte {
	t.Helper()
	for i := range steps {
		var compact bytes.Buffer
		if err := json.Compact(&compact, steps[i].Answer); err != nil {
			t.Fatalf("answer of step %d: %v", i, err)
		}
		steps[i].Answer = compact.Bytes()
	}
	var b bytes.Buffer
	b.WriteString("[\n")
	for i, s := range steps {
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		if i < len(steps)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]\n")
	return b.Bytes()
}

func TestChangesWireGolden(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "golden")
	steps := goldenScenario(t)
	wantFiles := []string{
		"changes_pr_changes.json", "changes_pr_changes_ack.json",
		"changes_ci_changes.json", "changes_ci_changes_ack.json",
	}
	if len(steps) != len(wantFiles) {
		t.Fatalf("scenario recorded %d files, want %d", len(steps), len(wantFiles))
	}
	update := os.Getenv(goldenUpdateEnv) != ""
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range wantFiles {
		path := filepath.Join(dir, name)
		if update {
			raw, err := json.MarshalIndent(steps[name], "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s: %v (regenerate with %s=1)", name, err, goldenUpdateEnv)
		}
		var committed []goldenStep
		if err := json.Unmarshal(raw, &committed); err != nil {
			t.Fatalf("decode golden %s: %v", name, err)
		}
		got, want := goldenFile(t, steps[name]), goldenFile(t, committed)
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the golden (regenerate with %s=1 only after an intended wire change)\n got: %s\nwant: %s", name, goldenUpdateEnv, got, want)
		}
	}
}

// TestChangesWireGoldenIsDeterministic runs the scenario twice: a golden that
// differed between two runs would be no oracle.
func TestChangesWireGoldenIsDeterministic(t *testing.T) {
	a, b := goldenScenario(t), goldenScenario(t)
	for name, steps := range a {
		if !bytes.Equal(goldenFile(t, steps), goldenFile(t, b[name])) {
			t.Errorf("%s differs between two runs of the scenario", name)
		}
	}
}
