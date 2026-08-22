package sync

// Tests for the "unmatched verdict marker" observable signal (pg2-4dz88.1.11
// / docs/behavior/invariants.md's INV-APPROVAL-5): a bot verdict comment
// carrying a configured generation's BodyMarker but resolving no
// generation's grammar (verdict.Authority Pending) must increment
// telemetry.VerdictPendingTotal, labeled only by repo, and must be
// distinguishable from both a definite verdict (Approved/Withheld, which
// must NOT increment it) and a non-verdict comment (Authority Absent, which
// must also NOT increment it).
//
// All markers/patterns/logins here are synthetic and test-local, reusing
// approver_ingest_test.go's own fixtures (testVerdictMarker, cleanApprovedBody,
// problemsBody, pendingBody, noMarkerBody, testVerdictGenerations) — pg-pr is
// a PUBLIC repo (pg2-4dz88.1.6's constraint) and none of this is real vendor
// text.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/verdict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ----------------------------------------------------------------------
// Unit level: botVerdictApprovals's second return value
// ----------------------------------------------------------------------

// TestBotVerdictApprovals_PendingSurfacedInSecondReturn proves the sibling
// mechanism directly: a Pending winning comment produces NO
// botVerdictApproval but DOES produce a botVerdictPending carrying the
// approver login and the winning comment's effective timestamp.
func TestBotVerdictApprovals_PendingSurfacedInSecondReturn(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{ID: "c1", Author: "approver-one", UpdatedAt: "2026-01-01T00:00:00Z", Body: pendingBody()},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"})

	approvals, pending := botVerdictApprovals(comments, allowlist, clf)
	if len(approvals) != 0 {
		t.Errorf("want 0 approvals for a Pending-only comment, got %+v", approvals)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending entry, got %d: %+v", len(pending), pending)
	}
	if pending[0].Approver != "approver-one" {
		t.Errorf("pending[0].Approver = %q, want %q", pending[0].Approver, "approver-one")
	}
	if pending[0].ObservedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("pending[0].ObservedAt = %q, want %q", pending[0].ObservedAt, "2026-01-01T00:00:00Z")
	}
}

// TestBotVerdictApprovals_DefiniteVerdictNotPending proves an Approved or
// Withheld winning comment produces NO botVerdictPending entry — only
// Pending does.
func TestBotVerdictApprovals_DefiniteVerdictNotPending(t *testing.T) {
	clf := mustTestClassifier(t)
	allowlist := approverAllowlistSet([]string{"approver-one"})

	t.Run("approved", func(t *testing.T) {
		comments := []api.Comment{
			{ID: "c1", Author: "approver-one", UpdatedAt: "2026-01-01T00:00:00Z", Body: cleanApprovedBody()},
		}
		approvals, pending := botVerdictApprovals(comments, allowlist, clf)
		if len(approvals) != 1 || approvals[0].Result.Authority != verdict.Approved {
			t.Errorf("want 1 Approved approval, got %+v", approvals)
		}
		if len(pending) != 0 {
			t.Errorf("want 0 pending entries for an Approved comment, got %+v", pending)
		}
	})

	t.Run("withheld", func(t *testing.T) {
		comments := []api.Comment{
			{ID: "c1", Author: "approver-one", UpdatedAt: "2026-01-01T00:00:00Z", Body: problemsBody()},
		}
		approvals, pending := botVerdictApprovals(comments, allowlist, clf)
		if len(approvals) != 1 || approvals[0].Result.Authority != verdict.Withheld {
			t.Errorf("want 1 Withheld approval, got %+v", approvals)
		}
		if len(pending) != 0 {
			t.Errorf("want 0 pending entries for a Withheld comment, got %+v", pending)
		}
	})
}

// TestBotVerdictApprovals_AbsentNotPending proves a comment with no
// configured BodyMarker at all (Authority Absent) produces NEITHER a
// botVerdictApproval NOR a botVerdictPending — Absent is correctly not a
// verdict and must never trigger the unmatched-marker signal.
func TestBotVerdictApprovals_AbsentNotPending(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{ID: "c1", Author: "approver-one", UpdatedAt: "2026-01-01T00:00:00Z", Body: noMarkerBody()},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"})

	approvals, pending := botVerdictApprovals(comments, allowlist, clf)
	if len(approvals) != 0 {
		t.Errorf("want 0 approvals for a non-verdict comment, got %+v", approvals)
	}
	if len(pending) != 0 {
		t.Errorf("want 0 pending entries for a non-verdict comment (Absent), got %+v", pending)
	}
}

// ----------------------------------------------------------------------
// Integration level: ingestFeedbackToStore -> telemetry.VerdictPendingTotal
// ----------------------------------------------------------------------
//
// Each test below uses its own unique synthetic repo name as the counter's
// only label, so tests in this file (and any peers sharing the process-wide
// telemetry.DefaultRegistry) cannot pollute one another's counter reading.

