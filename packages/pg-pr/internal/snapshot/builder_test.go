package snapshot

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
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
	snap = Build(BuilderInput{
		Self:                 "me",
		PRs:                  []PRInput{prInput},
		ExcludedChecksByRepo: map[string][]string{"o/n": {"^policy-bot"}},
	})
	if len(snap.Mine) != 1 || snap.Mine[0].CIStatus != "success" {
		t.Fatalf("with exclusion: got %+v, want CIStatus=success", snap.Mine)
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
		return buildMineRow(p, nil, nil)
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
