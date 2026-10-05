package apply

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/item"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

const (
	prID   = "acme/widgets#42"
	nodeID = "PR_node_42"
)

func prView() *view.View {
	return &view.View{Type: "pr", ID: prID, Snapshot: view.PRSnapshot{ID: prID, NodeID: nodeID}}
}

func str(s string) *string { return &s }

func run(t *testing.T, d *double, cfg *config.Config, actions []action.Action, hooks ...Hook) Result {
	t.Helper()
	return Run(context.Background(), Input{
		Type: "pr", ID: prID, View: prView(), Actions: actions, Env: testEnv(d, cfg), Hooks: hooks,
	})
}

func create(kind, title, key string, mod func(*action.Action)) action.Action {
	a := action.Action{
		Op: action.OpCreate, Kind: kind, Rule: "r." + kind,
		Fields: action.Fields{
			Title: title, IssueType: "task", Labels: []string{"zeta", "alpha"},
			Metadata: map[string]string{"dedup_key": key, "kind": kind},
		},
	}
	if mod != nil {
		mod(&a)
	}
	return a
}

func outcomes(r Result) []Outcome {
	var out []Outcome
	for _, e := range r.Events {
		out = append(out, e.Outcome)
	}
	return out
}

func TestCreateWithAbsentDedupKeyLooksUpThenCreatesThenRefreshes(t *testing.T) {
	d := newDouble(t)
	a := create("review-pr", "review-pr: acme/widgets#42", "pr:"+prID+":review-pr", func(a *action.Action) {
		a.Fields.Parent = "wb-anchor"
		a.Fields.Description = "please review"
		a.Fields.Priority = "2"
	})
	cfg := &config.Config{AgentTrackerBackend: "trk", BeadsDir: "/tmp/beads"}
	r := run(t, d, cfg, []action.Action{a})
	want := []string{
		"pg-connector issue list --query work-beads --backend trk",
		"pg-connector issue create --title review-pr: acme/widgets#42 --issue-type task --description please review --priority 2 --labels alpha,zeta --parent wb-anchor --metadata dedup_key=pr:acme/widgets#42:review-pr --metadata kind=review-pr --backend trk",
		"pg-desk issue refresh wb-2",
	}
	if got := d.lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("execs:\n got %q\nwant %q", got, want)
	}
	if r.ExitCode != 0 || len(r.Events) != 1 || r.Events[0].Outcome != OutcomeApplied || r.Events[0].WorkItemID != "wb-2" {
		t.Fatalf("result: %+v", r)
	}
}

func TestCreateDedupHitInEitherKeyFormIsDedupedAndExecsNoCreate(t *testing.T) {
	cases := map[string]struct{ actionKey, trackerKey string }{
		"id form tracked, node form action": {"pr:" + nodeID + ":review-pr", "pr:" + prID + ":review-pr"},
		"node form tracked, id form action": {"pr:" + prID + ":review-pr", "pr:" + nodeID + ":review-pr"},
		"same form":                         {"pr:" + prID + ":review-pr", "pr:" + prID + ":review-pr"},
		"suffix kept in both forms":         {"pr:" + prID + ":fix-ci:abc123", "pr:" + nodeID + ":fix-ci:abc123"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			kind := "review-pr"
			if strings.Contains(c.actionKey, "fix-ci") {
				kind = "fix-ci"
			}
			d := newDouble(t, respRule{
				Match:  "issue list",
				Stdout: `{"entities":[{"id":"wb-9","title":"t","metadata":{"dedup_key":"` + c.trackerKey + `"}}],"present_ids":["wb-9"],"sources":[]}`,
			})
			r := run(t, d, nil, []action.Action{create(kind, "t", c.actionKey, nil)})
			for _, l := range d.lines() {
				if strings.Contains(l, "issue create") || strings.Contains(l, "refresh") {
					t.Fatalf("a deduped create must exec no create and no refresh: %q", d.lines())
				}
			}
			if r.ExitCode != 0 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeDeduped}) || r.Events[0].WorkItemID != "wb-9" {
				t.Fatalf("result: %+v", r)
			}
		})
	}
}

