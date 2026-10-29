package rules

// The several-items-per-PR behavior of review.head-advanced: every case is a
// PR view built from the mine_create fixture with a chosen list of review items
// (state, assignee, head, key), so the cases read as the rows of the rule's
// table in docs/behavior/pg-decider/work-items.md.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// rItem is one review item of a built view.
type rItem struct {
	id, state, assignee, head, key string
	title                          string
}

const (
	rOldHead = "1a2b3c4"
	rNewHead = reviewHead // the PR's current head in every built view
)

func rKey(head string) string { return "pr:acme/widgets#42:review-pr:" + head }

// rNodeKey is the node_id form a refresh writes for the same item.
func rNodeKey(head string) string { return "pr:PR_node_42:review-pr:" + head }

// reviewBuild is the mine_create view with the given review items linked, the
// PR head at head, and the force-review flag as asked.
func reviewBuild(t *testing.T, force bool, items ...rItem) *view.View {
	t.Helper()
	var m map[string]any
	b := reviewReadFixture(t, "mine_create")
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["annotations"].(map[string]any)["force_review"] = force
	if force {
		m["annotations"].(map[string]any)["force_review_sha"] = rNewHead
	}
	links := m["links"].([]any)
	for _, it := range items {
		md := map[string]any{
			"repo": "acme/widgets", "pr_number": "42", "branch": "feature/retry",
			"head_sha": it.head, "ownership": "mine",
		}
		if it.key != "" {
			md["dedup_key"] = it.key
		}
		title := it.title
		if title == "" {
			title = "review-pr: acme/widgets#42 @ " + it.head
		}
		l := map[string]any{
			"type": "issue", "id": it.id, "relation": "work", "state": it.state,
			"title": title, "labels": []string{}, "metadata": md,
		}
		if it.assignee != "" {
			l["assignee"] = it.assignee
		}
		links = append(links, l)
	}
	m["links"] = links
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func reviewEval(v *view.View) decide.Result {
	return reviewRuleOf().Evaluate(decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR})
}

func reviewRuleOf() decide.Rule {
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == reviewRuleID {
			return r
		}
	}
	panic("review rule is not registered")
}

func reviewOps(res decide.Result) []string {
	var out []string
	for _, a := range res.Actions {
		out = append(out, string(a.Op)+" "+a.Kind)
	}
	return out
}

// A burst of pushes before any claim yields ONE item: each push refreshes the
// item the earlier one made, and never creates a second.
func TestReviewBurstOfPushesYieldsOneItem(t *testing.T) {
	// the item the first push created, at the head before the burst's last push
	v := reviewBuild(t, false, rItem{id: "bd-2", state: "open", head: rOldHead, key: rKey(rOldHead)})
	res := reviewEval(v)
	if got := reviewOps(res); !reflect.DeepEqual(got, []string{"update review-pr"}) {
		t.Fatalf("ops = %v", got)
	}
	reviewWantUpdateKey(t, res.Actions[0], "bd-2", rNewHead, rNodeKey(rNewHead))

	// the state after the refresh is quiet
	v = reviewBuild(t, false, rItem{id: "bd-2", state: "open", head: rNewHead, key: rKey(rNewHead)})
	if res := reviewEval(v); len(res.Actions) != 0 || res.Skip.Reason != action.ReasonReviewPending {
		t.Fatalf("after the refresh: %+v", res)
	}
}

// A claimed item at the current head means that head is being reviewed.
func TestReviewClaimedItemAtCurrentHeadCreatesNothing(t *testing.T) {
	for _, tc := range []rItem{
		{id: "bd-2", state: "in_progress", assignee: "worker-1", head: rNewHead, key: rKey(rNewHead)},
		{id: "bd-2", state: "open", assignee: "worker-1", head: rNewHead, key: rKey(rNewHead)},
	} {
		res := reviewEval(reviewBuild(t, false, tc))
		if len(res.Actions) != 0 || res.Skip == nil || res.Skip.Reason != action.ReasonReviewPending {
			t.Errorf("%+v: %+v", tc, res)
		}
	}
}

