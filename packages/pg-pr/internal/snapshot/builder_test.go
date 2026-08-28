package snapshot

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/checkinterpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prdeps"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// Placeholder fixtures for the agent-approval tests in this file (originally
// introduced for the review-state approval guard tests, pg2-4dz88.9; reused
// file-wide by pg2-mp02f to de-literalise the pre-existing tests that used to
// hardcode a real vendor bot login/verdict regex). Generic login and anchored
// pattern only — no real bot login or verdict phrasing, per this repo's
// public-repo identifier rule.
const (
	approvalGuardAgentLogin = "agent-one"
	approvalGuardPattern    = `(?im)^ok-to-land$`
	matchingBody            = "ok-to-land"
	nonMatchingBody         = "no-opinion"
)

// TestBuildSplitsMineFromReview verifies the partition under the broadened "PRs
// to Review" contract (6b/B5): PRs authored by Self go to Mine (even drafts);
// every OTHER non-draft PR that still carries a live match reason (team-authored
// ∪ requested ∪ labeled) goes to the review set (Team). Reasons are sourced from
// ingest; the builder re-checks they hold (B5 review #1). Others' drafts, and
// non-mine PRs with no live reason, are excluded. LinesChanged = Additions +
// Deletions.
func TestBuildSplitsMineFromReview(t *testing.T) {
	reg, _ := agentregistry.New(nil) // empty registry

	now := time.Now()
	in := BuilderInput{
		GeneratedAt:         now,
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{"bob", "carol"},
		Registry:            reg,
		PRs: []PRInput{
			// Mine: authored by Self
			{PR: api.PR{Repo: "org/repo", Number: 1, Author: "alice", Title: "my PR", URL: "u1"}, Ownership: ownership.Mine},
			// To-review: non-draft team member
			{PR: api.PR{Repo: "org/repo", Number: 2, Author: "bob", Title: "bob PR", URL: "u2", Draft: false, Additions: 10, Deletions: 5, ChangedFiles: 3}, Ownership: ownership.Team},
			// Excluded: a DRAFT that isn't mine
			{PR: api.PR{Repo: "org/repo", Number: 3, Author: "carol", Title: "carol draft", URL: "u3", Draft: true}, Ownership: ownership.Team},
			// To-review: non-team, non-self, non-draft — ingest surfaced it because
			// review was requested of me (a live reason, so it survives the B5 guard)
			{PR: api.PR{Repo: "org/repo", Number: 4, Author: "zara", Title: "review PR", URL: "u4", ReviewRequestedOfMe: true}, Ownership: ownership.Team},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 || snap.Mine[0].Number != 1 {
		t.Fatalf("expected Mine=[#1], got %+v", snap.Mine)
	}
	got := map[int]bool{}
	for _, r := range snap.Team {
		got[r.Number] = true
	}
	if len(snap.Team) != 2 || !got[2] || !got[4] {
		t.Fatalf("expected PRs-to-Review = {#2,#4}, got %+v", snap.Team)
	}
	if got[3] {
		t.Errorf("a non-mine draft must be excluded from PRs to Review")
	}
	for _, r := range snap.Team {
		if r.Number == 2 && (r.LinesChanged != 15 || r.FilesChanged != 3) {
			t.Errorf("bob row: LinesChanged=%d FilesChanged=%d, want 15/3", r.LinesChanged, r.FilesChanged)
		}
	}
}

// TestBuild_MatchReasons: MatchReason explains why each PR is in the review set —
// team-authored, review-requested (ReviewRequestedOfMe), reviewed-by-me (a
// submitted review of mine), assigned-to-me (AssignedToMe), one label:<name>
// per matched watch label — and a PR matching several criteria carries all
// reasons, in that fixed order.
func TestBuild_MatchReasons(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		WatchLabels: []string{"lbl-one", "lbl-two"},
		Registry:    reg,
		PRs: []PRInput{
			{PR: api.PR{Repo: "o/r", Number: 2, Author: "bob"}, Ownership: ownership.Team},                                                                             // team-authored
			{PR: api.PR{Repo: "o/r", Number: 5, Author: "zara", ReviewRequestedOfMe: true}, Ownership: ownership.Team},                                                 // requested
			{PR: api.PR{Repo: "o/r", Number: 6, Author: "yin", Labels: []string{"lbl-one", "unrelated"}}, Ownership: ownership.Team},                                   // labeled
			{PR: api.PR{Repo: "o/r", Number: 7, Author: "bob", ReviewRequestedOfMe: true, AssignedToMe: true, Labels: []string{"lbl-two"}}, Ownership: ownership.Team}, // authored+requested+assigned+labeled
			{ // reviewed by me only
				PR:        api.PR{Repo: "o/r", Number: 9, Author: "zara"},
				Reviews:   []api.Review{{ID: "r1", Author: "alice", State: "COMMENTED"}},
				Ownership: ownership.Team,
			},
			{PR: api.PR{Repo: "o/r", Number: 10, Author: "zara", AssignedToMe: true}, Ownership: ownership.Team}, // assigned only
		},
	}
	snap := Build(in)
	reasons := map[int][]string{}
	for _, r := range snap.Team {
		reasons[r.Number] = r.MatchReason
	}
	assertReasons(t, reasons[2], []string{"team-authored"})
	assertReasons(t, reasons[5], []string{"review-requested"})
	assertReasons(t, reasons[6], []string{"label:lbl-one"})
	assertReasons(t, reasons[7], []string{"team-authored", "review-requested", "assigned-to-me", "label:lbl-two"})
	assertReasons(t, reasons[9], []string{"reviewed-by-me"})
	assertReasons(t, reasons[10], []string{"assigned-to-me"})
}

// TestBuildAdmitsAssignedToMeOnly proves a non-draft PRInput whose ONLY
// qualifying reason is "assigned to me" (not team-authored, not requested,
// no watch label) is admitted to out.Team carrying exactly the
// MatchReasonAssignedToMe reason (pg2-4dz88.11.4, acceptance criterion 3).
func TestBuildAdmitsAssignedToMeOnly(t *testing.T) {
	snap := Build(BuilderInput{
		Self: "alice",
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 9, Author: "zara", AssignedToMe: true},
			Ownership: ownership.Team,
		}},
	})
	if len(snap.Team) != 1 || snap.Team[0].Number != 9 {
		t.Fatalf("PR assigned to me only must be admitted to Team, got %+v", snap.Team)
	}
	assertReasons(t, snap.Team[0].MatchReason, []string{MatchReasonAssignedToMe})
}

// TestBuild_SelfAssignedOwnPRStaysMine mirrors TestBuild_MinePRStaysMineEvenDraft
// for the new bucket: a PR I authored AND assigned to myself lands in Mine,
// never Team. This is the builder-side half of the "-author:<self> exclusion"
// decided fact — the query layer excludes my own PRs from the broadened
// buckets, and here the ownership-based dispatch (case
// p.Ownership.ActsAsMine(), checked BEFORE the reasons switch) independently
// keeps a self-authored PR out of the Team arm even if AssignedToMe were
// somehow true.
func TestBuild_SelfAssignedOwnPRStaysMine(t *testing.T) {
	snap := Build(BuilderInput{
		Self: "alice",
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", AssignedToMe: true},
			Ownership: ownership.Mine,
		}},
	})
	if len(snap.Mine) != 1 || len(snap.Team) != 0 {
		t.Fatalf("self-assigned own PR must stay Mine, not Team: mine=%+v team=%+v", snap.Mine, snap.Team)
	}
}

// TestBuild_AssignmentRemoved_RowDisappears is the pure-builder half of the
// EXIT rule (pg2-4dz88.11.4, acceptance criterion 4): rebuilding with the SAME
// PR whose only qualifier (assigned-to-me) is removed drops the Team row on
// that rebuild. (The companion "merge-request bead is untouched" half is
// proven at the sync layer, since snapshot.Build never touches beads at all —
// see internal/sync/refresh_test.go's
// TestRefreshPR_AssignmentRemoved_RowDisappearsBeadUntouched.)
func TestBuild_AssignmentRemoved_RowDisappears(t *testing.T) {
	build := func(assigned bool) *Snapshot {
		return Build(BuilderInput{
			Self: "alice",
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 13, Author: "zara", AssignedToMe: assigned},
				Ownership: ownership.Team,
			}},
		})
	}
	if snap := build(true); len(snap.Team) != 1 {
		t.Fatalf("expected the row while assigned, got %+v", snap.Team)
	}
	if snap := build(false); len(snap.Team) != 0 {
		t.Fatalf("expected the row to disappear once the ONLY qualifier (assigned-to-me) is removed, got %+v", snap.Team)
	}
}

// TestBuild_LosesAssignmentKeepsOtherReason_ReducedReasonSet is the "mirror
// case" the testing plan calls out: a PR that loses the assignment but keeps
// ANOTHER live reason (team-authored here) stays in Team, with a reduced
// MatchReason set rather than disappearing.
func TestBuild_LosesAssignmentKeepsOtherReason_ReducedReasonSet(t *testing.T) {
	reasonsFor := func(assigned bool) []string {
		snap := Build(BuilderInput{
			Self:        "alice",
			TeamMembers: []string{"bob"},
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 12, Author: "bob", AssignedToMe: assigned},
				Ownership: ownership.Team,
			}},
		})
		if len(snap.Team) != 1 {
			t.Fatalf("want 1 team row, got %d: %+v", len(snap.Team), snap.Team)
		}
		return snap.Team[0].MatchReason
	}
	assertReasons(t, reasonsFor(true), []string{"team-authored", "assigned-to-me"})
	assertReasons(t, reasonsFor(false), []string{"team-authored"})
}

func assertReasons(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MatchReason = %#v, want %#v", got, want)
	}
}

// TestBuild_MinePRStaysMineEvenDraft: my own PR is always Mine, never the review
// set, even as a draft (Q6 — exclude-mine applies only to the broadened criteria;
// mine is still self-reviewed elsewhere).
func TestBuild_MinePRStaysMineEvenDraft(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:     "alice",
		Registry: reg,
		PRs:      []PRInput{{PR: api.PR{Repo: "o/r", Number: 1, Author: "alice", Draft: true}, Ownership: ownership.Mine}},
	})
	if len(snap.Mine) != 1 || len(snap.Team) != 0 {
		t.Errorf("my draft PR must be Mine, not review set: mine=%+v team=%+v", snap.Mine, snap.Team)
	}
}

