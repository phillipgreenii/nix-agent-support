package classify

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
)

// richSummary is a fully populated summary, so a mutation of any one field is
// compared against real values on both sides.
func richSummary() summary.Summary {
	return summary.Summary{
		NodeID: "PR_node_1", Repo: "example/widgets", Number: 7, Title: "Add widget",
		URL: "https://example.invalid/example/widgets/pull/7", State: "OPEN", Body: "Adds a widget.",
		UpdatedAt: "2026-01-01T00:00:00Z", ReviewDecision: "REVIEW_REQUIRED", Mergeable: "MERGEABLE",
		Author: "phillipgreenii", LabelCount: 2, Labels: []string{"bug", "docs"},
		CommentCount: 2, ReviewCount: 1, ReviewThreadCount: 1,
		HeadRefOid: "aaa111", HeadRefName: "feature", BaseRefName: "main", BaseRefOid: "bbb222",
		ChecksState: "FAILURE",
	}
}

func TestSummaryKinds(t *testing.T) {
	type mut func(*summary.Summary, *summary.Stored)
	set := func(f func(*summary.Summary)) mut {
		return func(s *summary.Summary, _ *summary.Stored) { f(s) }
	}
	for _, tc := range []struct {
		name   string
		prev   func(*summary.Summary)
		change mut
		kinds  []string
		fields []string
	}{
		// reopened: yields / does not.
		{
			"reopened from merged", func(s *summary.Summary) { s.State = "MERGED" },
			set(func(s *summary.Summary) { s.State = "OPEN" }),
			[]string{KindReopened},
			[]string{"state"},
		},
		{
			"reopened from closed", func(s *summary.Summary) { s.State = "CLOSED" },
			set(func(s *summary.Summary) { s.State = "OPEN" }),
			[]string{KindReopened},
			[]string{"state"},
		},
		{
			"open to open is not reopened", nil,
			set(func(s *summary.Summary) { s.Title = "Add gadget" }), nil,
			[]string{"title"},
		},
		// closed.
		{
			"closed", nil,
			set(func(s *summary.Summary) { s.State = "CLOSED" }),
			[]string{KindClosed},
			[]string{"state"},
		},
		{
			"closed to closed is not closed", func(s *summary.Summary) { s.State = "CLOSED" },
			set(func(s *summary.Summary) { s.Title = "x" }), nil,
			[]string{"title"},
		},
		// merged.
		{
			"merged", nil,
			set(func(s *summary.Summary) { s.State = "MERGED" }),
			[]string{KindMerged},
			[]string{"state"},
		},
		{
			"merged to merged is not merged", func(s *summary.Summary) { s.State = "MERGED" },
			set(func(s *summary.Summary) { s.Title = "x" }), nil,
			[]string{"title"},
		},
		{
			"a state with no value is skipped", func(s *summary.Summary) { s.State = "" },
			set(func(s *summary.Summary) { s.State = "MERGED" }), nil,
			[]string{"state"},
		},
		// draft_changed.
		{
			"draft", nil,
			set(func(s *summary.Summary) { s.IsDraft = true }),
			[]string{KindDraftChanged},
			[]string{"draft"},
		},
		{
			"draft unchanged", nil,
			set(func(s *summary.Summary) { s.Author = "phillipg@ziprecruiter.com" }), nil,
			[]string{"author"},
		},
		// head_changed.
		{
			"head", nil,
			set(func(s *summary.Summary) { s.HeadRefOid = "ccc333" }),
			[]string{KindHeadChanged},
			[]string{"head_sha"},
		},
		{
			"head branch name alone is no kind", nil,
			set(func(s *summary.Summary) { s.HeadRefName = "feature-2" }), nil,
			[]string{"head_ref"},
		},
		{
			"a head with no value is skipped", func(s *summary.Summary) { s.HeadRefOid = "" },
			set(func(s *summary.Summary) { s.HeadRefOid = "ccc333" }), nil,
			[]string{"head_sha"},
		},
		// base_changed.
		{
			"base branch", nil,
			set(func(s *summary.Summary) { s.BaseRefName = "release" }),
			[]string{KindBaseChanged},
			[]string{"base"},
		},
		{
			"base tip alone is not a signal", nil,
			set(func(s *summary.Summary) { s.BaseRefOid = "ddd444" }), nil, nil,
		},
		// ci_changed through the rollup.
		{
			"rollup", nil,
			set(func(s *summary.Summary) { s.ChecksState = "SUCCESS" }),
			[]string{KindCIChanged},
			[]string{"checks_state"},
		},
		{
			"a rollup that appears is recorded but is no kind", func(s *summary.Summary) { s.ChecksState = "" },
			set(func(s *summary.Summary) { s.ChecksState = "PENDING" }), nil,
			[]string{"checks_state"},
		},
		{
			"rollup unchanged", nil,
			set(func(s *summary.Summary) { s.Title = "x" }), nil,
			[]string{"title"},
		},
		// mergeability_changed.
		{
			"mergeability moved between known values", nil,
			func(s *summary.Summary, st *summary.Stored) {
				s.Mergeable = "CONFLICTING"
				st.MergeabilityChanged, st.PrevMergeable = true, "MERGEABLE"
			},
			[]string{KindMergeabilityChanged},
			[]string{"mergeable"},
		},
		{
			"UNKNOWN alone is never a change", nil,
			set(func(s *summary.Summary) { s.Mergeable = "UNKNOWN" }), nil, nil,
		},
		// review_changed.
		{
			"review decision", nil,
			set(func(s *summary.Summary) { s.ReviewDecision = "APPROVED" }),
			[]string{KindReviewChanged},
			[]string{"review_decision"},
		},
		{
			"review count", nil,
			set(func(s *summary.Summary) { s.ReviewCount = 2 }),
			[]string{KindReviewChanged},
			[]string{"review_count"},
		},
		{
			"labels are no review signal", nil,
			set(func(s *summary.Summary) { s.Labels = []string{"bug"}; s.LabelCount = 1 }), nil,
			[]string{"label_count", "labels"},
		},
		// feedback_changed.
		{
			"comment count", nil,
			set(func(s *summary.Summary) { s.CommentCount = 3 }),
			[]string{KindFeedbackChanged},
			[]string{"comment_count"},
		},
		{
			"thread count alone is no kind", nil,
			set(func(s *summary.Summary) { s.ReviewThreadCount = 2 }), nil,
			[]string{"review_thread_count"},
		},
		// renamed.
		{
			"rename", nil,
			set(func(s *summary.Summary) { s.Repo = "example/gadgets" }),
			[]string{KindRenamed},
			[]string{"ref"},
		},
		{
			"renumbered reference", nil,
			set(func(s *summary.Summary) { s.Number = 8 }),
			[]string{KindRenamed},
			[]string{"ref"},
		},
		// everything that moves only excluded fields.
		{
			"updated_at, base tip, url and node id are excluded", nil,
			set(func(s *summary.Summary) {
				s.UpdatedAt, s.BaseRefOid, s.URL, s.NodeID = "2026-02-02T00:00:00Z", "eee555", "https://example.invalid/x", "PR_node_9"
			}), nil, nil,
		},
		// a reordered label answer is not a change.
		{
			"reordered labels", nil,
			set(func(s *summary.Summary) { s.Labels = []string{"docs", "bug"} }), nil, nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev, next := richSummary(), richSummary()
			if tc.prev != nil {
				tc.prev(&prev)
				tc.prev(&next)
			}
			st := summary.Stored{}
			tc.change(&next, &st)
			st.Summary = next
			wantResult(t, Summary(&prev, st, 100), tc.kinds, tc.fields)
		})
	}
}