func TestCreateDedupMissesADifferentKindOrSuffixOrEntity(t *testing.T) {
	d := newDouble(t, respRule{
		Match: "issue list",
		Stdout: `{"entities":[` +
			`{"id":"wb-1","metadata":{"dedup_key":"pr:` + prID + `:fix-ci:aaa"}},` +
			`{"id":"wb-2","metadata":{"dedup_key":"pr:other/repo#7:review-pr"}},` +
			`{"id":"wb-3","metadata":{}}]}`,
	})
	r := run(t, d, nil, []action.Action{create("fix-ci", "t", "pr:"+prID+":fix-ci:bbb", nil)})
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) {
		t.Fatalf("outcomes: %v", outcomes(r))
	}
}

func TestCreateDecodesWireEnvelopeListOutput(t *testing.T) {
	d := newDouble(t, respRule{
		Match:  "issue list",
		Stdout: `{"result":{"entities":[{"id":"wb-5","metadata":{"dedup_key":"pr:` + prID + `:review-pr"}}]}}`,
	})
	r := run(t, d, nil, []action.Action{create("review-pr", "t", "pr:"+prID+":review-pr", nil)})
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeDeduped}) || r.Events[0].WorkItemID != "wb-5" {
		t.Fatalf("result: %+v", r)
	}
}

func TestCreateWhenTheLookupFailsIsAFailedActionAndCreatesNothing(t *testing.T) {
	d := newDouble(t, respRule{Match: "issue list", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"down"}}`})
	r := run(t, d, nil, []action.Action{create("review-pr", "t", "pr:"+prID+":review-pr", nil)})
	if r.ExitCode != 2 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || r.Events[0].Err == nil {
		t.Fatalf("result: %+v", r)
	}
	if len(d.calls()) != 1 {
		t.Fatalf("only the lookup may be exec'd: %q", d.lines())
	}
}

func TestListDegradedSourceIsNotTrustedForDedup(t *testing.T) {
	d := newDouble(t, respRule{Match: "issue list", Exit: 2, Stdout: `{"entities":[],"present_ids":[],"sources":[]}`})
	r := run(t, d, nil, []action.Action{create("review-pr", "t", "pr:"+prID+":review-pr", nil)})
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || r.ExitCode != 2 {
		t.Fatalf("result: %+v", r)
	}
}

func anchorAndChild() []action.Action {
	anchor := create("anchor", "acme/widgets#42: anchor", "pr:"+prID+":anchor", nil)
	child := create("review-pr", "review-pr: acme/widgets#42", "pr:"+prID+":review-pr", func(a *action.Action) {
		a.Fields.Parent = action.AnchorParent
	})
	return []action.Action{anchor, child}
}

func TestAnchorCreateIdIsPassedAsTheChildsParent(t *testing.T) {
	d := newDouble(t)
	r := run(t, d, nil, anchorAndChild())
	var creates []string
	for _, l := range d.lines() {
		if strings.HasPrefix(l, "pg-connector issue create") {
			creates = append(creates, l)
		}
	}
	if len(creates) != 2 || !strings.Contains(creates[0], "--title acme/widgets#42: anchor") || strings.Contains(creates[0], "--parent") {
		t.Fatalf("anchor first: %q", creates)
	}
	anchorID := r.Events[0].WorkItemID
	if anchorID == "" || !strings.Contains(creates[1], "--parent "+anchorID+" ") {
		t.Fatalf("child must carry the anchor id %q as parent: %q", anchorID, creates[1])
	}
	if strings.Contains(creates[1], action.AnchorParent) {
		t.Fatalf("the placeholder leaked into argv: %q", creates[1])
	}
}

func TestDedupedAnchorProvidesTheExistingIdAsParent(t *testing.T) {
	d := newDouble(t, respRule{
		Match:  "issue list",
		Stdout: `{"entities":[{"id":"wb-old","metadata":{"dedup_key":"pr:` + prID + `:anchor"}}]}`,
	})
	run(t, d, nil, anchorAndChild())
	var childCreate string
	for _, l := range d.lines() {
		if strings.HasPrefix(l, "pg-connector issue create") {
			childCreate = l
		}
	}
	if !strings.Contains(childCreate, "--parent wb-old ") {
		t.Fatalf("child create: %q", childCreate)
	}
}

func TestFailedAnchorSkipsChildrenButLaterIndependentActionsRun(t *testing.T) {
	d := newDouble(t, respRule{Match: "--title acme/widgets#42: anchor", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"x"}}`})
	acts := anchorAndChild()
	acts = append(acts, action.Action{
		Op: action.OpAnnotate, Rule: "r.indep", Target: str("wip"), Fields: action.Fields{Value: str("true")},
	})
	r := run(t, d, nil, acts)
	want := []Outcome{OutcomeFailed, OutcomeSkippedDependency, OutcomeApplied}
	if !reflect.DeepEqual(outcomes(r), want) || r.ExitCode != 2 {
		t.Fatalf("outcomes %v exit %d, want %v exit 2", outcomes(r), r.ExitCode, want)
	}
	for _, l := range d.lines() {
		if strings.Contains(l, "--title review-pr") {
			t.Fatalf("the child create must not be attempted: %q", d.lines())
		}
	}
	if !strings.Contains(strings.Join(d.lines(), "\n"), "pg-desk pr annotate "+prID+" --key wip") {
		t.Fatalf("the independent action must still run: %q", d.lines())
	}
}

