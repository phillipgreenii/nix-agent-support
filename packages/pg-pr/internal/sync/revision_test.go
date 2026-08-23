package sync

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fixedClock returns a deterministic clock for tests.
func fixedClock() func() time.Time {
	t := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return func() time.Time { return t }
}

const fixedRFC3339 = "2026-01-02T03:04:05Z"

func TestCIRollupFromSync(t *testing.T) {
	fixed := fixedClock()

	tests := []struct {
		name        string
		runs        []api.CIRun
		wantState   string
		wantPassed  int
		wantFailed  int
		wantPending int
	}{
		{
			name:      "empty slice → none",
			runs:      []api.CIRun{},
			wantState: "none",
		},
		{
			name: "all completed+success → success",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "success"},
				{Status: "completed", Conclusion: "success"},
			},
			wantState:  "success",
			wantPassed: 2,
		},
		{
			name: "any not-completed (pending) with rest success → pending",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "success"},
				{Status: "in_progress", Conclusion: ""},
			},
			wantState:   "pending",
			wantPassed:  1,
			wantPending: 1,
		},
		{
			name: "any completed+failure → failure",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "failure"},
				{Status: "completed", Conclusion: "success"},
			},
			wantState:  "failure",
			wantPassed: 1,
			wantFailed: 1,
		},
		{
			name: "mixed failure + pending → failure dominates",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "failure"},
				{Status: "queued", Conclusion: ""},
				{Status: "completed", Conclusion: "success"},
			},
			wantState:   "failure",
			wantPassed:  1,
			wantFailed:  1,
			wantPending: 1,
		},
		{
			name: "completed+error alongside success → counted as failed, yields failure",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "success"},
				{Status: "completed", Conclusion: "error"},
			},
			wantState:  "failure",
			wantPassed: 1,
			wantFailed: 1,
		},
		{
			name: "policy-bot failure excluded → success",
			runs: []api.CIRun{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "policy-bot: approval required", Status: "completed", Conclusion: "failure"},
			},
			wantState:  "success",
			wantPassed: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ciRollupFromSync(tc.runs, fixed, cirollup.NewExcluder([]string{"^policy-bot"}))

			if got.State != tc.wantState {
				t.Errorf("State: got %q want %q", got.State, tc.wantState)
			}
			if got.Passed != tc.wantPassed {
				t.Errorf("Passed: got %d want %d", got.Passed, tc.wantPassed)
			}
			if got.Failed != tc.wantFailed {
				t.Errorf("Failed: got %d want %d", got.Failed, tc.wantFailed)
			}
			if got.Pending != tc.wantPending {
				t.Errorf("Pending: got %d want %d", got.Pending, tc.wantPending)
			}
			// Assert the injectable clock is actually used (not time.Now).
			if got.CapturedAt != fixedRFC3339 {
				t.Errorf("CapturedAt: got %q want %q (injectable clock not used)", got.CapturedAt, fixedRFC3339)
			}
		})
	}
}

func TestCIRollupFromSync_NilClockDoesNotPanic(t *testing.T) {
	// Passing nil must not panic; it should fall back to time.Now.
	got := ciRollupFromSync([]api.CIRun{}, nil, nil)
	if got.State != "none" {
		t.Errorf("State: got %q want \"none\"", got.State)
	}
	if got.CapturedAt == "" {
		t.Error("CapturedAt must not be empty when nil clock is passed")
	}
}