func TestSummaryRenameRecordsOldAndNewReference(t *testing.T) {
	prev, next := richSummary(), richSummary()
	next.Repo = "example/gadgets"
	// Detected from the references, not from Stored.Rename, which a retry after
	// a failed transaction would no longer carry.
	res := Summary(&prev, summary.Stored{Summary: next}, 100)
	wantResult(t, res, []string{KindRenamed}, []string{"ref"})
	if string(res.Fields[0].Before) != `"example/widgets#7"` || string(res.Fields[0].After) != `"example/gadgets#7"` {
		t.Fatalf("ref = %s -> %s", res.Fields[0].Before, res.Fields[0].After)
	}
}

func TestSummaryFirstSightingIsABaseline(t *testing.T) {
	rich := richSummary()
	other := richSummary()
	other.State, other.Title, other.HeadRefOid, other.Repo = "MERGED", "other", "zzz", "example/else"
	if res := Summary(nil, summary.Stored{Summary: other}, 100); !res.Empty() {
		t.Errorf("nil prev: %+v", res)
	}
	if res := Summary(&rich, summary.Stored{Summary: other, First: true}, 100); !res.Empty() {
		t.Errorf("First: %+v", res)
	}
	gone := summary.Summary{NodeID: "PR_node_1", Gone: true}
	if res := Summary(&rich, summary.Stored{Summary: gone}, 100); !res.Empty() {
		t.Errorf("next Gone: %+v", res)
	}
	if res := Summary(&gone, summary.Stored{Summary: rich}, 100); !res.Empty() {
		t.Errorf("prev Gone: %+v", res)
	}
}

