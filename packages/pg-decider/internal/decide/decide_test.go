package decide

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// fakeRule is a test-only rule. Each test registers its fakes under an entity
// type unique to the test, so the process-wide registry never leaks between
// tests.
type fakeRule struct {
	id     string
	kind   workitem.Kind
	result func(Input) Result
	calls  *int
}

func (f *fakeRule) ID() string          { return f.id }
func (f *fakeRule) Kind() workitem.Kind { return f.kind }
func (f *fakeRule) Evaluate(in Input) Result {
	if f.calls != nil {
		*f.calls++
	}
	if f.result != nil {
		return f.result(in)
	}
	return Result{Skip: &action.Skip{Reason: action.ReasonNotMatched}}
}

func emits(op action.Op, kind string) func(Input) Result {
	return func(Input) Result {
		return Result{Actions: []action.Action{{Op: op, Kind: kind, Facts: map[string]any{"k": kind}}}}
	}
}

func prView() *view.View {
	v := &view.View{Type: "pr", ID: "acme/widgets#42"}
	v.Snapshot.Repo, v.Snapshot.Number = "acme/widgets", 42
	v.Decorations.Relationship = "mine"
	return v
}

func TestRegisterSortsByOrdinal(t *testing.T) {
	et := t.Name()
	Register(et, OrdinalLandReady, &fakeRule{id: "c"})
	Register(et, OrdinalAllClosed, &fakeRule{id: "a"})
	Register(et, OrdinalReviewHeadAdvanced, &fakeRule{id: "b"})
	var got []string
	for _, r := range RulesFor(et) {
		got = append(got, r.ID())
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RulesFor order = %v, want %v", got, want)
	}
	if len(RulesFor("never-registered")) != 0 {
		t.Fatal("unknown entity type must have no rules")
	}
}

func TestRulesForReturnsACopy(t *testing.T) {
	et := t.Name()
	Register(et, 10, &fakeRule{id: "a"})
	rs := RulesFor(et)
	rs[0] = nil
	if RulesFor(et)[0] == nil {
		t.Fatal("mutating the returned slice changed the registry")
	}
}