// An item created at one head, updated to another and closed blocks a create
// for the head it was updated to: the test is on head_sha, not on the key, so
// it holds even when the key still names the old head.
func TestReviewClosedItemUpdatedToCurrentHeadBlocksARecreate(t *testing.T) {
	for _, key := range []string{rKey(rNewHead), rKey(rOldHead), ""} {
		res := reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "closed", head: rNewHead, key: key}))
		if len(res.Actions) != 0 || res.Skip == nil || res.Skip.Reason != action.ReasonAlreadyHandled {
			t.Errorf("key %q: %+v", key, res)
		}
	}
}

// Legacy items written before suffixes existed: the open one is refreshed (its
// key gains the suffix, keeping the identity segment), a closed one at the
// current head blocks, a closed one at an older head leaves the head to create.
func TestReviewLegacySuffixlessItems(t *testing.T) {
	legacy := "pr:acme/widgets#42:review-pr"
	nodeLegacy := "pr:PR_node_42:review-pr"

	res := reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "open", head: rOldHead, key: legacy, title: "review-pr: acme/widgets#42"}))
	reviewWantUpdateKey(t, res.Actions[0], "bd-2", rNewHead, rNodeKey(rNewHead))

	res = reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "open", head: rOldHead, key: nodeLegacy, title: "review-pr: acme/widgets#42"}))
	reviewWantUpdateKey(t, res.Actions[0], "bd-2", rNewHead, "pr:PR_node_42:review-pr:"+rNewHead)

	res = reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "closed", head: rNewHead, key: legacy, title: "review-pr: acme/widgets#42"}))
	if len(res.Actions) != 0 || res.Skip.Reason != action.ReasonAlreadyHandled {
		t.Errorf("legacy closed item at the current head: %+v", res)
	}

	res = reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "closed", head: rOldHead, key: legacy, title: "review-pr: acme/widgets#42"}))
	if got := reviewOps(res); !reflect.DeepEqual(got, []string{"create review-pr"}) {
		t.Errorf("legacy closed item at an older head: %v", got)
	}
}

// Force-review with only a claimed or a closed item at the same head: ONE new
// item whose dedup key differs from every existing key (so apply cannot report
// a dedup), then the consumption, whose actions require the create.
func TestReviewForceWithOnlyAClaimedOrClosedSameHeadItemCreatesOneNewItem(t *testing.T) {
	for name, it := range map[string]rItem{
		"claimed": {id: "bd-2", state: "in_progress", assignee: "worker-1", head: rNewHead, key: rKey(rNewHead)},
		"closed":  {id: "bd-2", state: "closed", head: rNewHead, key: rKey(rNewHead)},
	} {
		res := reviewEval(reviewBuild(t, true, it))
		if got := reviewOps(res); !reflect.DeepEqual(got, []string{"create review-pr", "annotate ", "annotate "}) {
			t.Fatalf("%s: ops = %v", name, got)
		}
		reviewWantCreateKey(t, res.Actions[0], "bd-1", "mine", ":r1")
		if res.Actions[0].Fields.Metadata["dedup_key"] == it.key {
			t.Errorf("%s: the new key equals the existing key", name)
		}
		reviewWantConsumption(t, res.Actions[1:], rNewHead)
	}

	// a second forced request gets another key, never the first request's
	res := reviewEval(reviewBuild(
		t, true,
		rItem{id: "bd-2", state: "closed", head: rNewHead, key: rKey(rNewHead)},
		rItem{id: "bd-3", state: "closed", head: rNewHead, key: rKey(rNewHead) + ":r1"},
	))
	reviewWantCreateKey(t, res.Actions[0], "bd-1", "mine", ":r2")
}

// An unclaimed item takes the refresh; force adds the consumption.
func TestReviewForceWithAnUnclaimedItemRefreshesIt(t *testing.T) {
	res := reviewEval(reviewBuild(
		t, true,
		rItem{id: "bd-2", state: "closed", head: rNewHead, key: rKey(rNewHead)},
		rItem{id: "bd-3", state: "open", head: rOldHead, key: rKey(rOldHead)},
	))
	if got := reviewOps(res); !reflect.DeepEqual(got, []string{"update review-pr", "annotate ", "annotate "}) {
		t.Fatalf("ops = %v", got)
	}
	// the refreshed key cannot be the key of the closed item at the same head
	reviewWantUpdateKey(t, res.Actions[0], "bd-3", rNewHead, rNodeKey(rNewHead)+":r1")
}

