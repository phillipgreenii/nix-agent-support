package store

import (
	"context"
	"testing"
)

// newUnaddressedPR seeds a PR row for the unaddressed-feedback tests.
func newUnaddressedPR(t *testing.T, db *DB, author string) (context.Context, int64) {
	t.Helper()
	ctx := context.Background()
	prID, err := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 7, Ownership: "mine", State: "open",
		Author: author, HeadSHA: "sha-head",
	})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	return ctx, prID
}

// TestUnaddressedFeedbackExcludesPRAuthor reproduces the self-feeding loop
// (pg2-onq1e): the PR author's OWN comments — which is what an agent reply
// posted on their behalf looks like, because pg-pr posts under the user's own
// login — must NOT count as feedback needing processing. Pre-fix every such
// comment produced a feedback.created event and therefore a fresh,
// substance-free process-feedback bead asking the agent to process its own
// replies.
//
// The login comparison must be case-insensitive: GitHub logins are.
func TestUnaddressedFeedbackExcludesPRAuthor(t *testing.T) {
	db := newTestDB(t)
	ctx, prID := newUnaddressedPR(t, db, "phillipg")

	// Two comments authored by the PR author, one of them with different casing.
	for _, tc := range []struct{ fp, login string }{
		{"fp-author-1", "phillipg"},
		{"fp-author-2", "PhillipG"},
	} {
		if _, err := db.UpsertFeedback(ctx, Feedback{
			PRID: prID, Kind: "pr-comments", Fingerprint: tc.fp,
			AuthorLogin: tc.login, AuthorKind: "human", Body: "replying to review",
		}); err != nil {
			t.Fatalf("UpsertFeedback %s: %v", tc.fp, err)
		}
	}

	sum, err := db.UnaddressedFeedback(ctx, prID, "phillipg")
	if err != nil {
		t.Fatalf("UnaddressedFeedback: %v", err)
	}
	if sum.Unaddressed != 0 {
		t.Fatalf("author's own comments counted as unaddressed feedback: got %d want 0 (summary %+v)", sum.Unaddressed, sum)
	}
	if sum.Digest != "" {
		t.Errorf("empty set must carry no digest, got %q", sum.Digest)
	}

	// A reviewer comment on the same PR IS unaddressed.
	if _, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-reviewer",
		AuthorLogin: "alice", AuthorKind: "human", Body: "please fix",
	}); err != nil {
		t.Fatalf("UpsertFeedback reviewer: %v", err)
	}
	sum, err = db.UnaddressedFeedback(ctx, prID, "phillipg")
	if err != nil {
		t.Fatalf("UnaddressedFeedback: %v", err)
	}
	if sum.Unaddressed != 1 || sum.ByKind["pr-comments"] != 1 {
		t.Fatalf("reviewer comment must count: got %+v", sum)
	}
	if len(sum.Reviewers) != 1 || sum.Reviewers[0] != "alice" {
		t.Errorf("reviewers: got %v want [alice]", sum.Reviewers)
	}
}

// TestUnaddressedFeedbackExcludesOursExceptSelfReview pins the second,
// independent guard against agent-authored content: marker-detected "ours" rows
// (an agent reply on a TEAMMATE's PR, where the author login is not mine) are
// excluded, while 'self-review' rows — ours BY CONSTRUCTION and ingested
// precisely so they get processed — are kept.
func TestUnaddressedFeedbackExcludesOursExceptSelfReview(t *testing.T) {
	db := newTestDB(t)
	ctx, prID := newUnaddressedPR(t, db, "teammate")

	if _, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-ours",
		AuthorLogin: "phillipg", AuthorKind: "agent", AgentName: "pg-pr", IsOurs: true,
	}); err != nil {
		t.Fatalf("UpsertFeedback ours: %v", err)
	}
	if _, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "self-review", Fingerprint: "fp-self",
		AuthorLogin: "pg-pr", AuthorKind: "agent", AgentName: "pg-pr", IsOurs: true,
	}); err != nil {
		t.Fatalf("UpsertFeedback self-review: %v", err)
	}

	sum, err := db.UnaddressedFeedback(ctx, prID, "teammate")
	if err != nil {
		t.Fatalf("UnaddressedFeedback: %v", err)
	}
	if sum.Unaddressed != 1 || sum.ByKind["self-review"] != 1 {
		t.Fatalf("expected only the self-review row to count, got %+v", sum)
	}
}