// The mergeability rule seen through the carried value: MERGEABLE, then UNKNOWN,
// then CONFLICTING is one change, and UNKNOWN alone is none.
func TestMergeabilitySequenceThroughTheCarriedValue(t *testing.T) {
	stored := richSummary() // carried MERGEABLE, last known MERGEABLE
	known := stored.Mergeable
	var total Result
	for _, step := range []struct {
		observed string
		wantKind bool
	}{{"UNKNOWN", false}, {"CONFLICTING", true}, {"UNKNOWN", false}} {
		carried, changed := entity.Carry(known, step.observed, false, false)
		next := stored
		next.Mergeable = carried
		st := summary.Stored{Summary: next, MergeabilityChanged: changed}
		if changed {
			st.PrevMergeable = known
		}
		res := Summary(&stored, st, 100)
		if got := len(res.Kinds) == 1 && res.Kinds[0] == KindMergeabilityChanged; got != step.wantKind {
			t.Fatalf("observed %s: kinds %v, want change=%v", step.observed, res.Kinds, step.wantKind)
		}
		if !step.wantKind && !res.Empty() {
			t.Fatalf("observed %s: UNKNOWN produced %+v", step.observed, res)
		}
		total = Merge(total, res)
		stored = next
		if entity.Known(carried) {
			known = carried
		}
	}
	wantResult(t, total, []string{KindMergeabilityChanged}, []string{"mergeable"})
	if string(total.Fields[0].Before) != `"MERGEABLE"` || string(total.Fields[0].After) != `"CONFLICTING"` {
		t.Fatalf("mergeable = %s -> %s", total.Fields[0].Before, total.Fields[0].After)
	}
}

func TestSummaryBodyIsCappedWithAHashOfTheFullText(t *testing.T) {
	prev, next := richSummary(), richSummary()
	prev.Body = strings.Repeat("a", 11) // bodyCap+1: cut
	next.Body = strings.Repeat("b", 10) // bodyCap: not cut
	res := Summary(&prev, summary.Stored{Summary: next}, 10)
	wantResult(t, res, nil, []string{"body"})
	if got, want := string(res.Fields[0].Before), `{"body":"`+strings.Repeat("a", 10)+`","hash":"`+textHash(prev.Body)+`"}`; got != want {
		t.Errorf("before = %s, want %s", got, want)
	}
	if got, want := string(res.Fields[0].After), `{"body":"`+strings.Repeat("b", 10)+`","hash":"`+textHash(next.Body)+`"}`; got != want {
		t.Errorf("after = %s, want %s", got, want)
	}
	// A change past the cap is still a change.
	prev.Body, next.Body = strings.Repeat("c", 10)+"1", strings.Repeat("c", 10)+"2"
	wantResult(t, Summary(&prev, summary.Stored{Summary: next}, 10), nil, []string{"body"})
}
