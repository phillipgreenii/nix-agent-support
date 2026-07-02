package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// newReviewHookEngineDefaultSinks builds an engine with NO injected sinks, so
// routing falls through to the engine's real default mine sink (the .34 fill).
func newReviewHookEngineDefaultSinks(t *testing.T, bdc *fakeReviewBeads, sp Spawner, db *store.DB, reviewsDir string) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": newFakeVCS()},
		Beads: &fakeDepBeads{},
		Store: db,
		Review: ReviewHookDeps{
			Beads:      bdc,
			Spawner:    sp,
			ReviewsDir: reviewsDir,
			// MineSink / TeamSink deliberately nil → default sinks.
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// draftSpawner writes a Draft with the given body+comments (simulating the
// orchestrator's staged review) and returns the configured head SHA.
type draftSpawner struct {
	headSHA    string
	reviewsDir string
	body       string
	comments   []api.Comment
}

func (s *draftSpawner) Produce(_ context.Context, ref ReviewRef) (string, error) {
	_, _ = reviewstage.Save(s.reviewsDir, &reviewstage.Draft{
		Repo: ref.Repo, PR: ref.Number, Body: s.body, Comments: s.comments,
	})
	return s.headSHA, nil
}

// The default (uninjected) mine sink MUST ingest the produced findings as
// self-review feedback rows that block the merge loop — with NO GitHub write
// (the review hook path has no provider access at all for mine).
func TestReviewHookCycle_DefaultMineSink_IngestsSelfReviewFeedback(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()

	prID, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h1"})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-1", Repo: "o/r", Number: 5, Mine: true}}
	sp := &draftSpawner{
		headSHA:    "h1",
		reviewsDir: dir,
		body:       "PR-level: consider splitting",
		comments:   []api.Comment{{Path: "a.go", Line: 4, Body: "handle the error"}},
	}
	e := newReviewHookEngineDefaultSinks(t, bdc, sp, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	// Bead closed reviewed.
	if bdc.closed["dr-1"] != "reviewed" {
		t.Fatalf("bead should be closed reason=reviewed, got %v", bdc.closed)
	}

	// Two self-review feedback rows ingested (body + one inline comment).
	items, err := db.ListFeedback(ctx, prID, store.ListFilter{Kind: "self-review"})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("self-review rows = %d, want 2", len(items))
	}
	for _, f := range items {
		if !f.IsOurs || f.AuthorKind != "agent" {
			t.Errorf("self-review row must be is_ours/agent, got %+v", f)
		}
	}

	// The findings gate the merge loop until dispositioned.
	blocked, err := db.HasBlockingFeedback(ctx, prID)
	if err != nil {
		t.Fatalf("HasBlockingFeedback: %v", err)
	}
	if !blocked {
		t.Fatal("ingested self-review findings MUST block the merge loop")
	}
}
