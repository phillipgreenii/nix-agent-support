package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// reviewerVCS is a fakeVCS that also satisfies reviewsink.VCSReviewer, so the
// default team sink can post through it without shelling out to gh.
type reviewerVCS struct {
	*fakeVCS
	hasPending bool
	posts      []reviewerPost
}

type reviewerPost struct {
	repo     string
	pr       int
	body     string
	comments []api.Comment
}

func (r *reviewerVCS) HasPendingReviewByViewer(_ context.Context, _ string, _ int) (bool, error) {
	return r.hasPending, nil
}

func (r *reviewerVCS) ListComments(_ context.Context, _ string, _ int) ([]api.Comment, error) {
	return nil, nil
}

func (r *reviewerVCS) PostReview(_ context.Context, repo string, pr int, body string, comments []api.Comment) (*api.Review, error) {
	r.posts = append(r.posts, reviewerPost{repo: repo, pr: pr, body: body, comments: comments})
	return &api.Review{ID: "RV_1", State: "pending", Body: body}, nil
}

func newReviewHookEngineTeamDefault(t *testing.T, bdc *fakeReviewBeads, sp Spawner, rv *reviewerVCS, db *store.DB, reviewsDir string, repos []config.RepoConfig) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: repos},
		VCS:   map[string]VCSProvider{"github": rv},
		Beads: &fakeDepBeads{},
		Store: db,
		Review: ReviewHookDeps{
			Beads:      bdc,
			Spawner:    sp,
			ReviewsDir: reviewsDir,
			// TeamSink deliberately nil → the engine's real default team sink.
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// The default (uninjected) team sink MUST apply the produced review to GitHub
// as a PENDING review (no event) via the configured VCS provider.
func TestReviewHookCycle_DefaultTeamSink_PostsPendingReview(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()

	prID, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 8, Ownership: "team", State: "open", HeadSHA: "h9"})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h9", "b"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-team", Repo: "o/r", Number: 8, Mine: false}}
	sp := &draftSpawner{headSHA: "h9", reviewsDir: dir, body: "team review body",
		comments: []api.Comment{{Path: "x.go", Line: 2, Body: "nit"}}}
	rv := &reviewerVCS{fakeVCS: newFakeVCS()}
	e := newReviewHookEngineTeamDefault(t, bdc, sp, rv, db, dir, []config.RepoConfig{{Remote: "o/r"}})

	e.reviewHookCycle(ctx, NewTextLogger())

	if bdc.closed["dr-team"] != "reviewed" {
		t.Fatalf("team bead should be closed reason=reviewed, got %v", bdc.closed)
	}
	if len(rv.posts) != 1 {
		t.Fatalf("default team sink must PostReview once, got %d", len(rv.posts))
	}
	if !marker.IsOurs(rv.posts[0].body) {
		t.Errorf("posted body must carry the agent marker, got %q", rv.posts[0].body)
	}
}

// When a pending review already exists, the default team sink MUST skip: no
// PostReview, but the bead still closes (the review IS produced; the human owns
// the pending review).
func TestReviewHookCycle_DefaultTeamSink_SkipsWhenPending(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()

	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 8, Ownership: "team", State: "open", HeadSHA: "h9"})
	if _, _, err := db.RecordRevision(ctx, prID, "h9", "b"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-team", Repo: "o/r", Number: 8, Mine: false}}
	sp := &draftSpawner{headSHA: "h9", reviewsDir: dir, body: "team review body"}
	rv := &reviewerVCS{fakeVCS: newFakeVCS(), hasPending: true}
	e := newReviewHookEngineTeamDefault(t, bdc, sp, rv, db, dir, []config.RepoConfig{{Remote: "o/r"}})

	e.reviewHookCycle(ctx, NewTextLogger())

	if len(rv.posts) != 0 {
		t.Fatalf("default team sink must NOT post when a pending review exists, got %d", len(rv.posts))
	}
	if bdc.closed["dr-team"] != "reviewed" {
		t.Fatalf("team bead should still close (review produced), got %v", bdc.closed)
	}
}

// M5 repo-scope guard: the sink MUST refuse to post to a repo not in the
// configured repo set (unattended write scope). The bead still closes (nothing
// was produced against a watched repo), so it does not retry forever.
func TestReviewHookCycle_DefaultTeamSink_RefusesUnconfiguredRepo(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()

	// Seed the PR + revision under o/r (so the spawner + close path work) but
	// configure a DIFFERENT repo so the sink's scope guard trips.
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 8, Ownership: "team", State: "open", HeadSHA: "h9"})
	if _, _, err := db.RecordRevision(ctx, prID, "h9", "b"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-team", Repo: "o/r", Number: 8, Mine: false}}
	sp := &draftSpawner{headSHA: "h9", reviewsDir: dir, body: "team review body"}
	rv := &reviewerVCS{fakeVCS: newFakeVCS()}
	// Config lists only some-other/repo — o/r is NOT configured.
	e := newReviewHookEngineTeamDefault(t, bdc, sp, rv, db, dir, []config.RepoConfig{{Remote: "some-other/repo"}})

	e.reviewHookCycle(ctx, NewTextLogger())

	if len(rv.posts) != 0 {
		t.Fatalf("must NOT post to an unconfigured repo (M5), got %d", len(rv.posts))
	}
}
