package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// TestIngestFeedbackToStore is an integration-style test for the store-ingest
// path. It constructs an Engine with Deps.Store set, calls ingestFeedbackToStore
// directly with a mixed enriched PR (human top-level comment, bot inline comment,
// pg-pr marker'd comment that must be skipped, one failing CI run), then asserts
// the store rows and outbox entries are correct.
func TestIngestFeedbackToStore(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	// Human top-level (PR) comment.
	humanComment := api.Comment{
		ID:         "c1",
		Author:     "alice",
		AuthorRole: "member",
		Body:       "Please add a unit test here.",
	}
	// Bot inline (code-comment-thread) comment — author ends with "[bot]" so it
	// classifies as agent via the [bot]-suffix fast-path inside commentAuthorRole,
	// but Classify uses typename; we use a known-bot login to make it classify as
	// "agent" via the [bot] suffix check in Classify.
	botComment := api.Comment{
		ID:         "c2",
		Author:     "coderabbit[bot]",
		AuthorRole: "none",
		Body:       "This function is overly complex.",
		Path:       "pkg/foo/foo.go",
		Line:       42,
		ThreadID:   "t-1",
	}
	// pg-pr marker'd comment — must be SKIPPED (IsOurs = true).
	markedComment := api.Comment{
		ID:     "c3",
		Author: "phillipg",
		Body:   marker.Stamp("pg-pr auto-reply"),
	}
	// Failing CI run.
	failRun := api.CIRun{
		ID:         "run-1",
		Name:       "unit-tests",
		Status:     "completed",
		Conclusion: "failure",
		URL:        "https://github.com/o/r/actions/runs/run-1",
		Provider:   "github-actions",
	}

	pr := api.PR{
		Repo:   "o/r",
		Number: 7,
		State:  "open",
		Branch: "feat/x",
		Base:   "main",
		Author: "phillipg",
		URL:    "https://github.com/o/r/pull/7",
	}
	enriched := &vcs.EnrichedPR{
		PR:       pr,
		Comments: []api.Comment{humanComment, botComment, markedComment},
		CIRuns:   []api.CIRun{failRun},
	}

	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "phillipg",
			Repos:     []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
		},
		VCS:      map[string]VCSProvider{"github": newFakeVCS()},
		Beads:    &noopBeads{},
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	// --- Verify store rows ---

	// UpsertPR must have written a row.
	storedPR, err := db.GetPR(ctx, "o/r", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if storedPR == nil {
		t.Fatal("expected PR row, got nil")
	}
	if storedPR.Ownership != "mine" {
		t.Errorf("ownership: got %q want \"mine\"", storedPR.Ownership)
	}

	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}

	// Expected: humanComment, botComment, failRun — markedComment must be absent.
	if len(rows) != 3 {
		t.Fatalf("expected 3 feedback rows (marked skipped), got %d: %+v", len(rows), rows)
	}

	// Index by ExternalID for easy lookup.
	byExtID := map[string]store.Feedback{}
	for _, r := range rows {
		byExtID[r.ExternalID] = r
	}

	// Marker'd comment must be absent.
	if _, ok := byExtID["c3"]; ok {
		t.Error("marked comment c3 must NOT be ingested (is_ours skip)")
	}

	// Human comment: kind=pr-comments, author_kind=human.
	h, ok := byExtID["c1"]
	if !ok {
		t.Fatal("human comment c1 missing from store")
	}
	if h.Kind != "pr-comments" {
		t.Errorf("human comment kind: got %q want \"pr-comments\"", h.Kind)
	}
	if h.AuthorKind != "human" {
		t.Errorf("human comment author_kind: got %q want \"human\"", h.AuthorKind)
	}
	if h.AuthorLogin != "alice" {
		t.Errorf("human comment author_login: got %q want \"alice\"", h.AuthorLogin)
	}

	// Bot comment: kind=code-comment-thread, author_kind=agent.
	b, ok := byExtID["c2"]
	if !ok {
		t.Fatal("bot comment c2 missing from store")
	}
	if b.Kind != "code-comment-thread" {
		t.Errorf("bot comment kind: got %q want \"code-comment-thread\"", b.Kind)
	}
	if b.AuthorKind != "agent" {
		t.Errorf("bot comment author_kind: got %q want \"agent\"", b.AuthorKind)
	}
	if b.File != "pkg/foo/foo.go" {
		t.Errorf("bot comment file: got %q want \"pkg/foo/foo.go\"", b.File)
	}
	if b.Line != 42 {
		t.Errorf("bot comment line: got %d want 42", b.Line)
	}

	// CI run: kind=ci-failure.
	ci, ok := byExtID["run-1"]
	if !ok {
		t.Fatal("ci run run-1 missing from store")
	}
	if ci.Kind != "ci-failure" {
		t.Errorf("ci run kind: got %q want \"ci-failure\"", ci.Kind)
	}
	if ci.CheckName != "unit-tests" {
		t.Errorf("ci run check_name: got %q want \"unit-tests\"", ci.CheckName)
	}
	if ci.Conclusion != "failure" {
		t.Errorf("ci run conclusion: got %q want \"failure\"", ci.Conclusion)
	}
	// SubjectSHA is empty because api.CIRun has no HeadSHA field.
	if ci.SubjectSHA != "" {
		t.Errorf("ci run subject_sha: got %q want \"\" (api.CIRun has no HeadSHA)", ci.SubjectSHA)
	}

	// --- Verify outbox events ---
	// Run the outbox; collect dispatched events.
	type collected struct {
		eventType string
		payload   json.RawMessage
	}
	var dispatched []collected
	if err := db.RunOutbox(ctx, func(_ context.Context, e store.Event) error {
		dispatched = append(dispatched, collected{e.Type, e.Payload})
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}

	// Expect 3 feedback.created events (one per non-skipped item: human, bot, CI).
	feedbackCreatedCount := 0
	for _, d := range dispatched {
		if d.eventType == store.EventFeedbackCreated {
			feedbackCreatedCount++
			// Verify the payload shape matches FeedbackPayload.
			var p struct {
				Repo   string `json:"repo"`
				Number int    `json:"number"`
				Mine   bool   `json:"mine"`
			}
			if err := json.Unmarshal(d.payload, &p); err != nil {
				t.Errorf("decode feedback.created payload: %v", err)
				continue
			}
			if p.Repo != "o/r" {
				t.Errorf("event repo: got %q want \"o/r\"", p.Repo)
			}
			if p.Number != 7 {
				t.Errorf("event number: got %d want 7", p.Number)
			}
			if !p.Mine {
				t.Error("event mine: got false want true (pr author is SelfLogin)")
			}
		}
	}
	if feedbackCreatedCount != 3 {
		t.Errorf("feedback.created events: got %d want 3", feedbackCreatedCount)
	}
}

