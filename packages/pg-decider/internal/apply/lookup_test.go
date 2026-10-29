package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// The bounded work-item lookup (design 6.2): the reader's fail-closed cases,
// the filter that keeps a PR's own items (keyless legacy ones included), and
// the plan dedup that apply shares.

const lookupQuery = "work-items-all"

func lookupCfg(ceiling int) *config.Config {
	return &config.Config{WorkItemsQuery: lookupQuery, WorkItemsCeiling: &ceiling}
}

func listing(entities string, extra string) string {
	return `{"entities":[` + entities + `],"present_ids":[],"sources":[{"source":"beads","status":"succeeded","count":1}],"truncated":false` + extra + `}`
}

func ent(id, title, state, md string) string {
	return `{"id":"` + id + `","title":"` + title + `","state":"` + state + `","metadata":{` + md + `}}`
}

func lookupView() *view.View {
	return &view.View{
		Type: "pr", ID: "acme/api#7",
		Snapshot: view.PRSnapshot{ID: "acme/api#7", Repo: "acme/api", Number: 7, NodeID: "PR_node7"},
	}
}

func TestReadLookupFailsClosedWithoutTheQueryOrTheCeiling(t *testing.T) {
	ceil := 10
	for _, tc := range []struct {
		name   string
		cfg    *config.Config
		reason string
	}{
		{"nil config", nil, LookupReasonQueryAbsent},
		{"no query", &config.Config{WorkItemsCeiling: &ceil}, LookupReasonQueryAbsent},
		{"no ceiling", &config.Config{WorkItemsQuery: lookupQuery}, LookupReasonCeilingAbsent},
	} {
		d := newDouble(t)
		env := testEnv(d, nil)
		env.Config = tc.cfg
		lk, err := ReadLookup(context.Background(), env)
		var le *LookupError
		if lk != nil || !errors.As(err, &le) || le.Reason != tc.reason {
			t.Fatalf("%s: lookup=%v err=%v, want reason %s", tc.name, lk, err, tc.reason)
		}
		if tc.reason == LookupReasonCeilingAbsent && !strings.Contains(err.Error(), "work_items_ceiling is not configured") {
			t.Errorf("%s: the message does not state the missing key: %v", tc.name, err)
		}
		if n := len(d.calls()); n != 0 {
			t.Errorf("%s: %d connector call(s) made without a usable configuration", tc.name, n)
		}
	}
}