// TestUnaddressedFeedbackExcludesProcessedAndInactive verifies the status and
// activity filters: anything the agent already handled, and anything upstream
// marked stale, is not work to process.
func TestUnaddressedFeedbackExcludesProcessedAndInactive(t *testing.T) {
	db := newTestDB(t)
	ctx, prID := newUnaddressedPR(t, db, "phillipg")

	cases := []struct {
		name    string
		row     Feedback
		counted bool
	}{
		{"new", Feedback{Kind: "pr-comments", Fingerprint: "fp-new", Status: "new", AuthorLogin: "alice"}, true},
		{"presented", Feedback{Kind: "pr-comments", Fingerprint: "fp-pres", Status: "presented", AuthorLogin: "alice"}, true},
		{"dispositioned", Feedback{Kind: "pr-comments", Fingerprint: "fp-disp", Status: "dispositioned", AuthorLogin: "alice"}, false},
		{"replied", Feedback{Kind: "pr-comments", Fingerprint: "fp-rep", Status: "replied", AuthorLogin: "alice"}, false},
		{"resolved", Feedback{Kind: "pr-comments", Fingerprint: "fp-res", Status: "resolved", AuthorLogin: "alice"}, false},
		{"superseded", Feedback{Kind: "ci-failure", Fingerprint: "fp-sup", Status: "superseded"}, false},
		{"outdated thread", Feedback{Kind: "code-comment-thread", Fingerprint: "fp-out", File: "a.go", IsOutdated: true, AuthorLogin: "alice"}, false},
		{"minimized thread", Feedback{Kind: "code-comment-thread", Fingerprint: "fp-min", File: "a.go", IsMinimized: true, AuthorLogin: "alice"}, false},
	}

	wantCount := 0
	for _, tc := range cases {
		row := tc.row
		row.PRID = prID
		if _, err := db.UpsertFeedback(ctx, row); err != nil {
			t.Fatalf("UpsertFeedback %s: %v", tc.name, err)
		}
		if tc.counted {
			wantCount++
		}
	}

	sum, err := db.UnaddressedFeedback(ctx, prID, "phillipg")
	if err != nil {
		t.Fatalf("UnaddressedFeedback: %v", err)
	}
	if sum.Unaddressed != wantCount {
		t.Fatalf("unaddressed count: got %d want %d (summary %+v)", sum.Unaddressed, wantCount, sum)
	}
}

// TestUnaddressedFeedbackDigestTracksTheSet pins the "genuinely new feedback"
// test the closed-predecessor branch relies on: the digest is stable while the
// unaddressed SET is unchanged (so a re-sync writes nothing) and changes as soon
// as an item joins or leaves it.
func TestUnaddressedFeedbackDigestTracksTheSet(t *testing.T) {
	db := newTestDB(t)
	ctx, prID := newUnaddressedPR(t, db, "phillipg")

	if _, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-a", AuthorLogin: "alice",
	}); err != nil {
		t.Fatalf("UpsertFeedback a: %v", err)
	}
	first, err := db.UnaddressedFeedback(ctx, prID, "phillipg")
	if err != nil {
		t.Fatalf("UnaddressedFeedback: %v", err)
	}
	if first.Digest == "" {
		t.Fatal("expected a non-empty digest for a non-empty set")
	}

	// Re-observing the same item (an upsert, as every tick performs) must not
	// move the digest — otherwise every tick would look like "new feedback".
	if _, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-a", AuthorLogin: "alice", Body: "edited body",
	}); err != nil {
		t.Fatalf("UpsertFeedback a again: %v", err)
	}
	again, err := db.UnaddressedFeedback(ctx, prID, "phillipg")
	if err != nil {
		t.Fatalf("UnaddressedFeedback: %v", err)
	}
	if again.Digest != first.Digest {
		t.Fatalf("digest moved on a re-observation of the same set: %q -> %q", first.Digest, again.Digest)
	}

	// A genuinely new item must move it.
	if _, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-b", AuthorLogin: "bob",
	}); err != nil {
		t.Fatalf("UpsertFeedback b: %v", err)
	}
	grown, err := db.UnaddressedFeedback(ctx, prID, "phillipg")
	if err != nil {
		t.Fatalf("UnaddressedFeedback: %v", err)
	}
	if grown.Digest == first.Digest {
		t.Fatalf("digest unchanged after a new item joined the set: %q", grown.Digest)
	}
	if grown.Unaddressed != 2 {
		t.Errorf("unaddressed: got %d want 2", grown.Unaddressed)
	}
	if len(grown.Reviewers) != 2 || grown.Reviewers[0] != "alice" || grown.Reviewers[1] != "bob" {
		t.Errorf("reviewers must be sorted and de-duplicated: got %v", grown.Reviewers)
	}
}
