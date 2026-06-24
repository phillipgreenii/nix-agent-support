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
	if err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if fake.commentPosts != 1 {
		t.Fatalf("commentPosts = %d, want 1", fake.commentPosts)
	}
	if !marker.IsOurs(fake.lastBody) {
		t.Fatal("posted body not marker-stamped")
	}

	// Idempotent: response_id now set, second reconcile posts nothing.
	_ = p.Reconcile(ctx)
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
	if err := New(db, fake).Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if fake.commentPosts != 0 || fake.threadPosts != 0 {
		t.Fatal("managed_upstream feedback should not be replied to")
	}
}