func TestMySubmittedReviews(t *testing.T) {
	const self = "phillipg"
	const commitOID = "abc123"
	const submittedAt = "2026-03-15T10:00:00Z"

	t.Run("empty self → nil", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "APPROVED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, "")
		if got != nil {
			t.Errorf("expected nil for empty self, got %+v", got)
		}
	})

	t.Run("review by different author → skipped", func(t *testing.T) {
		reviews := []api.Review{{Author: "other", State: "APPROVED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 0 {
			t.Errorf("expected 0 results, got %+v", got)
		}
	})

	// Regression guard for pg2-4dz88.1.7: DISMISSED used to fall through the
	// mapping's `default: continue` and vanish, so a self review that DID
	// approve was indistinguishable from one that never happened. It must now
	// come back as a STALE approval (INV-APPROVAL-3).
	t.Run("DISMISSED state → stale approval, NOT dropped", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "DISMISSED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("DISMISSED must survive the mapping as a stale approval, got %d: %+v", len(got), got)
		}
		if got[0].State != "approved" {
			t.Errorf("State: got %q want \"approved\" (a dismissed review is a stale APPROVAL)", got[0].State)
		}
		if !got[0].Dismissed {
			t.Errorf("Dismissed: got false want true — without it the row is indistinguishable from a current approval")
		}
		if got[0].Approver != self || got[0].CommitSHA != commitOID || got[0].SubmittedAt != submittedAt {
			t.Errorf("entry = %+v, want Approver=%s CommitSHA=%s SubmittedAt=%s", got[0], self, commitOID, submittedAt)
		}
	})

	t.Run("APPROVED is NOT marked dismissed", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "APPROVED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("expected 1 result, got %d: %+v", len(got), got)
		}
		if got[0].Dismissed {
			t.Errorf("a current APPROVED review must not be marked dismissed: %+v", got[0])
		}
	})

	t.Run("PENDING state → skipped", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "PENDING", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 0 {
			t.Errorf("PENDING must be skipped, got %+v", got)
		}
	})

	t.Run("unmapped state → skipped", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "UNKNOWN_STATE", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 0 {
			t.Errorf("unmapped state must be skipped, got %+v", got)
		}
	})

	t.Run("APPROVED → approved", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "APPROVED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("expected 1 result, got %d: %+v", len(got), got)
		}
		if got[0].Approver != self {
			t.Errorf("Approver: got %q want %q (pg2-4dz88.1.5 per-approver feed)", got[0].Approver, self)
		}
		if got[0].State != "approved" {
			t.Errorf("State: got %q want \"approved\"", got[0].State)
		}
		if got[0].CommitSHA != commitOID {
			t.Errorf("CommitSHA: got %q want %q", got[0].CommitSHA, commitOID)
		}
		if got[0].SubmittedAt != submittedAt {
			t.Errorf("SubmittedAt: got %q want %q", got[0].SubmittedAt, submittedAt)
		}
	})

	t.Run("CHANGES_REQUESTED → changes-requested", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "CHANGES_REQUESTED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("expected 1 result, got %d: %+v", len(got), got)
		}
		if got[0].State != "changes-requested" {
			t.Errorf("State: got %q want \"changes-requested\"", got[0].State)
		}
	})

	t.Run("COMMENTED → commented", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "COMMENTED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("expected 1 result, got %d: %+v", len(got), got)
		}
		if got[0].State != "commented" {
			t.Errorf("State: got %q want \"commented\"", got[0].State)
		}
	})

	t.Run("multiple matching reviews → multiple entries order preserved", func(t *testing.T) {
		reviews := []api.Review{
			{Author: self, State: "APPROVED", CommitOID: "sha-1", SubmittedAt: "2026-01-01T00:00:00Z"},
			{Author: "other", State: "APPROVED", CommitOID: "sha-skip", SubmittedAt: "2026-01-02T00:00:00Z"},
			{Author: self, State: "COMMENTED", CommitOID: "sha-2", SubmittedAt: "2026-01-03T00:00:00Z"},
		}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 2 {
			t.Fatalf("expected 2 results (other skipped), got %d: %+v", len(got), got)
		}
		if got[0].CommitSHA != "sha-1" || got[0].State != "approved" {
			t.Errorf("first entry: got CommitSHA=%q State=%q; want sha-1/approved", got[0].CommitSHA, got[0].State)
		}
		if got[1].CommitSHA != "sha-2" || got[1].State != "commented" {
			t.Errorf("second entry: got CommitSHA=%q State=%q; want sha-2/commented", got[1].CommitSHA, got[1].State)
		}
	})
}

