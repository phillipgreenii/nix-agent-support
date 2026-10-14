package revalidate

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
)

func baseSummary() summary.Summary {
	return summary.Summary{
		NodeID: "PR_1", Repo: "acme-org/widget", Number: 7, Title: "Add the widget", State: "OPEN",
		UpdatedAt: "2026-10-12T08:00:00Z", Mergeable: "MERGEABLE", CommentCount: 2, ReviewCount: 1, ReviewThreadCount: 1,
		HeadRefOid: "head-1", BaseRefOid: "base-1", BaseRefName: "main", ChecksState: "SUCCESS",
	}
}

// stored builds what Persist would report after prev became cur.
func stored(prev, cur summary.Summary) summary.Stored {
	return summary.Stored{Summary: cur, Fingerprint: cur.Fingerprint(), PrevFingerprint: prev.Fingerprint()}
}

func TestDecideTable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*summary.Summary)
		next   func(*summary.Stored)
		want   Plan
	}{
		{name: "unchanged fingerprint extends detail and conversation", want: Plan{Extend: []string{groups.Detail, groups.Conversation}}},
		{
			name: "head changed", mutate: func(s *summary.Summary) { s.HeadRefOid = "head-2" },
			want: Plan{Queue: []string{groups.Files, groups.Commits, groups.Runs}},
		},
		{
			name: "base oid changed", mutate: func(s *summary.Summary) { s.BaseRefOid = "base-2" },
			want: Plan{Queue: []string{groups.Detail, groups.Files, groups.Commits}},
		},
		{
			name: "base name changed", mutate: func(s *summary.Summary) { s.BaseRefName = "release" },
			want: Plan{Queue: []string{groups.Detail, groups.Files, groups.Commits}},
		},
		{
			name: "comment count changed", mutate: func(s *summary.Summary) { s.CommentCount++ },
			want: Plan{Queue: []string{groups.Conversation}},
		},
		{
			name: "review count changed", mutate: func(s *summary.Summary) { s.ReviewCount++ },
			want: Plan{Queue: []string{groups.Conversation}},
		},
		{
			name: "review thread count changed", mutate: func(s *summary.Summary) { s.ReviewThreadCount++ },
			want: Plan{Queue: []string{groups.Conversation}},
		},
		{
			name: "updatedAt changed", mutate: func(s *summary.Summary) { s.UpdatedAt = "2026-10-12T09:00:00Z" },
			want: Plan{Queue: []string{groups.Conversation}},
		},
		{
			name: "known mergeability change", mutate: func(s *summary.Summary) { s.Mergeable = "CONFLICTING" },
			next: func(st *summary.Stored) { st.MergeabilityChanged, st.PrevMergeable = true, "MERGEABLE" },
			want: Plan{Queue: []string{groups.Detail}},
		},
		{
			name: "UNKNOWN alone changes nothing", mutate: func(s *summary.Summary) { s.Mergeable = "UNKNOWN" },
			want: Plan{},
		},
		{
			name: "checks state changed", mutate: func(s *summary.Summary) { s.ChecksState = "FAILURE" },
			want: Plan{Queue: []string{groups.Runs}},
		},
		{
			name:   "head and base changed together queue each group once",
			mutate: func(s *summary.Summary) { s.HeadRefOid, s.BaseRefOid = "head-2", "base-2" },
			want:   Plan{Queue: []string{groups.Detail, groups.Files, groups.Commits, groups.Runs}},
		},
		{
			name: "every trigger at once",
			mutate: func(s *summary.Summary) {
				s.HeadRefOid, s.BaseRefOid, s.CommentCount, s.ChecksState = "head-2", "base-2", 9, "PENDING"
			},
			next: func(st *summary.Stored) { st.MergeabilityChanged = true },
			want: Plan{Queue: []string{groups.Detail, groups.Conversation, groups.Files, groups.Commits, groups.Runs}},
		},
		{name: "title only changes nothing", mutate: func(s *summary.Summary) { s.Title = "A new title" }, want: Plan{}},
		{name: "label only changes nothing", mutate: func(s *summary.Summary) { s.Labels = []string{"bug"}; s.LabelCount = 1 }, want: Plan{}},
		{
			name:   "terminal PR with an unchanged fingerprint is not extended",
			mutate: func(s *summary.Summary) { s.State = "MERGED" },
			next:   func(st *summary.Stored) { st.PrevFingerprint = st.Fingerprint },
			want:   Plan{},
		},
		{
			name:   "closed PR with an unchanged fingerprint is not extended",
			mutate: func(s *summary.Summary) { s.State = "CLOSED" },
			next:   func(st *summary.Stored) { st.PrevFingerprint = st.Fingerprint },
			want:   Plan{},
		},
		{
			name:   "terminal PR whose head changed still queues its dependents",
			mutate: func(s *summary.Summary) { s.State = "MERGED"; s.HeadRefOid = "head-2" },
			want:   Plan{Queue: []string{groups.Files, groups.Commits, groups.Runs}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prev, cur := baseSummary(), baseSummary()
			if tc.mutate != nil {
				tc.mutate(&cur)
			}
			next := stored(prev, cur)
			if tc.next != nil {
				tc.next(&next)
			}
			got := Decide(&prev, next)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decide = %+v, want %+v", got, tc.want)
			}
			if got.Empty() != tc.want.Empty() {
				t.Errorf("Empty() = %v", got.Empty())
			}
		})
	}
}

func TestDecideFirstSightingOnlyPopulates(t *testing.T) {
	cur := baseSummary()
	got := Decide(nil, summary.Stored{Summary: cur, Fingerprint: cur.Fingerprint(), First: true})
	if want := (Plan{Populate: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("Decide = %+v, want %+v", got, want)
	}
	if want := []string{groups.Detail, groups.Conversation, groups.Runs}; !reflect.DeepEqual(PopulateGroups(), want) {
		t.Errorf("PopulateGroups = %v, want %v", PopulateGroups(), want)
	}
}

func TestDecideWithoutAPreviousSummaryAndNotFirstOnlyComparesFingerprints(t *testing.T) {
	cur := baseSummary()
	// The previous content could not be decoded: nothing to compare, so a changed
	// fingerprint decides nothing and an unchanged one still extends.
	changed := summary.Stored{Summary: cur, Fingerprint: cur.Fingerprint(), PrevFingerprint: "something-else"}
	if got := Decide(nil, changed); !got.Empty() {
		t.Errorf("changed fingerprint, no prev: %+v, want nothing", got)
	}
	same := summary.Stored{Summary: cur, Fingerprint: cur.Fingerprint(), PrevFingerprint: cur.Fingerprint()}
	if got := Decide(nil, same); len(got.Extend) != 2 {
		t.Errorf("same fingerprint, no prev: %+v, want detail and conversation extended", got)
	}
}

func TestDecideAnEmptyPreviousFingerprintNeverCountsAsUnchanged(t *testing.T) {
	cur := baseSummary()
	got := Decide(&cur, summary.Stored{Summary: cur, Fingerprint: cur.Fingerprint()})
	if !got.Empty() {
		t.Errorf("Decide = %+v, want nothing", got)
	}
}
