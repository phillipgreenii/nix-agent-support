package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestUpsertPRInsertsThenUpdates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	pr := PullRequest{
		Repo: "owner/repo", Number: 42, Ownership: "mine",
		Author: "phillipg", State: "open", HeadSHA: "abc123",
	}
	id, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR insert: %v", err)
	}
	if id == 0 {
		t.Fatal("UpsertPR returned id 0")
	}

	pr.HeadSHA = "def456"
	id2, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR update: %v", err)
	}
	if id2 != id {
		t.Fatalf("upsert created a new row: id=%d id2=%d", id, id2)
	}

	got, err := db.GetPR(ctx, "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got == nil || got.HeadSHA != "def456" {
		t.Fatalf("GetPR = %+v, want head_sha def456", got)
	}
}

func TestListOpenPRs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 2, Ownership: "team", State: "closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/other", Number: 3, Ownership: "mine", State: "open"}); err != nil {
		t.Fatal(err)
	}
	// A draft PR must also be returned: ListOpenPRs selects state IN ('open','draft').
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 4, Ownership: "mine", State: "draft"}); err != nil {
		t.Fatal(err)
	}
	// A merged PR must be absent, like closed.
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 5, Ownership: "team", State: "merged"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListOpenPRs(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	gotNums := map[int]bool{}
	for _, pr := range got {
		gotNums[pr.Number] = true
	}
	if !gotNums[1] {
		t.Errorf("open o/r#1 missing from %+v", got)
	}
	if !gotNums[4] {
		t.Errorf("draft o/r#4 missing from %+v", got)
	}
	if gotNums[2] {
		t.Errorf("closed o/r#2 should be absent, got %+v", got)
	}
	if gotNums[5] {
		t.Errorf("merged o/r#5 should be absent, got %+v", got)
	}
	if len(got) != 2 {
		t.Fatalf("want exactly o/r#1 (open) and o/r#4 (draft), got %+v", got)
	}
}

// TestListOpenPRs_UnaffectedByHidden is the regression guard pg2-4dz88.4.3
// exists to enforce: ListOpenPRs is sync's ONLY source for disappeared-
// upstream close-detection (see internal/sync's Sync), so it MUST NOT filter
// on USER_HIDDEN — doing so would make a hidden-but-still-open PR look "gone
// upstream" and trigger a false pr.closed/pr.merged. A hidden PR must appear
// in ListOpenPRs's result set exactly as an unhidden one would, carrying its
// hidden flag + reason for any caller that cares.
func TestListOpenPRs_UnaffectedByHidden(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 2, Ownership: "mine", State: "draft"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetHidden(ctx, "o/r", 1, true, "noisy CI churn"); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}

	got, err := db.ListOpenPRs(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("hiding PR #1 changed ListOpenPRs's result set: got %d rows, want 2: %+v", len(got), got)
	}
	byNum := map[int]PullRequest{}
	for _, pr := range got {
		byNum[pr.Number] = pr
	}
	if !byNum[1].UserHidden || byNum[1].UserHiddenReason != "noisy CI churn" {
		t.Errorf("hidden PR #1 present but flag/reason lost: %+v", byNum[1])
	}
	if byNum[2].UserHidden {
		t.Errorf("unhidden PR #2 should not carry the hidden flag: %+v", byNum[2])
	}
}

func TestGetPRByID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	pr := PullRequest{
		Repo: "owner/repo", Number: 7, Ownership: "mine", State: "open",
	}
	id, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}

	got, err := db.GetPRByID(ctx, id)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetPRByID returned nil, want row")
	}
	if got.Repo != "owner/repo" || got.Number != 7 {
		t.Fatalf("GetPRByID = %+v, want repo=owner/repo number=7", got)
	}

	// Unknown id returns nil, no error.
	missing, err := db.GetPRByID(ctx, 99999)
	if err != nil {
		t.Fatalf("GetPRByID(unknown): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetPRByID(unknown) = %+v, want nil", missing)
	}
}

func TestSetEnrichment_RoundTripAndNoClobber(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	base := PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", Author: "me", State: "open", Branch: "b"}
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}

	enr := Enrichment{
		Kind: "bugfix", Languages: []string{"Go", "Nix"}, Size: "M",
		Urgency: "high", UrgencyScore: 5, UrgencyReasons: []string{"label:p0", "ci-failing"},
	}
	if err := db.SetEnrichment(ctx, "o/r", 5, enr); err != nil {
		t.Fatalf("SetEnrichment: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 5)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.Kind != "bugfix" || got.Size != "M" || got.Urgency != "high" || got.UrgencyScore != 5 {
		t.Fatalf("enrichment not persisted: %+v", got)
	}
	if !reflect.DeepEqual(got.Languages, []string{"Go", "Nix"}) || !reflect.DeepEqual(got.UrgencyReasons, []string{"label:p0", "ci-failing"}) {
		t.Fatalf("json columns not persisted: langs=%v reasons=%v", got.Languages, got.UrgencyReasons)
	}

	// A subsequent plain UpsertPR (as the lifecycle emit / ingest does) MUST
	// NOT clobber the enrichment columns.
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("re-UpsertPR: %v", err)
	}
	got2, err := db.GetPR(ctx, "o/r", 5)
	if err != nil || got2 == nil {
		t.Fatalf("GetPR2: %v %v", got2, err)
	}
	if got2.Kind != "bugfix" || got2.Urgency != "high" || !reflect.DeepEqual(got2.Languages, []string{"Go", "Nix"}) {
		t.Fatalf("UpsertPR clobbered enrichment: %+v", got2)
	}
}
