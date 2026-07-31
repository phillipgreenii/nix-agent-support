package reviewsink

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func seedMinePR(t *testing.T, db *store.DB) int64 {
	t.Helper()
	id, err := db.UpsertPR(context.Background(), store.PullRequest{
		Repo: "o/r", Number: 7, Ownership: "mine", State: "open", HeadSHA: "h1",
	})
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	return id
}

// countPendingFeedbackEvents drains the outbox and counts feedback.created
// events. (RunOutbox marks rows complete, so call it once per assertion point.)
func countPendingFeedbackEvents(t *testing.T, db *store.DB) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	if err := db.RunOutbox(ctx, func(_ context.Context, e store.Event) error {
		if e.Type == store.EventFeedbackCreated {
			n++
		}
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	return n
}

func TestIngestSelfReview_InlineFindings(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID := seedMinePR(t, db)

	draft := &reviewstage.Draft{
		Repo: "o/r", PR: 7,
		Comments: []api.Comment{
			{Path: "a.go", Line: 3, Body: "avoid the naked return"},
			{Path: "b.go", Line: 9, Body: "handle the error"},
		},
	}
	result := &reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "mine", HeadSHA: "h1", BeadID: "dr-1"}

	n, err := IngestSelfReview(ctx, db, "o/r", 7, draft, result)
	if err != nil {
		t.Fatalf("IngestSelfReview: %v", err)
	}
	if n != 2 {
		t.Fatalf("ingested count = %d, want 2", n)
	}

	items, err := db.ListFeedback(ctx, prID, store.ListFilter{Kind: "self-review"})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("self-review rows = %d, want 2", len(items))
	}
	for _, f := range items {
		if !f.IsOurs {
			t.Errorf("self-review row must be is_ours, got %+v", f)
		}
		if f.AuthorKind != "agent" {
			t.Errorf("self-review row must be author_kind=agent, got %q", f.AuthorKind)
		}
		if f.SubjectSHA != "h1" || f.FirstSeenHeadSHA != "h1" {
			t.Errorf("subject/first-seen head must be h1, got subj=%q first=%q", f.SubjectSHA, f.FirstSeenHeadSHA)
		}
		if f.Status != "new" {
			t.Errorf("new finding must have status=new, got %q", f.Status)
		}
	}

	// ONE feedback.created event for the PR, not one per finding (pg2-onq1e): the
	// payload was already identical per finding, and it now carries the summary of
	// everything unaddressed — which is a per-PR fact, computed once after the
	// findings are committed.
	if got := countPendingFeedbackEvents(t, db); got != 1 {
		t.Fatalf("feedback.created events = %d, want 1 (one per PR, not one per finding)", got)
	}
}

// TestIngestSelfReview_EventCarriesSummary pins the substance the projected
// process-feedback bead's description is built from: the self-review findings are
// "ours" by construction, so they MUST still be counted as needing processing.
func TestIngestSelfReview_EventCarriesSummary(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	seedMinePR(t, db)

	draft := &reviewstage.Draft{
		Repo: "o/r", PR: 7,
		Comments: []api.Comment{
			{Path: "a.go", Line: 3, Body: "avoid the naked return"},
			{Path: "b.go", Line: 9, Body: "handle the error"},
		},
	}
	result := &reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "mine", HeadSHA: "h1", BeadID: "dr-1"}
	if _, err := IngestSelfReview(ctx, db, "o/r", 7, draft, result); err != nil {
		t.Fatalf("IngestSelfReview: %v", err)
	}

	var payloads []store.FeedbackPayload
	if err := db.RunOutbox(ctx, func(_ context.Context, e store.Event) error {
		if e.Type != store.EventFeedbackCreated {
			return nil
		}
		var p store.FeedbackPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		payloads = append(payloads, p)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("feedback.created events = %d, want 1", len(payloads))
	}
	p := payloads[0]
	if p.Summary == nil {
		t.Fatal("payload summary is nil — the bead description would have no substance")
	}
	if p.Summary.Unaddressed != 2 || p.Summary.ByKind["self-review"] != 2 {
		t.Fatalf("self-review findings must count as unaddressed: %+v", p.Summary)
	}
	if !p.Mine {
		t.Error("my-PR sink must mark the payload mine")
	}
}