// TestIngestFeedbackToStore_WritesPerApproverRows is the regression-equivalence
// test for pg2-4dz88.1.5's ported write path: for the SAME inputs
// mySubmittedReviews/othersApprovedReviews handle (a self-APPROVED review and a
// teammate-APPROVED review), ingestFeedbackToStore must land BOTH as distinct
// rows in the pr_approval table, correctly attributed to their own approver
// login (proving self is never conflated with a teammate, X3).
func TestIngestFeedbackToStore_WritesPerApproverRows(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 7, State: "open", Author: "alice", // teammate-authored
		HeadSHA: "sha-head", BaseSHA: "sha-base",
		URL: "https://github.com/o/r/pull/7",
	}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Reviews: []api.Review{
			// The viewer's OWN approval.
			{Author: "phillipg", State: "APPROVED", CommitOID: "sha-head", SubmittedAt: "2026-07-01T00:00:00Z"},
			// A teammate's approval.
			{Author: "bob", State: "APPROVED", CommitOID: "sha-head", SubmittedAt: "2026-07-02T00:00:00Z"},
		},
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

	storedPR, err := db.GetPR(ctx, "o/r", 7)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: pr=%v err=%v", storedPR, err)
	}

	// --- New table: both observations land as distinct pr_approval rows. ---
	approvals, err := db.ListApprovals(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("want 2 pr_approval rows (self + teammate), got %d: %+v", len(approvals), approvals)
	}
	byApprover := map[string]store.Approval{}
	for _, a := range approvals {
		byApprover[a.Approver] = a
	}
	self, ok := byApprover["phillipg"]
	if !ok {
		t.Fatalf("no pr_approval row for self (phillipg): %+v", approvals)
	}
	if self.State != "approved" || self.HeadSHA != "sha-head" || self.ObservedAt != "2026-07-01T00:00:00Z" {
		t.Errorf("self pr_approval row = %+v, want state=approved head_sha=sha-head observed_at=2026-07-01T00:00:00Z", self)
	}
	teammate, ok := byApprover["bob"]
	if !ok {
		t.Fatalf("no pr_approval row for teammate (bob): %+v", approvals)
	}
	if teammate.State != "approved" || teammate.HeadSHA != "sha-head" || teammate.ObservedAt != "2026-07-02T00:00:00Z" {
		t.Errorf("teammate pr_approval row = %+v, want state=approved head_sha=sha-head observed_at=2026-07-02T00:00:00Z", teammate)
	}
}

// newApprovalIngestEngine builds an Engine wired to db with selfLogin as the
// viewer, for the pr_approval ingest tests below.
func newApprovalIngestEngine(t *testing.T, db *store.DB, selfLogin string) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: selfLogin,
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

// A DISMISSED review — self or teammate — lands as a STALE per-approver row
// rather than vanishing (pg2-4dz88.1.7, INV-APPROVAL-3). The dismissals here
// sit AT the PR's current head, so nothing but the recorded dismissal can make
// them read stale.
func TestIngestFeedbackToStore_DismissedReviewsLandAsStaleApprovals(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	const head = "sha-head"
	pr := api.PR{
		Repo: "o/r", Number: 9, State: "open", Author: "alice", // teammate-authored
		HeadSHA: head, BaseSHA: "sha-base",
		URL: "https://github.com/o/r/pull/9",
	}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Reviews: []api.Review{
			// The viewer's own approval, dismissed by the host.
			{Author: "me", State: "DISMISSED", CommitOID: head, SubmittedAt: "2026-07-01T00:00:00Z"},
			// A teammate's approval, dismissed by the host.
			{Author: "teammate", State: "DISMISSED", CommitOID: head, SubmittedAt: "2026-07-02T00:00:00Z"},
		},
	}

	e := newApprovalIngestEngine(t, db, "me")
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "o/r", 9)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: pr=%v err=%v", storedPR, err)
	}

	approvals, err := db.ListApprovals(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("want 2 pr_approval rows (self + teammate dismissals), got %d: %+v", len(approvals), approvals)
	}
	byApprover := map[string]store.Approval{}
	for _, a := range approvals {
		byApprover[a.Approver] = a
	}
	for _, want := range []struct {
		approver   string
		observedAt string
	}{
		{"me", "2026-07-01T00:00:00Z"},
		{"teammate", "2026-07-02T00:00:00Z"},
	} {
		got, ok := byApprover[want.approver]
		if !ok {
			t.Fatalf("no pr_approval row for %q — a dismissed review must never vanish: %+v", want.approver, approvals)
		}
		if got.State != "approved" {
			t.Errorf("%s: State = %q, want approved (a dismissed review is a stale APPROVAL)", want.approver, got.State)
		}
		if !got.Dismissed {
			t.Errorf("%s: Dismissed = false, want true (row=%+v)", want.approver, got)
		}
		if got.HeadSHA != head || got.ObservedAt != want.observedAt {
			t.Errorf("%s: row = %+v, want head_sha=%s observed_at=%s", want.approver, got, head, want.observedAt)
		}
		if !got.IsStale(head) {
			t.Errorf("%s: a dismissed approval AT the current head must still read stale (row=%+v)", want.approver, got)
		}
	}
}

