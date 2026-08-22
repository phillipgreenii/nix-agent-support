package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/verdict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// All markers/patterns/logins in this file are synthetic and test-local —
// pg-pr is a PUBLIC repo (pg2-4dz88.1.6's constraint). None of them are real
// vendor text; they reproduce the SHAPE of a verdict-bearing bot comment
// with invented placeholder strings only, following the precedent already
// established in internal/verdict/verdict_test.go.
const testVerdictMarker = "X-TEST-MARKER"

const (
	testCleanPat    = `(?im)^GEN2-CLEAN$`
	testProblemsPat = `(?im)^GEN2-PROBLEMS$`
	testApprovedPat = `(?im)^GEN2-AUTHORITY-GRANTED$`
	testWithheldPat = `(?im)^GEN2-AUTHORITY-BLOCKED$`
)

// testVerdictGenerations returns one synthetic generation exercising both
// axes — sufficient for this bead's grouping/ordering/allowlist tests,
// which are orthogonal to the classifier's own generation-dispatch logic
// (already covered by internal/verdict/verdict_test.go).
func testVerdictGenerations() []config.VerdictGeneration {
	return []config.VerdictGeneration{
		{
			ID:                "gen2",
			BodyMarker:        testVerdictMarker,
			FindingsPatterns:  []string{testCleanPat, testProblemsPat},
			AuthorityPatterns: []string{testApprovedPat, testWithheldPat},
		},
	}
}

func mustTestClassifier(t *testing.T) *verdict.Classifier {
	t.Helper()
	clf, err := buildVerdictClassifier(testVerdictGenerations())
	if err != nil {
		t.Fatalf("buildVerdictClassifier: %v", err)
	}
	return clf
}

// cleanApprovedBody is a synthetic "findings clean, authority approved" bot
// verdict comment body.
func cleanApprovedBody() string {
	return testVerdictMarker + "\nGEN2-CLEAN\nGEN2-AUTHORITY-GRANTED\n"
}

// problemsBody is a synthetic "findings problems, authority withheld" bot
// verdict comment body (Withheld is always the default for Problems — no
// authority line needed).
func problemsBody() string {
	return testVerdictMarker + "\nGEN2-PROBLEMS\n"
}

// pendingBody carries the marker but matches no configured pattern at
// all — an unresolved/in-progress-shaped verdict (Authority Pending).
func pendingBody() string {
	return testVerdictMarker + "\nsomething not matching any pattern\n"
}

// noMarkerBody carries no configured BodyMarker at all — not a verdict
// (Authority Absent).
func noMarkerBody() string {
	return "just chatting, no verdict marker here"
}

// ----------------------------------------------------------------------
// botVerdictApprovals — pure-function ordering/allowlist/grouping tests
// ----------------------------------------------------------------------

// TestBotVerdictApprovals_LatestUpdatedWinsOverEarlierCreated proves the
// exact adversarial case the updatedAt-fetch leaf made representable: a
// comment with an EARLIER creation time but a LATER update time must beat
// a comment with a LATER creation time but an EARLIER update time.
func TestBotVerdictApprovals_LatestUpdatedWinsOverEarlierCreated(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{
			ID: "c1", Author: "approver-one",
			CreatedAt: "2026-01-01T00:00:00Z", // earliest creation
			UpdatedAt: "2026-01-05T00:00:00Z", // LATEST update — must win
			Body:      cleanApprovedBody(),
		},
		{
			ID: "c2", Author: "approver-one",
			CreatedAt: "2026-01-03T00:00:00Z", // later creation than c1
			UpdatedAt: "2026-01-03T00:00:00Z", // but earlier update than c1
			Body:      problemsBody(),
		},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"})

	got := botVerdictApprovals(comments, allowlist, clf)
	if len(got) != 1 {
		t.Fatalf("want 1 approval, got %d: %+v", len(got), got)
	}
	if got[0].Result.Authority != verdict.Approved {
		t.Errorf("Authority = %q, want Approved (c1, the later-UPDATED comment, must win despite its earlier creation time)", got[0].Result.Authority)
	}
	if got[0].ObservedAt != "2026-01-05T00:00:00Z" {
		t.Errorf("ObservedAt = %q, want c1's updatedAt 2026-01-05T00:00:00Z", got[0].ObservedAt)
	}
}

