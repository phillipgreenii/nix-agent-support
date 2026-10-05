package rules

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

// ---- anchor.priority --------------------------------------------------

func TestAnchorPriorityActions(t *testing.T) {
	cases := []struct {
		fixture  string
		add, rem []string
		priority string
	}{
		// mine/co-owned raise toward P0 and stash the baseline.
		{"prio_mine_conflict", []string{"pbase:2"}, nil, "P1"},
		{"prio_coowned_conflict", []string{"pbase:3"}, nil, "P2"},
		// team lowers toward P4.
		{"prio_team_conflict", []string{"pbase:2"}, nil, "P3"},
		// Already at the floor: the baseline is stashed, the priority is not touched.
		{"prio_mine_conflict_at_floor", []string{"pbase:0"}, nil, ""},
		// The view does not report an anchor priority: the tracker default (P2) seeds the baseline.
		{"prio_mine_conflict_default_priority", []string{"pbase:2"}, nil, "P1"},
		// Either mergeability signal counts as a conflict.
		{"prio_dirty_merge_state", []string{"pbase:2"}, nil, "P1"},
		// A priority carried in the link metadata is read when the link has no priority member.
		{"prio_metadata_priority", []string{"pbase:4"}, nil, "P3"},
		// Conflict cleared: baseline restored, marker dropped.
		{"prio_cleared_restore", nil, []string{"pbase:2"}, "P2"},
		{"prio_cleared_marker_only", nil, []string{"pbase:0"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := anchorEval(t, "anchor.priority", tc.fixture)
			if len(res.Actions) != 1 {
				t.Fatalf("want exactly one update, got %+v", res)
			}
			a := res.Actions[0]
			if a.Op != action.OpUpdate || a.Kind != "anchor" || a.Target == nil || *a.Target != "bd-1" {
				t.Fatalf("action = %+v", a)
			}
			f := a.Fields
			if !reflect.DeepEqual(f.AddLabels, tc.add) || !reflect.DeepEqual(f.RemoveLabels, tc.rem) || f.Priority != tc.priority {
				t.Fatalf("fields = %+v, want add=%v remove=%v priority=%q", f, tc.add, tc.rem, tc.priority)
			}
			if len(f.Metadata) != 0 || f.Title != "" || len(f.Labels) != 0 {
				t.Fatalf("a nudge writes labels and priority only: %+v", f)
			}
		})
	}
}

// TestAnchorPriorityIdempotent evaluates the state AFTER each action above
// was applied (hand-written fixtures): a repeated conflicting tick and a
// completed restore both yield nothing.
func TestAnchorPriorityIdempotent(t *testing.T) {
	cases := []struct{ fixture, skip string }{
		{"prio_mine_conflict_after", action.ReasonAlreadyHandled},
		{"prio_coowned_conflict_after", action.ReasonAlreadyHandled},
		{"prio_team_conflict_after", action.ReasonAlreadyHandled},
		{"prio_mine_conflict_at_floor_after", action.ReasonAlreadyHandled},
		{"prio_cleared_restore_after", action.ReasonNotMatched},
		{"prio_no_conflict_no_marker", action.ReasonNotMatched},
		{"prio_no_anchor", action.ReasonNotMatched},
		{"prio_unknown_relationship", action.ReasonNotMatched},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := anchorEval(t, "anchor.priority", tc.fixture)
			if len(res.Actions) != 0 || anchorSkipReason(res) != tc.skip {
				t.Fatalf("got %+v, want no action, skip %q", res, tc.skip)
			}
		})
	}
}

func TestAnchorPriorityNudgeAndRestoreAreInverse(t *testing.T) {
	// Raise from the conflict, then restore from the "after" view with the
	// conflict cleared: the original priority comes back.
	up := anchorEval(t, "anchor.priority", "prio_mine_conflict").Actions[0].Fields
	v := anchorLoadView(t, "prio_mine_conflict_after")
	v.Snapshot.Mergeable = "MERGEABLE"
	res := anchorRule(t, "anchor.priority").Evaluate(anchorInput(v))
	if len(res.Actions) != 1 {
		t.Fatalf("got %+v", res)
	}
	down := res.Actions[0].Fields
	if up.Priority != "P1" || down.Priority != "P2" ||
		!reflect.DeepEqual(up.AddLabels, []string{"pbase:2"}) || !reflect.DeepEqual(down.RemoveLabels, []string{"pbase:2"}) {
		t.Fatalf("up=%+v down=%+v", up, down)
	}
}