// TestBuildExcludesReasonlessReviewPR verifies the self-correcting membership
// guard (pg2-ynhr.13 B5 review #1): a non-mine, non-draft PR that carries NO
// qualifying match reason — not team-authored, not review-requested, no watch
// label — is EXCLUDED from the "PRs to Review" set. This is the removal path for
// a PR that ENTERED the set (labeled/requested) then lost the qualifier while
// still open+non-draft; without the guard it would linger with an empty reason.
func TestBuildExcludesReasonlessReviewPR(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		WatchLabels: []string{"lbl-one"},
		Registry:    reg,
		PRs: []PRInput{
			// non-mine, non-draft, but NO reason: author is not on the team, not
			// requested of me, carries no watch label, and I have not reviewed it.
			{PR: api.PR{Repo: "o/r", Number: 8, Author: "zara", Labels: []string{"unrelated"}}, Ownership: ownership.Team},
		},
	})
	if len(snap.Team) != 0 {
		t.Errorf("a reasonless non-mine PR must be excluded from PRs to Review; got %+v", snap.Team)
	}
	// pg2-4dz88.7.6: this exact dropped shape (non-mine, non-draft, zero
	// match reasons) must be counted by Build()'s new default: branch.
	if snap.DroppedCount != 1 {
		t.Errorf("DroppedCount = %d, want 1 for the reasonless non-mine PR", snap.DroppedCount)
	}
}

// TestBuildExcludesCommentedOnlyPR is the snapshot half of the FALLBACK for the
// still-open interacted-with fork (the detector half is
// TestFingerprintTick_CommentedOnlyPRNotRetrieved in internal/sync): a PR I have
// only COMMENTED on — a bare comment, NO submitted review, not team-authored, not
// requested of me, no watch label — carries no match reason and so is absent from
// out.Team. A comment is not a review commitment, so the reviewed-by reason must
// read p.Reviews and MUST NOT be satisfied by p.Comments.
func TestBuildExcludesCommentedOnlyPR(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		WatchLabels: []string{"lbl-one"},
		Registry:    reg,
		PRs: []PRInput{{
			PR: api.PR{Repo: "o/r", Number: 15, Author: "zara", Labels: []string{"unrelated"}},
			// A top-level comment of mine, and NO review at all.
			Comments:  []api.Comment{{Author: "alice", Body: "a passing thought"}},
			Ownership: ownership.Team,
		}},
	})
	if len(snap.Team) != 0 {
		t.Errorf("a commented-only PR must be absent from PRs to Review; got %+v", snap.Team)
	}
	// pg2-4dz88.7.6: a commented-only PR is the SAME dropped shape as the
	// reasonless case above (non-mine, non-draft, zero live match reasons —
	// a bare comment does not produce one) and must also be counted.
	if snap.DroppedCount != 1 {
		t.Errorf("DroppedCount = %d, want 1 for the commented-only PR", snap.DroppedCount)
	}
}

// TestBuildCountsDraftNotMinePR is the third dropped shape (pg2-4dz88.7.6),
// not covered by either test above: a DRAFT PR I do not own. It fails the
// `!p.PR.Draft` half of the second admitting case regardless of how many
// match reasons it carries, and — being non-mine — never reaches the first
// case either, so it falls through to the new default: branch and must be
// counted, without landing in either Mine or Team.
func TestBuildCountsDraftNotMinePR(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			// non-mine, DRAFT, and carries a live reason (team-authored) — proves
			// the drop is driven by Draft, not by an absence of reasons.
			{PR: api.PR{Repo: "o/r", Number: 21, Author: "bob", Draft: true}, Ownership: ownership.Team},
		},
	})
	if len(snap.Mine) != 0 || len(snap.Team) != 0 {
		t.Fatalf("a draft PR not owned by me must land in neither Mine nor Team; got mine=%+v team=%+v", snap.Mine, snap.Team)
	}
	if snap.DroppedCount != 1 {
		t.Errorf("DroppedCount = %d, want 1 for the draft-not-mine PR", snap.DroppedCount)
	}
}

// reviewedByInput is the minimal "only qualifier is my own submitted review"
// PRInput: non-draft, author not on the team, review not requested of me, no
// watch label. state is the GitHub review state under test.
func reviewedByInput(state string) BuilderInput {
	reg, _ := agentregistry.New(nil)
	return BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		WatchLabels: []string{"lbl-one"},
		Registry:    reg,
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 12, Author: "zara", Title: "reviewed", URL: "u12", Labels: []string{"unrelated"}},
			Reviews:   []api.Review{{ID: "r1", Author: "alice", State: state}},
			Ownership: ownership.Team,
		}},
	}
}

// TestBuildAdmitsReviewedByMeOnly is the test that stops the reviewed-by
// retrieval bucket shipping INERT. The detector's reviewed-by:<self> bucket
// enqueues PRs I have already reviewed, but a PR retrieved ONLY that way carries
// no team-authored / review-requested / watch-label reason — so without the
// matching builder re-check, Build's `len(reasons) > 0` admission guard silently
// drops it and the new bucket buys nothing.
//
// Both states that mean "I have a submitted review that still holds" must admit:
// APPROVED and CHANGES_REQUESTED are commitments, and COMMENTED is a submitted
// review too (a review event, unlike a bare comment).
func TestBuildAdmitsReviewedByMeOnly(t *testing.T) {
	for _, state := range []string{"APPROVED", "CHANGES_REQUESTED", "COMMENTED"} {
		t.Run(state, func(t *testing.T) {
			snap := Build(reviewedByInput(state))
			if len(snap.Team) != 1 {
				t.Fatalf("a PR whose only qualifier is my %s review must be admitted to Team; got %+v", state, snap.Team)
			}
			row := snap.Team[0]
			if row.Number != 12 {
				t.Errorf("wrong PR admitted: %+v", row)
			}
			assertReasons(t, row.MatchReason, []string{MatchReasonReviewedByMe})
		})
	}
}

// TestBuildReviewedByMeIgnoresDismissedAndPending pins the boundary of the
// reviewed-by definition. DISMISSED is where this predicate deliberately
// DIVERGES from internal/sync/revision.go's mySubmittedReviews, which keeps a
// dismissed review as a STALE approval (INV-APPROVAL-3) because the approval
// record must remember the approver DID approve. Here the question is whether my
// review still HOLDS the PR in the review set, and a dismissed one does not — so
// dismissal is this reason's exit path. PENDING was never submitted at all.
func TestBuildReviewedByMeIgnoresDismissedAndPending(t *testing.T) {
	for _, state := range []string{"DISMISSED", "PENDING"} {
		t.Run(state, func(t *testing.T) {
			snap := Build(reviewedByInput(state))
			if len(snap.Team) != 0 {
				t.Errorf("a %s review must NOT carry the reviewed-by reason; got %+v", state, snap.Team)
			}
		})
	}
}

// TestBuildReviewedByMeRequiresExactLogin: the reviewer-author comparison is an
// EXACT GitHub-login match, matching internal/sync/refresh.go's
// reviewRequestedOfSelf — never case-insensitive and never a substring. A bot
// whose login merely CONTAINS mine ("alice[bot]"), or differs only in case
// ("Alice"), is a different reviewer, so neither admits the PR. Empty self
// likewise matches nothing.
func TestBuildReviewedByMeRequiresExactLogin(t *testing.T) {
	for _, reviewer := range []string{"alice[bot]", "Alice", "alicia", "ali"} {
		t.Run(reviewer, func(t *testing.T) {
			in := reviewedByInput("APPROVED")
			in.PRs[0].Reviews[0].Author = reviewer
			if snap := Build(in); len(snap.Team) != 0 {
				t.Errorf("reviewer %q must not match self %q; got %+v", reviewer, in.Self, snap.Team)
			}
		})
	}
	t.Run("empty self", func(t *testing.T) {
		in := reviewedByInput("APPROVED")
		in.Self = ""
		// The review author stays "alice"; with no configured self login there is
		// nothing to compare against, so the reason must not fire.
		if snap := Build(in); len(snap.Team) != 0 {
			t.Errorf("empty self must match no reviewer; got %+v", snap.Team)
		}
	})
	// The case the empty-self guard actually buys: a review whose AUTHOR is also
	// empty — a ghost/deleted account, or a provider that left Author unpopulated.
	// Without the guard, "" == "" matches and an unrelated PR is admitted to the
	// review set on a review that is not mine. Exact-match alone does NOT save us
	// here, which is why the guard is a separate early return.
	t.Run("empty self and empty review author", func(t *testing.T) {
		in := reviewedByInput("APPROVED")
		in.Self = ""
		in.PRs[0].Reviews[0].Author = ""
		if snap := Build(in); len(snap.Team) != 0 {
			t.Errorf("an empty self must not match an empty review author; got %+v", snap.Team)
		}
	})
}

// TestBuildReviewedByMeCombinesWithTeamAuthored: a PR that is BOTH team-authored
// and reviewed by me yields ONE row carrying BOTH reasons — the reasons are a
// union on a single row, not one row per reason.
func TestBuildReviewedByMeCombinesWithTeamAuthored(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 13, Author: "bob"},
			Reviews:   []api.Review{{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}},
			Ownership: ownership.Team,
		}},
	})
	if len(snap.Team) != 1 {
		t.Fatalf("a doubly-qualified PR must produce exactly ONE row; got %+v", snap.Team)
	}
	assertReasons(t, snap.Team[0].MatchReason,
		[]string{MatchReasonTeamAuthored, MatchReasonReviewedByMe})
}

// TestBuildMyOwnReviewedPRStaysMine: exclude-mine applies to the reviewed-by
// reason exactly as it does to the detector's -author:<self> bucket. A PR I
// authored AND reviewed myself is Mine, never the review set — otherwise
// self-reviewing my own PR would surface it as someone-else's-to-review. Both
// halves are asserted in the same test: absent from Team, present in Mine.
func TestBuildMyOwnReviewedPRStaysMine(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 14, Author: "alice", Title: "mine", URL: "u14"},
			Reviews:   []api.Review{{ID: "r1", Author: "alice", State: "APPROVED"}},
			Ownership: ownership.Mine,
		}},
	})
	if len(snap.Team) != 0 {
		t.Errorf("my own self-reviewed PR must be absent from the review set; got %+v", snap.Team)
	}
	if len(snap.Mine) != 1 || snap.Mine[0].Number != 14 {
		t.Errorf("my own self-reviewed PR must be present in Mine; got %+v", snap.Mine)
	}
}

