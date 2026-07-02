package sync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fakeReviewBeads is a full in-memory ReviewBeadClient double.
type fakeReviewBeads struct {
	ready       []beads.DraftReviewRef
	readyErr    error
	claimed     []string
	unclaimed   []string
	closed      map[string]string // id -> reason
	reopened    []string
	deadLetter  []string
	failCounts  map[string]int
	findResults map[string]findResult // "repo#n" -> result
}

type findResult struct {
	id     string
	closed bool
	found  bool
}

func newFakeReviewBeads() *fakeReviewBeads {
	return &fakeReviewBeads{
		closed:      map[string]string{},
		failCounts:  map[string]int{},
		findResults: map[string]findResult{},
	}
}

func (f *fakeReviewBeads) ListReadyDraftReviews(context.Context) ([]beads.DraftReviewRef, error) {
	return f.ready, f.readyErr
}
func (f *fakeReviewBeads) ClaimDraftReview(_ context.Context, id string) error {
	f.claimed = append(f.claimed, id)
	return nil
}
func (f *fakeReviewBeads) UnclaimDraftReview(_ context.Context, id string) error {
	f.unclaimed = append(f.unclaimed, id)
	return nil
}
func (f *fakeReviewBeads) CloseDraftReview(_ context.Context, id, reason string) error {
	f.closed[id] = reason
	return nil
}
func (f *fakeReviewBeads) ReopenDraftReview(_ context.Context, id string) error {
	f.reopened = append(f.reopened, id)
	return nil
}
func (f *fakeReviewBeads) DeadLetterDraftReview(_ context.Context, id string) error {
	f.deadLetter = append(f.deadLetter, id)
	return nil
}
func (f *fakeReviewBeads) ReviewFailCount(_ context.Context, id string) (int, error) {
	return f.failCounts[id], nil
}
func (f *fakeReviewBeads) BumpReviewFailCount(_ context.Context, id string) (int, error) {
	f.failCounts[id]++
	return f.failCounts[id], nil
}
func (f *fakeReviewBeads) ResetReviewFailCount(_ context.Context, id string) error {
	delete(f.failCounts, id)
	return nil
}
func (f *fakeReviewBeads) FindDraftReviewForPR(_ context.Context, repo string, number int) (string, bool, bool, error) {
	r := f.findResults[key(repo, number)]
	return r.id, r.closed, r.found, nil
}

func key(repo string, n int) string {
	return fmt.Sprintf("%s#%d", repo, n)
}

// fakeSpawner records Produce calls; it writes a Draft (simulating the
// orchestrator's `pg-pr review draft`) unless writeDraft is false, and returns
// the configured head SHA or error.
type fakeSpawner struct {
	mu         sync.Mutex
	calls      []ReviewRef
	headSHA    string
	err        error
	writeDraft bool
	reviewsDir string
}

func (s *fakeSpawner) Produce(_ context.Context, ref ReviewRef) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, ref)
	if s.err != nil {
		return "", s.err
	}
	if s.writeDraft {
		_, _ = reviewstage.Save(s.reviewsDir, &reviewstage.Draft{
			Repo: ref.Repo, PR: ref.Number, Body: "reviewed",
		})
	}
	return s.headSHA, nil
}

// perPRSpawner fails production for one PR number and succeeds (writes a draft)
// for all others.
type perPRSpawner struct {
	failNumber int
	headSHA    string
	reviewsDir string
}

func (s *perPRSpawner) Produce(_ context.Context, ref ReviewRef) (string, error) {
	if ref.Number == s.failNumber {
		return "", errors.New("production failed")
	}
	_, _ = reviewstage.Save(s.reviewsDir, &reviewstage.Draft{Repo: ref.Repo, PR: ref.Number, Body: "reviewed"})
	return s.headSHA, nil
}

// recordingSinks capture routed results without touching GitHub.
type recordingSinks struct {
	mine []reviewstage.Result
	team []reviewstage.Result
}

func (r *recordingSinks) mineSink(_ context.Context, res reviewstage.Result) error {
	r.mine = append(r.mine, res)
	return nil
}
func (r *recordingSinks) teamSink(_ context.Context, res reviewstage.Result) error {
	r.team = append(r.team, res)
	return nil
}