// newVerdictPendingTestEngine builds an Engine wired for one repo with the
// synthetic test verdict generation, backed by a fresh in-memory store.
func newVerdictPendingTestEngine(t *testing.T, repo string) *Engine {
	t.Helper()
	db := store.OpenForTest(t)
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin:          "phillipg",
			Repos:              []config.RepoConfig{{Remote: repo, VCS: "github"}},
			ApproverAllowlist:  []string{"approver-one", "approver-two"},
			VerdictGenerations: testVerdictGenerations(),
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

// TestIngestFeedbackToStore_UnmatchedVerdictMarker_IncrementsCounterOnce
// proves a single synthetic comment carrying the anchor marker but matching
// no generation increments telemetry.VerdictPendingTotal for that repo
// exactly once.
func TestIngestFeedbackToStore_UnmatchedVerdictMarker_IncrementsCounterOnce(t *testing.T) {
	const repo = "synth/verdict-pending-once"
	ctx := context.Background()
	e := newVerdictPendingTestEngine(t, repo)

	before := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))

	pr := api.PR{Repo: repo, Number: 101, State: "open", Author: "someone-else", HeadSHA: "sha-1", BaseSHA: "sha-0"}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Comments: []api.Comment{
			{ID: "c1", Author: "approver-one", Path: "", UpdatedAt: "2026-01-01T00:00:00Z", Body: pendingBody()},
		},
	}

	if err := e.ingestFeedbackToStore(ctx, repo, pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	got := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))
	if got != before+1 {
		t.Errorf("VerdictPendingTotal(%q) = %v, want %v (before=%v +1)", repo, got, before+1, before)
	}
}

// TestIngestFeedbackToStore_UnmatchedVerdictMarker_ApprovedDoesNotIncrement
// proves a comment that DOES match a generation (Authority Approved) leaves
// the counter untouched.
func TestIngestFeedbackToStore_UnmatchedVerdictMarker_ApprovedDoesNotIncrement(t *testing.T) {
	const repo = "synth/verdict-pending-approved-noop"
	ctx := context.Background()
	e := newVerdictPendingTestEngine(t, repo)

	before := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))

	pr := api.PR{Repo: repo, Number: 102, State: "open", Author: "someone-else", HeadSHA: "sha-1", BaseSHA: "sha-0"}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Comments: []api.Comment{
			{ID: "c1", Author: "approver-one", Path: "", UpdatedAt: "2026-01-01T00:00:00Z", Body: cleanApprovedBody()},
		},
	}

	if err := e.ingestFeedbackToStore(ctx, repo, pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	got := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))
	if got != before {
		t.Errorf("VerdictPendingTotal(%q) = %v, want unchanged %v (a matched/Approved verdict must not count as pending)", repo, got, before)
	}
}

// TestIngestFeedbackToStore_UnmatchedVerdictMarker_AbsentDoesNotIncrement
// proves a comment with no configured marker at all (Authority Absent)
// leaves the counter untouched — Absent is correctly not a verdict and must
// not trigger this signal.
func TestIngestFeedbackToStore_UnmatchedVerdictMarker_AbsentDoesNotIncrement(t *testing.T) {
	const repo = "synth/verdict-pending-absent-noop"
	ctx := context.Background()
	e := newVerdictPendingTestEngine(t, repo)

	before := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))

	pr := api.PR{Repo: repo, Number: 103, State: "open", Author: "someone-else", HeadSHA: "sha-1", BaseSHA: "sha-0"}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Comments: []api.Comment{
			{ID: "c1", Author: "approver-one", Path: "", UpdatedAt: "2026-01-01T00:00:00Z", Body: noMarkerBody()},
		},
	}

	if err := e.ingestFeedbackToStore(ctx, repo, pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	got := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))
	if got != before {
		t.Errorf("VerdictPendingTotal(%q) = %v, want unchanged %v (a non-verdict comment must not count as pending)", repo, got, before)
	}
}

// TestIngestFeedbackToStore_UnmatchedVerdictMarker_TwoApproversIncrementTwice
// proves two DIFFERENT approvers each posting an unmatched-marker comment on
// the same PR, in the same ingest cycle, increment the (repo-labeled, not
// per-login) counter TWICE — not once — since the signal must fire per
// unmatched approver, not merely "at least one occurred this cycle".
func TestIngestFeedbackToStore_UnmatchedVerdictMarker_TwoApproversIncrementTwice(t *testing.T) {
	const repo = "synth/verdict-pending-twice"
	ctx := context.Background()
	e := newVerdictPendingTestEngine(t, repo)

	before := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))

	pr := api.PR{Repo: repo, Number: 104, State: "open", Author: "someone-else", HeadSHA: "sha-1", BaseSHA: "sha-0"}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Comments: []api.Comment{
			{ID: "c1", Author: "approver-one", Path: "", UpdatedAt: "2026-01-01T00:00:00Z", Body: pendingBody()},
			{ID: "c2", Author: "approver-two", Path: "", UpdatedAt: "2026-01-01T00:00:00Z", Body: pendingBody()},
		},
	}

	if err := e.ingestFeedbackToStore(ctx, repo, pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	got := testutil.ToFloat64(telemetry.VerdictPendingTotal.WithLabelValues(repo))
	if got != before+2 {
		t.Errorf("VerdictPendingTotal(%q) = %v, want %v (before=%v +2, one per unmatched approver)", repo, got, before+2, before)
	}
}