// TestBuildReviewedByMeExitOnDismissal is the EXIT rule for the new reason,
// mirroring TestBuildExcludesReasonlessReviewPR's self-correcting-membership
// guard. Build the SAME input twice, changing ONLY the review state from
// COMMENTED to DISMISSED: the row appears on the first build and is GONE on the
// second. Membership is recomputed from live facts on every Build — there is no
// timer and no persisted "seen" state, so the removal happens on the very next
// rebuild.
//
// The second half is the merge-request BEAD lifecycle (decided fact #2): losing
// the qualifier removes the dashboard ROW ONLY and MUST NOT close or otherwise
// mutate the PR's bead. Like the precedent, this needs no fakes or mocks —
// snapshot.Build is a PURE function whose entire contract is
// BuilderInput -> *Snapshot, so the assertion is structural: the input (its
// BeadsDeps included) is unchanged by the call, and BuilderInput/PRInput expose
// no func, channel, or client interface through which Build could reach a bead
// at all. Bead closure is driven solely by the PR closing or merging
// (internal/beadsbridge's EventPRClosed / EventPRMerged).
func TestBuildReviewedByMeExitOnDismissal(t *testing.T) {
	in := reviewedByInput("COMMENTED")
	// Give the PR a merge-request bead dep so a mutation of bead state would be
	// observable in the input after the build.
	in.PRs[0].BeadsDeps = []beads.DepNode{
		{ID: "mr-12", Title: "merge request", Status: "open"},
	}
	pristine := deepCopyInput(t, in)

	if snap := Build(in); len(snap.Team) != 1 || snap.Team[0].Number != 12 {
		t.Fatalf("with a COMMENTED review the row must be present; got %+v", snap.Team)
	}

	// SAME input, ONLY the review state changes: the qualifier is gone.
	in.PRs[0].Reviews[0].State = "DISMISSED"
	if snap := Build(in); len(snap.Team) != 0 {
		t.Errorf("after the review is dismissed the row must disappear on the next rebuild; got %+v", snap.Team)
	}
	// Removing the review entirely is the same exit.
	in.PRs[0].Reviews = nil
	if snap := Build(in); len(snap.Team) != 0 {
		t.Errorf("after the review is removed the row must disappear; got %+v", snap.Team)
	}

	// --- bead lifecycle: the row disappeared, the bead did not change ---
	in.PRs[0].Reviews = pristine.PRs[0].Reviews // restore the only field we mutated
	if !reflect.DeepEqual(in, pristine) {
		t.Errorf("Build mutated its input:\n got=%+v\nwant=%+v", in, pristine)
	}
	assertNoIODependency(t, reflect.TypeOf(BuilderInput{}))
	assertNoIODependency(t, reflect.TypeOf(PRInput{}))
}

// deepCopyInput round-trips a BuilderInput through JSON-independent manual copy
// of the fields the exit test mutates, so the pristine comparison is against a
// value Build cannot alias.
func deepCopyInput(t *testing.T, in BuilderInput) BuilderInput {
	t.Helper()
	out := in
	out.PRs = make([]PRInput, len(in.PRs))
	copy(out.PRs, in.PRs)
	for i := range out.PRs {
		out.PRs[i].Reviews = append([]api.Review(nil), in.PRs[i].Reviews...)
		out.PRs[i].BeadsDeps = append([]beads.DepNode(nil), in.PRs[i].BeadsDeps...)
	}
	return out
}

// assertNoIODependency proves structurally that Build cannot perform IO — and so
// cannot close or mutate a bead — by checking the input struct carries only DATA.
// A func, channel, or interface field would be a channel through which a bead
// client (or any other side effect) could be injected; the sole pointer allowed
// is the read-only *agentregistry.Registry. If a future change adds such a field,
// this fails and the bead-lifecycle guarantee must be re-argued deliberately.
func assertNoIODependency(t *testing.T, typ reflect.Type) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.Func, reflect.Chan, reflect.Interface, reflect.UnsafePointer:
			t.Errorf("%s.%s is a %s: Build must take only data, never an injectable dependency",
				typ.Name(), f.Name, f.Type.Kind())
		case reflect.Pointer:
			if f.Type != reflect.TypeOf(&agentregistry.Registry{}) {
				t.Errorf("%s.%s is an unexpected pointer (%s); only *agentregistry.Registry is allowed",
					typ.Name(), f.Name, f.Type)
			}
		}
	}
}

// TestBuildDerivesApprovalAndWaiting verifies:
//   - human_approved from a non-agent approver's PER-APPROVER row (the read path
//     since pg2-4dz88.1.9 — a live APPROVED review reaches the snapshot through
//     the row internal/sync/ingest.go writes for it, not through the review
//     object)
//   - agent_approved from an agent's review body matching the approval regex —
//     the legacy fallback, retained for a registry agent the store has NO row
//     for (see classifyApprovals's doc)
//   - the ONE human + ONE agent shape that renders as `human,agent`, so this
//     pair is exactly the fixture cmd/pg-pr's rendering contract rests on
//   - ci_status=success when all runs pass
//   - waiting_on_me derived from beads dep set
func TestBuildDerivesApprovalAndWaiting(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}

	deps := []beads.DepNode{
		{ID: "T-1", Title: "human task", Status: "open", Labels: []string{"human"}},
	}

	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 30,
		Self:                "alice",
		TeamMembers:         []string{},
		Registry:            reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "org/repo", Number: 5, Author: "alice", Title: "feat", URL: "u5", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: []store.Approval{
					{Approver: "humanreviewer", State: "approved", HeadSHA: "h1"},
				},
				Reviews: []api.Review{
					{ID: "r1", Author: "humanreviewer", State: "APPROVED", Body: "LGTM"},
					{ID: "r2", Author: approvalGuardAgentLogin, State: "APPROVED", Body: matchingBody + "\nDetails here"},
				},
				CIRuns: []api.CIRun{
					{ID: "ci1", Status: "completed", Conclusion: "success"},
					{ID: "ci2", Status: "completed", Conclusion: "success"},
				},
				BeadsDeps: deps,
			},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]

	if !row.HumanApproved {
		t.Error("expected HumanApproved=true")
	}
	if !row.AgentApproved {
		t.Error("expected AgentApproved=true")
	}
	// The single-approver-per-class shape: one of each, never two. This is the
	// snapshot half of the `human,agent` backward-compatibility contract
	// cmd/pg-pr/open_test.go asserts byte-for-byte on the rendered column.
	if row.HumanApprovers != 1 || row.AgentApprovers != 1 {
		t.Errorf("approver counts = human %d / agent %d, want 1/1", row.HumanApprovers, row.AgentApprovers)
	}
	if row.CIStatus != "success" {
		t.Errorf("expected CIStatus=success, got %q", row.CIStatus)
	}
	if !row.WaitingOnMe {
		t.Error("expected WaitingOnMe=true (open dep with human label)")
	}
}

// TestBuildCountsTwoApproversAsTwo is the core INV-APPROVAL-1 assertion this
// leaf exists for: TWO distinct approvers approving reports TWO, not one. The
// retired (human, agent) boolean pair structurally could not express it — both
// fixtures below set exactly the same two bits.
func TestBuildCountsTwoApproversAsTwo(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	mk := func(approvals []store.Approval) MineRow {
		snap := Build(BuilderInput{
			Self:     "alice",
			Registry: reg,
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: approvals,
			}},
		})
		if len(snap.Mine) != 1 {
			t.Fatalf("want 1 mine row, got %d", len(snap.Mine))
		}
		return snap.Mine[0]
	}

	one := mk([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
	})
	if one.HumanApprovers != 1 {
		t.Errorf("one human approver: HumanApprovers = %d, want 1", one.HumanApprovers)
	}

	two := mk([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1"},
	})
	if two.HumanApprovers != 2 {
		t.Errorf("two human approvers: HumanApprovers = %d, want 2 — approvals must not collapse", two.HumanApprovers)
	}
	// The retired booleans read IDENTICALLY for both fixtures, which is exactly
	// why the counts had to be added rather than the booleans reinterpreted.
	if one.HumanApproved != two.HumanApproved {
		t.Fatalf("fixture invalid: the boolean is supposed to be blind to the difference")
	}

	// Two agents split the same way, and the two classes are counted separately.
	agents := mk([]store.Approval{
		{Approver: approvalGuardAgentLogin, State: "approved", HeadSHA: "h1"},
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1"},
	})
	if agents.AgentApprovers != 1 || agents.HumanApprovers != 2 {
		t.Errorf("mixed set: agent %d / human %d, want 1/2", agents.AgentApprovers, agents.HumanApprovers)
	}
}

// A per-approver row that is not a STANDING approval MUST NOT be counted: a
// teammate asking for changes, a neutral comment, an approval of an EARLIER
// head, and one the code host dismissed. None of the four was expressible in
// the collapsed booleans (pg2-4dz88.1.9).
func TestBuildApproverCountsExcludeNonStandingRows(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	tests := []struct {
		name string
		row  store.Approval
	}{
		{"changes-requested is not an approval", store.Approval{Approver: "carol", State: "changes-requested", HeadSHA: "h1"}},
		{"commented is not an approval", store.Approval{Approver: "carol", State: "commented", HeadSHA: "h1"}},
		{"approval of an earlier head is stale", store.Approval{Approver: "carol", State: "approved", HeadSHA: "h0"}},
		{"dismissed approval is stale", store.Approval{Approver: "carol", State: "approved", HeadSHA: "h1", Dismissed: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs: []PRInput{{
					PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
					Ownership: ownership.Mine,
					Approvals: []store.Approval{tc.row},
				}},
			})
			row := snap.Mine[0]
			if row.HumanApprovers != 0 || row.AgentApprovers != 0 || row.HumanApproved || row.AgentApproved {
				t.Errorf("counted a non-standing row: human=%d agent=%d bools=%v/%v",
					row.HumanApprovers, row.AgentApprovers, row.HumanApproved, row.AgentApproved)
			}
		})
	}
}

// One approver observed several ways counts ONCE: the store row and the legacy
// regex-mined body are the SAME login, so a per-approver count must dedupe them
// rather than double-count. This also pins the precedence: the store row is
// authoritative for a login it knows, so a body that still matches the approval
// regex cannot resurrect that login's DISMISSED approval.
func TestBuildApproverCountsDedupeAndPreferStore(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	mkRow := func(approvals []store.Approval) MineRow {
		snap := Build(BuilderInput{
			Self:     "alice",
			Registry: reg,
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: approvals,
				Comments:  []api.Comment{{Author: approvalGuardAgentLogin, Body: matchingBody}},
			}},
		})
		return snap.Mine[0]
	}

	// Standing store row + matching body, same login → ONE agent approver.
	if got := mkRow([]store.Approval{
		{Approver: approvalGuardAgentLogin, State: "approved", HeadSHA: "h1"},
	}).AgentApprovers; got != 1 {
		t.Errorf("AgentApprovers = %d, want 1 — one approver observed twice is still one", got)
	}

	// DISMISSED store row + still-matching body → ZERO. The store wins.
	if got := mkRow([]store.Approval{
		{Approver: approvalGuardAgentLogin, State: "approved", HeadSHA: "h1", Dismissed: true},
	}).AgentApprovers; got != 0 {
		t.Errorf("AgentApprovers = %d, want 0 — a matching body must not resurrect a dismissed approval", got)
	}
}