func newReviewHookEngine(t *testing.T, bdc *fakeReviewBeads, sp Spawner, sinks *recordingSinks, db *store.DB, reviewsDir string) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": newFakeVCS()},
		Beads: &fakeDepBeads{},
		Store: db,
		Review: ReviewHookDeps{
			Beads:      bdc,
			Spawner:    sp,
			MineSink:   sinks.mineSink,
			TeamSink:   sinks.teamSink,
			ReviewsDir: reviewsDir,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestReviewHookCycle_HappyPath_Mine(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()

	// Seed the PR + a revision at head "h1".
	prID, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h1"})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-1", Repo: "o/r", Number: 5, Mine: true}}
	sp := &fakeSpawner{headSHA: "h1", writeDraft: true, reviewsDir: dir}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	if len(bdc.claimed) != 1 || bdc.claimed[0] != "dr-1" {
		t.Fatalf("bead should be claimed once, got %v", bdc.claimed)
	}
	if len(sp.calls) != 1 {
		t.Fatalf("spawner should be called once, got %d", len(sp.calls))
	}
	if bdc.closed["dr-1"] != "reviewed" {
		t.Fatalf("bead should be closed reason=reviewed, got %v", bdc.closed)
	}
	// Mine routed; team NOT.
	if len(sinks.mine) != 1 || len(sinks.team) != 0 {
		t.Fatalf("routing wrong: mine=%d team=%d", len(sinks.mine), len(sinks.team))
	}
	if sinks.mine[0].Ownership != "mine" || sinks.mine[0].HeadSHA != "h1" || sinks.mine[0].BeadID != "dr-1" {
		t.Fatalf("Result wrong: %+v", sinks.mine[0])
	}
	// Result sidecar persisted.
	got, err := reviewstage.LoadResult(dir, "o/r", 5)
	if err != nil {
		t.Fatalf("LoadResult: %v", err)
	}
	if got.HeadSHA != "h1" || got.Ownership != "mine" {
		t.Fatalf("sidecar wrong: %+v", got)
	}
	// reviewed_by_agent_at stamped on the matching revision.
	latest, err := db.LatestRevision(ctx, prID)
	if err != nil {
		t.Fatalf("LatestRevision: %v", err)
	}
	if latest.ReviewedByAgentAt == "" {
		t.Fatalf("reviewed_by_agent_at should be stamped")
	}
}

func TestReviewHookCycle_Routing_Team(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 8, Ownership: "team", State: "open", HeadSHA: "h9"})
	_, _, _ = db.RecordRevision(ctx, prID, "h9", "b")

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-2", Repo: "o/r", Number: 8, Mine: false}}
	sp := &fakeSpawner{headSHA: "h9", writeDraft: true, reviewsDir: dir}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	if len(sinks.team) != 1 || len(sinks.mine) != 0 {
		t.Fatalf("team routing wrong: mine=%d team=%d", len(sinks.mine), len(sinks.team))
	}
	if sinks.team[0].Ownership != "team" {
		t.Fatalf("team Result ownership wrong: %+v", sinks.team[0])
	}
}

func TestReviewHookCycle_GracefulFailure_SpawnerError(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h1"})
	_, _, _ = db.RecordRevision(ctx, prID, "h1", "b")

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-1", Repo: "o/r", Number: 5, Mine: true}}
	sp := &fakeSpawner{err: errors.New("claude -p unavailable")}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	// Bead left OPEN + UNCLAIMED, not closed; no Result; fail count bumped.
	if _, ok := bdc.closed["dr-1"]; ok {
		t.Fatalf("bead MUST NOT be closed on production failure")
	}
	if len(bdc.unclaimed) != 1 {
		t.Fatalf("bead should be unclaimed, got %v", bdc.unclaimed)
	}
	if bdc.failCounts["dr-1"] != 1 {
		t.Fatalf("fail count should be 1, got %d", bdc.failCounts["dr-1"])
	}
	if _, err := reviewstage.LoadResult(dir, "o/r", 5); err == nil {
		t.Fatalf("no Result should be written on failure")
	}
	if len(sinks.mine) != 0 || len(sinks.team) != 0 {
		t.Fatalf("no sink should be called on failure")
	}
}

func TestReviewHookCycle_GracefulFailure_NoDraftWritten(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h1"})
	_, _, _ = db.RecordRevision(ctx, prID, "h1", "b")

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-1", Repo: "o/r", Number: 5, Mine: true}}
	// Spawner returns success but writes NO draft.
	sp := &fakeSpawner{headSHA: "h1", writeDraft: false, reviewsDir: dir}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	if _, ok := bdc.closed["dr-1"]; ok {
		t.Fatalf("bead MUST NOT be closed when no Draft was produced")
	}
	if len(bdc.unclaimed) != 1 {
		t.Fatalf("bead should be unclaimed, got %v", bdc.unclaimed)
	}
}