// TestBotVerdictApprovals_EqualUpdatedAtTiebreak proves the documented
// deterministic tiebreak: when two comments from the same login carry an
// EXACTLY equal effective timestamp, the comment LATER in enriched.Comments'
// slice order wins.
func TestBotVerdictApprovals_EqualUpdatedAtTiebreak(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{
			ID: "first", Author: "approver-one",
			CreatedAt: "2026-01-01T00:00:00Z",
			UpdatedAt: "2026-01-01T00:00:00Z",
			Body:      cleanApprovedBody(), // would resolve Approved
		},
		{
			ID: "second", Author: "approver-one",
			CreatedAt: "2026-01-01T00:00:00Z",
			UpdatedAt: "2026-01-01T00:00:00Z", // EXACT tie with "first"
			Body:      problemsBody(),         // resolves Withheld
		},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"})

	got := botVerdictApprovals(comments, allowlist, clf)
	if len(got) != 1 {
		t.Fatalf("want 1 approval, got %d: %+v", len(got), got)
	}
	if got[0].Result.Authority != verdict.Withheld {
		t.Errorf("Authority = %q, want Withheld (the LATER-in-slice-order comment \"second\" must win an exact tie)", got[0].Result.Authority)
	}
}

// TestBotVerdictApprovals_EmptyUpdatedAtFallsBackToCreatedAt proves the
// documented fallback: when updatedAt is empty, ordering falls back to
// createdAt rather than some undocumented sort behaviour. Both comments here
// have an empty updatedAt, so the later createdAt must decide the winner —
// this would fail under a buggy implementation that treats an empty
// updatedAt as an unconditional loss/win or as a plain tiebreak-by-slice-
// order case.
func TestBotVerdictApprovals_EmptyUpdatedAtFallsBackToCreatedAt(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{
			ID: "c1", Author: "approver-one",
			CreatedAt: "2026-01-01T00:00:00Z",
			UpdatedAt: "", // never edited
			Body:      problemsBody(),
		},
		{
			ID: "c2", Author: "approver-one",
			CreatedAt: "2026-01-02T00:00:00Z", // later createdAt
			UpdatedAt: "",                     // never edited
			Body:      cleanApprovedBody(),
		},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"})

	got := botVerdictApprovals(comments, allowlist, clf)
	if len(got) != 1 {
		t.Fatalf("want 1 approval, got %d: %+v", len(got), got)
	}
	if got[0].Result.Authority != verdict.Approved {
		t.Errorf("Authority = %q, want Approved (c2's later createdAt must win when both updatedAt are empty)", got[0].Result.Authority)
	}
	if got[0].ObservedAt != "2026-01-02T00:00:00Z" {
		t.Errorf("ObservedAt = %q, want c2's createdAt fallback 2026-01-02T00:00:00Z", got[0].ObservedAt)
	}
}

// TestBotVerdictApprovals_MiddleCommentWins uses three comments whose
// updatedAt-order winner sits in the MIDDLE slice position — neither first
// nor last — so an accidental "take comments[0]" or "take the last
// comment" implementation fails this test even though it might pass the
// two-comment cases above.
func TestBotVerdictApprovals_MiddleCommentWins(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{
			// slice position 0: earliest updatedAt.
			ID: "early", Author: "approver-one",
			UpdatedAt: "2026-01-01T00:00:00Z",
			Body:      problemsBody(),
		},
		{
			// slice position 1 (middle): LATEST updatedAt — the true winner.
			ID: "latest", Author: "approver-one",
			UpdatedAt: "2026-01-10T00:00:00Z",
			Body:      cleanApprovedBody(),
		},
		{
			// slice position 2 (last): a middling updatedAt — NOT the winner,
			// but would win under a buggy "take the last comment" implementation.
			ID: "middling", Author: "approver-one",
			UpdatedAt: "2026-01-05T00:00:00Z",
			Body:      problemsBody(),
		},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"})

	got := botVerdictApprovals(comments, allowlist, clf)
	if len(got) != 1 {
		t.Fatalf("want 1 approval, got %d: %+v", len(got), got)
	}
	if got[0].Result.Authority != verdict.Approved {
		t.Errorf("Authority = %q, want Approved (the middle-slice-position comment carries the latest updatedAt)", got[0].Result.Authority)
	}
	if got[0].ObservedAt != "2026-01-10T00:00:00Z" {
		t.Errorf("ObservedAt = %q, want 2026-01-10T00:00:00Z", got[0].ObservedAt)
	}
}

// TestBotVerdictApprovals_NonAllowlistedLoginNeverApproves proves a verdict
// from a login absent from the allowlist never becomes an approval, even
// when it is the newest, most "approved"-shaped comment in the set.
func TestBotVerdictApprovals_NonAllowlistedLoginNeverApproves(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{
			ID: "c1", Author: "not-an-approver",
			UpdatedAt: "2026-01-10T00:00:00Z", // newest comment of the set
			Body:      cleanApprovedBody(),    // best possible verdict
		},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"}) // does NOT include "not-an-approver"

	got := botVerdictApprovals(comments, allowlist, clf)
	if len(got) != 0 {
		t.Errorf("want 0 approvals for a non-allowlisted login, got %+v", got)
	}
}