// The legacy approval-regex fallback mines only TOP-LEVEL comment bodies. This
// pins BOTH halves — that a top-level body DOES count (the positive case the
// pre-cutover suite never asserted; its agent approval always came through a
// review body instead) and that each of the three anchored shapes does NOT.
// "Anchored" means a path OR a line: a file-level comment carries a path with no
// line, and a body that carries a line anchor is diff feedback whatever its
// path, so the guard tests both fields independently rather than as a pair.
func TestBuildRegexFallbackMinesOnlyTopLevelComments(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	tests := []struct {
		name    string
		comment api.Comment
		want    int
	}{
		{"top-level body is mined", api.Comment{Author: approvalGuardAgentLogin, Body: matchingBody}, 1},
		{"inline diff comment is not", api.Comment{Author: approvalGuardAgentLogin, Body: matchingBody, Path: "foo.go", Line: 42}, 0},
		{"file-level comment (path, no line) is not", api.Comment{Author: approvalGuardAgentLogin, Body: matchingBody, Path: "foo.go"}, 0},
		{"line anchor with no path is not", api.Comment{Author: approvalGuardAgentLogin, Body: matchingBody, Line: 42}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs: []PRInput{{
					PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
					Ownership: ownership.Mine,
					Comments:  []api.Comment{tc.comment},
				}},
			})
			if got := snap.Mine[0].AgentApprovers; got != tc.want {
				t.Errorf("AgentApprovers = %d, want %d", got, tc.want)
			}
		})
	}
}

// Only the EXACT store state "approved" is an approval. The three states the
// schema allows all sort at or after "approved" lexicographically, so an
// ordering comparison would be indistinguishable from equality over them —
// hence the deliberately unrecognized state below, which sorts BEFORE
// "approved" and pins that the seam matches the literal rather than a range.
func TestBuildApprovalStateMatchIsExact(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	for _, state := range []string{"", "APPROVED", "approve"} {
		t.Run("state="+state, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs: []PRInput{{
					PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
					Ownership: ownership.Mine,
					Approvals: []store.Approval{{Approver: "carol", State: state, HeadSHA: "h1"}},
				}},
			})
			if got := snap.Mine[0].HumanApprovers; got != 0 {
				t.Errorf("state %q counted as an approval: HumanApprovers = %d, want 0", state, got)
			}
		})
	}
}

// TestBuildDismissedReviewBodyDoesNotApprove verifies a DISMISSED review whose
// body matches the configured approval pattern does NOT set agent_approved:
// GitHub already invalidated that approval, and the body-text fallback must
// not resurrect it. Fixture deliberately has no Approvals (store) row and no
// Comments, so only the review-body fallback under test can set the result.
// (pg2-4dz88.9)
func TestBuildDismissedReviewBodyDoesNotApprove(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	snap := Build(BuilderInput{
		Self:     "alice",
		Registry: reg,
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
			Ownership: ownership.Mine,
			Reviews:   []api.Review{{ID: "r1", Author: approvalGuardAgentLogin, State: "DISMISSED", Body: matchingBody}},
		}},
	})
	if len(snap.Mine) != 1 {
		t.Fatalf("want 1 mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]
	if row.AgentApproved {
		t.Error("a DISMISSED review's matching body must not set AgentApproved")
	}
	if row.HumanApproved {
		t.Error("a DISMISSED review's matching body must not set HumanApproved either")
	}
}

// TestBuildAgentApprovalReviewStateGuard pins, per GitHub review state,
// whether a body-text match in a review SUMMARY may set agent_approved. Two
// rows are load-bearing: "COMMENTED ... still approves" fails if the guard is
// over-corrected to an APPROVED-only allow list (a review-summary verdict is
// normally state COMMENTED, not APPROVED — see classifyApprovals' doc
// comment); the two APPROVED rows together prove the fix did not break the
// legitimate approval paths. The APPROVED/non-matching-body row demonstrates
// approval via the store ledger (the current "tier 1"; see
// TestBuildApproverCountsDedupeAndPreferStore) rather than the body fallback,
// since post-pg2-4dz88.1.9 a review's State alone no longer feeds approval —
// only the body-text fallback under test, and the separate per-approver
// store. (pg2-4dz88.9)
func TestBuildAgentApprovalReviewStateGuard(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	for _, tc := range []struct {
		name      string
		state     string
		body      string
		approvals []store.Approval
		wantAgent bool
	}{
		{"APPROVED matching body approves", "APPROVED", matchingBody, nil, true},
		{"COMMENTED review summary still approves", "COMMENTED", matchingBody, nil, true},
		{"DISMISSED matching body must NOT approve", "DISMISSED", matchingBody, nil, false},
		{"CHANGES_REQUESTED matching body must NOT approve", "CHANGES_REQUESTED", matchingBody, nil, false},
		{"PENDING matching body must NOT approve", "PENDING", matchingBody, nil, false},
		{"unknown state fails closed", "SOME_FUTURE_STATE", matchingBody, nil, false},
		{"empty state fails closed", "", matchingBody, nil, false},
		{
			"APPROVED non-matching body still approves via the store ledger",
			"APPROVED", nonMatchingBody,
			[]store.Approval{{Approver: approvalGuardAgentLogin, State: "approved", HeadSHA: "h1"}},
			true,
		},
		{"COMMENTED non-matching body does not approve", "COMMENTED", nonMatchingBody, nil, false},
		{"DISMISSED non-matching body does not approve", "DISMISSED", nonMatchingBody, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs: []PRInput{{
					PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
					Ownership: ownership.Mine,
					Approvals: tc.approvals,
					Reviews:   []api.Review{{ID: "r1", Author: approvalGuardAgentLogin, State: tc.state, Body: tc.body}},
				}},
			})
			if len(snap.Mine) != 1 {
				t.Fatalf("want 1 mine row, got %d", len(snap.Mine))
			}
			if got := snap.Mine[0].AgentApproved; got != tc.wantAgent {
				t.Errorf("state=%q body=%q: AgentApproved = %v, want %v", tc.state, tc.body, got, tc.wantAgent)
			}
		})
	}
}

// TestBuildDismissedReviewNonRegisteredLoginDoesNotApprove pins that the
// review-state guard does not accidentally start counting a non-agent login:
// a DISMISSED review from a login the registry does not know, whose body
// still matches the configured pattern, must set neither agent- nor
// human-approved. (pg2-4dz88.9)
func TestBuildDismissedReviewNonRegisteredLoginDoesNotApprove(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	snap := Build(BuilderInput{
		Self:     "alice",
		Registry: reg,
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
			Ownership: ownership.Mine,
			Reviews:   []api.Review{{ID: "r1", Author: "human-one", State: "DISMISSED", Body: matchingBody}},
		}},
	})
	if len(snap.Mine) != 1 {
		t.Fatalf("want 1 mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]
	if row.AgentApproved || row.HumanApproved {
		t.Errorf("non-registered login must not set approval: agent=%v human=%v", row.AgentApproved, row.HumanApproved)
	}
}

// The TEAM row carries the same per-approver facts as the Mine row. Asserted
// separately because buildTeamRow and buildMineRow map the facts onto their own
// row types independently, so a mapping mistake in one is invisible in the
// other's tests.
func TestBuildTeamRowApproverCounts(t *testing.T) {
	// Registered with no ApprovalRegex: enough to make IsAgent true (the
	// human/agent split), with the regex fallback inert.
	reg, err := agentregistry.New([]agentregistry.Entry{{Login: approvalGuardAgentLogin}})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	row := func(approvals []store.Approval) TeamRow {
		snap := Build(BuilderInput{
			Self:        "alice",
			TeamMembers: []string{"bob"},
			Registry:    reg,
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 2, Author: "bob", HeadSHA: "h1"},
				Ownership: ownership.Team,
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
				Approvals: approvals,
			}},
		})
		if len(snap.Team) != 1 {
			t.Fatalf("want 1 team row, got %d", len(snap.Team))
		}
		return snap.Team[0]
	}

	mixed := row([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: approvalGuardAgentLogin, State: "approved", HeadSHA: "h1"},
	})
	if mixed.HumanApprovers != 1 || mixed.AgentApprovers != 1 {
		t.Errorf("mixed: human %d / agent %d, want 1/1", mixed.HumanApprovers, mixed.AgentApprovers)
	}
	if !mixed.HumanApproved || !mixed.AgentApproved {
		t.Errorf("derived booleans = %v/%v, want true/true", mixed.HumanApproved, mixed.AgentApproved)
	}

	two := row([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1"},
	})
	if two.HumanApprovers != 2 || two.AgentApprovers != 0 {
		t.Errorf("two humans: human %d / agent %d, want 2/0", two.HumanApprovers, two.AgentApprovers)
	}

	none := row(nil)
	if none.HumanApprovers != 0 || none.AgentApprovers != 0 || none.HumanApproved || none.AgentApproved {
		t.Errorf("no approvers: human %d / agent %d bools %v/%v, want 0/0 false/false",
			none.HumanApprovers, none.AgentApprovers, none.HumanApproved, none.AgentApproved)
	}
}

// TestBuildEmptyArraysNotNil verifies that JIRA and Beads fields are
// initialised as empty slices, not nil — important for JSON serialisation
// ([] vs null).
func TestBuildEmptyArraysNotNil(t *testing.T) {
	reg, _ := agentregistry.New(nil)

	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{"bob"},
		Registry:            reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "org/repo", Number: 10, Author: "alice", Title: "empty", URL: "u10"},
				Ownership: ownership.Mine,
				// No JIRA, no BeadsDeps
			},
			{
				PR:        api.PR{Repo: "org/repo", Number: 11, Author: "bob", Title: "team empty", URL: "u11"},
				Ownership: ownership.Team,
				// No JIRA
			},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	if len(snap.Team) != 1 {
		t.Fatalf("expected 1 Team row, got %d", len(snap.Team))
	}

	// JSON round-trip: nil slices marshal as null, empty slices as [].
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(b)
	// Ensure we don't see "null" for jira or beads fields.
	// The easiest check: the JSON should not contain `:null` for these fields.
	// We specifically check Mine[0].jira and Mine[0].beads and Team[0].jira.
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_ = s

	mine := raw["mine"].([]interface{})
	mineRow := mine[0].(map[string]interface{})
	if mineRow["jira"] == nil {
		t.Error("Mine jira field must not be null")
	}
	if mineRow["beads"] == nil {
		t.Error("Mine beads field must not be null")
	}

	team := raw["team"].([]interface{})
	teamRow := team[0].(map[string]interface{})
	if teamRow["jira"] == nil {
		t.Error("Team jira field must not be null")
	}
}