// A DISMISSED review must not clobber a later, still-current approval from the
// SAME approver at a newer head. Both orderings are asserted, for self and for
// a teammate: dismiss-then-reapprove ends FRESH, reapprove-then-dismiss ends
// STALE (pg2-4dz88.1.7).
func TestIngestFeedbackToStore_DismissAndReapproveOrdering(t *testing.T) {
	const self = "me"
	const oldHead = "sha-old"
	const head = "sha-head"

	tests := []struct {
		name          string
		approver      string
		states        []string // review states in the order the host reports them
		commits       []string // the commit each review was submitted against
		wantHeadSHA   string
		wantDismissed bool
		wantStale     bool
	}{
		{
			name:          "self: dismiss then reapprove at a newer head → fresh",
			approver:      self,
			states:        []string{"DISMISSED", "APPROVED"},
			commits:       []string{oldHead, head},
			wantHeadSHA:   head,
			wantDismissed: false,
			wantStale:     false,
		},
		{
			name:          "self: reapprove then dismiss → stale",
			approver:      self,
			states:        []string{"APPROVED", "DISMISSED"},
			commits:       []string{head, head},
			wantHeadSHA:   head,
			wantDismissed: true,
			wantStale:     true,
		},
		{
			name:          "teammate: dismiss then reapprove at a newer head → fresh",
			approver:      "teammate",
			states:        []string{"DISMISSED", "APPROVED"},
			commits:       []string{oldHead, head},
			wantHeadSHA:   head,
			wantDismissed: false,
			wantStale:     false,
		},
		{
			name:          "teammate: reapprove then dismiss → stale",
			approver:      "teammate",
			states:        []string{"APPROVED", "DISMISSED"},
			commits:       []string{head, head},
			wantHeadSHA:   head,
			wantDismissed: true,
			wantStale:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := store.OpenForTest(t)

			pr := api.PR{
				Repo: "o/r", Number: 11, State: "open", Author: "alice",
				HeadSHA: head, BaseSHA: "sha-base",
				URL: "https://github.com/o/r/pull/11",
			}
			reviews := make([]api.Review, 0, len(tc.states))
			for i, st := range tc.states {
				reviews = append(reviews, api.Review{
					Author: tc.approver, State: st, CommitOID: tc.commits[i],
					// Ascending timestamps, matching the host's report order.
					SubmittedAt: []string{"2026-07-01T00:00:00Z", "2026-07-02T00:00:00Z"}[i],
				})
			}
			enriched := &vcs.EnrichedPR{PR: pr, Reviews: reviews}

			e := newApprovalIngestEngine(t, db, self)
			if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
				t.Fatalf("ingestFeedbackToStore: %v", err)
			}

			storedPR, err := db.GetPR(ctx, "o/r", 11)
			if err != nil || storedPR == nil {
				t.Fatalf("GetPR: pr=%v err=%v", storedPR, err)
			}
			approvals, err := db.ListApprovals(ctx, storedPR.ID)
			if err != nil {
				t.Fatalf("ListApprovals: %v", err)
			}
			if len(approvals) != 1 {
				t.Fatalf("the same approver must occupy ONE row; got %d: %+v", len(approvals), approvals)
			}
			got := approvals[0]
			if got.Approver != tc.approver {
				t.Fatalf("Approver = %q, want %q", got.Approver, tc.approver)
			}
			if got.State != "approved" {
				t.Errorf("State = %q, want approved", got.State)
			}
			if got.HeadSHA != tc.wantHeadSHA {
				t.Errorf("HeadSHA = %q, want %q (the LAST observation wins)", got.HeadSHA, tc.wantHeadSHA)
			}
			if got.Dismissed != tc.wantDismissed {
				t.Errorf("Dismissed = %v, want %v (row=%+v)", got.Dismissed, tc.wantDismissed, got)
			}
			if got.IsStale(head) != tc.wantStale {
				t.Errorf("IsStale(%q) = %v, want %v (row=%+v)", head, got.IsStale(head), tc.wantStale, got)
			}
		})
	}
}