func TestReviewHookCycle_PerItemContinue(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()
	p1, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h1"})
	_, _, _ = db.RecordRevision(ctx, p1, "h1", "b")
	p2, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 6, Ownership: "mine", State: "open", HeadSHA: "h2"})
	_, _, _ = db.RecordRevision(ctx, p2, "h2", "b")

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{
		{ID: "dr-fail", Repo: "o/r", Number: 5, Mine: true},
		{ID: "dr-ok", Repo: "o/r", Number: 6, Mine: true},
	}
	// A spawner that fails for #5 but succeeds (writes draft) for #6.
	sp := &perPRSpawner{failNumber: 5, headSHA: "h2", reviewsDir: dir}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	// #6 produced+closed; #5 stays open with fail count 1.
	if bdc.closed["dr-ok"] != "reviewed" {
		t.Fatalf("dr-ok should be closed, got %v", bdc.closed)
	}
	if _, ok := bdc.closed["dr-fail"]; ok {
		t.Fatalf("dr-fail should NOT be closed")
	}
	if bdc.failCounts["dr-fail"] != 1 {
		t.Fatalf("dr-fail count should be 1, got %d", bdc.failCounts["dr-fail"])
	}
	if len(sinks.mine) != 1 {
		t.Fatalf("only dr-ok should be routed, got %d", len(sinks.mine))
	}
}

func TestReviewHookCycle_DeadLetterOnThirdFailure(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h1"})
	_, _, _ = db.RecordRevision(ctx, prID, "h1", "b")

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-1", Repo: "o/r", Number: 5, Mine: true}}
	bdc.failCounts["dr-1"] = 2 // already failed twice; the 3rd failure dead-letters.
	sp := &fakeSpawner{err: errors.New("still broken")}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	if len(bdc.deadLetter) != 1 || bdc.deadLetter[0] != "dr-1" {
		t.Fatalf("bead should be dead-lettered on 3rd failure, got %v", bdc.deadLetter)
	}
}

func TestMaintenanceCycle_InvokesReviewHook(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h1"})
	_, _, _ = db.RecordRevision(ctx, prID, "h1", "b")

	bdc := newFakeReviewBeads()
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-1", Repo: "o/r", Number: 5, Mine: true}}
	sp := &fakeSpawner{headSHA: "h1", writeDraft: true, reviewsDir: dir}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	// One maintenance tick must drive the review hook.
	e.maintenanceCycle(ctx, NewTextLogger())

	if len(sp.calls) != 1 {
		t.Fatalf("maintenanceCycle must invoke the review hook once, spawner calls=%d", len(sp.calls))
	}
	if bdc.closed["dr-1"] != "reviewed" {
		t.Fatalf("bead should be produced+closed via maintenanceCycle, got %v", bdc.closed)
	}
}

func TestReviewHookCycle_ReReviewOnHeadAdvance(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	dir := t.TempDir()

	// PR reviewed at h1 (old), head has since advanced to h2.
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", State: "open", HeadSHA: "h2"})
	_, _, _ = db.RecordRevision(ctx, prID, "h1", "b")
	if err := db.MarkRevisionAgentReviewed(ctx, prID, "h1", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("mark h1 reviewed: %v", err)
	}
	// Head advances to h2 (new revision, unreviewed).
	if _, _, err := db.RecordRevision(ctx, prID, "h2", "b"); err != nil {
		t.Fatalf("record h2: %v", err)
	}

	bdc := newFakeReviewBeads()
	// The draft-review bead is CLOSED (a prior review completed).
	bdc.findResults[key("o/r", 5)] = findResult{id: "dr-1", closed: true, found: true}
	// After reopen, it becomes ready.
	bdc.ready = []beads.DraftReviewRef{{ID: "dr-1", Repo: "o/r", Number: 5, Mine: true}}
	sp := &fakeSpawner{headSHA: "h2", writeDraft: true, reviewsDir: dir}
	sinks := &recordingSinks{}
	e := newReviewHookEngine(t, bdc, sp, sinks, db, dir)

	e.reviewHookCycle(ctx, NewTextLogger())

	// The closed bead must have been reopened.
	if len(bdc.reopened) != 1 || bdc.reopened[0] != "dr-1" {
		t.Fatalf("closed bead should be reopened on head advance, got %v", bdc.reopened)
	}
	// And produced against the new head h2.
	latest, err := db.LatestRevision(ctx, prID)
	if err != nil {
		t.Fatalf("LatestRevision: %v", err)
	}
	if latest.HeadSHA != "h2" || latest.ReviewedByAgentAt == "" {
		t.Fatalf("h2 should be agent-reviewed after re-review, got %+v", latest)
	}
}