func TestReadLookupCallsTheNamedQueryOnceOnTheConfiguredBackend(t *testing.T) {
	d := newDouble(t, respRule{Match: "issue list", Stdout: listing(ent("b1", "acme/api#7: x", "open", `"repo":"acme/api"`), "")})
	cfg := lookupCfg(5)
	cfg.AgentTrackerBackend = "trk"
	lk, err := ReadLookup(context.Background(), testEnv(d, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if lk.Items() != 1 || lk.Ceiling() != 5 {
		t.Fatalf("items=%d ceiling=%d", lk.Items(), lk.Ceiling())
	}
	got := d.lines()
	if len(got) != 1 || got[0] != "pg-connector issue list --query work-items-all --backend trk" {
		t.Fatalf("calls = %q", got)
	}
}

func TestReadLookupAcceptsAListingExactlyAtTheCeiling(t *testing.T) {
	d := newDouble(t, respRule{Match: "issue list", Stdout: listing(ent("b1", "t", "closed", "")+","+ent("b2", "t", "open", ""), "")})
	if _, err := ReadLookup(context.Background(), testEnv(d, lookupCfg(2))); err != nil {
		t.Fatalf("2 items at ceiling 2: %v", err)
	}
}

func TestReadLookupAboveTheCeilingFailsClosedAndReportsTheFigures(t *testing.T) {
	d := newDouble(t, respRule{Match: "issue list", Stdout: listing(ent("b1", "t", "closed", "")+","+ent("b2", "t", "open", "")+","+ent("b3", "t", "open", ""), "")})
	lk, err := ReadLookup(context.Background(), testEnv(d, lookupCfg(2)))
	var le *LookupError
	if lk != nil || !errors.As(err, &le) || le.Reason != LookupReasonAboveCeiling {
		t.Fatalf("lookup=%v err=%v", lk, err)
	}
	if le.Items == nil || *le.Items != 3 || le.Ceiling == nil || *le.Ceiling != 2 {
		t.Fatalf("figures = %v %v, want 3 and 2", le.Items, le.Ceiling)
	}
	if !strings.Contains(err.Error(), "work_items_ceiling=2") || !strings.Contains(err.Error(), "nothing was written") {
		t.Errorf("message = %v", err)
	}
}

func TestReadLookupTruncatedListingFailsClosed(t *testing.T) {
	d := newDouble(t, respRule{Match: "issue list", Stdout: `{"entities":[` + ent("b1", "t", "open", "") + `],"sources":[{"source":"beads","status":"succeeded","count":1}],"truncated":true}`})
	lk, err := ReadLookup(context.Background(), testEnv(d, lookupCfg(100)))
	var le *LookupError
	if lk != nil || !errors.As(err, &le) || le.Reason != LookupReasonTruncated {
		t.Fatalf("lookup=%v err=%v", lk, err)
	}
	if le.Items == nil || *le.Items != 1 {
		t.Errorf("items = %v, want 1", le.Items)
	}
}

func TestReadLookupUnavailableListingFailsClosed(t *testing.T) {
	cases := map[string]respRule{
		"the connector exits non-zero": {Match: "issue list", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"tracker down"}}`},
		"the answer is not JSON":       {Match: "issue list", Stdout: `not json`},
		"a source is degraded": {
			Match:  "issue list",
			Stdout: `{"entities":[],"sources":[{"source":"beads","status":"degraded","count":0,"reason":"cache fallback"}],"truncated":false}`,
		},
		"a source failed": {
			Match:  "issue list",
			Stdout: `{"entities":[],"sources":[{"source":"beads","status":"failed","count":0,"reason":"boom"}],"truncated":false}`,
		},
	}
	for name, rule := range cases {
		d := newDouble(t, rule)
		lk, err := ReadLookup(context.Background(), testEnv(d, lookupCfg(100)))
		var le *LookupError
		if lk != nil || !errors.As(err, &le) || le.Reason != LookupReasonUnavailable {
			t.Fatalf("%s: lookup=%v err=%v", name, lk, err)
		}
		if le.Ceiling == nil || *le.Ceiling != 100 {
			t.Errorf("%s: the ceiling is not reported: %v", name, le.Ceiling)
		}
	}
}

func TestReadLookupToleratesNonStringMetadataAndTheEnvelopeShape(t *testing.T) {
	d := newDouble(t, respRule{
		Match:  "issue list",
		Stdout: `{"result":{"entities":[{"id":"b1","title":"t","state":"open","metadata":{"pr_number":7,"dedup_key":"pr:acme/api#7:fix-ci:abc","flag":true}}]}}`,
	})
	lk, err := ReadLookup(context.Background(), testEnv(d, lookupCfg(10)))
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := lk.Find(workitem.EntityRef{Type: "pr", ID: "acme/api#7"}, "pr:acme/api#7:fix-ci:abc"); !ok || id != "b1" {
		t.Fatalf("find = %q %v", id, ok)
	}
	if got := lk.items[0].Metadata["pr_number"]; got != "7" {
		t.Errorf("a numeric metadata value reads as %q, want 7", got)
	}
}

func linkIDs(v *view.View) []string {
	var out []string
	for _, l := range v.Links {
		out = append(out, l.ID)
	}
	return out
}

func TestAugmentAddsTheClosedItemsOfThePRAndNothingOfAnotherPR(t *testing.T) {
	lk := NewLookup([]ListedItem{
		{ID: "ci-closed", Title: "fix-ci: acme/api#7", State: "closed", Metadata: map[string]string{"dedup_key": "pr:acme/api#7:fix-ci:abc123"}},
		{ID: "node-form", Title: "fix-ci: acme/api#7", State: "closed", Metadata: map[string]string{"dedup_key": "pr:PR_node7:fix-ci:def456"}},
		{ID: "other-pr", Title: "fix-ci: acme/api#8", State: "closed", Metadata: map[string]string{"dedup_key": "pr:acme/api#8:fix-ci:abc123"}},
		{ID: "legacy-anchor", Title: "acme/api#7: Add export", State: "open", Metadata: map[string]string{}},
		{ID: "legacy-other-anchor", Title: "acme/api#70: Another", State: "open", Metadata: map[string]string{}},
		{ID: "legacy-node", Title: "review-pr: whatever", State: "closed", Metadata: map[string]string{"node_id": "PR_node7"}},
		{ID: "legacy-meta", Title: "process-feedback: acme/api#7", State: "closed", Metadata: map[string]string{"repo": "acme/api", "pr_number": "7"}},
		{ID: "unrelated", Title: "write docs", State: "open", Metadata: map[string]string{"repo": "acme/api", "pr_number": "9"}},
	}, 100)
	v := lookupView()
	v.Links = []view.Link{{Type: "issue", ID: "legacy-meta", Title: "process-feedback: acme/api#7", State: "open", Metadata: map[string]string{}}}

	got := lk.Augment(v)
	ids := strings.Join(linkIDs(got), ",")
	for _, want := range []string{"ci-closed", "node-form", "legacy-anchor", "legacy-node"} {
		if !strings.Contains(ids, want) {
			t.Errorf("link %s is missing: %s", want, ids)
		}
	}
	for _, not := range []string{"other-pr", "legacy-other-anchor", "unrelated"} {
		if strings.Contains(ids, not) {
			t.Errorf("link %s was added: %s", not, ids)
		}
	}
	if strings.Count(ids, "legacy-meta") != 1 {
		t.Errorf("an item the view already links was added again: %s", ids)
	}
	if len(v.Links) != 1 {
		t.Errorf("the view was modified: %v", linkIDs(v))
	}
	// The rules see the closed fix-ci item and the keyless anchor as work items.
	ix := workitem.BuildIndex(got)
	if _, ok := ix.Find(workitem.KindFixCI, workitem.Context{HeadSHA: "abc123"}); !ok {
		t.Error("the closed fix-ci item of the head is not found through the augmented view")
	}
	if _, ok := ix.Anchor(); !ok {
		t.Error("the keyless legacy anchor is not seen through the augmented view")
	}
}

func TestAugmentLinksAFocusBeadWithTheSourceRelation(t *testing.T) {
	lk := NewLookup([]ListedItem{{ID: "f1", Title: "focus: acme/api#7", State: "closed", Metadata: map[string]string{"dedup_key": "pr:acme/api#7:focus-item"}}}, 10)
	got := lk.Augment(lookupView())
	if len(got.Links) != 1 || got.Links[0].Relation != workitem.RelationSource {
		t.Fatalf("links = %+v", got.Links)
	}
}

func TestAugmentOfNilOrEmptyLookupReturnsTheViewItself(t *testing.T) {
	v := lookupView()
	var nilLookup *Lookup
	if nilLookup.Augment(v) != v {
		t.Error("a nil lookup must return the view unchanged")
	}
	if NewLookup(nil, 5).Augment(v) != v {
		t.Error("a lookup with nothing to add must return the view unchanged")
	}
}

func createAction(rule, kind, key, parent string) action.Action {
	return action.Action{
		Op: action.OpCreate, Kind: kind, Rule: rule,
		Fields: action.Fields{Title: kind, Parent: parent, Metadata: map[string]string{"dedup_key": key}},
	}
}

func TestDedupePlanMovesAHeldCreateToSkippedAndReparentsItsChildren(t *testing.T) {
	lk := NewLookup([]ListedItem{
		{ID: "anchor-1", Title: "acme/api#7: x", State: "closed", Metadata: map[string]string{"dedup_key": "pr:acme/api#7:anchor"}},
		{ID: "ci-1", Title: "fix-ci: acme/api#7", State: "closed", Metadata: map[string]string{"dedup_key": "pr:acme/api#7:fix-ci:aaa"}},
	}, 10)
	ref := workitem.EntityRef{Type: "pr", ID: "acme/api#7", NodeID: "PR_node7"}
	in := action.PlanResult{
		Actions: []action.Action{
			createAction("anchor.lazy", "anchor", "pr:acme/api#7:anchor", ""),
			createAction("fixci.failing-on-head", "fix-ci", "pr:PR_node7:fix-ci:aaa", action.AnchorParent),
			createAction("fixci.failing-on-head", "fix-ci", "pr:acme/api#7:fix-ci:bbb", action.AnchorParent),
			{Op: action.OpUpdate, Kind: "anchor", Rule: "all.closed", Target: ptr("anchor-1")},
		},
		Skipped: []action.Skip{{Rule: "x", Reason: action.ReasonNotMatched}},
	}
	out := lk.DedupePlan(ref, in)
	if len(out.Actions) != 2 || out.Actions[0].Fields.Metadata["dedup_key"] != "pr:acme/api#7:fix-ci:bbb" || out.Actions[1].Op != action.OpUpdate {
		t.Fatalf("actions = %+v", out.Actions)
	}
	if out.Actions[0].Fields.Parent != "anchor-1" {
		t.Errorf("the child is parented at %q, want the id the listing holds", out.Actions[0].Fields.Parent)
	}
	if len(out.Skipped) != 3 {
		t.Fatalf("skipped = %+v", out.Skipped)
	}
	for _, s := range out.Skipped[1:] {
		if s.Reason != action.ReasonAlreadyHandled || s.Facts["op"] != "create" || s.Facts["work_item"] == "" {
			t.Errorf("skip = %+v", s)
		}
	}
	if len(in.Actions) != 4 {
		t.Error("the input plan was modified")
	}
	var nilLookup *Lookup
	if got := nilLookup.DedupePlan(ref, in); len(got.Actions) != 4 {
		t.Error("a nil lookup must leave the plan unchanged")
	}
}

func ptr(s string) *string { return &s }

// The create of apply uses the run's listing, not one `issue list` per create.
func TestApplyDedupsAgainstTheRunListingWithoutAnotherListCall(t *testing.T) {
	d := newDouble(t)
	lk := NewLookup([]ListedItem{{ID: "ci-1", Title: "fix-ci: acme/api#7", State: "closed", Metadata: map[string]string{"dedup_key": "pr:acme/api#7:fix-ci:aaa"}}}, 10)
	res := Run(context.Background(), Input{
		Type: "pr", ID: "acme/api#7", View: lookupView(), Lookup: lk, Env: testEnv(d, lookupCfg(10)),
		Actions: []action.Action{
			createAction("fixci.failing-on-head", "fix-ci", "pr:acme/api#7:fix-ci:aaa", ""),
			createAction("fixci.failing-on-head", "fix-ci", "pr:acme/api#7:fix-ci:bbb", ""),
		},
	})
	if len(res.Events) != 2 || res.Events[0].Outcome != OutcomeDeduped || res.Events[0].WorkItemID != "ci-1" || res.Events[1].Outcome != OutcomeApplied {
		t.Fatalf("events = %+v", res.Events)
	}
	for _, l := range d.lines() {
		if strings.HasPrefix(l, "pg-connector issue list") {
			t.Errorf("a per-create list call was made: %s", l)
		}
	}
}

// A PR-lane create writes the node_id key form; an item an older binary wrote
// carries the id form. Both directions must read as one identity, so a create
// planned by the current code against a listed item of the other form is
// deduped, not duplicated.
func TestMixedKeyFormsAreOneIdentityForAugmentAndDedupe(t *testing.T) {
	ref := workitem.EntityRef{Type: "pr", ID: "acme/api#7", NodeID: "PR_node7"}
	ctx := workitem.Context{HeadSHA: "aaa"}
	idKey := workitem.DedupKey(ref, workitem.KindFixCI, ctx)
	nodeKey := workitem.CreateKey(ref, workitem.KindFixCI, ctx)
	if idKey == nodeKey {
		t.Fatalf("test setup: id and node_id forms are equal: %q", idKey)
	}
	for _, tc := range []struct {
		name, listed, create string
	}{
		{"old id-form item, current node_id create", idKey, nodeKey},
		{"node_id-form item, id-form create", nodeKey, idKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lk := NewLookup([]ListedItem{
				{ID: "ci-1", Title: "fix-ci: acme/api#7 @ aaa", State: "open", Metadata: map[string]string{"dedup_key": tc.listed}},
			}, 10)
			// Owns: the listed item belongs to the PR, so the rules can see it.
			if got := linkIDs(lk.Augment(lookupView())); strings.Join(got, ",") != "ci-1" {
				t.Fatalf("the item keyed %q was not linked: %v", tc.listed, got)
			}
			// sameKey: the create of the other form is deduped, not duplicated.
			out := lk.DedupePlan(ref, action.PlanResult{Actions: []action.Action{
				createAction("fixci.failing-on-head", "fix-ci", tc.create, action.AnchorParent),
			}})
			if len(out.Actions) != 0 {
				t.Fatalf("a duplicate create survived: %+v", out.Actions)
			}
			if len(out.Skipped) != 1 || out.Skipped[0].Reason != action.ReasonAlreadyHandled || out.Skipped[0].Facts["work_item"] != "ci-1" {
				t.Fatalf("skipped = %+v", out.Skipped)
			}
		})
	}
}