func TestUnresolvedAnchorPlaceholderIsAFailedAction(t *testing.T) {
	d := newDouble(t)
	child := anchorAndChild()[1]
	r := run(t, d, nil, []action.Action{child})
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || len(d.calls()) != 0 {
		t.Fatalf("outcomes %v calls %q", outcomes(r), d.lines())
	}
}

func TestReopenCloseAndUpdateArgv(t *testing.T) {
	d := newDouble(t)
	acts := []action.Action{
		{Op: action.OpReopen, Kind: "review-pr", Rule: "r.reopen", Target: str("wb-1")},
		{Op: action.OpClose, Kind: "review-pr", Rule: "close.all-closed", Target: str("wb-2"), Fields: action.Fields{Description: "the PR merged"}},
		{Op: action.OpUpdate, Kind: "anchor", Rule: "r.update", Target: str("wb-3"), Fields: action.Fields{
			Metadata:  map[string]string{"zz": "1", "aa": "2"},
			AddLabels: []string{"b", "a"}, RemoveLabels: []string{"y", "x"},
			Priority: "1", Title: "new title", Description: "new body",
		}},
	}
	r := run(t, d, &config.Config{AgentTrackerBackend: "trk"}, acts)
	want := []string{
		"pg-connector issue update wb-1 --status open --clear-assignee --clear-defer --backend trk",
		"pg-desk issue refresh wb-1",
		"pg-connector issue close wb-2 --reason close.all-closed: the PR merged --backend trk",
		"pg-desk issue refresh wb-2",
		"pg-connector issue update wb-3 --metadata aa=2 --metadata zz=1 --add-label a --add-label b --remove-label x --remove-label y --priority 1 --title new title --description new body --backend trk",
		"pg-desk issue refresh wb-3",
	}
	if got := d.lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("execs:\n got %q\nwant %q", got, want)
	}
	if r.ExitCode != 0 || r.Events[2].WorkItemID != "wb-3" {
		t.Fatalf("result: %+v", r)
	}
}

func TestCloseReasonFallsBackWhenNoSummaryIsGiven(t *testing.T) {
	d := newDouble(t)
	run(t, d, nil, []action.Action{{Op: action.OpClose, Kind: "fix-ci", Rule: "r.c", Target: str("wb-1")}})
	if got := d.lines()[0]; got != "pg-connector issue close wb-1 --reason r.c: close fix-ci work item" {
		t.Fatalf("close argv: %q", got)
	}
}