// TestBuildAgentApprovedViaInlineCommentIgnored verifies that an agent comment
// with an approval body but with Path/Line set (inline diff comment) does NOT
// trigger agent_approved.
func TestBuildAgentApprovedViaInlineCommentIgnored(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: approvalGuardAgentLogin, ApprovalRegex: approvalGuardPattern},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}

	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{},
		Registry:            reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "org/repo", Number: 20, Author: "alice", Title: "test", URL: "u20"},
				Ownership: ownership.Mine,
				// Inline comment — should be ignored for approval
				Comments: []api.Comment{
					{Author: approvalGuardAgentLogin, Body: matchingBody, Path: "foo.go", Line: 42},
				},
				// Reviews without a body that matches the approval regex
				Reviews: []api.Review{
					{ID: "r3", Author: approvalGuardAgentLogin, State: "CHANGES_REQUESTED", Body: "needs work"},
				},
			},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]

	if row.AgentApproved {
		t.Error("AgentApproved must be false: inline comment approval should be ignored")
	}
	if row.HumanApproved {
		t.Error("HumanApproved must be false: no human approval")
	}
}

// TestBuildExcludesAdvisoryCIChecks verifies the snapshot rollup drops
// excluded checks per repo via the shared cirollup classifier. (pg2-qs46b)
func TestBuildExcludesAdvisoryCIChecks(t *testing.T) {
	prInput := PRInput{
		PR:        api.PR{Repo: "o/n", Number: 1, Author: "me"},
		Ownership: ownership.Mine,
		CIRuns: []api.CIRun{
			{Name: "build", Status: "completed", Conclusion: "success"},
			{Name: "policy-bot: approval required (click for details): main", Status: "completed", Conclusion: "failure"},
		},
	}
	// No exclusion: policy-bot failure makes CIStatus "failure".
	snap := Build(BuilderInput{Self: "me", PRs: []PRInput{prInput}})
	if len(snap.Mine) != 1 || snap.Mine[0].CIStatus != "failure" {
		t.Fatalf("no exclusion: got %+v, want CIStatus=failure", snap.Mine)
	}
	// With exclusion: policy-bot dropped, real check passes → "success".
	// Sourced from CheckInterpretersByRepo (pg2-4dz88.2.8's registry-shaped
	// replacement for the old raw-pattern ExcludedChecksByRepo) rather than a
	// flat pattern list, but the excluder Build derives from it must still
	// exclude the exact same names — this is the regression this bead's own
	// testing plan (case 5) requires: the pre-existing no-exclusion/
	// with-exclusion ci_status behavior must survive the excluders map being
	// swapped from a directly-configured cirollup.Excluder to one derived
	// from the pg2-4dz88.2.4 registry's own Interpreter declarations.
	snap = Build(BuilderInput{
		Self: "me",
		PRs:  []PRInput{prInput},
		CheckInterpretersByRepo: map[string][]checkinterpret.Interpreter{
			"o/n": {{Patterns: []string{"^policy-bot"}, Type: checkinterpret.ApprovalGateType}},
		},
	})
	if len(snap.Mine) != 1 || snap.Mine[0].CIStatus != "success" {
		t.Fatalf("with exclusion: got %+v, want CIStatus=success", snap.Mine)
	}
}

// TestBuildGateState_IndependentFromCIStatus pins INV-GATE-1: the gate's own
// state and the CI-rollup state are computed from independent sources (the
// gate from the PR's latest persisted revision, store.Revision.GateState;
// CIStatus from the live CIRuns/excluder rollup) and must never collapse
// into each other. Two directions, each asserting BOTH fields in the SAME
// result per the bead's acceptance criteria.
func TestBuildGateState_IndependentFromCIStatus(t *testing.T) {
	// Direction 1: a failing gate alongside a green real check. ci_status
	// must read success (the gate is its own axis, not folded into CI
	// health) AND the gate field must read unsatisfied.
	failingGate := PRInput{
		PR:        api.PR{Repo: "o/n", Number: 1, Author: "me"},
		Ownership: ownership.Mine,
		CIRuns: []api.CIRun{
			{Name: "build", Status: "completed", Conclusion: "success"},
		},
		Revisions: []store.Revision{
			{Seq: 1, HeadSHA: "h1", GateState: "unsatisfied", GateStateM: 1},
		},
	}
	snap := Build(BuilderInput{Self: "me", PRs: []PRInput{failingGate}})
	if len(snap.Mine) != 1 {
		t.Fatalf("len(Mine) = %d, want 1", len(snap.Mine))
	}
	if got := snap.Mine[0]; got.CIStatus != "success" || got.GateState != "unsatisfied" || got.GateStateM != 1 {
		t.Fatalf("failing gate + green checks: got %+v, want CIStatus=success, GateState=unsatisfied, GateStateM=1", got)
	}

	// Direction 2 (the inverse): a red real check alongside a satisfied
	// gate. ci_status must read failure AND the gate field must read
	// satisfied — proving the two axes are independent in both directions,
	// not merely that the gate never suppresses a failure.
	satisfiedGate := PRInput{
		PR:        api.PR{Repo: "o/n", Number: 2, Author: "me"},
		Ownership: ownership.Mine,
		CIRuns: []api.CIRun{
			{Name: "build", Status: "completed", Conclusion: "failure"},
		},
		Revisions: []store.Revision{
			{Seq: 1, HeadSHA: "h1", GateState: "satisfied"},
		},
	}
	snap = Build(BuilderInput{Self: "me", PRs: []PRInput{satisfiedGate}})
	if len(snap.Mine) != 1 {
		t.Fatalf("len(Mine) = %d, want 1", len(snap.Mine))
	}
	if got := snap.Mine[0]; got.CIStatus != "failure" || got.GateState != "satisfied" || got.GateStateN != 0 || got.GateStateM != 0 {
		t.Fatalf("red check + satisfied gate: got %+v, want CIStatus=failure, GateState=satisfied, N=M=0", got)
	}
}

// TestBuildGateState_AbsentWhenNoObservation pins INV-GATE-2 on this read
// seam: a PR that carries no gate observation at all — no revisions, or a
// revision whose gate state is the store's own "unknown" default — MUST
// project to the zero value (omitted on the wire), never "satisfied" or any
// other positive state. Covers both TeamRow and MineRow, and both the
// no-revisions and explicit-unknown shapes.
func TestBuildGateState_AbsentWhenNoObservation(t *testing.T) {
	noRevisions := PRInput{
		PR:        api.PR{Repo: "o/n", Number: 1, Author: "me"},
		Ownership: ownership.Mine,
	}
	explicitUnknown := PRInput{
		PR:        api.PR{Repo: "o/n", Number: 2, Author: "someone", ReviewRequestedOfMe: true},
		Ownership: ownership.Team,
		Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1", GateState: "unknown"}},
	}
	snap := Build(BuilderInput{Self: "me", PRs: []PRInput{noRevisions, explicitUnknown}})
	if len(snap.Mine) != 1 || snap.Mine[0].GateState != "" || snap.Mine[0].GateStateN != 0 || snap.Mine[0].GateStateM != 0 {
		t.Fatalf("no revisions at all: got %+v, want zero-value GateState", snap.Mine)
	}
	if len(snap.Team) != 1 || snap.Team[0].GateState != "" || snap.Team[0].GateStateN != 0 || snap.Team[0].GateStateM != 0 {
		t.Fatalf("explicit unknown revision: got %+v, want zero-value GateState", snap.Team)
	}
}

// TestGateStateFields_NoSeparateFreshnessField pins the freshness half of
// this bead's acceptance criteria (INV-ASOF-1, INV-GATE-4): the gate state
// carries no as-of/stale stamp of its own on this seam — it rides the SAME
// payload-level freshness contract (Snapshot.GeneratedAt/WithFreshness)
// every other row-level fact does. A field literally named to carry a
// parallel freshness computation (e.g. AsOf/Stale/CapturedAt) would be
// exactly the "second, unstamped extra field" the design forbids. Modelled
// on TestDependencyFields_AreScalarOnly's reflection style.
func TestGateStateFields_NoSeparateFreshnessField(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(MineRow{}), reflect.TypeOf(TeamRow{})} {
		found := 0
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !strings.HasPrefix(f.Name, "GateState") {
				continue
			}
			found++
			if f.Type.Kind() != reflect.String && f.Type.Kind() != reflect.Int {
				t.Errorf("%s.%s has kind %s; a GateState* field must be a scalar, never a grouping structure",
					typ.Name(), f.Name, f.Type.Kind())
			}
		}
		if found != 3 {
			t.Errorf("%s: found %d GateState* fields, want 3 (GateState, GateStateN, GateStateM) — no separate as-of/stale field",
				typ.Name(), found)
		}
	}
}

// TestGateState_RidesPayloadLevelFreshness proves the gate field is
// unaffected by WithFreshness's serve-time stamp: the row-level facts
// (including GateState) are copied through unchanged while only the
// payload-level AgeSeconds/Stale scalars change, exactly like every other
// row fact (CIStatus included).
func TestGateState_RidesPayloadLevelFreshness(t *testing.T) {
	in := BuilderInput{
		// freshness.BoundSeconds(60) == 120 (BoundIntervals == 2): age this
		// past the bound so Stale is deterministically true below.
		GeneratedAt:         time.Now().Add(-150 * time.Second),
		SyncIntervalSeconds: 60,
		Self:                "me",
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/n", Number: 1, Author: "me"},
			Ownership: ownership.Mine,
			Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1", GateState: "unsatisfied", GateStateM: 1}},
		}},
	}
	snap := Build(in)
	if len(snap.Mine) != 1 || snap.Mine[0].GateState != "unsatisfied" {
		t.Fatalf("build: got %+v, want GateState=unsatisfied", snap.Mine)
	}

	served := snap.WithFreshness(time.Now())
	if len(served.Mine) != 1 || served.Mine[0].GateState != "unsatisfied" || served.Mine[0].GateStateM != 1 {
		t.Fatalf("WithFreshness must not alter row-level gate facts: got %+v", served.Mine)
	}
	if served.AgeSeconds <= 0 {
		t.Errorf("AgeSeconds = %d, want > 0 after WithFreshness on a snapshot built 90s ago", served.AgeSeconds)
	}
	if !served.Stale {
		t.Errorf("Stale = false, want true (age 90s > bound derived from a 60s sync interval)")
	}
}

