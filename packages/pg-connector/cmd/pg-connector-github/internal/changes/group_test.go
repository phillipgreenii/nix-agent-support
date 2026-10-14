package changes

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
)

func TestAGroupsFirstFetchAppendsNothingAndCreatesNoPendingRow(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	// The entity's other groups already have content; this group has none.
	r.seedGroup(groups.Files, filesContent("a"), "h1", "b1")
	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", r.clk.Now()))
	wantNoChange(t, r)
	if r.version() != 5 {
		t.Errorf("version = %d, want 5", r.version())
	}
}

func TestAGroupRefreshedByItselfWithNoPendingRowAppendsAtOnce(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.clk.Advance(time.Minute)
	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	wantKinds(t, row.Kinds, classify.KindCIChanged)
	if row.ID != ref || row.Version != 6 || row.HeadSHA != "h1" || row.Origin != "refresh" || !row.At.Equal(r.clk.Now()) {
		t.Errorf("row = %+v", row)
	}
	if _, ok := r.pending(); ok {
		t.Error("a pending row was created")
	}
}

func TestAGroupRefreshWithNoStoredSummaryAppendsNothing(t *testing.T) {
	r := newRig(t)
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", r.clk.Now()))
	wantNoChange(t, r)
}

func TestAFetchThatStartedBeforeTheDetectionDoesNotSatisfyTheBarrierButItsFieldsMerge(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.clk.Advance(time.Minute)
	started := r.clk.Now().Add(-time.Second) // began before the detection
	r.must(r.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))

	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", started))
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("an old fetch ended the barrier: %+v", rows)
	}
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Runs}) {
		t.Fatalf("awaited = %v, want runs still awaited", got)
	}
	p, _ := r.pending()
	fs, _ := classify.DecodeFields(p.Fields)
	if !slices.ContainsFunc(fs, func(f classify.Field) bool { return f.Name == "run:1" }) {
		t.Errorf("the old fetch's fields did not merge: %+v", fs)
	}

	// A fetch that started exactly at the detection counts.
	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 || !slices.Equal(fieldNames(rows[0]), []string{"checks_state", "run:1"}) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAFetchRecordedForAnotherHeadOrBaseDoesNotSatisfyTheBarrier(t *testing.T) {
	for _, c := range []struct{ name, head, base string }{
		{"older head", "h1", "b1"},
		{"other base", "h2", "b0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.seed(sample())
			r.seedGroup(groups.Files, filesContent("a"), "h1", "b1")
			r.seedGroup(groups.Commits, commitsContent("c1"), "h1", "b1")
			r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
			r.clk.Advance(time.Minute)
			r.must(r.changeSummary(func(s *summary.Summary) { s.HeadRefOid = "h2" }, nil))
			r.clk.Advance(time.Second)

			r.must(r.refresh(groups.Files, filesContent("b"), c.head, c.base, r.clk.Now()))
			if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Files, groups.Commits, groups.Runs}) {
				t.Fatalf("awaited = %v, want all three still awaited", got)
			}
			p, _ := r.pending()
			fs, _ := classify.DecodeFields(p.Fields)
			if !slices.ContainsFunc(fs, func(f classify.Field) bool { return f.Name == "files" }) {
				t.Errorf("the mismatched fetch's fields did not merge: %+v", fs)
			}

			r.must(r.refresh(groups.Files, filesContent("c"), "h2", "b1", r.clk.Now()))
			if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Commits, groups.Runs}) {
				t.Errorf("awaited = %v, want commits runs after the matching fetch", got)
			}
		})
	}
}

func TestAnUnstampedGroupIgnoresTheRecordedHeadAndBase(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Conversation, conversationContent(), "", "")
	r.must(r.changeSummary(func(s *summary.Summary) { s.CommentCount = 3 }, nil))
	r.clk.Advance(time.Second)
	r.must(r.refresh(groups.Conversation, conversationContent(), "", "", r.clk.Now()))
	if _, ok := r.pending(); ok {
		t.Fatal("the conversation fetch did not end the barrier")
	}
	if rows := r.changes(); len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAGroupsBaselineFetchStillSatisfiesTheBarrierWhenItCounts(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.must(r.changeSummary(func(s *summary.Summary) { s.Mergeable = "CONFLICTING" }, func(st *summary.Stored) {
		st.MergeabilityChanged, st.PrevMergeable = true, "MERGEABLE"
	}))
	r.clk.Advance(time.Second)
	// Detail has never been fetched: prev is nil, the fetch is its baseline.
	r.must(r.refresh(groups.Detail, detailContent(4), "", "", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !slices.Equal(fieldNames(rows[0]), []string{"mergeable"}) {
		t.Errorf("fields = %v, want only the summary's mergeable (the baseline adds none)", fieldNames(rows[0]))
	}
}

func TestADetectionOfTheSummaryGroupOrOfAnotherKindIsIgnored(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.must(r.refresh(groups.Summary, `{"title":"x"}`, "h1", "b1", r.clk.Now()))
	wantNoChange(t, r)
}

func TestAGroupWhoseContentCannotBeClassifiedFailsTheDetection(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	if err := r.refresh(groups.Runs, `not json`, "h1", "b1", r.clk.Now()); err == nil {
		t.Fatal("undecodable content was accepted")
	}
	// The transaction rolled back: the stored row is the seeded one.
	if rows := r.changes(); len(rows) != 0 {
		t.Errorf("rows = %+v", rows)
	}
}