func TestTargetlessUpdateReopenCloseFail(t *testing.T) {
	d := newDouble(t)
	r := run(t, d, nil, []action.Action{
		{Op: action.OpUpdate, Rule: "r"}, {Op: action.OpReopen, Rule: "r"}, {Op: action.OpClose, Rule: "r"},
	})
	if r.ExitCode != 2 || len(d.calls()) != 0 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed, OutcomeFailed, OutcomeFailed}) {
		t.Fatalf("outcomes %v calls %q", outcomes(r), d.lines())
	}
}

func TestUnknownOpFails(t *testing.T) {
	d := newDouble(t)
	r := run(t, d, nil, []action.Action{{Op: "frobnicate", Rule: "r"}})
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || len(d.calls()) != 0 {
		t.Fatalf("result: %+v", r)
	}
}

func TestAnnotateSetsWithOriginAndConfiguredActor(t *testing.T) {
	d := newDouble(t)
	r := run(t, d, &config.Config{Actor: "bot"}, []action.Action{
		{Op: action.OpAnnotate, Rule: "r", Target: str("decider.pr-decider.force_review_consumed"), Fields: action.Fields{Value: str("abc123")}},
	})
	want := []string{"pg-desk pr annotate " + prID + " --key decider.pr-decider.force_review_consumed --value abc123 --origin decider:pr-decider --actor bot"}
	if got := d.lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("execs:\n got %q\nwant %q", got, want)
	}
	if r.ExitCode != 0 || r.Events[0].WorkItemID != "" {
		t.Fatalf("result: %+v", r)
	}
}

func TestAnnotateActorDefaultsToPgDecider(t *testing.T) {
	d := newDouble(t)
	run(t, d, nil, []action.Action{
		{Op: action.OpAnnotate, Rule: "r", Target: str("k"), Fields: action.Fields{Value: str("v")}},
	})
	if got := d.lines()[0]; !strings.HasSuffix(got, "--origin decider:pr-decider --actor pg-decider") {
		t.Fatalf("argv: %q", got)
	}
}

func TestClearForceReviewUsesTheForceReviewClearPath(t *testing.T) {
	d := newDouble(t)
	r := run(t, d, &config.Config{Actor: "bot"}, []action.Action{
		{Op: action.OpAnnotate, Rule: "r", Target: str("force_review"), Fields: action.Fields{Clear: true}},
	})
	want := []string{"pg-desk pr force-review " + prID + " --clear --origin decider:pr-decider --actor bot"}
	if got := d.lines(); !reflect.DeepEqual(got, want) || r.ExitCode != 0 {
		t.Fatalf("execs %q result %+v", got, r)
	}
}

func TestClearOfAnyOtherKeyIsAFailedAction(t *testing.T) {
	d := newDouble(t)
	r := run(t, d, nil, []action.Action{
		{Op: action.OpAnnotate, Rule: "r", Target: str("hidden"), Fields: action.Fields{Clear: true}},
	})
	if r.ExitCode != 2 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || len(d.calls()) != 0 {
		t.Fatalf("result %+v calls %q", r, d.lines())
	}
}

func TestAnnotateWithoutAValueOrTargetFails(t *testing.T) {
	d := newDouble(t)
	r := run(t, d, nil, []action.Action{
		{Op: action.OpAnnotate, Rule: "r", Target: str("k")},
		{Op: action.OpAnnotate, Rule: "r", Fields: action.Fields{Value: str("v")}},
	})
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed, OutcomeFailed}) || len(d.calls()) != 0 {
		t.Fatalf("result %+v calls %q", r, d.lines())
	}
}

func TestAnnotationFailureIsAFailedAction(t *testing.T) {
	d := newDouble(t, respRule{Match: "pg-desk pr annotate", Exit: 1, Stderr: "boom"})
	r := run(t, d, nil, []action.Action{
		{Op: action.OpAnnotate, Rule: "r", Target: str("k"), Fields: action.Fields{Value: str("v")}},
	})
	if r.ExitCode != 2 || r.Events[0].Outcome != OutcomeFailed || !strings.Contains(r.Events[0].Err.Error(), "boom") {
		t.Fatalf("result: %+v", r)
	}
}