// TestBuildNilRegistry verifies that when Registry is nil all approvers count
// as human and there is no panic. With no registry there is no agent to
// recognise, so the whole regex-mining fallback is skipped too — a login that
// WOULD be an agent under a populated registry is simply another human approver
// here (both approvers below therefore land in HumanApprovers).
func TestBuildNilRegistry(t *testing.T) {
	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{},
		Registry:            nil, // explicit nil
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "org/repo", Number: 30, Author: "alice", Title: "nil-reg", URL: "u30", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: []store.Approval{
					{Approver: "anyone", State: "approved", HeadSHA: "h1"},
					{Approver: approvalGuardAgentLogin, State: "approved", HeadSHA: "h1"},
				},
				Reviews: []api.Review{
					{ID: "r4", Author: "anyone", State: "APPROVED", Body: ""},
					{ID: "r5", Author: approvalGuardAgentLogin, State: "APPROVED", Body: matchingBody},
				},
			},
		},
	}

	// Must not panic.
	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]

	if !row.HumanApproved {
		t.Error("expected HumanApproved=true when registry is nil")
	}
	// AgentApproved stays false because with nil registry we only set human.
	if row.AgentApproved {
		t.Error("expected AgentApproved=false when registry is nil")
	}
	if row.HumanApprovers != 2 || row.AgentApprovers != 0 {
		t.Errorf("nil registry: human %d / agent %d, want 2/0", row.HumanApprovers, row.AgentApprovers)
	}
}

// A nil registry MUST short-circuit the regex-mining block outright, not merely
// happen to skip it. The fixture is the one that distinguishes the two: a review
// and a comment from a login the store has NO row for, so the mining block would
// be REACHED and would call a method on the nil *agentregistry.Registry — which
// panics. TestBuildNilRegistry above cannot catch that, because every login it
// names has a store row and is therefore skipped inside the block.
func TestBuildNilRegistrySkipsRegexMiningEntirely(t *testing.T) {
	snap := Build(BuilderInput{
		Self:     "alice",
		Registry: nil,
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 40, Author: "alice", HeadSHA: "h1"},
			Ownership: ownership.Mine,
			// No Approvals at all, so nothing is pre-recorded.
			Comments: []api.Comment{{Author: approvalGuardAgentLogin, Body: matchingBody}},
			Reviews:  []api.Review{{ID: "r1", Author: approvalGuardAgentLogin, State: "APPROVED", Body: matchingBody}},
		}},
	})
	row := snap.Mine[0]
	if row.HumanApprovers != 0 || row.AgentApprovers != 0 {
		t.Errorf("nil registry with un-recorded logins: human %d / agent %d, want 0/0 — "+
			"with no registry there is no approval regex to mine, and a live review is not a store row",
			row.HumanApprovers, row.AgentApprovers)
	}
}

// TestBuildMineRowMergeReminder verifies MineRow surfaces GitHub's
// authoritative MergeStateStatus/AutoMergeEnabled and derives the
// "ready to merge / automerge-forgotten" NeedsMergeReminder signal. (pg2-dwfld)
func TestBuildMineRowMergeReminder(t *testing.T) {
	mk := func(state string, auto bool) MineRow {
		p := PRInput{PR: api.PR{Repo: "o/n", Number: 1, Author: "me", MergeStateStatus: state, AutoMergeEnabled: auto}, Ownership: ownership.Mine}
		return buildMineRow(p, nil, nil, dependencyFacts{}, nil)
	}
	if !mk("CLEAN", false).NeedsMergeReminder {
		t.Errorf("CLEAN + no automerge should need reminder")
	}
	if mk("CLEAN", true).NeedsMergeReminder {
		t.Errorf("CLEAN + automerge armed should NOT need reminder")
	}
	if mk("BLOCKED", false).NeedsMergeReminder {
		t.Errorf("BLOCKED should NOT need reminder")
	}
	if got := mk("CLEAN", false).MergeStateStatus; got != "CLEAN" {
		t.Errorf("MergeStateStatus passthrough got %q", got)
	}
}

// TestBuild_CoOwnedInMinePanelBadged verifies the partition keys on the
// ownership classifier, not raw author-equality: a teammate-authored PR
// classified CoOwned (I pushed a commit onto it) lands in the Mine panel,
// badged, rather than the Team panel.
func TestBuild_CoOwnedInMinePanelBadged(t *testing.T) {
	in := BuilderInput{
		Self:        "me",
		TeamMembers: []string{"you"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 5, Author: "you", Draft: false},
			Ownership: ownership.CoOwned,
		}},
	}
	out := Build(in)
	if len(out.Mine) != 1 || len(out.Team) != 0 {
		t.Fatalf("want 1 mine / 0 team; got %d / %d", len(out.Mine), len(out.Team))
	}
	if !out.Mine[0].CoOwned {
		t.Errorf("MineRow.CoOwned = false, want true")
	}
}

// TestBuild_MineConflictFlags verifies a mine PR flagged CONFLICTING by GitHub
// surfaces HasConflicts on its MineRow.
func TestBuild_MineConflictFlags(t *testing.T) {
	in := BuilderInput{
		Self: "me",
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 1, Author: "me", Mergeable: "CONFLICTING"},
			Ownership: ownership.Mine,
		}},
	}
	out := Build(in)
	if len(out.Mine) != 1 {
		t.Fatalf("want 1 mine row, got %d", len(out.Mine))
	}
	if !out.Mine[0].HasConflicts {
		t.Errorf("mine HasConflicts = %v, want true", out.Mine[0].HasConflicts)
	}
}

// TestBuild_TeamConflictFlag verifies a team PR flagged DIRTY by GitHub
// surfaces HasConflicts on its TeamRow.
func TestBuild_TeamConflictFlag(t *testing.T) {
	in := BuilderInput{
		Self:        "me",
		TeamMembers: []string{"you"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 2, Author: "you", Draft: false, MergeStateStatus: "DIRTY"},
			Ownership: ownership.Team,
		}},
	}
	out := Build(in)
	if len(out.Team) != 1 || !out.Team[0].HasConflicts {
		t.Fatalf("want 1 team row with HasConflicts; got %d rows", len(out.Team))
	}
}

// ----------------------------------------------------------------------
// Hidden PR default exclusion (pg2-4dz88.4.3)
// ----------------------------------------------------------------------

// TestBuild_HiddenPR_ExcludedFromMineByDefault_IncludedWithFlag is the
// Mine-half acceptance-criteria test: a Hidden PRInput for a PR of mine is
// omitted from Mine when BuilderInput.IncludeHidden is unset (the default),
// and admitted -- carrying MineRow.Hidden/HiddenReason -- when it is set.
func TestBuild_HiddenPR_ExcludedFromMineByDefault_IncludedWithFlag(t *testing.T) {
	in := PRInput{
		PR:        api.PR{Repo: "o/r", Number: 1, Author: "me", Title: "hidden PR"},
		Ownership: ownership.Mine,
		Hidden:    true, HiddenReason: "noisy CI churn",
	}

	excluded := Build(BuilderInput{Self: "me", PRs: []PRInput{in}})
	if len(excluded.Mine) != 0 {
		t.Fatalf("default (IncludeHidden=false) must exclude the hidden PR from Mine, got %+v", excluded.Mine)
	}

	included := Build(BuilderInput{Self: "me", PRs: []PRInput{in}, IncludeHidden: true})
	if len(included.Mine) != 1 || included.Mine[0].Number != 1 {
		t.Fatalf("IncludeHidden=true must admit the hidden PR to Mine, got %+v", included.Mine)
	}
	if !included.Mine[0].Hidden || included.Mine[0].HiddenReason != "noisy CI churn" {
		t.Errorf("admitted MineRow must carry Hidden+HiddenReason, got %+v", included.Mine[0])
	}
}

// TestBuild_HiddenPR_ExcludedFromTeamByDefault_IncludedWithFlag mirrors the
// above for the Team ("PRs to Review") half: a hidden, otherwise-qualifying
// team PR (team-authored) is dropped by default and admitted, with the flag
// + reason carried through, once IncludeHidden is set.
func TestBuild_HiddenPR_ExcludedFromTeamByDefault_IncludedWithFlag(t *testing.T) {
	in := PRInput{
		PR:        api.PR{Repo: "o/r", Number: 2, Author: "bob", Title: "hidden team PR"},
		Ownership: ownership.Team,
		Hidden:    true, HiddenReason: "duplicate of #1",
	}

	excluded := Build(BuilderInput{Self: "me", TeamMembers: []string{"bob"}, PRs: []PRInput{in}})
	if len(excluded.Team) != 0 {
		t.Fatalf("default (IncludeHidden=false) must exclude the hidden PR from Team, got %+v", excluded.Team)
	}

	included := Build(BuilderInput{
		Self: "me", TeamMembers: []string{"bob"}, PRs: []PRInput{in}, IncludeHidden: true,
	})
	if len(included.Team) != 1 || included.Team[0].Number != 2 {
		t.Fatalf("IncludeHidden=true must admit the hidden PR to Team, got %+v", included.Team)
	}
	if !included.Team[0].Hidden || included.Team[0].HiddenReason != "duplicate of #1" {
		t.Errorf("admitted TeamRow must carry Hidden+HiddenReason, got %+v", included.Team[0])
	}
}

// TestBuild_UnhiddenPR_NeverAffectedByIncludeHidden proves IncludeHidden is a
// pure widen-the-admission-set toggle: an ordinary (Hidden=false) PR is
// admitted identically whether IncludeHidden is set or not, and never carries
// a stray Hidden=true.
func TestBuild_UnhiddenPR_NeverAffectedByIncludeHidden(t *testing.T) {
	in := PRInput{PR: api.PR{Repo: "o/r", Number: 3, Author: "me"}, Ownership: ownership.Mine}

	for _, includeHidden := range []bool{false, true} {
		snap := Build(BuilderInput{Self: "me", PRs: []PRInput{in}, IncludeHidden: includeHidden})
		if len(snap.Mine) != 1 || snap.Mine[0].Hidden {
			t.Fatalf("IncludeHidden=%v: an unhidden PR must always be present and never carry Hidden=true, got %+v",
				includeHidden, snap.Mine)
		}
	}
}

// --- PR-dependency annotation (pg2-4dz88.3.7) ---
//
// Build now calls prdeps.DeriveWithNativeStack ONCE over the whole in.PRs
// set and projects the result onto MineRow/TeamRow's Dependency* fields. The
// tests below exercise each prdeps.Resolution the annotation distinguishes:
// Trunk (no relation — row unchanged), Upstream (the ranking effect), and
// the two DeriveWithNativeStack-only outcomes, UpstreamOutOfSet (marker) and
// Unblocked (merged-middle).