// TestBotVerdictApprovals_PerLoginIndependentGrouping proves grouping is
// per-login: two allowlisted logins each resolve their own latest-wins
// winner independently, without interfering with each other.
func TestBotVerdictApprovals_PerLoginIndependentGrouping(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{ID: "a1", Author: "approver-one", UpdatedAt: "2026-01-01T00:00:00Z", Body: problemsBody()},
		{ID: "a2", Author: "approver-one", UpdatedAt: "2026-01-05T00:00:00Z", Body: cleanApprovedBody()}, // approver-one's winner
		{ID: "b1", Author: "approver-two", UpdatedAt: "2026-01-06T00:00:00Z", Body: cleanApprovedBody()},
		{ID: "b2", Author: "approver-two", UpdatedAt: "2026-01-02T00:00:00Z", Body: problemsBody()}, // earlier than b1, must not win
	}
	allowlist := approverAllowlistSet([]string{"approver-one", "approver-two"})

	got := botVerdictApprovals(comments, allowlist, clf)
	byLogin := map[string]verdict.Result{}
	for _, bv := range got {
		byLogin[bv.Approver] = bv.Result
	}
	if len(got) != 2 {
		t.Fatalf("want 2 approvals, got %d: %+v", len(got), got)
	}
	if byLogin["approver-one"].Authority != verdict.Approved {
		t.Errorf("approver-one Authority = %q, want Approved (a2 is the latest)", byLogin["approver-one"].Authority)
	}
	if byLogin["approver-two"].Authority != verdict.Approved {
		t.Errorf("approver-two Authority = %q, want Approved (b1 is the latest, must not be shadowed by approver-one's stream)", byLogin["approver-two"].Authority)
	}
}

// TestBotVerdictApprovals_InlineCommentsExcluded proves only top-level
// (Path=="") comments are considered — an inline/thread comment (Path!="")
// from an allowlisted login, even with the latest timestamp and a
// verdict-shaped body, must not be picked.
func TestBotVerdictApprovals_InlineCommentsExcluded(t *testing.T) {
	clf := mustTestClassifier(t)
	comments := []api.Comment{
		{ID: "top", Author: "approver-one", Path: "", UpdatedAt: "2026-01-01T00:00:00Z", Body: cleanApprovedBody()},
		{ID: "inline", Author: "approver-one", Path: "some/file.go", Line: 10, UpdatedAt: "2026-01-10T00:00:00Z", Body: problemsBody()},
	}
	allowlist := approverAllowlistSet([]string{"approver-one"})

	got := botVerdictApprovals(comments, allowlist, clf)
	if len(got) != 1 {
		t.Fatalf("want 1 approval, got %d: %+v", len(got), got)
	}
	if got[0].Result.Authority != verdict.Approved {
		t.Errorf("Authority = %q, want Approved (the inline comment must be excluded from consideration entirely)", got[0].Result.Authority)
	}
}

// TestBotVerdictApprovals_NoMatchSkipsEntirely proves a Pending (marker
// present, no generation resolved) or Absent (no marker at all) winning
// comment produces NO entry — this function never invents a store state
// for either case.
func TestBotVerdictApprovals_NoMatchSkipsEntirely(t *testing.T) {
	clf := mustTestClassifier(t)
	allowlist := approverAllowlistSet([]string{"approver-one", "approver-two"})

	t.Run("pending", func(t *testing.T) {
		comments := []api.Comment{
			{ID: "c1", Author: "approver-one", UpdatedAt: "2026-01-01T00:00:00Z", Body: pendingBody()},
		}
		got := botVerdictApprovals(comments, allowlist, clf)
		if len(got) != 0 {
			t.Errorf("want 0 approvals for a Pending-only comment, got %+v", got)
		}
	})

	t.Run("absent", func(t *testing.T) {
		comments := []api.Comment{
			{ID: "c1", Author: "approver-two", UpdatedAt: "2026-01-01T00:00:00Z", Body: noMarkerBody()},
		}
		got := botVerdictApprovals(comments, allowlist, clf)
		if len(got) != 0 {
			t.Errorf("want 0 approvals for a non-verdict comment, got %+v", got)
		}
	})
}

// ----------------------------------------------------------------------
// approverApprovalState — (findings, authority) -> store-state mapping
// ----------------------------------------------------------------------

func TestApproverApprovalState(t *testing.T) {
	tests := []struct {
		name string
		res  verdict.Result
		want string
	}{
		{"clean+approved", verdict.Result{Findings: verdict.Clean, Authority: verdict.Approved}, "approved"},
		{"problems+withheld", verdict.Result{Findings: verdict.Problems, Authority: verdict.Withheld}, "changes-requested"},
		{"clean+withheld (contradiction default)", verdict.Result{Findings: verdict.Clean, Authority: verdict.Withheld, Contradiction: true}, "changes-requested"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := approverApprovalState(tc.res); got != tc.want {
				t.Errorf("approverApprovalState(%+v) = %q, want %q", tc.res, got, tc.want)
			}
		})
	}
}

