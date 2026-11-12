package main

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/attention"
)

// feedGroup is one group as a consumer reconstructs it from the merged feed.
type feedGroup struct {
	Key string
	IDs []string
}

// regroupFeed is the menu bar's rule (docs/behavior/pg-desk/attention.md,
// "Grouping and order"): group by group.key in order of first appearance in
// the feed, and keep feed order inside each group. Items with no group are
// singletons keyed by their own entity.
func regroupFeed(feed []schema.AttentionItem) []feedGroup {
	var groups []feedGroup
	index := map[string]int{}
	for _, it := range feed {
		key := it.Type + ":" + it.ID
		if it.Group != nil {
			key = it.Group.Key
		}
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, feedGroup{Key: key})
		}
		groups[i].IDs = append(groups[i].IDs, it.ID)
	}
	return groups
}

// assertFeedRoundTripsToEvaluatorOrder asserts the consumer-side regrouping of
// the merged feed reproduces the evaluator's own groups: the same groups, in
// the same order, with the same items in the same order inside each.
func assertFeedRoundTripsToEvaluatorOrder(t *testing.T, feed []schema.AttentionItem, groups []attention.Group) {
	t.Helper()
	want := make([]feedGroup, 0, len(groups))
	for _, g := range groups {
		fg := feedGroup{Key: g.Key}
		for _, it := range g.Items {
			fg.IDs = append(fg.IDs, it.ID)
		}
		want = append(want, fg)
	}
	if got := regroupFeed(feed); !reflect.DeepEqual(got, want) {
		t.Errorf("regrouped merged feed = %+v, want the evaluator's groups %+v", got, want)
	}
}

// TestRegroupFeed_StableSeverityResortKeepsGroupOrder documents why the
// round-trip holds even though the umbrella re-sorts the merged feed by
// severity: with the evaluator's order [A:high, A:low, B:high] the stable sort
// yields [A:high, B:high, A:low], and regrouping by first appearance still
// gives the groups A then B with A's items in evaluator order.
func TestRegroupFeed_StableSeverityResortKeepsGroupOrder(t *testing.T) {
	a := &schema.AttentionGroup{Key: "issue:ABC-1", Label: "ABC-1"}
	b := &schema.AttentionGroup{Key: "issue:ABC-2", Label: "ABC-2"}
	resorted := []schema.AttentionItem{
		{Type: "pr", ID: "a1", Group: a, Severity: schema.SeverityHigh},
		{Type: "pr", ID: "b1", Group: b, Severity: schema.SeverityHigh},
		{Type: "pr", ID: "a2", Group: a, Severity: schema.SeverityLow},
		{Type: "issue", ID: "other"}, // another source: no group, a singleton
	}
	want := []feedGroup{
		{Key: "issue:ABC-1", IDs: []string{"a1", "a2"}},
		{Key: "issue:ABC-2", IDs: []string{"b1"}},
		{Key: "issue:other", IDs: []string{"other"}},
	}
	if got := regroupFeed(resorted); !reflect.DeepEqual(got, want) {
		t.Errorf("regroupFeed = %+v, want %+v", got, want)
	}
}
