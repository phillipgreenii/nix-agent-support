package replyposter

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

type fakeReplier struct {
	threadPosts  int
	commentPosts int
	lastBody     string
	id           string
}

func (f *fakeReplier) ReplyToThread(ctx context.Context, repo, threadID, body string) (*api.Comment, error) {
	f.threadPosts++
	f.lastBody = body
	return &api.Comment{ID: f.id}, nil
}

func (f *fakeReplier) AddComment(ctx context.Context, repo string, number int, body string) (*api.Comment, error) {
	f.commentPosts++
	f.lastBody = body
	return &api.Comment{ID: f.id}, nil
}

func TestReconcilePostsPendingReplyOnce(t *testing.T) {
	db := store.OpenForTest(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})
	id, _ := db.UpsertFeedback(ctx, store.Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "f", ExternalID: "c1"})
	_ = db.SetDisposition(ctx, id, "wont-fix", "intentional", "Not acting — intentional.")

	fake := &fakeReplier{id: "resp-9"}
	p := New(db, fake)
	if _, err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if fake.commentPosts != 1 {
		t.Fatalf("commentPosts = %d, want 1", fake.commentPosts)
	}
	if !marker.IsOurs(fake.lastBody) {
		t.Fatal("posted body not marker-stamped")
	}

	// Idempotent: response_id now set, second reconcile posts nothing.
	_, _ = p.Reconcile(ctx)
	if fake.commentPosts != 1 {
		t.Fatalf("second reconcile posted again: commentPosts = %d", fake.commentPosts)
	}
}

func TestReconcileSkipsManagedUpstream(t *testing.T) {
	db := store.OpenForTest(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})
	id, _ := db.UpsertFeedback(ctx, store.Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "f", ManagedUpstream: true})
	_ = db.SetDisposition(ctx, id, "no-action", "", "noted")

	fake := &fakeReplier{id: "x"}
	if _, err := New(db, fake).Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if fake.commentPosts != 0 || fake.threadPosts != 0 {
		t.Fatal("managed_upstream feedback should not be replied to")
	}
}

func TestReconcileReturnsCount(t *testing.T) {
	db := store.OpenForTest(t)
	ctx := context.Background()

	// Two pending replies on mine-owned PRs.
	prID1, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 10, Ownership: "mine", State: "open"})
	id1, _ := db.UpsertFeedback(ctx, store.Feedback{PRID: prID1, Kind: "pr-comments", Fingerprint: "f1", ExternalID: "c10"})
	_ = db.SetDisposition(ctx, id1, "wont-fix", "intentional", "Not acting — intentional.")

	prID2, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 11, Ownership: "mine", State: "open"})
	id2, _ := db.UpsertFeedback(ctx, store.Feedback{PRID: prID2, Kind: "pr-comments", Fingerprint: "f2", ExternalID: "c11"})
	_ = db.SetDisposition(ctx, id2, "wont-fix", "intentional", "Not acting — intentional.")

	fake := &fakeReplier{id: "resp-count"}
	n, err := New(db, fake).Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 replies posted, got %d", n)
	}
}

// TestReconcileOwnershipGate verifies M2: pending replies on team-owned PRs are
// NOT posted; only mine-owned PRs trigger auto-reply.
func TestReconcileOwnershipGate(t *testing.T) {
	db := store.OpenForTest(t)
	ctx := context.Background()

	// team-owned PR: reply must be skipped.
	teamPRID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 2, Ownership: "team", State: "open"})
	teamFBID, _ := db.UpsertFeedback(ctx, store.Feedback{PRID: teamPRID, Kind: "pr-comments", Fingerprint: "f-team", ExternalID: "ct1"})
	_ = db.SetDisposition(ctx, teamFBID, "wont-fix", "noted", "Not acting on team PR.")

	// mine-owned PR: reply must be posted.
	minePRID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 3, Ownership: "mine", State: "open"})
	mineFBID, _ := db.UpsertFeedback(ctx, store.Feedback{PRID: minePRID, Kind: "pr-comments", Fingerprint: "f-mine", ExternalID: "cm1"})
	_ = db.SetDisposition(ctx, mineFBID, "wont-fix", "noted", "Not acting — intentional.")

	fake := &fakeReplier{id: "resp-mine"}
	p := New(db, fake)
	if _, err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Only the mine-owned PR's reply should have been posted (1 comment post).
	if fake.commentPosts != 1 {
		t.Fatalf("commentPosts = %d, want 1 (only mine-owned PR)", fake.commentPosts)
	}
	if fake.threadPosts != 0 {
		t.Fatalf("threadPosts = %d, want 0", fake.threadPosts)
	}

	// Team-owned PR's feedback must still be pending (response_id unset).
	teamFB, err := db.GetFeedback(ctx, teamFBID)
	if err != nil || teamFB == nil {
		t.Fatalf("GetFeedback team: %v", err)
	}
	if teamFB.ResponseID != "" {
		t.Errorf("team-owned PR feedback must not have response_id set, got %q", teamFB.ResponseID)
	}
}