// A deferred or blocked item is not unclaimed: it is left alone and does not
// block the item for the new head.
func TestReviewDeferredAndBlockedItemsAreLeftAloneAndDoNotBlock(t *testing.T) {
	for _, state := range []string{"deferred", "blocked", "in_progress"} {
		res := reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: state, head: rOldHead, key: rKey(rOldHead)}))
		if got := reviewOps(res); !reflect.DeepEqual(got, []string{"create review-pr"}) {
			t.Errorf("%s: ops = %v", state, got)
			continue
		}
		for _, a := range res.Actions {
			if a.Target != nil && *a.Target == "bd-2" {
				t.Errorf("%s: the item was written: %+v", state, a)
			}
		}
		reviewWantCreate(t, res.Actions[0], "bd-1", "mine")
	}
	// an assigned item whose status is still open is claimed
	res := reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "open", assignee: "worker-1", head: rOldHead, key: rKey(rOldHead)}))
	if got := reviewOps(res); !reflect.DeepEqual(got, []string{"create review-pr"}) {
		t.Errorf("assigned open item: %v", got)
	}
}

// A claimed item for an older head is left alone; the new head gets its own item.
func TestReviewClaimedItemForAnOlderHeadIsLeftAlone(t *testing.T) {
	res := reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "in_progress", assignee: "worker-1", head: rOldHead, key: rKey(rOldHead)}))
	if got := reviewOps(res); !reflect.DeepEqual(got, []string{"create review-pr"}) {
		t.Fatalf("ops = %v", got)
	}
	reviewWantCreate(t, res.Actions[0], "bd-1", "mine")
}

// One create for the head, whatever the number of closed items for older heads.
func TestReviewSeveralClosedItemsForOlderHeadsCreateOneItem(t *testing.T) {
	res := reviewEval(reviewBuild(
		t, false,
		rItem{id: "bd-2", state: "closed", head: "aaaaaaa", key: rKey("aaaaaaa")},
		rItem{id: "bd-3", state: "closed", head: rOldHead, key: rKey(rOldHead)},
	))
	if got := reviewOps(res); !reflect.DeepEqual(got, []string{"create review-pr"}) {
		t.Fatalf("ops = %v", got)
	}
}

// A keyless review item (an adoptable one whose title names the PR, with or
// without the head suffix) counts as existing for the head it carries.
func TestReviewKeylessItemsCountByTheirHead(t *testing.T) {
	for _, title := range []string{"review-pr: acme/widgets#42", "review-pr: acme/widgets#42 @ " + rNewHead} {
		res := reviewEval(reviewBuild(t, false, rItem{id: "bd-2", state: "closed", head: rNewHead, title: title}))
		if len(res.Actions) != 0 || res.Skip.Reason != action.ReasonAlreadyHandled {
			t.Errorf("%q: %+v", title, res)
		}
	}
}

// The reopen path: an open PR whose anchor is closed gets its anchor reopened,
// and its closed review item for the current head neither reopened nor
// recreated, through the whole registered rule set.
func TestReopenedPRWithAClosedReviewedHeadGetsNoReviewItem(t *testing.T) {
	v := reviewBuild(t, false, rItem{id: "bd-2", state: "closed", head: rNewHead, key: rKey(rNewHead)})
	patchLinkState(t, v, "bd-1", "closed")
	res := suiteDecide(v)
	if got := suiteActionsOfRule(res, reviewRuleID); len(got) != 0 {
		t.Fatalf("review actions: %+v", got)
	}
	var reopened []string
	for _, a := range res.Actions {
		if a.Op == action.OpReopen {
			reopened = append(reopened, a.Kind)
		}
	}
	if !reflect.DeepEqual(reopened, []string{"anchor"}) {
		t.Fatalf("reopens = %v, want only the anchor", reopened)
	}
}

// patchLinkState rebuilds v with link id in the given state.
func patchLinkState(t *testing.T, v *view.View, id, state string) {
	t.Helper()
	for i := range v.Links {
		if v.Links[i].ID == id {
			v.Links[i].State = state
		}
	}
}