// TestBuild_DependencyAnnotation_TrunkUnchanged is acceptance criterion #2:
// a PR with no derivable relation (ResolutionTrunk: its base ref is a
// configured trunk ref, so it sits at the bottom of a chain or is
// standalone) must be byte-for-byte unchanged from before this bead — every
// Dependency* field reads as its zero value, which is exactly what
// `omitempty` drops from the wire.
func TestBuild_DependencyAnnotation_TrunkUnchanged(t *testing.T) {
	snap := Build(BuilderInput{
		Self:      "alice",
		TrunkRefs: []string{"main"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", Branch: "feat-x", Base: "main", State: "open"},
			Ownership: ownership.Mine,
		}},
	})
	if len(snap.Mine) != 1 {
		t.Fatalf("want 1 mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]
	if row.DependencyBlockedBy != "" || row.DependencyBlockedByUnresolvedRef != "" ||
		row.DependencyUnblockedFrom != "" || row.DependencyOrderingKey != 0 {
		t.Errorf("a ResolutionTrunk row must carry the zero value on every dependency field, got %+v", row)
	}
}

// TestBuild_DependencyAnnotation_NoRelationResolutionsAllReadIdentically
// widens the same claim to EVERY prdeps.Resolution that means "no live
// relation to rank or mark" — not just ResolutionTrunk, but also
// ResolutionForeign (a repo-qualified base) and ResolutionSelf (a
// self-referential base): none of them is individually called out by the
// acceptance criteria, but the row-level annotation must not distinguish any
// of them from the unchanged-row-shape baseline either.
func TestBuild_DependencyAnnotation_NoRelationResolutionsAllReadIdentically(t *testing.T) {
	tests := []struct {
		name string
		pr   api.PR
	}{
		{"foreign base (repo-qualified)", api.PR{Repo: "o/r", Number: 1, Author: "alice", Branch: "feat-x", Base: "other:main", State: "open"}},
		{"self base", api.PR{Repo: "o/r", Number: 1, Author: "alice", Branch: "feat-x", Base: "feat-x", State: "open"}},
		{"empty base", api.PR{Repo: "o/r", Number: 1, Author: "alice", Branch: "feat-x", Base: "", State: "open"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self: "alice",
				PRs:  []PRInput{{PR: tc.pr, Ownership: ownership.Mine}},
			})
			if len(snap.Mine) != 1 {
				t.Fatalf("want 1 mine row, got %d", len(snap.Mine))
			}
			row := snap.Mine[0]
			if row.DependencyBlockedBy != "" || row.DependencyBlockedByUnresolvedRef != "" ||
				row.DependencyUnblockedFrom != "" || row.DependencyOrderingKey != 0 {
				t.Errorf("a no-relation row must carry the zero value on every dependency field, got %+v", row)
			}
		})
	}
}

// TestBuild_DependencyAnnotation_RankingEffect is acceptance criterion #3
// (the ranking effect) over a three-deep stack, admitted to Team via
// team-authored: each PR's DependencyBlockedBy names its IMMEDIATE upstream
// (never a transitive one), and DependencyOrderingKey strictly decreases
// going up the stack — proving ruling #1 ("rank lower, don't suppress") in a
// form a later multi-key comparator can consume directly.
func TestBuild_DependencyAnnotation_RankingEffect(t *testing.T) {
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		TrunkRefs:   []string{"main"},
		PRs: []PRInput{
			{PR: api.PR{Repo: "o/r", Number: 1, Author: "bob", Branch: "feat-a", Base: "main", State: "open"}, Ownership: ownership.Team},
			{PR: api.PR{Repo: "o/r", Number: 2, Author: "bob", Branch: "feat-b", Base: "feat-a", State: "open"}, Ownership: ownership.Team},
			{PR: api.PR{Repo: "o/r", Number: 3, Author: "bob", Branch: "feat-c", Base: "feat-b", State: "open"}, Ownership: ownership.Team},
		},
	})
	got := map[int]TeamRow{}
	for _, r := range snap.Team {
		got[r.Number] = r
	}
	if len(got) != 3 {
		t.Fatalf("want 3 team rows, got %+v", snap.Team)
	}
	row1, row2, row3 := got[1], got[2], got[3]

	if row1.DependencyBlockedBy != "" || row1.DependencyOrderingKey != 0 {
		t.Errorf("bottom-of-chain row #1 must carry no blocking relation, got %+v", row1)
	}
	if row2.DependencyBlockedBy != "o/r#1" {
		t.Errorf("row #2 DependencyBlockedBy = %q, want %q", row2.DependencyBlockedBy, "o/r#1")
	}
	if row3.DependencyBlockedBy != "o/r#2" {
		t.Errorf("row #3 DependencyBlockedBy = %q, want %q (its IMMEDIATE upstream, not #1)", row3.DependencyBlockedBy, "o/r#2")
	}
	if row3.DependencyOrderingKey >= row2.DependencyOrderingKey || row2.DependencyOrderingKey >= row1.DependencyOrderingKey {
		t.Fatalf("ordering keys must strictly decrease going up the stack: row1=%d row2=%d row3=%d",
			row1.DependencyOrderingKey, row2.DependencyOrderingKey, row3.DependencyOrderingKey)
	}
	if row1.DependencyOrderingKey != 0 || row2.DependencyOrderingKey != -1 || row3.DependencyOrderingKey != -2 {
		t.Errorf("ordering keys = %d/%d/%d, want 0/-1/-2", row1.DependencyOrderingKey, row2.DependencyOrderingKey, row3.DependencyOrderingKey)
	}
}

// TestBuild_DependencyAnnotation_MineRowRankingEffect is the MineRow half of
// the ranking-effect assertion — buildMineRow and buildTeamRow map the
// dependencyFacts independently, so a mapping mistake in one is invisible in
// the other's test (same rationale as TestBuildTeamRowApproverCounts above).
func TestBuild_DependencyAnnotation_MineRowRankingEffect(t *testing.T) {
	snap := Build(BuilderInput{
		Self: "alice",
		PRs: []PRInput{
			{PR: api.PR{Repo: "o/r", Number: 7, Author: "alice", Branch: "feat-g", Base: "main", State: "open"}, Ownership: ownership.Mine},
			{PR: api.PR{Repo: "o/r", Number: 8, Author: "alice", Branch: "feat-h", Base: "feat-g", State: "open"}, Ownership: ownership.Mine},
		},
	})
	got := map[int]MineRow{}
	for _, r := range snap.Mine {
		got[r.Number] = r
	}
	if len(got) != 2 {
		t.Fatalf("want 2 mine rows, got %+v", snap.Mine)
	}
	if got[8].DependencyBlockedBy != "o/r#7" {
		t.Errorf("MineRow #8 DependencyBlockedBy = %q, want %q", got[8].DependencyBlockedBy, "o/r#7")
	}
	if got[8].DependencyOrderingKey >= got[7].DependencyOrderingKey {
		t.Errorf("MineRow #8 (waiting on #7) must have a strictly lower ordering key: got8=%d got7=%d",
			got[8].DependencyOrderingKey, got[7].DependencyOrderingKey)
	}
}

// TestBuild_DependencyAnnotation_OutOfSetMarker is ruling #2 (out-of-set
// upstream: marker only, no fetch): a base ref that names no PR anywhere in
// the set must still populate a VISIBLE fact (the unresolved ref name)
// rather than reading identically to "no relation at all" — see
// TestBuild_DependencyAnnotation_TrunkUnchanged for that baseline.
func TestBuild_DependencyAnnotation_OutOfSetMarker(t *testing.T) {
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 4, Author: "bob", Branch: "feat-d", Base: "feat-ghost", State: "open"},
			Ownership: ownership.Team,
		}},
	})
	if len(snap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(snap.Team))
	}
	row := snap.Team[0]
	if row.DependencyBlockedByUnresolvedRef != "feat-ghost" {
		t.Errorf("DependencyBlockedByUnresolvedRef = %q, want %q", row.DependencyBlockedByUnresolvedRef, "feat-ghost")
	}
	if row.DependencyBlockedBy != "" {
		t.Errorf("an out-of-set marker must not populate the live DependencyBlockedBy ref, got %q", row.DependencyBlockedBy)
	}
	if row.DependencyOrderingKey != 0 {
		t.Errorf("an out-of-set marker carries no live blocking relation to rank against, want ordering key 0, got %d", row.DependencyOrderingKey)
	}
}

// TestBuild_DependencyAnnotation_MergedMiddleUnblocked is ruling #4
// (merged-middle): a PR whose native-or-base-chain upstream has MERGED reads
// as unblocked, carrying DependencyUnblockedFrom for traceability rather
// than a live DependencyBlockedBy, and with no live relation left to rank
// against (ordering key 0) — read straight from prdeps.Node.MergedUpstream,
// never re-derived.
func TestBuild_DependencyAnnotation_MergedMiddleUnblocked(t *testing.T) {
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		PRs: []PRInput{
			{ // upstream, merged
				PR:        api.PR{Repo: "o/r", Number: 5, Author: "bob", Branch: "feat-e", Base: "main", State: "closed", Merged: true},
				Ownership: ownership.Team,
			},
			{ // downstream, still open, was stacked on #5
				PR:        api.PR{Repo: "o/r", Number: 6, Author: "bob", Branch: "feat-f", Base: "feat-e", State: "open"},
				Ownership: ownership.Team,
			},
		},
	})
	got := map[int]TeamRow{}
	for _, r := range snap.Team {
		got[r.Number] = r
	}
	row, ok := got[6]
	if !ok {
		t.Fatalf("want row #6 present, got %+v", snap.Team)
	}
	if row.DependencyUnblockedFrom != "o/r#5" {
		t.Errorf("DependencyUnblockedFrom = %q, want %q", row.DependencyUnblockedFrom, "o/r#5")
	}
	if row.DependencyBlockedBy != "" {
		t.Errorf("an unblocked (merged-middle) row must not carry a live DependencyBlockedBy, got %q", row.DependencyBlockedBy)
	}
	if row.DependencyOrderingKey != 0 {
		t.Errorf("merged-middle: no live blocking relation left to rank against, want ordering key 0, got %d", row.DependencyOrderingKey)
	}
}