// TestIngestFeedbackToStore_NilEnrichedIsNoop verifies that ingestFeedbackToStore
// is a no-op when enriched is nil (edge-case guard).
func TestIngestFeedbackToStore_NilEnrichedIsNoop(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos:     []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
		},
		VCS:      map[string]VCSProvider{"github": newFakeVCS()},
		Beads:    &noopBeads{},
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pr := api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, nil); err != nil {
		t.Fatalf("nil enriched must be a no-op, got error: %v", err)
	}
}

// TestIngestFeedbackToStore_StoreGuardInProcessFeedback verifies that the
// existing processFeedback bead path still runs when Deps.Store is not set
// (no regression to the bead-creation path).
func TestIngestFeedbackToStore_StoreGuardInProcessFeedback(t *testing.T) {
	ctx := context.Background()
	comment := api.Comment{ID: "c1", Author: "bot", Body: "needs tests"}
	bdc := &fpCountBeads{} // from feedback_dedup_test.go; records created beads
	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})

	// No Deps.Store — ingest path must be a no-op.
	if e.deps.Store != nil {
		t.Fatal("this test requires Deps.Store == nil")
	}

	enriched := &vcs.EnrichedPR{Comments: []api.Comment{comment}}
	summary := &Summary{}
	if err := e.processFeedback(ctx, bdc, nil, enriched, "o/r",
		api.PR{Repo: "o/r", Number: 1}, "pr-bead-1", summary); err != nil {
		t.Fatalf("processFeedback: %v", err)
	}

	// Bead path must have created a feedback bead.
	if len(bdc.created) != 1 {
		t.Fatalf("expected 1 feedback bead created via bead path, got %d", len(bdc.created))
	}
	// No store errors.
	if len(summary.Errors) != 0 {
		t.Errorf("unexpected summary errors: %+v", summary.Errors)
	}
}