func TestFailedRefreshIsStalenessNotAFailureAndTheWriteStands(t *testing.T) {
	d := newDouble(t, respRule{Match: "pg-desk issue refresh", Exit: 3, Stderr: "not found"})
	var stderr bytes.Buffer
	env := testEnv(d, nil)
	env.Stderr = &stderr
	r := Run(context.Background(), Input{
		Type: "pr", ID: prID, View: prView(), Env: env,
		Actions: []action.Action{{Op: action.OpUpdate, Rule: "r", Target: str("wb-1"), Fields: action.Fields{Priority: "1"}}},
	})
	if r.ExitCode != 0 || r.Events[0].Outcome != OutcomeApplied {
		t.Fatalf("result: %+v", r)
	}
	lines := d.lines()
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "pg-connector issue update wb-1") || lines[1] != "pg-desk issue refresh wb-1" {
		t.Fatalf("execs: %q (no rollback, no retry)", lines)
	}
	if !strings.Contains(stderr.String(), "refresh") || !strings.Contains(stderr.String(), "wb-1") {
		t.Fatalf("the refresh failure must be logged on stderr: %q", stderr.String())
	}
}

func TestBeadsDirEnvAndBackendFlagArePresentOnlyWhenConfigured(t *testing.T) {
	acts := func() []action.Action {
		return []action.Action{
			create("review-pr", "t", "pr:"+prID+":review-pr", nil),
			{Op: action.OpUpdate, Rule: "r", Target: str("wb-1"), Fields: action.Fields{Priority: "1"}},
			{Op: action.OpClose, Rule: "r", Target: str("wb-2")},
			{Op: action.OpReopen, Rule: "r", Target: str("wb-3")},
		}
	}
	t.Run("configured", func(t *testing.T) {
		d := newDouble(t)
		run(t, d, &config.Config{AgentTrackerBackend: "trk", BeadsDir: "/tmp/beads"}, acts())
		n := 0
		for _, c := range d.calls() {
			if c.Name == "pg-connector" {
				n++
				if c.BeadsDir == nil || *c.BeadsDir != "/tmp/beads" {
					t.Errorf("%s: PG_CONNECTOR_ISSUE_BEADS_DIR = %v", c.line(), c.BeadsDir)
				}
				if !strings.HasSuffix(c.line(), "--backend trk") {
					t.Errorf("%s: missing --backend", c.line())
				}
			}
		}
		if n != 5 { // list, create, update, close, update(reopen)
			t.Fatalf("pg-connector execs = %d", n)
		}
	})
	t.Run("not configured", func(t *testing.T) {
		d := newDouble(t)
		run(t, d, nil, acts())
		for _, c := range d.calls() {
			if c.BeadsDir != nil {
				t.Errorf("%s: PG_CONNECTOR_ISSUE_BEADS_DIR must be absent, got %q", c.line(), *c.BeadsDir)
			}
			if strings.Contains(c.line(), "--backend") {
				t.Errorf("%s: --backend must be absent", c.line())
			}
		}
	})
}

func TestCommaMetadataIsOneCSVQuotedFlagInCreateAndUpdate(t *testing.T) {
	d := newDouble(t)
	md := map[string]string{
		"failing_builds": "r1:1,r2:1",
		"plain":          "x",
		"quoted":         `say "hi"`,
	}
	run(t, d, nil, []action.Action{
		create("fix-ci", "t", "pr:"+prID+":fix-ci:abc", func(a *action.Action) { a.Fields.Metadata = md }),
		{Op: action.OpUpdate, Rule: "r", Target: str("wb-1"), Fields: action.Fields{Metadata: md}},
	})
	var got [][]string
	for _, c := range d.calls() {
		if c.Name == "pg-connector" && len(c.Args) > 1 && (c.Args[1] == "create" || c.Args[1] == "update") {
			var flags []string
			for i, a := range c.Args {
				if a == "--metadata" {
					flags = append(flags, c.Args[i+1])
				}
			}
			got = append(got, flags)
		}
	}
	want := []string{`"failing_builds=r1:1,r2:1"`, `plain=x`, `"quoted=say ""hi"""`}
	if len(got) != 2 || !reflect.DeepEqual(got[0], want) || !reflect.DeepEqual(got[1], want) {
		t.Fatalf("metadata flags:\n got %q\nwant %q twice", got, want)
	}
}

