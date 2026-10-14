package changes

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestASummarysFirstSightingAppendsNothingAndCreatesNoPendingRow(t *testing.T) {
	r := newRig(t)
	s := sample()
	r.seed(s)
	ctx := context.Background()
	// A nil prev, then a First flag over a non-nil prev: both are the baseline.
	r.must(r.inTx(func(tx *store.Tx) error {
		if err := r.e.DetectSummary(ctx, tx, nil, summary.Stored{Summary: s, First: true}, summary.FetchInfo{}); err != nil {
			return err
		}
		prev := s
		next := s
		next.HeadRefOid = "h2"
		return r.e.DetectSummary(ctx, tx, &prev, summary.Stored{Summary: next, First: true}, summary.FetchInfo{})
	}))
	wantNoChange(t, r)
	if v := r.version(); v != 5 {
		t.Errorf("version = %d, want 5 (no bump)", v)
	}
}

func TestARollupChangeAppendsNoRowUntilRunsIsRefreshedThenOneRowCarriesBoth(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.clk.Advance(time.Minute)
	detected := r.clk.Now()

	r.must(r.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("a row was appended before runs was refreshed: %+v", rows)
	}
	p, ok := r.pending()
	if !ok {
		t.Fatal("no pending row")
	}
	if p.Kind != LogKindPR || p.ID != node || p.Origin != "refresh" || !p.DetectedAt.Equal(detected) ||
		!p.Deadline.Equal(detected.Add(30*time.Second)) || p.HeadSHA != "h1" || p.BaseSHA != "b1" {
		t.Errorf("pending = %+v", p)
	}
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Runs}) {
		t.Errorf("awaited = %v, want [runs]", got)
	}
	if v := r.version(); v != 5 {
		t.Errorf("version moved to %d before the row", v)
	}

	r.clk.Advance(time.Second)
	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	wantKinds(t, row.Kinds, classify.KindCIChanged)
	if !slices.Equal(fieldNames(row), []string{"checks_state", "run:1"}) {
		t.Errorf("fields = %v, want checks_state and run:1", fieldNames(row))
	}
	if row.Kind != LogKindPR || row.ID != ref || row.Version != 6 || row.HeadSHA != "h1" || row.Origin != "refresh" ||
		!row.At.Equal(r.clk.Now()) || !row.OriginUpdatedAt.Equal(time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("row = %+v", row)
	}
	if v := r.version(); v != 6 {
		t.Errorf("version = %d, want 6 (moved once)", v)
	}
	if _, ok := r.pending(); ok {
		t.Error("the pending row survived the append")
	}
	if !slices.Equal(r.obs.settled, []string{ResultSettled}) {
		t.Errorf("settled = %v, want [settled]", r.obs.settled)
	}
	want := []detectedCall{{classify.KindCIChanged, "refresh", row.At.Sub(row.OriginUpdatedAt)}}
	if !slices.Equal(r.obs.detected, want) {
		t.Errorf("detected = %+v, want %+v", r.obs.detected, want)
	}
}

func TestNewlyAwaitedGroupsWithStoredContentGoStaleInTheSameTransaction(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.seedGroup(groups.Detail, detailContent(1), "h1", "b1")
	r.clk.Advance(time.Minute)
	r.must(r.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))

	for group, wantFresh := range map[string]time.Time{groups.Runs: r.clk.Now(), groups.Detail: t0.Add(time.Hour)} {
		var row store.GroupRow
		r.must(r.inTx(func(tx *store.Tx) error {
			var err error
			row, _, err = tx.GetGroup(context.Background(), LogKindPR, node, group)
			return err
		}))
		if !row.FreshUntil.Equal(wantFresh) {
			t.Errorf("%s fresh_until = %v, want %v", group, row.FreshUntil, wantFresh)
		}
		if row.Generation != 1 || len(row.Content) == 0 {
			t.Errorf("%s generation/content changed: %+v", group, row)
		}
	}
}

func TestAHeadChangeWaitsForFilesCommitsAndRuns(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Files, filesContent("a"), "h1", "b1")
	r.seedGroup(groups.Commits, commitsContent("c1"), "h1", "b1")
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.clk.Advance(time.Minute)
	r.must(r.changeSummary(func(s *summary.Summary) { s.HeadRefOid = "h2" }, nil))
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Files, groups.Commits, groups.Runs}) {
		t.Fatalf("awaited = %v, want files commits runs", got)
	}

	r.clk.Advance(time.Second)
	r.must(r.refresh(groups.Files, filesContent("b"), "h2", "b1", r.clk.Now()))
	r.must(r.refresh(groups.Commits, commitsContent("c2"), "h2", "b1", r.clk.Now()))
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("appended before runs: %+v", rows)
	}
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Runs}) {
		t.Fatalf("awaited = %v, want [runs]", got)
	}
	r.must(r.refresh(groups.Runs, runsContent("failure"), "h2", "b1", r.clk.Now()))

	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	wantKinds(t, rows[0].Kinds, classify.KindHeadChanged, classify.KindCIChanged)
	if !slices.Equal(fieldNames(rows[0]), []string{"commits", "files", "head_sha", "run:1"}) {
		t.Errorf("fields = %v", fieldNames(rows[0]))
	}
	if rows[0].HeadSHA != "h2" || rows[0].Version != 6 || r.version() != 6 {
		t.Errorf("row head %q version %d entity version %d, want h2 6 6", rows[0].HeadSHA, rows[0].Version, r.version())
	}
}

