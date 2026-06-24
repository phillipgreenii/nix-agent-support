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
	// classifies as agent via the [bot]-suffix fast-path: ingestFeedbackToStore
	// derives a "Bot" typename from the suffix and feedbackclassify.Classify
	// maps that to author_kind=agent.
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
	// Failing CI run — carries a HeadSHA so subject_sha is set on the row.
	failRun := api.CIRun{
		ID:         "run-1",
		Name:       "unit-tests",
		Status:     "completed",
		Conclusion: "failure",
		URL:        "https://github.com/o/r/actions/runs/run-1",
		Provider:   "github-actions",
		HeadSHA:    "sha-abc",
	}

	pr := api.PR{
		Repo:    "o/r",
		Number:  7,
		State:   "open",
		Branch:  "feat/x",
		Base:    "main",
		Author:  "phillipg",
		URL:     "https://github.com/o/r/pull/7",
		HeadSHA: "sha-abc",
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
	if storedPR.HeadSHA != "sha-abc" {
		t.Errorf("head_sha: got %q want \"sha-abc\"", storedPR.HeadSHA)
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
	// SubjectSHA must be propagated from api.CIRun.HeadSHA.
	if ci.SubjectSHA != "sha-abc" {
		t.Errorf("ci run subject_sha: got %q want \"sha-abc\"", ci.SubjectSHA)
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

// TestIngestCIFailure_PerRevision verifies that two failing runs of the same
// check but with different HeadSHAs produce TWO distinct feedback rows (each
// having its own fingerprint), providing per-revision history.
func TestIngestCIFailure_PerRevision(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo:    "o/r",
		Number:  10,
		State:   "open",
		Branch:  "feat/y",
		Base:    "main",
		Author:  "alice",
		URL:     "https://github.com/o/r/pull/10",
		HeadSHA: "sha-v2",
	}

	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "bot",
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

	// First ingest: sha-v1 fails "unit-tests".
	run1 := api.CIRun{
		ID: "run-sha1", Name: "unit-tests", Status: "completed",
		Conclusion: "failure", URL: "https://u/1", Provider: "github-actions",
		HeadSHA: "sha-v1",
	}
	enriched1 := &vcs.EnrichedPR{PR: pr, CIRuns: []api.CIRun{run1}}
	pr1 := pr
	pr1.HeadSHA = "sha-v1"
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr1, enriched1); err != nil {
		t.Fatalf("ingest 1: %v", err)
	}

	// Second ingest: sha-v2 also fails "unit-tests" (different head).
	run2 := api.CIRun{
		ID: "run-sha2", Name: "unit-tests", Status: "completed",
		Conclusion: "failure", URL: "https://u/2", Provider: "github-actions",
		HeadSHA: "sha-v2",
	}
	enriched2 := &vcs.EnrichedPR{PR: pr, CIRuns: []api.CIRun{run2}}
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched2); err != nil {
		t.Fatalf("ingest 2: %v", err)
	}

	// Should have two distinct feedback rows (different fingerprints).
	storedPR, err := db.GetPR(ctx, "o/r", 10)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v", err)
	}
	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 feedback rows (one per revision), got %d: %+v", len(rows), rows)
	}
	byExtID := map[string]store.Feedback{}
	for _, r := range rows {
		byExtID[r.ExternalID] = r
	}
	r1, ok1 := byExtID["run-sha1"]
	r2, ok2 := byExtID["run-sha2"]
	if !ok1 || !ok2 {
		t.Fatalf("missing rows: have %v", byExtID)
	}
	if r1.SubjectSHA != "sha-v1" {
		t.Errorf("run-sha1 subject_sha: got %q want sha-v1", r1.SubjectSHA)
	}
	if r2.SubjectSHA != "sha-v2" {
		t.Errorf("run-sha2 subject_sha: got %q want sha-v2", r2.SubjectSHA)
	}
	if r1.Fingerprint == r2.Fingerprint {
		t.Errorf("fingerprints must differ across revisions; both = %q", r1.Fingerprint)
	}

	// After the second ingest (head=sha-v2), the sha-v1 row must be superseded.
	if r1.Status != "superseded" {
		t.Errorf("sha-v1 row status: got %q want \"superseded\" after head moved to sha-v2", r1.Status)
	}
	if r2.Status == "superseded" {
		t.Errorf("sha-v2 row (current head) must NOT be superseded; status=%q", r2.Status)
	}
}

// TestIngestCIFailure_SubjectSHASet verifies the basic contract that a CI run
// with a HeadSHA produces a feedback row with that subject_sha populated.
func TestIngestCIFailure_SubjectSHASet(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 20, State: "open",
		Branch: "feat/z", Base: "main", Author: "alice",
		URL: "https://github.com/o/r/pull/20", HeadSHA: "deadbeef",
	}
	run := api.CIRun{
		ID: "run-x", Name: "lint", Status: "completed",
		Conclusion: "failure", URL: "https://u", Provider: "github-actions",
		HeadSHA: "deadbeef",
	}
	e, err := New(Deps{
		Cfg:      &config.Config{SelfLogin: "bot", Repos: []config.RepoConfig{{Remote: "o/r", VCS: "github"}}},
		VCS:      map[string]VCSProvider{"github": newFakeVCS()},
		Beads:    &noopBeads{},
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, &vcs.EnrichedPR{PR: pr, CIRuns: []api.CIRun{run}}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	storedPR, _ := db.GetPR(ctx, "o/r", 20)
	rows, _ := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].SubjectSHA != "deadbeef" {
		t.Errorf("subject_sha: got %q want deadbeef", rows[0].SubjectSHA)
	}
}

// TestProcessFeedback_NoStoreIsNoop verifies that processFeedback does nothing
// when Deps.Store is unset: the bead-feedback path is gone, so a PR with
// enriched feedback produces no errors and no side effects. (The store-backed
// ingest path is covered by TestIngestFeedbackToStore above.)
func TestProcessFeedback_NoStoreIsNoop(t *testing.T) {
	ctx := context.Background()
	comment := api.Comment{ID: "c1", Author: "bot", Body: "needs tests"}
	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})

	// No Deps.Store — processFeedback must be a no-op.
	if e.deps.Store != nil {
		t.Fatal("this test requires Deps.Store == nil")
	}

	enriched := &vcs.EnrichedPR{Comments: []api.Comment{comment}}
	summary := &Summary{}
	if err := e.processFeedback(ctx, &refreshFakeBeads{}, nil, enriched, "o/r",
		api.PR{Repo: "o/r", Number: 1}, "pr-bead-1", summary); err != nil {
		t.Fatalf("processFeedback: %v", err)
	}

	// No store errors and no feedback counters moved.
	if len(summary.Errors) != 0 {
		t.Errorf("unexpected summary errors: %+v", summary.Errors)
	}
	if summary.FeedbackCreated != 0 || summary.CyclesCreated != 0 {
		t.Errorf("processFeedback with no store must not create beads/cycles; got FeedbackCreated=%d CyclesCreated=%d",
			summary.FeedbackCreated, summary.CyclesCreated)
	}
}
