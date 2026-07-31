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

// newSelfFeedEngine builds an ingest-only engine whose SelfLogin is `self`.
func newSelfFeedEngine(t *testing.T, db *store.DB, self string) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: self,
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
	return e
}

// countFeedbackEvents runs the outbox and counts feedback.created events,
// returning the decoded payload of the last one (nil when there were none).
func countFeedbackEvents(t *testing.T, db *store.DB) (int, *store.FeedbackPayload) {
	t.Helper()
	n := 0
	var last *store.FeedbackPayload
	for _, ev := range collectOutboxEvents(t, db) {
		if ev.Type != store.EventFeedbackCreated {
			continue
		}
		n++
		var p store.FeedbackPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode feedback.created payload: %v", err)
		}
		last = &p
	}
	return n, last
}

// TestIngestEmitsNothingWhenOnlyTheAuthorCommented is the end-to-end
// reproduction of the observed self-feeding loop (pg2-onq1e, PR #102096): the
// only new activity on the PR was the agent's OWN reply comments, posted under
// the PR author's login because pg-pr posts as the user. Pre-fix each of those
// comments produced a feedback.created event, which produced a fresh,
// substance-free process-feedback bead asking the agent to process its own
// replies. The tick must now emit NOTHING.
//
// Both agent-reply shapes are covered:
//   - a marker-stamped reply (pg-pr's own poster) — skipped at classification;
//   - an UNMARKED comment under the author's login (e.g. posted with `gh pr
//     comment`) — skipped by the PR-author exclusion. This second one is the
//     case the old code had no defence against.
func TestIngestEmitsNothingWhenOnlyTheAuthorCommented(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 102096, State: "open", Branch: "feat/x", Base: "main",
		Author: "phillipg", URL: "https://github.com/o/r/pull/102096", HeadSHA: "sha-1",
	}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Comments: []api.Comment{
			{ID: "reply-1", Author: "phillipg", AuthorRole: "owner", Body: marker.Stamp("addressed in 1a2b3c4")},
			{ID: "reply-2", Author: "phillipg", AuthorRole: "owner", Body: "addressed in 1a2b3c4"},
			{
				ID: "reply-3", Author: "phillipg", AuthorRole: "owner", Body: "good catch, fixed",
				Path: "pkg/foo/foo.go", Line: 12, ThreadID: "t-1",
			},
		},
	}

	e := newSelfFeedEngine(t, db, "phillipg")
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	n, _ := countFeedbackEvents(t, db)
	if n != 0 {
		t.Fatalf("the author's own replies produced %d feedback.created event(s); want 0 "+
			"(each one becomes an empty process-feedback bead — the self-feeding loop)", n)
	}
}

// TestIngestEmitsOneEventWithSubstanceForRealFeedback is the positive control:
// once a genuine reviewer comment arrives alongside the author's own replies,
// exactly ONE event is emitted, and its summary counts only the reviewer's item.
func TestIngestEmitsOneEventWithSubstanceForRealFeedback(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 102096, State: "open", Branch: "feat/x", Base: "main",
		Author: "phillipg", URL: "https://github.com/o/r/pull/102096", HeadSHA: "sha-1",
	}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Comments: []api.Comment{
			{ID: "reply-1", Author: "phillipg", AuthorRole: "owner", Body: "addressed in 1a2b3c4"},
			{ID: "rev-1", Author: "alice", AuthorRole: "member", Body: "this still leaks the handle"},
		},
	}

	e := newSelfFeedEngine(t, db, "phillipg")
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	n, p := countFeedbackEvents(t, db)
	if n != 1 {
		t.Fatalf("feedback.created events: got %d want 1", n)
	}
	if p.Summary == nil {
		t.Fatal("payload summary is nil — the bead description would have no substance")
	}
	if p.Summary.Unaddressed != 1 || p.Summary.ByKind["pr-comments"] != 1 {
		t.Fatalf("summary must count only the reviewer's comment: %+v", p.Summary)
	}
	if len(p.Summary.Reviewers) != 1 || p.Summary.Reviewers[0] != "alice" {
		t.Errorf("reviewers: got %v want [alice]", p.Summary.Reviewers)
	}
}

// TestIngestReSyncAfterDispositionEmitsNothing closes the loop the other way: an
// agent processed the reviewer feedback (dispositioned it) and pushed. The next
// tick re-observes the same comment, but there is nothing left unaddressed, so no
// event — and therefore no bead — may be produced.
func TestIngestReSyncAfterDispositionEmitsNothing(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 102096, State: "open", Author: "phillipg", HeadSHA: "sha-1",
	}
	enriched := &vcs.EnrichedPR{
		PR:       pr,
		Comments: []api.Comment{{ID: "rev-1", Author: "alice", AuthorRole: "member", Body: "this still leaks the handle"}},
	}
	e := newSelfFeedEngine(t, db, "phillipg")
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if n, _ := countFeedbackEvents(t, db); n != 1 {
		t.Fatalf("first tick: got %d events want 1", n)
	}

	// The agent processes it.
	storedPR, err := db.GetPR(ctx, "o/r", 102096)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v (pr=%v)", err, storedPR)
	}
	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListFeedback: %v (rows=%d)", err, len(rows))
	}
	if err := db.SetDisposition(ctx, rows[0].ID, "will-fix", "fixed in 1a2b3c4", ""); err != nil {
		t.Fatalf("SetDisposition: %v", err)
	}

	// The agent's reply lands under its own (== the author's) login, and the next
	// tick re-observes both comments.
	enriched.Comments = append(enriched.Comments,
		api.Comment{ID: "reply-1", Author: "phillipg", AuthorRole: "owner", Body: "fixed in 1a2b3c4"})
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if n, _ := countFeedbackEvents(t, db); n != 0 {
		t.Fatalf("re-sync after disposition emitted %d event(s); want 0", n)
	}
}