func TestRequiresPrior(t *testing.T) {
	mk := func() []action.Action {
		return []action.Action{
			{Op: action.OpUpdate, Rule: "r.a", Target: str("wb-1"), Fields: action.Fields{Priority: "1"}},
			{Op: action.OpUpdate, Rule: "r.b", Target: str("wb-2"), Fields: action.Fields{Priority: "1"}},
			{Op: action.OpUpdate, Rule: "r.a", Target: str("wb-3"), Fields: action.Fields{Priority: "1"}, RequiresPrior: true},
		}
	}
	t.Run("an earlier action of the same rule failed", func(t *testing.T) {
		d := newDouble(t, respRule{Match: "issue update wb-1", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"x"}}`})
		r := run(t, d, nil, mk())
		want := []Outcome{OutcomeFailed, OutcomeApplied, OutcomeSkippedDependency}
		if !reflect.DeepEqual(outcomes(r), want) || r.ExitCode != 2 {
			t.Fatalf("outcomes %v exit %d", outcomes(r), r.ExitCode)
		}
		for _, l := range d.lines() {
			if strings.Contains(l, "wb-3") {
				t.Fatalf("wb-3 must not be exec'd: %q", d.lines())
			}
		}
	})
	t.Run("an earlier action of the same rule was skipped", func(t *testing.T) {
		acts := []action.Action{
			{Op: action.OpUpdate, Rule: "r.a", Target: str("wb-1"), Fields: action.Fields{Priority: "1"}, RequiresPrior: true},
			{Op: action.OpUpdate, Rule: "r.a", Target: str("wb-3"), Fields: action.Fields{Priority: "1"}, RequiresPrior: true},
		}
		d := newDouble(t, respRule{Match: "issue update wb-1", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"x"}}`})
		r := run(t, d, nil, acts)
		want := []Outcome{OutcomeFailed, OutcomeSkippedDependency}
		if !reflect.DeepEqual(outcomes(r), want) {
			t.Fatalf("outcomes %v", outcomes(r))
		}
	})
	t.Run("all earlier actions of the rule applied", func(t *testing.T) {
		d := newDouble(t)
		r := run(t, d, nil, mk())
		if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied, OutcomeApplied, OutcomeApplied}) || r.ExitCode != 0 {
			t.Fatalf("outcomes %v exit %d", outcomes(r), r.ExitCode)
		}
	})
	t.Run("a deduped earlier action counts as applied", func(t *testing.T) {
		d := newDouble(t, respRule{Match: "issue list", Stdout: `{"entities":[{"id":"wb-9","metadata":{"dedup_key":"pr:` + prID + `:review-pr"}}]}`})
		acts := []action.Action{
			create("review-pr", "t", "pr:"+prID+":review-pr", func(a *action.Action) { a.Rule = "r.a" }),
			{Op: action.OpUpdate, Rule: "r.a", Target: str("wb-3"), Fields: action.Fields{Priority: "1"}, RequiresPrior: true},
		}
		r := run(t, d, nil, acts)
		if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeDeduped, OutcomeApplied}) {
			t.Fatalf("outcomes %v", outcomes(r))
		}
	})
	t.Run("a failure of a different rule does not block", func(t *testing.T) {
		d := newDouble(t, respRule{Match: "issue update wb-2", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"x"}}`})
		r := run(t, d, nil, mk())
		want := []Outcome{OutcomeApplied, OutcomeFailed, OutcomeApplied}
		if !reflect.DeepEqual(outcomes(r), want) {
			t.Fatalf("outcomes %v", outcomes(r))
		}
	})
}

func TestExitCodes(t *testing.T) {
	t.Run("none needed", func(t *testing.T) {
		d := newDouble(t)
		r := run(t, d, nil, nil)
		if r.ExitCode != 0 || len(r.Events) != 0 || len(d.calls()) != 0 {
			t.Fatalf("result %+v calls %q", r, d.lines())
		}
	})
	t.Run("all applied", func(t *testing.T) {
		d := newDouble(t)
		r := run(t, d, nil, []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1")}})
		if r.ExitCode != 0 {
			t.Fatalf("exit %d", r.ExitCode)
		}
	})
	t.Run("one failed among applied", func(t *testing.T) {
		d := newDouble(t, respRule{Match: "issue close wb-1", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"x"}}`})
		r := run(t, d, nil, []action.Action{
			{Op: action.OpClose, Rule: "r", Target: str("wb-1")},
			{Op: action.OpClose, Rule: "r", Target: str("wb-2")},
		})
		if r.ExitCode != 2 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed, OutcomeApplied}) {
			t.Fatalf("result %+v", r)
		}
	})
	t.Run("not found is an error", func(t *testing.T) {
		d := newDouble(t, respRule{Match: "issue close", Exit: 4, Stdout: `{"error":{"code":"not_found","message":"gone"}}`})
		r := run(t, d, nil, []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1")}})
		if r.ExitCode != 2 || !strings.Contains(r.Events[0].Err.Error(), "not_found") {
			t.Fatalf("result %+v", r)
		}
	})
}