func TestIngestSelfReview_PRLevelBody_Fileless(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID := seedMinePR(t, db)

	draft := &reviewstage.Draft{Repo: "o/r", PR: 7, Body: "overall: split this PR"}
	result := &reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "mine", HeadSHA: "h1", BeadID: "dr-1"}

	n, err := IngestSelfReview(ctx, db, "o/r", 7, draft, result)
	if err != nil {
		t.Fatalf("IngestSelfReview: %v", err)
	}
	if n != 1 {
		t.Fatalf("ingested count = %d, want 1 (PR-level body)", n)
	}
	items, _ := db.ListFeedback(ctx, prID, store.ListFilter{Kind: "self-review"})
	if len(items) != 1 {
		t.Fatalf("rows = %d, want 1", len(items))
	}
	if items[0].File != "" || items[0].Line != 0 {
		t.Fatalf("PR-level finding must be fileless, got file=%q line=%d", items[0].File, items[0].Line)
	}
	if items[0].Body != "overall: split this PR" {
		t.Fatalf("body = %q", items[0].Body)
	}
}

func TestIngestSelfReview_Idempotent_SameHead(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID := seedMinePR(t, db)

	draft := &reviewstage.Draft{
		Repo: "o/r", PR: 7, Body: "top-level note",
		Comments: []api.Comment{{Path: "a.go", Line: 3, Body: "fix"}},
	}
	result := &reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "mine", HeadSHA: "h1", BeadID: "dr-1"}

	n1, err := IngestSelfReview(ctx, db, "o/r", 7, draft, result)
	if err != nil || n1 != 2 {
		t.Fatalf("first ingest: n=%d err=%v", n1, err)
	}
	// Re-run the exact same draft+result: no new rows.
	n2, err := IngestSelfReview(ctx, db, "o/r", 7, draft, result)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("re-ingest at same head must add 0 rows, got %d", n2)
	}
	items, _ := db.ListFeedback(ctx, prID, store.ListFilter{Kind: "self-review"})
	if len(items) != 2 {
		t.Fatalf("total rows after re-ingest = %d, want 2 (deduped)", len(items))
	}
}

func TestIngestSelfReview_HeadAdvance_IsNewFinding(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID := seedMinePR(t, db)

	draft := &reviewstage.Draft{Repo: "o/r", PR: 7, Comments: []api.Comment{{Path: "a.go", Line: 3, Body: "fix"}}}

	if _, err := IngestSelfReview(ctx, db, "o/r", 7, draft, &reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "mine", HeadSHA: "h1"}); err != nil {
		t.Fatalf("ingest h1: %v", err)
	}
	// Same finding text, new head SHA → a distinct finding row.
	n, err := IngestSelfReview(ctx, db, "o/r", 7, draft, &reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "mine", HeadSHA: "h2"})
	if err != nil {
		t.Fatalf("ingest h2: %v", err)
	}
	if n != 1 {
		t.Fatalf("new-head ingest must add 1 row, got %d", n)
	}
	items, _ := db.ListFeedback(ctx, prID, store.ListFilter{Kind: "self-review"})
	if len(items) != 2 {
		t.Fatalf("rows after head advance = %d, want 2", len(items))
	}
}

// Empty draft (no body, no comments) is a legal no-op — a "review not yet
// produced" or a clean review.
func TestIngestSelfReview_EmptyDraft_NoOp(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID := seedMinePR(t, db)

	n, err := IngestSelfReview(ctx, db, "o/r", 7, &reviewstage.Draft{Repo: "o/r", PR: 7}, &reviewstage.Result{Repo: "o/r", PR: 7, HeadSHA: "h1"})
	if err != nil {
		t.Fatalf("IngestSelfReview: %v", err)
	}
	if n != 0 {
		t.Fatalf("empty draft must ingest 0, got %d", n)
	}
	items, _ := db.ListFeedback(ctx, prID, store.ListFilter{Kind: "self-review"})
	if len(items) != 0 {
		t.Fatalf("empty draft must create no rows, got %d", len(items))
	}
}