// ----------------------------------------------------------------------
// Integration: ingestFeedbackToStore end-to-end via fakeVCS
// ----------------------------------------------------------------------

// TestIngestFeedbackToStore_BotVerdictApprovals drives ingestFeedbackToStore
// with a scripted synthetic comment stream and asserts the STORE's resulting
// per-approver rows match the expected winner per login after one ingest
// cycle — the only level that proves "fetch updatedAt, order by it,
// classify, persist" as one chain (pg2-4dz88.1.6's acceptance criteria).
func TestIngestFeedbackToStore_BotVerdictApprovals(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 42, State: "open", Author: "someone-else",
		HeadSHA: "sha-head", BaseSHA: "sha-base",
		URL: "https://github.com/o/r/pull/42",
	}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Comments: []api.Comment{
			// approver-one: adversarial ordering — c1 has the earlier creation
			// time but the LATER update time, and must win over c2.
			{
				ID: "c1", Author: "approver-one", Path: "",
				CreatedAt: "2026-01-01T00:00:00Z",
				UpdatedAt: "2026-01-05T00:00:00Z",
				Body:      cleanApprovedBody(),
			},
			{
				ID: "c2", Author: "approver-one", Path: "",
				CreatedAt: "2026-01-03T00:00:00Z",
				UpdatedAt: "2026-01-03T00:00:00Z",
				Body:      problemsBody(),
			},
			// An inline comment from approver-one with an even later timestamp
			// and a "worse" verdict body — must be excluded from consideration
			// entirely (top-level only), or it would flip approver-one's result.
			{
				ID: "c-inline", Author: "approver-one", Path: "some/file.go", Line: 3,
				CreatedAt: "2026-01-20T00:00:00Z",
				UpdatedAt: "2026-01-20T00:00:00Z",
				Body:      problemsBody(),
			},
			// approver-two: an independent stream resolving to withheld.
			{
				ID: "c3", Author: "approver-two", Path: "",
				CreatedAt: "2026-01-02T00:00:00Z",
				UpdatedAt: "2026-01-02T00:00:00Z",
				Body:      problemsBody(),
			},
			// not-an-approver: the newest, best-shaped verdict comment of the
			// entire set — must never become an approval (not on the allowlist).
			{
				ID: "c4", Author: "not-an-approver", Path: "",
				CreatedAt: "2026-01-10T00:00:00Z",
				UpdatedAt: "2026-01-10T00:00:00Z",
				Body:      cleanApprovedBody(),
			},
		},
	}

	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin:          "phillipg",
			Repos:              []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
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

	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "o/r", 42)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: pr=%v err=%v", storedPR, err)
	}

	approverOne, err := db.GetApproval(ctx, storedPR.ID, "approver-one")
	if err != nil {
		t.Fatalf("GetApproval(approver-one): %v", err)
	}
	if approverOne == nil {
		t.Fatalf("approver-one must have a pr_approval row")
	}
	if approverOne.State != "approved" {
		t.Errorf("approver-one State = %q, want \"approved\" (c1's later-updated, earlier-created comment must win)", approverOne.State)
	}
	if approverOne.HeadSHA != "sha-head" {
		t.Errorf("approver-one HeadSHA = %q, want pr.HeadSHA \"sha-head\"", approverOne.HeadSHA)
	}
	if approverOne.ObservedAt != "2026-01-05T00:00:00Z" {
		t.Errorf("approver-one ObservedAt = %q, want c1's updatedAt 2026-01-05T00:00:00Z", approverOne.ObservedAt)
	}

	approverTwo, err := db.GetApproval(ctx, storedPR.ID, "approver-two")
	if err != nil {
		t.Fatalf("GetApproval(approver-two): %v", err)
	}
	if approverTwo == nil {
		t.Fatalf("approver-two must have a pr_approval row")
	}
	if approverTwo.State != "changes-requested" {
		t.Errorf("approver-two State = %q, want \"changes-requested\"", approverTwo.State)
	}

	notApprover, err := db.GetApproval(ctx, storedPR.ID, "not-an-approver")
	if err != nil {
		t.Fatalf("GetApproval(not-an-approver): %v", err)
	}
	if notApprover != nil {
		t.Errorf("not-an-approver must never land in pr_approval, got %+v", notApprover)
	}

	all, err := db.ListApprovals(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want exactly 2 pr_approval rows (approver-one, approver-two), got %d: %+v", len(all), all)
	}
}