type recHook struct {
	afters   []Event
	finished []Event
	afterErr error
	finErr   error
}

func (h *recHook) After(_ context.Context, _ Env, ev Event) error {
	h.afters = append(h.afters, ev)
	return h.afterErr
}

func (h *recHook) Finish(_ context.Context, _ Env, evs []Event) error {
	h.finished = append([]Event{}, evs...)
	return h.finErr
}

func TestHooksSeeEveryOutcomeInOrderWithSeq(t *testing.T) {
	d := newDouble(t, respRule{Match: "issue update wb-2", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"x"}}`})
	h := &recHook{}
	it := &item.Routed{}
	it.Metadata.Seq = 17
	r := Run(context.Background(), Input{
		Type: "pr", ID: prID, View: prView(), Env: testEnv(d, nil), Hooks: []Hook{h}, Item: it,
		Actions: []action.Action{
			{Op: action.OpUpdate, Rule: "r", Target: str("wb-1"), Fields: action.Fields{Priority: "1"}},
			{Op: action.OpUpdate, Rule: "r", Target: str("wb-2"), Fields: action.Fields{Priority: "1"}},
			{Op: action.OpUpdate, Rule: "r", Target: str("wb-3"), Fields: action.Fields{Priority: "1"}, RequiresPrior: true},
		},
	})
	want := []Outcome{OutcomeApplied, OutcomeFailed, OutcomeSkippedDependency}
	if !reflect.DeepEqual(outcomes(r), want) {
		t.Fatalf("outcomes %v", outcomes(r))
	}
	if len(h.afters) != 3 || len(h.finished) != 3 {
		t.Fatalf("afters=%d finished=%d", len(h.afters), len(h.finished))
	}
	for i, ev := range h.afters {
		if ev.Outcome != want[i] || ev.Seq != 17 || !ev.HasSeq || !reflect.DeepEqual(ev, r.Events[i]) {
			t.Errorf("event %d: %+v", i, ev)
		}
	}
	if h.afters[0].WorkItemID != "wb-1" || h.afters[1].Err == nil {
		t.Fatalf("events: %+v", h.afters)
	}
}

func TestEventsCarryNoSeqWithoutAnItem(t *testing.T) {
	d := newDouble(t)
	h := &recHook{}
	run(t, d, nil, []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1")}}, h)
	if h.afters[0].HasSeq || h.afters[0].Seq != 0 {
		t.Fatalf("event: %+v", h.afters[0])
	}
}

func TestHookErrorIsReportedOnStderrAndMakesTheRunExit2(t *testing.T) {
	for name, h := range map[string]*recHook{
		"After":  {afterErr: errors.New("audit write failed")},
		"Finish": {finErr: errors.New("audit write failed")},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDouble(t)
			var stderr bytes.Buffer
			env := testEnv(d, nil)
			env.Stderr = &stderr
			r := Run(context.Background(), Input{
				Type: "pr", ID: prID, View: prView(), Env: env, Hooks: []Hook{h},
				Actions: []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1")}},
			})
			if r.ExitCode != 2 || !strings.Contains(stderr.String(), "audit write failed") {
				t.Fatalf("exit %d stderr %q", r.ExitCode, stderr.String())
			}
			if r.Events[0].Outcome != OutcomeApplied {
				t.Fatalf("a hook error must not rewrite the action outcome: %+v", r.Events[0])
			}
		})
	}
}

func TestFinishRunsEvenWithNoActions(t *testing.T) {
	d := newDouble(t)
	h := &recHook{}
	run(t, d, nil, nil, h)
	if h.afters != nil || h.finished == nil || len(h.finished) != 0 {
		t.Fatalf("after=%+v finish=%+v (Finish must run once, with no events)", h.afters, h.finished)
	}
}

func TestHelpersShareArgvRules(t *testing.T) {
	d := newDouble(t)
	env := testEnv(d, &config.Config{AgentTrackerBackend: "trk", BeadsDir: "/tmp/beads", Actor: "bot"})
	ctx := context.Background()
	if err := Annotate(ctx, env, "pr", prID, "k", "v"); err != nil {
		t.Fatal(err)
	}
	id, err := CreateIssue(ctx, env, action.Fields{Title: "t", IssueType: "task", Labels: []string{"b", "a"}, Metadata: map[string]string{"m": "1,2"}, Parent: "wb-p"})
	if err != nil || id == "" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if err := Comment(ctx, env, "wb-1", "audit text"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pg-desk pr annotate " + prID + " --key k --value v --origin decider:pr-decider --actor bot",
		"pg-connector issue create --title t --issue-type task --labels a,b --parent wb-p --metadata \"m=1,2\" --backend trk",
		"pg-connector issue comment wb-1 --body audit text --backend trk",
	}
	if got := d.lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("execs:\n got %q\nwant %q", got, want)
	}
	for _, c := range d.calls()[1:] {
		if c.BeadsDir == nil || *c.BeadsDir != "/tmp/beads" {
			t.Errorf("%s: beads dir %v", c.line(), c.BeadsDir)
		}
	}
}

func TestMissingCommandFactoryDefaultsToExec(t *testing.T) {
	// An Env with no factory must not panic; with no pg-desk on PATH the
	// action simply fails.
	t.Setenv("PATH", t.TempDir())
	_ = os.Unsetenv("PG_CONNECTOR_ISSUE_BEADS_DIR")
	r := Run(context.Background(), Input{
		Type: "pr", ID: prID, View: prView(), Env: Env{Config: &config.Config{}},
		Actions: []action.Action{{Op: action.OpAnnotate, Rule: "r", Target: str("k"), Fields: action.Fields{Value: str("v")}}},
	})
	if r.ExitCode != 2 || r.Events[0].Outcome != OutcomeFailed {
		t.Fatalf("result: %+v", r)
	}
}

func TestDedupAgainstTheRecordedWorkBeadsFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/work_beads.json")
	if err != nil {
		t.Fatal(err)
	}
	d := newDouble(t, respRule{Match: "issue list", Stdout: string(b)})
	r := run(t, d, nil, append(anchorAndChild(),
		create("process-feedback", "t", "pr:"+prID+":process-feedback:d1", nil)))
	// The anchor (tracked under its node_id form) and the review-pr child are
	// deduped; the feedback item of a different PR does not match this one.
	want := []Outcome{OutcomeDeduped, OutcomeDeduped, OutcomeApplied}
	if !reflect.DeepEqual(outcomes(r), want) {
		t.Fatalf("outcomes %v, want %v", outcomes(r), want)
	}
	if r.Events[0].WorkItemID != "wb-11" || r.Events[1].WorkItemID != "wb-12" {
		t.Fatalf("events: %+v", r.Events)
	}
}