func TestRegisterRejectsDuplicateIDAndNil(t *testing.T) {
	et := t.Name()
	Register(et, 10, &fakeRule{id: "a"})
	for name, fn := range map[string]func(){
		"duplicate id": func() { Register(et, 20, &fakeRule{id: "a"}) },
		"nil rule":     func() { Register(et, 20, nil) },
		"empty id":     func() { Register(et, 20, &fakeRule{id: ""}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected a panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestCurationOrdinalsMirrorSpecTable(t *testing.T) {
	want := map[string]int{
		"all.closed": 10, "all.reopened": 20, "anchor.lazy": 30, "anchor.backfill": 40,
		"anchor.priority": 50, "adoption": 55, "adoption.node-id": 56,
		"review.head-advanced": 60, "feedback.digest-changed": 70,
		"fixci.failing-on-head": 80, "conflict.present": 90, "land.ready": 100,
	}
	got := map[string]int{
		"all.closed": OrdinalAllClosed, "all.reopened": OrdinalAllReopened,
		"anchor.lazy": OrdinalAnchorLazy, "anchor.backfill": OrdinalAnchorBackfill,
		"anchor.priority": OrdinalAnchorPriority, "adoption": OrdinalAdoption,
		"adoption.node-id":     OrdinalAdoptionNodeID,
		"review.head-advanced": OrdinalReviewHeadAdvanced, "feedback.digest-changed": OrdinalFeedbackDigestChanged,
		"fixci.failing-on-head": OrdinalFixCIFailingOnHead, "conflict.present": OrdinalConflictPresent,
		"land.ready": OrdinalLandReady,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinals = %v, want %v", got, want)
	}
}

func TestPrecedenceHiddenSkipsEveryRule(t *testing.T) {
	et := t.Name()
	calls := 0
	Register(et, 10, &fakeRule{id: "r.one", kind: workitem.KindReviewPR, result: emits(action.OpCreate, "review-pr"), calls: &calls})
	Register(et, 20, &fakeRule{id: "r.two", result: emits(action.OpClose, "anchor"), calls: &calls})
	v := prView()
	v.Annotations.Hidden.Value = true
	res := Decide(v, et)
	if len(res.Actions) != 0 {
		t.Fatalf("hidden entity must yield zero actions, got %+v", res.Actions)
	}
	if calls != 0 {
		t.Fatalf("no rule may be evaluated for a hidden entity, got %d calls", calls)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	for i, id := range []string{"r.one", "r.two"} {
		if s := res.Skipped[i]; s.Rule != id || s.Reason != action.ReasonHidden {
			t.Errorf("skipped[%d] = %+v, want rule %s reason hidden", i, s, id)
		}
	}
}

func TestHiddenReasonIsReportedAsAFact(t *testing.T) {
	et := t.Name()
	Register(et, 10, &fakeRule{id: "r.one"})
	v := prView()
	v.Annotations.Hidden.Value = true
	why := "operator parked it"
	v.Annotations.Hidden.Reason = &why
	if got := Decide(v, et).Skipped[0].Facts["reason"]; got != why {
		t.Fatalf("hidden reason fact = %v", got)
	}
}

func TestPrecedenceSuppressSkipsOnlyThatKind(t *testing.T) {
	et := t.Name()
	var fixCalls, reviewCalls, anyCalls int
	Register(et, 10, &fakeRule{id: "r.fix", kind: workitem.KindFixCI, result: emits(action.OpCreate, "fix-ci"), calls: &fixCalls})
	Register(et, 20, &fakeRule{id: "r.review", kind: workitem.KindReviewPR, result: emits(action.OpCreate, "review-pr"), calls: &reviewCalls})
	Register(et, 30, &fakeRule{id: "r.untied", kind: "", result: emits(action.OpClose, "anchor"), calls: &anyCalls})
	v := prView()
	v.Annotations.Suppress = []string{"fix-ci"}
	res := Decide(v, et)
	if fixCalls != 0 || reviewCalls != 1 || anyCalls != 1 {
		t.Fatalf("calls fix=%d review=%d untied=%d", fixCalls, reviewCalls, anyCalls)
	}
	if len(res.Actions) != 2 || res.Actions[0].Rule != "r.review" || res.Actions[1].Rule != "r.untied" {
		t.Fatalf("actions = %+v", res.Actions)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Rule != "r.fix" || res.Skipped[0].Reason != action.ReasonSuppressed {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	if res.Skipped[0].Facts["kind"] != "fix-ci" {
		t.Fatalf("suppressed skip must name the kind: %+v", res.Skipped[0].Facts)
	}
}

func TestSuppressNeverTouchesARuleWithoutAKind(t *testing.T) {
	et := t.Name()
	calls := 0
	Register(et, 10, &fakeRule{id: "r.untied", kind: "", calls: &calls})
	v := prView()
	v.Annotations.Suppress = []string{"", "fix-ci", "review-pr", "anchor"}
	Decide(v, et)
	if calls != 1 {
		t.Fatal("a rule with an empty kind must always be evaluated")
	}
}

func TestEvaluationOrderIsOrdinalThenWithinRule(t *testing.T) {
	et := t.Name()
	multi := func(Input) Result {
		return Result{Actions: []action.Action{
			{Op: action.OpUpdate, Kind: "x", Facts: map[string]any{"n": 1}},
			{Op: action.OpUpdate, Kind: "x", Facts: map[string]any{"n": 2}},
		}}
	}
	// Registered out of order on purpose.
	Register(et, 20, &fakeRule{id: "second", result: multi})
	Register(et, 10, &fakeRule{id: "first", result: emits(action.OpCreate, "y")})
	res := Decide(prView(), et)
	var got []string
	for _, a := range res.Actions {
		got = append(got, a.Rule+":"+string(a.Op)+":"+a.Kind)
	}
	want := []string{"first:create:y", "second:update:x", "second:update:x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("action order = %v, want %v", got, want)
	}
	if res.Actions[1].Facts["n"] != 1 || res.Actions[2].Facts["n"] != 2 {
		t.Fatalf("within-rule order lost: %+v", res.Actions)
	}
}

func TestCoreFillsRuleIDOnActionsAndSkips(t *testing.T) {
	et := t.Name()
	Register(et, 10, &fakeRule{id: "has.action", result: emits(action.OpCreate, "y")})
	Register(et, 20, &fakeRule{id: "has.skip", result: func(Input) Result {
		return Result{Skip: &action.Skip{Reason: action.ReasonAlreadyHandled, Facts: map[string]any{"head": "abc"}}}
	}})
	res := Decide(prView(), et)
	if res.Actions[0].Rule != "has.action" {
		t.Fatalf("action rule = %q", res.Actions[0].Rule)
	}
	if s := res.Skipped[0]; s.Rule != "has.skip" || s.Reason != "already handled" || s.Facts["head"] != "abc" {
		t.Fatalf("skip = %+v", s)
	}
}

func TestRuleWithNoActionAndNoSkipIsNotMatched(t *testing.T) {
	et := t.Name()
	Register(et, 10, &fakeRule{id: "silent", result: func(Input) Result { return Result{} }})
	res := Decide(prView(), et)
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != action.ReasonNotMatched {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
}

func TestSkipReasonVocabularyIsClosed(t *testing.T) {
	et := t.Name()
	Register(et, 10, &fakeRule{id: "bad", result: func(Input) Result {
		return Result{Skip: &action.Skip{Reason: "person-dismissed"}}
	}})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a skip reason outside the five-value vocabulary must be rejected")
		}
		if !strings.Contains(r.(string), "person-dismissed") {
			t.Fatalf("panic should name the bad reason: %v", r)
		}
	}()
	Decide(prView(), et)
}

func TestValidSkipReasonsAreExactlyTheFive(t *testing.T) {
	want := []string{"hidden", "suppressed", "already handled", "review-pending", "not matched"}
	if got := SkipReasons(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SkipReasons() = %v, want %v", got, want)
	}
}

func TestResultsAreNeverNilSlices(t *testing.T) {
	res := Decide(prView(), t.Name()) // no rules registered
	if res.Actions == nil || res.Skipped == nil {
		t.Fatalf("Decide must return non-nil lists: %+v", res)
	}
}

type anchorFinalizer struct {
	fakeRule
	insert bool
}

func (f *anchorFinalizer) Finalize(in Input, produced []action.Action) []action.Action {
	needs := false
	for _, a := range produced {
		if a.Fields.Parent == action.AnchorParent {
			needs = true
		}
	}
	if !needs || !f.insert {
		return produced
	}
	return append(produced, action.Action{Op: action.OpCreate, Kind: "anchor", Rule: "anchor.lazy"})
}

func TestFinalizerAnchorCreateComesBeforeAnchorChildren(t *testing.T) {
	et := t.Name()
	child := func(Input) Result {
		return Result{Actions: []action.Action{{Op: action.OpCreate, Kind: "review-pr", Fields: action.Fields{Parent: action.AnchorParent}}}}
	}
	// The finalizer rule has the LOWEST ordinal; the child rule runs after it,
	// so the finalizer can only act once every rule was evaluated.
	Register(et, 30, &anchorFinalizer{fakeRule: fakeRule{id: "anchor.lazy", kind: workitem.KindAnchor}, insert: true})
	Register(et, 60, &fakeRule{id: "review", result: child})
	res := Decide(prView(), et)
	if len(res.Actions) != 2 {
		t.Fatalf("actions = %+v", res.Actions)
	}
	if res.Actions[0].Op != action.OpCreate || res.Actions[0].Kind != "anchor" {
		t.Fatalf("anchor create must come first: %+v", res.Actions)
	}
	if res.Actions[1].Fields.Parent != action.AnchorParent {
		t.Fatalf("child must follow: %+v", res.Actions)
	}
}

func TestFinalizerOrderingIsGuaranteedByTheCore(t *testing.T) {
	et := t.Name()
	childFirst := func(Input) Result {
		return Result{Actions: []action.Action{
			{Op: action.OpCreate, Kind: "review-pr", Fields: action.Fields{Parent: action.AnchorParent}},
			{Op: action.OpCreate, Kind: "fix-ci", Fields: action.Fields{Parent: action.AnchorParent}},
		}}
	}
	Register(et, 10, &fakeRule{id: "children", result: childFirst})
	Register(et, 20, &anchorFinalizer{fakeRule: fakeRule{id: "anchor.lazy"}, insert: true})
	res := Decide(prView(), et)
	if len(res.Actions) != 3 || res.Actions[0].Kind != "anchor" || res.Actions[1].Kind != "review-pr" || res.Actions[2].Kind != "fix-ci" {
		t.Fatalf("anchor create must precede both children, children keep order: %+v", res.Actions)
	}
}

func TestFinalizerNotCalledForSuppressedOrHiddenRule(t *testing.T) {
	et := t.Name()
	f := &countingFinalizer{fakeRule: fakeRule{id: "fin", kind: workitem.KindAnchor}}
	Register(et, 10, f)
	v := prView()
	v.Annotations.Suppress = []string{"anchor"}
	Decide(v, et)
	v2 := prView()
	v2.Annotations.Hidden.Value = true
	Decide(v2, et)
	if f.finalizes != 0 {
		t.Fatalf("finalizer ran %d times for a skipped rule", f.finalizes)
	}
	Decide(prView(), et)
	if f.finalizes != 1 {
		t.Fatalf("finalizer ran %d times, want 1", f.finalizes)
	}
}

type countingFinalizer struct {
	fakeRule
	finalizes int
}

func (c *countingFinalizer) Finalize(_ Input, produced []action.Action) []action.Action {
	c.finalizes++
	return produced
}

func TestInputIsBuiltOnceAndShared(t *testing.T) {
	et := t.Name()
	var seen []Input
	rec := func(in Input) Result { seen = append(seen, in); return Result{} }
	Register(et, 10, &fakeRule{id: "a", result: rec})
	Register(et, 20, &fakeRule{id: "b", result: rec})
	v := prView()
	Decide(v, et)
	if len(seen) != 2 || seen[0].Items == nil || seen[0].Items != seen[1].Items {
		t.Fatalf("rules must share one work-item index: %+v", seen)
	}
	if seen[0].View != v || seen[0].EntityType != et {
		t.Fatalf("input = %+v", seen[0])
	}
}

func TestDecideIsIdempotentAndOrderStable(t *testing.T) {
	et := t.Name()
	Register(et, 10, &fakeRule{id: "a", result: emits(action.OpCreate, "review-pr")})
	Register(et, 20, &fakeRule{id: "b"})
	Register(et, 30, &fakeRule{id: "c", result: emits(action.OpUpdate, "process-feedback")})
	v := prView()
	first := Decide(v, et)
	for i := 0; i < 5; i++ {
		if got := Decide(v, et); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, got, first)
		}
	}
}

func TestDecideDoesNotBranchOnRoutedKindOrMutateTheView(t *testing.T) {
	// decide(view) takes the view alone: there is no parameter for the routed
	// item, and the view is not modified.
	et := t.Name()
	Register(et, 10, &fakeRule{id: "a", result: emits(action.OpCreate, "x")})
	v := prView()
	before := *v
	Decide(v, et)
	if !reflect.DeepEqual(*v, before) {
		t.Fatal("Decide modified the view")
	}
}

func TestHasDeciderPRIsAlwaysTrue(t *testing.T) {
	if !HasDecider("pr") {
		t.Fatal("pr has a decider even before any rule registers")
	}
	if HasDecider("issue") || HasDecider("thread") {
		t.Fatal("no issue/thread decider ships on day one")
	}
	et := t.Name()
	Register(et, 10, &fakeRule{id: "a"})
	if !HasDecider(et) {
		t.Fatal("a type with registered rules has a decider")
	}
}

// Nothing in the package may read who closed a work item (S26).
func TestNoCloserIdentityIsReadAnywhere(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v %v", files, err)
	}
	forbidden := []string{"closed_by", "closedby", "closer", "person-dismissed", "persondismissed"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		low := strings.ToLower(string(b))
		for _, w := range forbidden {
			if strings.Contains(low, w) {
				t.Errorf("%s mentions %q", f, w)
			}
		}
	}
}

// DecideWith hands the configuration to every rule through Input.Config, and
// Decide is DecideWith with none.
func TestDecideWithPassesTheConfigToRules(t *testing.T) {
	et := t.Name()
	var seen []*config.Config
	Register(et, 10, &fakeRule{id: "a", result: func(in Input) Result {
		seen = append(seen, in.Config)
		return Result{}
	}})
	cfg := &config.Config{BeadIDPattern: "^x"}
	DecideWith(prView(), et, cfg)
	Decide(prView(), et)
	if len(seen) != 2 || seen[0] != cfg || seen[1] != nil {
		t.Fatalf("configs seen = %v, want [cfg nil]", seen)
	}
}

func TestFocusOrdinalFollowsEveryPRRuleAndTheEntityTypeNamesAreExact(t *testing.T) {
	if EntityTypeIssue != "issue" || EntityTypePR != "pr" {
		t.Fatalf("entity type constants = %q %q", EntityTypeIssue, EntityTypePR)
	}
	if OrdinalFocusItem <= OrdinalLandReady {
		t.Fatalf("OrdinalFocusItem %d must follow OrdinalLandReady %d", OrdinalFocusItem, OrdinalLandReady)
	}
}