func TestABaseBranchChangeAndAMergeabilityChangeWaitForDetail(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		r := newRig(t)
		r.seed(sample())
		r.must(r.changeSummary(func(s *summary.Summary) { s.BaseRefName = "release" }, nil))
		if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Detail, groups.Files, groups.Commits}) {
			t.Errorf("awaited = %v, want detail files commits", got)
		}
	})
	t.Run("mergeable", func(t *testing.T) {
		r := newRig(t)
		r.seed(sample())
		r.seedGroup(groups.Detail, detailContent(1), "h1", "b1")
		r.must(r.changeSummary(func(s *summary.Summary) { s.Mergeable = "CONFLICTING" }, func(st *summary.Stored) {
			st.MergeabilityChanged, st.PrevMergeable = true, "MERGEABLE"
		}))
		if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Detail}) {
			t.Fatalf("awaited = %v, want [detail]", got)
		}
		r.clk.Advance(time.Second)
		r.must(r.refresh(groups.Detail, detailContent(2), "", "", r.clk.Now()))
		rows := r.changes()
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		wantKinds(t, rows[0].Kinds, classify.KindMergeabilityChanged)
		if !slices.Equal(fieldNames(rows[0]), []string{"additions", "mergeable"}) {
			t.Errorf("fields = %v", fieldNames(rows[0]))
		}
	})
}

func TestASummaryChangeThatQueuesNothingAppendsAtOnce(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.clk.Advance(time.Minute)
	r.must(r.changeSummary(func(s *summary.Summary) { s.Title = "Another title" }, nil))
	rows := r.changes()
	if len(rows) != 1 || r.version() != 6 {
		t.Fatalf("rows = %d, version = %d; want 1 and 6", len(rows), r.version())
	}
	if !slices.Equal(fieldNames(rows[0]), []string{"title"}) || len(rows[0].Kinds) != 0 || rows[0].Origin != "refresh" {
		t.Errorf("row = %+v fields %v", rows[0], fieldNames(rows[0]))
	}
	if _, ok := r.pending(); ok {
		t.Error("a pending row was created")
	}
	if len(r.obs.detected) != 0 {
		t.Errorf("Detected fired for a row with no kind: %+v", r.obs.detected)
	}
}

func TestASummaryChangeWithAnEmptyResultThatQueuesDependentsStillCreatesThePendingRow(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Conversation, conversationContent(), "", "")
	r.must(r.changeSummary(func(s *summary.Summary) { s.UpdatedAt = "2026-01-02T05:00:00Z" }, nil))
	p, ok := r.pending()
	if !ok || len(p.Kinds) != 0 {
		t.Fatalf("pending = %+v, ok %v; want a pending row with no kinds", p, ok)
	}
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Conversation}) {
		t.Fatalf("awaited = %v, want [conversation]", got)
	}
	// The dependent refreshes and changes nothing: the merged result is empty.
	r.clk.Advance(time.Second)
	r.must(r.refresh(groups.Conversation, conversationContent(), "", "", r.clk.Now()))
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
	if r.version() != 5 {
		t.Errorf("version = %d, want 5 (an empty merged result bumps nothing)", r.version())
	}
	if _, ok := r.pending(); ok {
		t.Error("the pending row survived")
	}
	if !slices.Equal(r.obs.settled, []string{ResultSettled}) {
		t.Errorf("settled = %v", r.obs.settled)
	}
}