// Merging closes every open review item of the PR, claimed ones included, and
// leaves the closed ones; the anchor closes last.
func TestMergeClosesEveryOpenReviewItem(t *testing.T) {
	v := reviewBuild(
		t, false,
		rItem{id: "bd-2", state: "closed", head: "aaaaaaa", key: rKey("aaaaaaa")},
		rItem{id: "bd-3", state: "in_progress", assignee: "worker-1", head: rOldHead, key: rKey(rOldHead)},
		rItem{id: "bd-4", state: "open", head: rNewHead, key: rKey(rNewHead)},
	)
	v.Snapshot.State, v.Snapshot.Merged = "merged", true
	res := suiteDecide(v)
	var closed []string
	for _, a := range res.Actions {
		if a.Op == action.OpClose {
			closed = append(closed, a.Kind+" "+*a.Target)
		}
	}
	want := []string{"review-pr bd-3", "review-pr bd-4", "anchor bd-1"}
	if !reflect.DeepEqual(closed, want) {
		t.Fatalf("closes = %v, want %v", closed, want)
	}
	for _, a := range res.Actions {
		if a.Kind == "review-pr" && a.Op != action.OpClose && a.Rule == reviewRuleID {
			t.Errorf("a merged PR gets no other review write: %+v", a)
		}
	}
}

// An open review item the view lists without the anchor-child shape (a listed
// item of the all-status lookup carries no relation) still closes on merge.
func TestMergeClosesAnOpenReviewItemWithoutTheWorkRelation(t *testing.T) {
	v := reviewBuild(t, false, rItem{id: "bd-4", state: "open", head: rNewHead, key: rKey(rNewHead)})
	for i := range v.Links {
		if v.Links[i].ID == "bd-4" {
			v.Links[i].Relation = ""
		}
	}
	v.Snapshot.State, v.Snapshot.Merged = "merged", true
	res := suiteDecide(v)
	found := false
	for _, a := range res.Actions {
		if a.Op == action.OpClose && a.Target != nil && *a.Target == "bd-4" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the open review item was not closed: %+v", res.Actions)
	}
}

// Titles carry the short head, and the short head is the first seven characters.
func TestReviewTitlesCarryTheShortHead(t *testing.T) {
	v := reviewBuild(t, false)
	v.Snapshot.HeadSHA = "0123456789abcdef"
	res := reviewEval(v)
	if got := res.Actions[0].Fields.Title; got != "review-pr: acme/widgets#42 @ 0123456" {
		t.Errorf("title = %q", got)
	}
	if k := res.Actions[0].Fields.Metadata["dedup_key"]; !strings.HasSuffix(k, ":review-pr:0123456789abcdef") {
		t.Errorf("the key carries the whole head: %q", k)
	}
}

func reviewReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "review", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A keyless review item is adopted with the key of the head it carries; one
// that carries no head has no head-scoped key to write, so it is reported as
// unadoptable (its disposition is the operator's, ADOPT or CLOSE) and the
// review rule still finds an unclaimed one to refresh.
func TestAdoptionOfKeylessReviewItemsUsesTheirHead(t *testing.T) {
	v := reviewBuild(t, false, rItem{id: "bd-2", state: "closed", head: rOldHead, title: "review-pr: acme/widgets#42"})
	res := anchorRule(t, "adoption").Evaluate(anchorInput(v))
	if len(res.Actions) != 1 || res.Actions[0].Fields.Metadata["dedup_key"] != strings.Replace(rKey(rOldHead), "acme/widgets#42", "PR_node_42", 1) {
		t.Fatalf("adoption = %+v", res)
	}

	v = reviewBuild(t, false, rItem{id: "bd-2", state: "open", head: "", title: "review-pr: acme/widgets#42"})
	res = anchorRule(t, "adoption").Evaluate(anchorInput(v))
	if len(res.Actions) != 0 || res.Skip == nil || !reflect.DeepEqual(res.Skip.Facts["unadoptable"], []string{"bd-2"}) {
		t.Fatalf("a review item without a head is unadoptable: %+v", res)
	}
	// the review rule treats that unclaimed item as the one to refresh
	if got := reviewOps(reviewEval(v)); !reflect.DeepEqual(got, []string{"update review-pr"}) {
		t.Fatalf("ops = %v", got)
	}
}
