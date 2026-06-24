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

	// Bot comment: keyed by ThreadID "t-1" (one feedback row per thread).
	b, ok := byExtID["t-1"]
	if !ok {
		t.Fatal("bot thread t-1 missing from store (expected ExternalID=thread id)")
	}
	if b.Kind != "code-comment-thread" {
		t.Errorf("bot thread kind: got %q want \"code-comment-thread\"", b.Kind)
	}
	if b.AuthorKind != "agent" {
		t.Errorf("bot thread author_kind: got %q want \"agent\"", b.AuthorKind)
	}
	if b.File != "pkg/foo/foo.go" {
		t.Errorf("bot thread file: got %q want \"pkg/foo/foo.go\"", b.File)
	}
	if b.Line != 42 {
		t.Errorf("bot thread line: got %d want 42", b.Line)
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

// TestIngestThreadGrouping verifies the core M1 behaviour:
//   - A review thread with 2 comments sharing the same ThreadID produces exactly
//     ONE code-comment-thread feedback row (not two).
//   - Both comments are stored as code_comment_message rows.
//   - A top-level pr-comment produces ONE pr-comments feedback row with 0 messages.
func TestIngestThreadGrouping(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	// Two inline comments on the same thread (same ThreadID, same Path).
	threadComment1 := api.Comment{
		ID:         "tc1",
		Author:     "reviewer",
		AuthorRole: "member",
		Body:       "This method is too long.",
		Path:       "pkg/bar/bar.go",
		Line:       10,
		ThreadID:   "thread-abc",
	}
	threadComment2 := api.Comment{
		ID:         "tc2",
		Author:     "coderabbit[bot]",
		AuthorRole: "none",
		Body:       "Agreed, consider splitting at line 25.",
		Path:       "pkg/bar/bar.go",
		Line:       10,
		ThreadID:   "thread-abc",
	}
	// Top-level (pr-comments) comment — no Path.
	topComment := api.Comment{
		ID:         "top1",
		Author:     "alice",
		AuthorRole: "contributor",
		Body:       "Overall LGTM.",
	}

	pr := api.PR{
		Repo:    "o/r",
		Number:  42,
		State:   "open",
		Branch:  "feat/thread",
		Base:    "main",
		Author:  "someone",
		URL:     "https://github.com/o/r/pull/42",
		HeadSHA: "sha-thread",
	}
	enriched := &vcs.EnrichedPR{
		PR:       pr,
		Comments: []api.Comment{threadComment1, threadComment2, topComment},
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

	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "o/r", 42)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v / nil=%v", err, storedPR == nil)
	}

	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}

	// Must be exactly 2 rows: one code-comment-thread (for "thread-abc") and one pr-comments.
	if len(rows) != 2 {
		t.Fatalf("expected 2 feedback rows (1 thread + 1 top-level), got %d: %+v", len(rows), rows)
	}

	byExtID := map[string]store.Feedback{}
	for _, r := range rows {
		byExtID[r.ExternalID] = r
	}

	// Thread row: ExternalID must be the ThreadID "thread-abc", not a comment id.
	threadRow, ok := byExtID["thread-abc"]
	if !ok {
		t.Fatalf("expected feedback row with ExternalID=\"thread-abc\"; got keys: %v", func() []string {
			var ks []string
			for k := range byExtID {
				ks = append(ks, k)
			}
			return ks
		}())
	}
	if threadRow.Kind != "code-comment-thread" {
		t.Errorf("thread row kind: got %q want \"code-comment-thread\"", threadRow.Kind)
	}
	if threadRow.File != "pkg/bar/bar.go" {
		t.Errorf("thread row file: got %q want \"pkg/bar/bar.go\"", threadRow.File)
	}
	// There must NOT be a separate row for the individual comment ids.
	if _, ok := byExtID["tc1"]; ok {
		t.Error("tc1 must not be its own feedback row (should be a message in thread-abc)")
	}
	if _, ok := byExtID["tc2"]; ok {
		t.Error("tc2 must not be its own feedback row (should be a message in thread-abc)")
	}

	// Messages: both comments must be stored as code_comment_message rows.
	msgs, err := db.ListMessages(ctx, threadRow.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages for thread-abc, got %d: %+v", len(msgs), msgs)
	}
	msgByExtID := map[string]store.Message{}
	for _, m := range msgs {
		msgByExtID[m.ExternalID] = m
	}
	if _, ok := msgByExtID["tc1"]; !ok {
		t.Error("message tc1 missing from code_comment_message")
	}
	if _, ok := msgByExtID["tc2"]; !ok {
		t.Error("message tc2 missing from code_comment_message")
	}

	// Top-level pr-comments row: ExternalID = "top1", no messages.
	topRow, ok := byExtID["top1"]
	if !ok {
		t.Fatal("top-level comment top1 missing from store")
	}
	if topRow.Kind != "pr-comments" {
		t.Errorf("top row kind: got %q want \"pr-comments\"", topRow.Kind)
	}
	topMsgs, err := db.ListMessages(ctx, topRow.ID)
	if err != nil {
		t.Fatalf("ListMessages for top row: %v", err)
	}
	if len(topMsgs) != 0 {
		t.Errorf("pr-comments row must have 0 messages, got %d", len(topMsgs))
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

// TestIngestPathlessThreadComment_DoesNotAbort is the I1 regression test.
// A path-less thread comment (ThreadID set, Path empty) previously caused
// ingestFeedbackToStore to abort via return — dropping subsequent CI ingestion
// and ReconcileStaleness. The fix:
//  1. classifies path-less comments as pr-comments (not code-comment-thread),
//  2. continues on per-item errors instead of returning,
//  3. stores the comment as pr-comments with first_seen_head_sha set (D4).
func TestIngestPathlessThreadComment_DoesNotAbort(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	// Path-less thread comment: ThreadID set but Path is empty.
	pathlessComment := api.Comment{
		ID:       "c-pathless",
		Author:   "reviewer",
		Body:     "Overall looks good to me.",
		ThreadID: "thread-42",
		// Path is intentionally empty — this triggers the bug without the fix.
	}
	// CI failure follows the path-less comment.
	failRun := api.CIRun{
		ID:         "run-pathless",
		Name:       "unit-tests",
		Status:     "completed",
		Conclusion: "failure",
		URL:        "https://github.com/o/r/actions/runs/runX",
		Provider:   "github-actions",
		HeadSHA:    "sha-pathless",
	}

	pr := api.PR{
		Repo:    "o/r",
		Number:  99,
		State:   "open",
		Branch:  "feat/pathless",
		Base:    "main",
		Author:  "alice",
		URL:     "https://github.com/o/r/pull/99",
		HeadSHA: "sha-pathless",
	}
	enriched := &vcs.EnrichedPR{
		PR:       pr,
		Comments: []api.Comment{pathlessComment},
		CIRuns:   []api.CIRun{failRun},
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

	// Must not return an error — the path-less comment must not abort ingestion.
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore must not abort on path-less comment: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "o/r", 99)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v / nil=%v", err, storedPR == nil)
	}

	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}

	byExtID := map[string]store.Feedback{}
	for _, r := range rows {
		byExtID[r.ExternalID] = r
	}

	// The path-less comment must be stored as pr-comments (not code-comment-thread).
	pc, ok := byExtID["c-pathless"]
	if !ok {
		t.Fatal("path-less comment c-pathless must be stored (not dropped)")
	}
	if pc.Kind != "pr-comments" {
		t.Errorf("path-less comment kind: got %q want \"pr-comments\"", pc.Kind)
	}
	// D4: first_seen_head_sha must be set for pr-comments.
	if pc.FirstSeenHeadSHA != "sha-pathless" {
		t.Errorf("path-less comment first_seen_head_sha: got %q want \"sha-pathless\"", pc.FirstSeenHeadSHA)
	}

	// CI failure must also be stored — ingestion must NOT have aborted.
	ci, ok := byExtID["run-pathless"]
	if !ok {
		t.Fatal("CI failure run-pathless must be stored; ingestion must not abort on path-less comment")
	}
	if ci.Kind != "ci-failure" {
		t.Errorf("CI failure kind: got %q want \"ci-failure\"", ci.Kind)
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