// TestPrdepsState pins prdepsState's three-way precedence directly (it is
// unexported, so this calls it in-package rather than forcing every branch
// through Build): Merged wins outright regardless of the raw State string;
// an open, non-draft PR reads "open"; an open, draft PR reads "draft"; and
// the raw State is reported lower-cased otherwise. Case-insensitivity on
// "open" is exercised explicitly, matching internal/sync's stateForPR this
// mirrors.
func TestPrdepsState(t *testing.T) {
	tests := []struct {
		name string
		pr   api.PR
		want string
	}{
		{"merged wins over any raw state", api.PR{Merged: true, State: "closed"}, "merged"},
		{"merged wins even over draft", api.PR{Merged: true, State: "open", Draft: true}, "merged"},
		{"open, not draft", api.PR{State: "open"}, "open"},
		{"open, draft", api.PR{State: "open", Draft: true}, "draft"},
		{"OPEN case-insensitive, not draft", api.PR{State: "OPEN"}, "open"},
		{"OPEN case-insensitive, draft", api.PR{State: "OPEN", Draft: true}, "draft"},
		{"closed", api.PR{State: "closed"}, "closed"},
		{"raw state lower-cased", api.PR{State: "CLOSED"}, "closed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prdepsState(tc.pr); got != tc.want {
				t.Errorf("prdepsState(%+v) = %q, want %q", tc.pr, got, tc.want)
			}
		})
	}
}

// TestUnresolvedUpstreamRefNames pins the filter directly (it is unexported,
// so this hand-builds a prdeps.Graph rather than forcing every diagnostic
// kind through Build/DeriveWithNativeStack): ONLY DiagnosticUpstreamOutOfSet
// entries are indexed, by their FIRST ref — a diagnostic of any OTHER kind
// (even one carrying a non-empty RefName and Refs, e.g. a hand-built
// DiagnosticSelfBase) must be excluded, and a DiagnosticUpstreamOutOfSet
// with an EMPTY Refs slice (defensive; native.go never actually emits one)
// must not panic or produce a spurious entry.
func TestUnresolvedUpstreamRefNames(t *testing.T) {
	blocked := prdeps.Ref{Repo: "o/r", Number: 1}
	other := prdeps.Ref{Repo: "o/r", Number: 2}
	g := prdeps.Graph{
		Diagnostics: []prdeps.Diagnostic{
			// A DIFFERENT kind, deliberately carrying a non-empty RefName and a
			// non-empty Refs naming `other` first — must NOT be picked up.
			{Kind: prdeps.DiagnosticSelfBase, Refs: []prdeps.Ref{other}, RefName: "feat-other"},
			// The kind under test.
			{Kind: prdeps.DiagnosticUpstreamOutOfSet, Refs: []prdeps.Ref{blocked}, RefName: "feat-ghost"},
			// Defensive: right kind, no Refs at all.
			{Kind: prdeps.DiagnosticUpstreamOutOfSet, Refs: nil, RefName: "unreachable"},
			// A Kind NUMERICALLY GREATER than DiagnosticUpstreamOutOfSet (which is
			// currently the highest-valued DiagnosticKind) — a value no real
			// derivation emits today, but the filter's `==` MUST still exclude it
			// rather than treat "greater than" as a match. This is the forward-
			// compat guard against DiagnosticUpstreamOutOfSet ever losing its
			// place as the last enum member.
			{Kind: prdeps.DiagnosticKind(int(prdeps.DiagnosticUpstreamOutOfSet) + 1), Refs: []prdeps.Ref{other}, RefName: "feat-future"},
		},
	}
	got := unresolvedUpstreamRefNames(g)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 entry (only the DiagnosticUpstreamOutOfSet with Refs), got %d: %+v", len(got), got)
	}
	if got[blocked] != "feat-ghost" {
		t.Errorf("unresolvedUpstreamRefNames[blocked] = %q, want %q", got[blocked], "feat-ghost")
	}
	if _, present := got[other]; present {
		t.Errorf("neither the DiagnosticSelfBase nor the out-of-range-Kind entry must be indexed, but key %v is present: %+v", other, got)
	}
}

// TestBuild_DependencyAnnotation_OutOfSetMarker_ClosedNotMergedUpstream is a
// second out-of-set fixture, distinguishing prdeps' "matched a real PR that
// is neither open/draft nor merged" arm from the "no PR heads that ref at
// all" arm TestBuild_DependencyAnnotation_OutOfSetMarker already covers —
// both share ResolutionUpstreamOutOfSet, but this one additionally exercises
// prdepsState's raw-state passthrough (a closed, non-merged PR reports its
// lower-cased raw State, which prdeps.IsOpen/isMerged both then reject).
func TestBuild_DependencyAnnotation_OutOfSetMarker_ClosedNotMergedUpstream(t *testing.T) {
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		PRs: []PRInput{
			{ // present in the set, but closed WITHOUT merging
				PR:        api.PR{Repo: "o/r", Number: 9, Author: "bob", Branch: "feat-i", Base: "main", State: "closed"},
				Ownership: ownership.Team,
			},
			{ // based on #9's head, which is neither a live candidate nor merged
				PR:        api.PR{Repo: "o/r", Number: 10, Author: "bob", Branch: "feat-j", Base: "feat-i", State: "open"},
				Ownership: ownership.Team,
			},
		},
	})
	got := map[int]TeamRow{}
	for _, r := range snap.Team {
		got[r.Number] = r
	}
	row, ok := got[10]
	if !ok {
		t.Fatalf("want row #10 present, got %+v", snap.Team)
	}
	if row.DependencyBlockedByUnresolvedRef != "feat-i" {
		t.Errorf("DependencyBlockedByUnresolvedRef = %q, want %q", row.DependencyBlockedByUnresolvedRef, "feat-i")
	}
	if row.DependencyBlockedBy != "" || row.DependencyUnblockedFrom != "" || row.DependencyOrderingKey != 0 {
		t.Errorf("a closed-not-merged out-of-set upstream must not populate any other dependency field, got %+v", row)
	}
}

// TestBuild_DependencyAnnotation_DraftUpstreamStillLive proves a DRAFT
// upstream is still a LIVE candidate for ResolutionUpstream — prdeps.IsOpen
// treats "draft" as open, so prdepsState must actually report "draft" for a
// draft PR rather than collapsing it to something IsOpen rejects. Without
// this, a stack built on top of a draft PR would silently read as an
// out-of-set marker instead of a live blocking relation.
func TestBuild_DependencyAnnotation_DraftUpstreamStillLive(t *testing.T) {
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		PRs: []PRInput{
			{ // the upstream: open AND draft
				PR:        api.PR{Repo: "o/r", Number: 11, Author: "bob", Branch: "feat-k", Base: "main", State: "open", Draft: true},
				Ownership: ownership.Team,
			},
			{ // stacked on the draft PR's head
				PR:        api.PR{Repo: "o/r", Number: 12, Author: "bob", Branch: "feat-l", Base: "feat-k", State: "open"},
				Ownership: ownership.Team,
			},
		},
	})
	got := map[int]TeamRow{}
	for _, r := range snap.Team {
		got[r.Number] = r
	}
	row, ok := got[12]
	if !ok {
		t.Fatalf("want row #12 present, got %+v", snap.Team)
	}
	if row.DependencyBlockedBy != "o/r#11" {
		t.Errorf("DependencyBlockedBy = %q, want %q (a draft upstream is still a LIVE candidate)", row.DependencyBlockedBy, "o/r#11")
	}
	if row.DependencyOrderingKey >= 0 {
		t.Errorf("DependencyOrderingKey = %d, want < 0 (a live blocking relation)", row.DependencyOrderingKey)
	}
}

// TestDependencyFields_AreScalarOnly is presentation ruling #3 (no
// grouped/collapsible stack row — FACT + ordering-key only, same visual
// treatment as any other row plus a marker). Every Dependency* field on both
// row types must be a plain scalar (string or int); a slice, map, or nested
// struct would be exactly the "stack group" structure the ruling forbids.
func TestDependencyFields_AreScalarOnly(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(MineRow{}), reflect.TypeOf(TeamRow{})} {
		found := 0
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !strings.HasPrefix(f.Name, "Dependency") {
				continue
			}
			found++
			switch f.Type.Kind() {
			case reflect.String, reflect.Int:
				// scalar: fine.
			default:
				t.Errorf("%s.%s has kind %s; a Dependency* field must be a scalar, never a grouping structure (ruling #3)",
					typ.Name(), f.Name, f.Type.Kind())
			}
		}
		if found != 4 {
			t.Errorf("%s: found %d Dependency* fields, want 4 (BlockedBy, BlockedByUnresolvedRef, UnblockedFrom, OrderingKey)",
				typ.Name(), found)
		}
	}
}

// TestBuild_TeamRowsAreComparatorSorted is the ordering-is-SHARED acceptance
// test (pg2-4dz88.7.2, parent design section 2): build from inputs
// deliberately supplied in an order the comparator must change, then assert
// snap.Team equals the result of sorting those same rows with CompareTeamRows.
// The expectation is COMPUTED by calling the comparator on a clone of the
// actual output, not hand-copied -- if Build stopped sorting Team at all (or
// sorted it some other way), re-sorting that clone with CompareTeamRows would
// change it and this test would fail; only a snap.Team that is ALREADY a
// fixed point of the comparator's own sort passes.
func TestBuild_TeamRowsAreComparatorSorted(t *testing.T) {
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		PRs: []PRInput{
			// Fed in the WORST-to-best reviewer-role order on purpose: rest,
			// already-engaged, requested-reviewer -- the comparator must
			// reorder this to already-engaged, requested-reviewer, rest.
			{PR: api.PR{Repo: "o/r", Number: 1, Author: "bob", Title: "team-authored only"}, Ownership: ownership.Team},
			{PR: api.PR{Repo: "o/r", Number: 2, Author: "dave", Title: "assigned to me", AssignedToMe: true}, Ownership: ownership.Team},
			{PR: api.PR{Repo: "o/r", Number: 3, Author: "eve", Title: "review requested", ReviewRequestedOfMe: true}, Ownership: ownership.Team},
		},
	}
	snap := Build(in)

	if len(snap.Team) != 3 {
		t.Fatalf("fixture too small to prove anything: got %d Team rows, want 3: %+v", len(snap.Team), snap.Team)
	}
	want := slices.Clone(snap.Team)
	slices.SortStableFunc(want, CompareTeamRows)
	if !reflect.DeepEqual(snap.Team, want) {
		t.Fatalf("snap.Team is not CompareTeamRows-sorted:\n got  %+v\n want %+v", snap.Team, want)
	}
	// Sanity: the fixture really did require reordering (input order was
	// 1,2,3; comparator order is 2,3,1), so this cannot pass vacuously.
	gotOrder := []int{snap.Team[0].Number, snap.Team[1].Number, snap.Team[2].Number}
	wantOrder := []int{2, 3, 1}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("Team order = %v, want %v (already-engaged, requested-reviewer, rest)", gotOrder, wantOrder)
		}
	}
}