func TestASecondSummaryChangeMergesIntoThePendingRow(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Files, filesContent("a"), "h1", "b1")
	r.seedGroup(groups.Commits, commitsContent("c1"), "h1", "b1")
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.clk.Advance(time.Minute)
	r.must(r.changeSummary(func(s *summary.Summary) { s.HeadRefOid = "h2"; s.Title = "B" }, nil))
	first, _ := r.pending()
	r.clk.Advance(time.Second)
	r.must(r.refresh(groups.Files, filesContent("b"), "h2", "b1", r.clk.Now()))
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groups.Commits, groups.Runs}) {
		t.Fatalf("awaited = %v", got)
	}

	r.clk.Advance(5 * time.Second)
	secondAt := r.clk.Now()
	r.must(r.changeSummary(func(s *summary.Summary) { s.HeadRefOid = "h3"; s.Title = "A title" }, nil))
	p, ok := r.pending()
	if !ok {
		t.Fatal("pending row gone")
	}
	if !p.Deadline.Equal(first.Deadline) || !p.DetectedAt.Equal(first.DetectedAt) {
		t.Errorf("deadline moved: %v -> %v", first.Deadline, p.Deadline)
	}
	if p.HeadSHA != "h3" {
		t.Errorf("pending head = %q, want h3", p.HeadSHA)
	}
	aw, err := DecodeAwaited(p.Awaited)
	if err != nil {
		t.Fatal(err)
	}
	if len(aw) != 3 || aw[0].Group != groups.Files || !aw[0].Since.Equal(secondAt) {
		t.Errorf("awaited = %+v, want files (renewed at the second change), commits, runs", aw)
	}
	fs, _ := classify.DecodeFields(p.Fields)
	var names []string
	for _, f := range fs {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"files", "head_sha"}) { // title changed and changed back: dropped
		t.Errorf("merged fields = %v, want files and head_sha", names)
	}
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("appended early: %+v", rows)
	}

	r.clk.Advance(time.Second)
	for _, g := range []struct{ name, content string }{
		{groups.Files, filesContent("c")}, {groups.Commits, commitsContent("c3")}, {groups.Runs, runsContent("failure")},
	} {
		r.must(r.refresh(g.name, g.content, "h3", "b1", r.clk.Now()))
	}
	rows := r.changes()
	if len(rows) != 1 || rows[0].Version != 6 || r.version() != 6 {
		t.Fatalf("rows = %+v, entity version %d; want one row at version 6", rows, r.version())
	}
	head := fieldOf(t, rows[0], "head_sha")
	if string(head.Before) != `"h1"` || string(head.After) != `"h3"` {
		t.Errorf("head_sha = %s -> %s, want h1 -> h3", head.Before, head.After)
	}
	// files: the earliest before (path a) and the latest after (path c) survive.
	files := fieldOf(t, rows[0], "files")
	wantBefore, wantAfter := filesFieldValue(t, "a"), filesFieldValue(t, "c")
	if string(files.Before) != wantBefore || string(files.After) != wantAfter {
		t.Errorf("files = %s -> %s, want %s -> %s", files.Before, files.After, wantBefore, wantAfter)
	}
}

func TestAnAppendedRowReportsItsOriginFromAHintThenFromThePurpose(t *testing.T) {
	cases := []struct {
		name    string
		hint    string
		purpose governor.Purpose
		want    string
	}{
		{"hint wins over purpose", "write", governor.PurposeInteractive, "write"},
		{"interactive is read", "", governor.PurposeInteractive, "read"},
		{"background is refresh", "", governor.PurposeFill, "refresh"},
		{"settle is refresh", "", governor.PurposeSettle, "refresh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.seed(sample())
			r.purpose = c.purpose
			if c.hint != "" {
				r.e.NoteOrigin(node, groups.Summary, c.hint)
			}
			r.must(r.changeSummary(func(s *summary.Summary) { s.Title = "Another title" }, nil))
			rows := r.changes()
			if len(rows) != 1 || rows[0].Origin != c.want {
				t.Fatalf("rows = %+v, want origin %s", rows, c.want)
			}
		})
	}
}

func TestAMergedPendingRowKeepsTheOriginOfTheChangeThatCreatedIt(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.purpose = governor.PurposeInteractive
	r.must(r.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))
	r.purpose = governor.PurposeRefresh
	r.e.NoteOrigin(node, groups.Runs, "write")
	r.clk.Advance(time.Second)
	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 || rows[0].Origin != "read" {
		t.Fatalf("rows = %+v, want the creator's origin read", rows)
	}
	// The unused hint for runs was consumed by the refresh, not left behind.
	if got := r.e.originFor(node, groups.Runs, governor.PurposeRefresh); got != "refresh" {
		t.Errorf("a hint outlived its detection: %q", got)
	}
}

func TestAnUnparseableUpdatedAtLeavesTheOriginTimeUnknownAndSkipsDetected(t *testing.T) {
	r := newRig(t)
	s := sample()
	s.UpdatedAt = "not a time"
	r.seed(s)
	r.must(r.changeSummary(func(s *summary.Summary) { s.IsDraft = true }, nil))
	rows := r.changes()
	if len(rows) != 1 || !rows[0].OriginUpdatedAt.IsZero() {
		t.Fatalf("rows = %+v, want one with no origin time", rows)
	}
	if len(r.obs.detected) != 0 {
		t.Errorf("Detected fired without an origin time: %+v", r.obs.detected)
	}
}

// filesFieldValue is the bounded value the classifier records for a files list
// holding one file of the given path.
func filesFieldValue(t *testing.T, path string) string {
	t.Helper()
	res, err := classify.Group(groups.Files, []byte(filesContent("zz")), []byte(filesContent(path)), 64)
	if err != nil {
		t.Fatal(err)
	}
	return string(res.Fields[0].After)
}
