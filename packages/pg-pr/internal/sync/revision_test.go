package sync

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
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

	t.Run("DISMISSED state → skipped", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "DISMISSED", CommitOID: commitOID, SubmittedAt: submittedAt}}
		got := mySubmittedReviews(reviews, self)
		if len(got) != 0 {
			t.Errorf("DISMISSED must be skipped, got %+v", got)
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
