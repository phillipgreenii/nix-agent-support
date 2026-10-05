package rules

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

// ---- adoption ---------------------------------------------------------

func TestAnchorAdoption(t *testing.T) {
	cases := []struct {
		fixture string
		kind    string
		id      string
		key     string
		by      string
	}{
		{"adopt_review_by_title", "review-pr", "bd-2", "pr:acme/widgets#42:review-pr", "title"},
		{"adopt_anchor_by_prefix", "anchor", "bd-1", "pr:acme/widgets#42:anchor", "anchor-prefix"},
		// A closed item is adopted like an open one; the node_id form is NOT written here.
		{"adopt_by_node_id", "review-pr", "bd-2", "pr:acme/widgets#42:review-pr", "node_id"},
		{"adopt_feedback_digest", "process-feedback", "bd-4", "pr:acme/widgets#42:process-feedback:abc123", "title"},
		{"adopt_fixci_head", "fix-ci", "bd-5", "pr:acme/widgets#42:fix-ci:9f3c1e2", "title"},
		{"adopt_human_child", "review-pr", "bd-6", "pr:acme/widgets#42:review-pr", "title"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := anchorEval(t, "adoption", tc.fixture)
			if len(res.Actions) != 1 {
				t.Fatalf("want exactly one update, got %+v", res)
			}
			a := res.Actions[0]
			if a.Op != action.OpUpdate || a.Kind != tc.kind || a.Target == nil || *a.Target != tc.id {
				t.Fatalf("action = %+v", a)
			}
			if !reflect.DeepEqual(a.Fields, action.Fields{Metadata: map[string]string{"dedup_key": tc.key}}) {
				t.Fatalf("an adoption writes the dedup_key and nothing else, fields = %+v", a.Fields)
			}
			if a.Facts["matched_by"] != tc.by || a.Facts["dedup_key"] != tc.key {
				t.Fatalf("facts = %v", a.Facts)
			}
		})
	}
}

func TestAnchorAdoptionNeverRelabelsAParkedChild(t *testing.T) {
	res := anchorEval(t, "adoption", "adopt_human_child")
	f := res.Actions[0].Fields
	if len(f.Labels)+len(f.AddLabels)+len(f.RemoveLabels) != 0 {
		t.Fatalf("a human-labeled child is adopted but never re-labeled: %+v", f)
	}
}

func TestAnchorAdoptionSeveralItemsKeepLinkOrder(t *testing.T) {
	res := anchorEval(t, "adoption", "adopt_several")
	want := []string{
		"update anchor bd-1",
		"update review-pr bd-2",
		"update process-feedback bd-4",
	}
	if got := anchorSummary(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	if got := res.Actions[2].Fields.Metadata["dedup_key"]; got != "pr:acme/widgets#42:process-feedback:zz9" {
		t.Fatalf("feedback key = %q", got)
	}
}

func TestAnchorAdoptionSkipsAnItemWhoseContextIsUnknown(t *testing.T) {
	// A fix-ci item without a head_sha has no head-scoped key to write.
	res := anchorEval(t, "adoption", "adopt_fixci_no_head")
	if len(res.Actions) != 0 || anchorSkipReason(res) != action.ReasonNotMatched {
		t.Fatalf("got %+v", res)
	}
}

// TestAnchorAdoptionIdempotent evaluates the state AFTER adoption and the
// views with nothing to adopt.
func TestAnchorAdoptionIdempotent(t *testing.T) {
	cases := []struct{ fixture, skip string }{
		{"adopt_review_by_title_after", action.ReasonNotMatched},
		{"adopt_human_child_after", action.ReasonNotMatched},
		{"adopt_already_keyed", action.ReasonNotMatched},
		{"adopt_other_entity", action.ReasonNotMatched},
		{"adopt_no_items", action.ReasonNotMatched},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := anchorEval(t, "adoption", tc.fixture)
			if len(res.Actions) != 0 || anchorSkipReason(res) != tc.skip {
				t.Fatalf("got %+v, want no action, skip %q", res, tc.skip)
			}
		})
	}
}
