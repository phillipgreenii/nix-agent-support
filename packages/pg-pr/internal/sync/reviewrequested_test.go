package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// TestReviewRequestedOfSelf: the sync layer derives ReviewRequestedOfMe from the
// provider's raw RequestedReviewers against the configured self login.
func TestReviewRequestedOfSelf(t *testing.T) {
	if !reviewRequestedOfSelf("me", []string{"you", "me"}) {
		t.Error("self among requested reviewers should be true")
	}
	if reviewRequestedOfSelf("me", []string{"you"}) {
		t.Error("self not requested should be false")
	}
	if reviewRequestedOfSelf("", []string{"me"}) {
		t.Error("empty self should be false")
	}
	if reviewRequestedOfSelf("me", nil) {
		t.Error("no requested reviewers should be false")
	}
}

// TestAssignedToSelf mirrors TestReviewRequestedOfSelf one-for-one: pure data
// plumbing for assignedToSelf, the assignee analog of reviewRequestedOfSelf.
// This leaf deliberately does not wire it into any retrieval bucket
// (internal/sync/detector.go) or snapshot predicate — that is the sibling
// "assignee-to-me" leaf's job.
func TestAssignedToSelf(t *testing.T) {
	if !assignedToSelf("me", []string{"teammate", "me"}) {
		t.Error("self among assignees should be true")
	}
	if assignedToSelf("me", []string{"teammate"}) {
		t.Error("self not assigned should be false")
	}
	if assignedToSelf("", []string{"me"}) {
		t.Error("empty self should be false")
	}
	if assignedToSelf("me", nil) {
		t.Error("no assignees should be false")
	}
	// Exact-login match only: no case-insensitive or substring match.
	if assignedToSelf("me", []string{"Me"}) {
		t.Error("case-differing login should not match")
	}
	if assignedToSelf("me", []string{"teammate-me"}) {
		t.Error("substring login should not match")
	}
}

// TestBuildPRInput_DerivesReviewRequestedOfMe proves the derivation lives in
// buildPRInput — the single convergence point BOTH the daemon per-PR refresh AND
// the one-shot full-sync snapshot paths call. Deriving here (rather than only in
// refreshPR) keeps the two paths consistent, so the one-shot `pg-pr sync` snapshot
// no longer silently omits the flag (pg2-ynhr.13 B5 review #2 / B2 review #2).
func TestBuildPRInput_DerivesReviewRequestedOfMe(t *testing.T) {
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: &refreshFakeBeads{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rcfg := config.RepoConfig{Remote: "o/r"}

	// self among the provider's raw RequestedReviewers -> true
	in := e.buildPRInput(context.Background(),
		api.PR{Repo: "o/r", Number: 1, Author: "teammate", RequestedReviewers: []string{"other", "me"}},
		nil, &refreshFakeBeads{}, nil, rcfg, "")
	if !in.PR.ReviewRequestedOfMe {
		t.Error("ReviewRequestedOfMe must be true when self is among RequestedReviewers")
	}

	// self absent -> false
	in = e.buildPRInput(context.Background(),
		api.PR{Repo: "o/r", Number: 2, Author: "teammate", RequestedReviewers: []string{"other"}},
		nil, &refreshFakeBeads{}, nil, rcfg, "")
	if in.PR.ReviewRequestedOfMe {
		t.Error("ReviewRequestedOfMe must be false when self is not among RequestedReviewers")
	}
}

// TestRefreshPR_DerivesReviewRequestedOfMe proves the end-to-end daemon wiring
// (pg2-ynhr.13 B2 review #1): when the PR's reviewRequests (RequestedReviewers)
// include the configured self login, the snapshot input refreshPR yields carries
// ReviewRequestedOfMe==true; when absent, false. Previously proven only by
// inspection.
func TestRefreshPR_DerivesReviewRequestedOfMe(t *testing.T) {
	cases := []struct {
		name      string
		number    int
		requested []string
		want      bool
	}{
		{"requested of me", 21, []string{"other", "me"}, true},
		{"not requested", 22, []string{"other"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := store.OpenForTest(t)
			pr := api.PR{
				Repo: "o/r", Number: tc.number, State: "open",
				Author: "teammate", URL: "https://github.com/o/r/pull/1",
				RequestedReviewers: tc.requested,
			}
			e := newRefreshEngineWithStore(t, "me", &refreshFakeBeads{}, pr, db)
			in, err := e.refreshPR(context.Background(), "o/r", tc.number)
			if err != nil {
				t.Fatalf("refreshPR: %v", err)
			}
			if in == nil {
				t.Fatal("active team PR must yield a non-nil snapshot input")
			}
			if in.PR.ReviewRequestedOfMe != tc.want {
				t.Errorf("ReviewRequestedOfMe = %v, want %v", in.PR.ReviewRequestedOfMe, tc.want)
			}
		})
	}
}
