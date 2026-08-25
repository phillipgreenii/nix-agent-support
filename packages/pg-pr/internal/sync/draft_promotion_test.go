package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// This file exercises the rebuilt maybePromoteDraft predicate (pg2-4dz88.4.5):
// the three NEW gates (WIP, merge-conflict incl. fail-closed-on-unknown, and
// bot-verdict-withheld) layered on top of the pre-existing CI-green check.
//
// Every case below calls e.maybePromoteDraft DIRECTLY except the co-owned
// case, which per the design doc MUST be driven through Engine.Sync/SyncPR:
// the self-authored/co-owned guard lives at the CALL SITES
// (authoredByMe/isSelfAuthored), not inside maybePromoteDraft itself, so
// calling the predicate directly would bypass the very guard that case is
// meant to prove and demonstrate nothing.

// draftPromoteTestCfg mirrors cfgWithCICD (sync_test.go) but also carries an
// ApproverAllowlist, needed by the bot-verdict-withheld gate's tests. Kept
// separate from cfgWithCICD (used by many unrelated tests) to avoid widening
// its blast radius.
func draftPromoteTestCfg(allowlist []string) *config.Config {
	return &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: "/tmp/wr",
		Repos: []config.RepoConfig{
			{Remote: "foo/bar", VCS: "github", CICD: []string{"ci"}, TeamMembers: []string{"coworker"}},
		},
		ApproverAllowlist: allowlist,
	}
}

// cleanEnrichedPR returns an EnrichedPR with a resolved, conflict-free merge
// state and one green CI run — the "every OTHER gate is green" baseline each
// negative test starts from, so the one gate under test is what actually
// blocks promotion.
func cleanEnrichedPR() *vcs.EnrichedPR {
	return &vcs.EnrichedPR{
		PR:     api.PR{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
		CIRuns: []api.CIRun{successRun()},
	}
}

// newDraftPromoteEngine builds a minimal Engine for direct maybePromoteDraft
// calls: a fakeVCS (DraftToggler, records SetDraft calls), a fakeCICD (only
// consulted when a test passes enriched=nil), and a real (in-temp-dir) store.
func newDraftPromoteEngine(t *testing.T, cfg *config.Config) (*Engine, *fakeVCS, *fakeCICD, *store.DB) {
	t.Helper()
	vp := newFakeVCS()
	cicd := newFakeCICD()
	db := store.OpenForTest(t)
	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": vp},
		CICD:     map[string]CICDProvider{"ci": cicd},
		Store:    db,
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e, vp, cicd, db
}

// seedPRRow upserts a minimal store row for (repo, number) and returns its
// id, mirroring what emitPREvent already wrote by the time maybePromoteDraft
// runs in production (both real call sites emit the PR event first).
func seedPRRow(t *testing.T, ctx context.Context, db *store.DB, repo string, number int, author string) int64 {
	t.Helper()
	id, err := db.UpsertPR(ctx, store.PullRequest{
		Repo: repo, Number: number, Author: author, State: "draft", Ownership: "mine",
	})
	if err != nil {
		t.Fatalf("seed PR row %s#%d: %v", repo, number, err)
	}
	return id
}

// TestMaybePromoteDraft_PromotesWhenAllGatesGreen is the primary "happy path"
// case: WIP=false + CI green + no bot disapproval + no conflict => promoted.
// Asserts all three required signals: SetDraft(false) called exactly once,
// summary.DraftPromoted incremented, and one pr.updated event emitted.
func TestMaybePromoteDraft_PromotesWhenAllGatesGreen(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(200, "foo/bar", "feat/all-green")
	pr.HeadSHA = "sha-200"
	seedPRRow(t, ctx, db, "foo/bar", 200, "phillipg") // WIP defaults false; no approval rows

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}

	if len(vp.setDraftCalls) != 1 {
		t.Fatalf("SetDraft calls: got %d want 1: %+v", len(vp.setDraftCalls), vp.setDraftCalls)
	}
	if got := vp.setDraftCalls[0]; got.Repo != "foo/bar" || got.Number != 200 || got.Draft != false {
		t.Fatalf("unexpected SetDraft call: %+v", got)
	}
	if summary.DraftPromoted != 1 {
		t.Fatalf("DraftPromoted: got %d want 1", summary.DraftPromoted)
	}

	var found bool
	if err := db.RunOutbox(ctx, func(_ context.Context, ev store.Event) error {
		if ev.Type != store.EventPRUpdated {
			return nil
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return err
		}
		if p.Repo == "foo/bar" && p.Number == 200 && p.State == "open" && !p.Draft {
			found = true
		}
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	if !found {
		t.Fatal("expected one pr.updated event with State=open Draft=false in the outbox")
	}
}

// TestMaybePromoteDraft_PromotesWithNoStoreRow proves the store-optional
// degrade: a PR the store has never observed (no row at all) is treated as
// WIP=false / no bot disapproval — permissive, matching this package's
// established store-optional convention (cmd/pg-pr/review.go's selfDraftWIP)
// rather than the merge-conflict gate's deliberately different fail-closed
// rule.
func TestMaybePromoteDraft_PromotesWithNoStoreRow(t *testing.T) {
	ctx := context.Background()
	e, vp, _, _ := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(201, "foo/bar", "feat/never-observed")
	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 1 {
		t.Fatalf("expected 1 SetDraft call for a never-observed PR; got %d: %+v", len(vp.setDraftCalls), vp.setDraftCalls)
	}
	if summary.DraftPromoted != 1 {
		t.Fatalf("DraftPromoted: got %d want 1", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_PromotesWhenStoreNil proves the Store==nil degrade:
// a deployment with no store wired at all must keep behaving exactly as
// before this leaf (CI-green-only gating), since the WIP/bot-verdict gate
// has nowhere to read from.
func TestMaybePromoteDraft_PromotesWhenStoreNil(t *testing.T) {
	ctx := context.Background()
	vp := newFakeVCS()
	e, err := New(Deps{
		Cfg:      draftPromoteTestCfg(nil),
		VCS:      map[string]VCSProvider{"github": vp},
		StateDir: t.TempDir(),
		// Store deliberately omitted (nil).
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pr := selfDraftPR(202, "foo/bar", "feat/no-store")
	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 1 {
		t.Fatalf("expected 1 SetDraft call with Store nil; got %d: %+v", len(vp.setDraftCalls), vp.setDraftCalls)
	}
}

// TestMaybePromoteDraft_BlockedByWIP is the single most important assertion
// in this leaf: WIP=true blocks promotion regardless of every other
// condition being green.
func TestMaybePromoteDraft_BlockedByWIP(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(210, "foo/bar", "feat/wip")
	seedPRRow(t, ctx, db, "foo/bar", 210, "phillipg")
	if err := db.SetWIP(ctx, "foo/bar", 210, true); err != nil {
		t.Fatalf("SetWIP: %v", err)
	}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls while WIP=true; got %+v", vp.setDraftCalls)
	}
	if summary.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_CIRedStillBlocks pins the pre-existing CI-green
// condition unchanged by this leaf (regression guard).
func TestMaybePromoteDraft_CIRedStillBlocks(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(211, "foo/bar", "feat/ci-red")
	seedPRRow(t, ctx, db, "foo/bar", 211, "phillipg")

	enriched := cleanEnrichedPR()
	enriched.CIRuns = []api.CIRun{{Status: "completed", Conclusion: "failure"}}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, enriched, "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls with CI red; got %+v", vp.setDraftCalls)
	}
	if summary.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_BlockedByBotVerdictWithheld: an allowlisted login's
// current (non-stale) pr_approval row reading "changes-requested" blocks
// promotion.
func TestMaybePromoteDraft_BlockedByBotVerdictWithheld(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg([]string{"policy-bot"}))

	pr := selfDraftPR(220, "foo/bar", "feat/bot-withheld")
	pr.HeadSHA = "sha-220"
	id := seedPRRow(t, ctx, db, "foo/bar", 220, "phillipg")
	if err := db.SetApproval(ctx, id, "policy-bot", "sha-220", "changes-requested", "2026-08-25T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval: %v", err)
	}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls with a bot verdict withheld; got %+v", vp.setDraftCalls)
	}
	if summary.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_BotVerdictWithheldButStaleDoesNotBlock proves the
// "current" half of "current, non-stale": a withheld verdict recorded
// against an EARLIER head no longer stands (Approval.IsStale), so it must
// NOT block promotion of the PR's current head.
func TestMaybePromoteDraft_BotVerdictWithheldButStaleDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg([]string{"policy-bot"}))

	pr := selfDraftPR(221, "foo/bar", "feat/bot-withheld-stale")
	pr.HeadSHA = "sha-221-new"
	id := seedPRRow(t, ctx, db, "foo/bar", 221, "phillipg")
	// Recorded against an OLDER head than the PR's current one.
	if err := db.SetApproval(ctx, id, "policy-bot", "sha-221-old", "changes-requested", "2026-08-25T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval: %v", err)
	}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 1 {
		t.Fatalf("a stale bot disapproval must not block promotion; got %d SetDraft calls: %+v", len(vp.setDraftCalls), vp.setDraftCalls)
	}
	if summary.DraftPromoted != 1 {
		t.Fatalf("DraftPromoted: got %d want 1", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_NonAllowlistedDisapprovalDoesNotBlock proves the
// allowlist gate: a "changes-requested" row from a login NOT on
// cfg.ApproverAllowlist must never block promotion.
func TestMaybePromoteDraft_NonAllowlistedDisapprovalDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	// Allowlist names a DIFFERENT login than the one that recorded a
	// disapproval below.
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg([]string{"policy-bot"}))

	pr := selfDraftPR(222, "foo/bar", "feat/non-allowlisted")
	pr.HeadSHA = "sha-222"
	id := seedPRRow(t, ctx, db, "foo/bar", 222, "phillipg")
	if err := db.SetApproval(ctx, id, "random-reviewer", "sha-222", "changes-requested", "2026-08-25T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval: %v", err)
	}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 1 {
		t.Fatalf("a non-allowlisted disapproval must not block promotion; got %d SetDraft calls: %+v", len(vp.setDraftCalls), vp.setDraftCalls)
	}
}

// TestMaybePromoteDraft_AllowlistedApprovalDoesNotBlock proves the State
// check is specific to "changes-requested": an allowlisted login's row
// recording an ordinary APPROVAL ("approved") must never block promotion.
// Without this case, a mutation flipping the State comparison could survive
// undetected by the withheld/non-allowlisted cases alone, since both of
// those short-circuit before the State comparison is even reached.
func TestMaybePromoteDraft_AllowlistedApprovalDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg([]string{"policy-bot"}))

	pr := selfDraftPR(223, "foo/bar", "feat/bot-approved")
	pr.HeadSHA = "sha-223"
	id := seedPRRow(t, ctx, db, "foo/bar", 223, "phillipg")
	if err := db.SetApproval(ctx, id, "policy-bot", "sha-223", "approved", "2026-08-25T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval: %v", err)
	}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 1 {
		t.Fatalf("an allowlisted APPROVAL (not a disapproval) must not block promotion; got %d SetDraft calls: %+v", len(vp.setDraftCalls), vp.setDraftCalls)
	}
}

// TestDraftPromoteBlockedByWIPOrBotVerdict_StoreErrorDegradesPermissively
// proves the store-error half of the gate's permissive degrade: a genuine
// store read error (not just a missing row) must not block promotion either,
// mirroring this package's established store-optional convention. Calls the
// gate helper directly (rather than the full maybePromoteDraft) because
// simulating the error by closing the store's connection would ALSO break
// the later emitPREvent write maybePromoteDraft performs on a successful
// promotion — an unrelated side effect of the same fixture, not something
// this gate's own contract is responsible for.
func TestDraftPromoteBlockedByWIPOrBotVerdict_StoreErrorDegradesPermissively(t *testing.T) {
	ctx := context.Background()
	e, _, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(224, "foo/bar", "feat/store-error")
	seedPRRow(t, ctx, db, "foo/bar", 224, "phillipg")
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if blocked := e.draftPromoteBlockedByWIPOrBotVerdict(ctx, "foo/bar", pr); blocked {
		t.Fatal("a store read error must degrade permissively (not block); got blocked=true")
	}
}

// TestMaybePromoteDraft_BlockedByMergeConflict_Mergeable covers the
// Mergeable=="CONFLICTING" half of api.PR.HasConflict().
func TestMaybePromoteDraft_BlockedByMergeConflict_Mergeable(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(230, "foo/bar", "feat/conflicting")
	seedPRRow(t, ctx, db, "foo/bar", 230, "phillipg")

	enriched := cleanEnrichedPR()
	enriched.PR.Mergeable = "CONFLICTING"

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, enriched, "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls with Mergeable=CONFLICTING; got %+v", vp.setDraftCalls)
	}
}

// TestMaybePromoteDraft_BlockedByMergeConflict_DirtyMergeState covers the
// MergeStateStatus=="DIRTY" half of api.PR.HasConflict().
func TestMaybePromoteDraft_BlockedByMergeConflict_DirtyMergeState(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(231, "foo/bar", "feat/dirty")
	seedPRRow(t, ctx, db, "foo/bar", 231, "phillipg")

	enriched := cleanEnrichedPR()
	enriched.PR.MergeStateStatus = "DIRTY"

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, enriched, "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls with MergeStateStatus=DIRTY; got %+v", vp.setDraftCalls)
	}
}

// TestMaybePromoteDraft_BlockedByUnknownMergeState_NilEnriched is the
// fail-closed case: enrichment never ran at all (enriched == nil), so merge
// state is unknown and MUST be treated as a conflict, per the binding
// operator ruling on pg2-4dz88.4.5. CI is deliberately green via the CICD
// fallback provider, so the merge-conflict gate — not a coincidentally
// missing CI signal — is what blocks this promotion.
func TestMaybePromoteDraft_BlockedByUnknownMergeState_NilEnriched(t *testing.T) {
	ctx := context.Background()
	e, vp, cicd, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(240, "foo/bar", "feat/unknown-nil")
	seedPRRow(t, ctx, db, "foo/bar", 240, "phillipg")
	cicd.runs[keyOf("foo/bar", 240)] = []api.CIRun{successRun()}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, nil, "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls with enriched==nil (unknown merge state, fail-closed); got %+v", vp.setDraftCalls)
	}
	if summary.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_BlockedByUnknownMergeState_MergeableUnknown is the
// other fail-closed trigger: enrichment DID run, but GitHub itself has not
// yet resolved mergeability (Mergeable == "UNKNOWN"). This must ALSO block,
// even though api.PR.HasConflict() itself deliberately treats UNKNOWN as "no
// conflict" for its own (unrelated) dashboard/attention purpose — see
// draftPromoteBlockedByConflict's doc for why the two are allowed to
// disagree.
func TestMaybePromoteDraft_BlockedByUnknownMergeState_MergeableUnknown(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(241, "foo/bar", "feat/unknown-mergeable")
	seedPRRow(t, ctx, db, "foo/bar", 241, "phillipg")

	enriched := cleanEnrichedPR()
	enriched.PR.Mergeable = "UNKNOWN"
	enriched.PR.MergeStateStatus = ""

	// Sanity-check the claim in the doc comment above: HasConflict() itself
	// says "not a conflict" for this exact state.
	if enriched.PR.HasConflict() {
		t.Fatalf("test setup invalid: HasConflict() must be false for Mergeable=UNKNOWN alone")
	}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, enriched, "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls with Mergeable=UNKNOWN (fail-closed); got %+v", vp.setDraftCalls)
	}
	if summary.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_NotDraftIsNoOp: a PR that is not currently draft is
// left alone entirely — no upstream call of any kind.
func TestMaybePromoteDraft_NotDraftIsNoOp(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(250, "foo/bar", "feat/ready-already")
	pr.Draft = false
	seedPRRow(t, ctx, db, "foo/bar", 250, "phillipg")

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected no upstream calls for a non-draft PR; got %+v", vp.setDraftCalls)
	}
	if summary.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", summary.DraftPromoted)
	}
}

// noOpDraftTogglerFacade implements VCSProvider by delegating to a wrapped
// *fakeVCS, but deliberately does NOT forward fakeVCS's SetDraft method (Go
// has no way to "un-promote" an embedded method, so a delegating facade is
// used instead of embedding). It therefore never satisfies DraftToggler,
// letting a test prove maybePromoteDraft degrades to a silent no-op — never
// an error — when the configured provider cannot toggle draft state at all.
func newNoDraftTogglerVCS() VCSProvider {
	return noOpDraftTogglerFacade{inner: newFakeVCS()}
}

type noOpDraftTogglerFacade struct {
	inner *fakeVCS
}

func (f noOpDraftTogglerFacade) GetPR(ctx context.Context, repo string, n int) (*api.PR, error) {
	return f.inner.GetPR(ctx, repo, n)
}

func (f noOpDraftTogglerFacade) ListMyPRs(ctx context.Context, repo string) ([]api.PR, error) {
	return f.inner.ListMyPRs(ctx, repo)
}

func (f noOpDraftTogglerFacade) ListTeamPRs(ctx context.Context, repo string, members []string) ([]api.PR, error) {
	return f.inner.ListTeamPRs(ctx, repo, members)
}

// TestMaybePromoteDraft_ProviderNotDraftToggler_NoOpNoError proves the type
// assertion's negative path: a VCSProvider that does not implement
// DraftToggler yields no promotion AND no error, never a panic or a hard
// failure.
func TestMaybePromoteDraft_ProviderNotDraftToggler_NoOpNoError(t *testing.T) {
	ctx := context.Background()
	vp := newNoDraftTogglerVCS()
	db := store.OpenForTest(t)
	e, err := New(Deps{
		Cfg:      draftPromoteTestCfg(nil),
		VCS:      map[string]VCSProvider{"github": vp},
		Store:    db,
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pr := selfDraftPR(260, "foo/bar", "feat/no-toggler")
	seedPRRow(t, ctx, db, "foo/bar", 260, "phillipg")

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft must not error when the provider lacks DraftToggler: %v", err)
	}
	if summary.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", summary.DraftPromoted)
	}
}

// TestMaybePromoteDraft_ReadyPRUnaffectedByLaterCIFailure,
// TestMaybePromoteDraft_ReadyPRUnaffectedByLaterConflict, and
// TestMaybePromoteDraft_ReadyPRUnaffectedByLaterBotWithheld each pin one leg
// of the invariant that this predicate gates PROMOTION ONLY: a PR that is
// already ready (Draft==false) must never be pushed back to draft by CI
// turning red, a conflict appearing, or a bot verdict flipping to withheld.
// The `!pr.Draft` guard at the top of maybePromoteDraft returns before any
// of the three checks are even evaluated, so these are regression locks on
// that ordering, one trigger per test as required by the plan.

func TestMaybePromoteDraft_ReadyPRUnaffectedByLaterCIFailure(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(270, "foo/bar", "feat/ready-ci-red")
	pr.Draft = false
	seedPRRow(t, ctx, db, "foo/bar", 270, "phillipg")

	enriched := cleanEnrichedPR()
	enriched.CIRuns = []api.CIRun{{Status: "completed", Conclusion: "failure"}}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, enriched, "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("a ready PR must never regress to draft on CI turning red; got %+v", vp.setDraftCalls)
	}
}

func TestMaybePromoteDraft_ReadyPRUnaffectedByLaterConflict(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg(nil))

	pr := selfDraftPR(271, "foo/bar", "feat/ready-conflict")
	pr.Draft = false
	seedPRRow(t, ctx, db, "foo/bar", 271, "phillipg")

	enriched := cleanEnrichedPR()
	enriched.PR.Mergeable = "CONFLICTING"

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, enriched, "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("a ready PR must never regress to draft on acquiring a conflict; got %+v", vp.setDraftCalls)
	}
}

func TestMaybePromoteDraft_ReadyPRUnaffectedByLaterBotWithheld(t *testing.T) {
	ctx := context.Background()
	e, vp, _, db := newDraftPromoteEngine(t, draftPromoteTestCfg([]string{"policy-bot"}))

	pr := selfDraftPR(272, "foo/bar", "feat/ready-bot-withheld")
	pr.Draft = false
	pr.HeadSHA = "sha-272"
	id := seedPRRow(t, ctx, db, "foo/bar", 272, "phillipg")
	if err := db.SetApproval(ctx, id, "policy-bot", "sha-272", "changes-requested", "2026-08-25T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval: %v", err)
	}

	summary := &Summary{}
	if err := e.maybePromoteDraft(ctx, cleanEnrichedPR(), "foo/bar", pr, summary); err != nil {
		t.Fatalf("maybePromoteDraft: %v", err)
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("a ready PR must never regress to draft on a bot verdict flipping to withheld; got %+v", vp.setDraftCalls)
	}
}

// TestSyncPR_CoOwnedPRNeverAutoPromoted proves the authorship guard from the
// REAL call path: a PR authored by a teammate, onto which self ALSO pushed a
// commit (ownership.Classify => CoOwned, not Team), with every OTHER gate
// green, must never be auto-promoted. Driven through Engine.SyncPR — never by
// calling maybePromoteDraft directly — because the guard that blocks this
// lives at the call site (isSelfAuthored, a raw login-equality check), not
// inside the predicate; calling the predicate directly would skip the guard
// entirely and prove nothing about it.
func TestSyncPR_CoOwnedPRNeverAutoPromoted(t *testing.T) {
	ctx := realBDCtx(t)

	vp := &enricherVCS{}
	vp.fakeVCS = *newFakeVCS()

	pr := teammatePR(300, "foo/bar", "feat/co-owned")
	vp.views[keyOf("foo/bar", 300)] = pr
	vp.ep = &vcs.EnrichedPR{
		PR:            api.PR{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
		CommitAuthors: []string{"coworker", "phillipg"}, // self pushed a commit too => co-owned
		CIRuns:        []api.CIRun{successRun()},
	}

	bd := newRealBDClient(t)
	db := store.OpenForTest(t)
	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vp},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.SyncPR(ctx, "foo/bar", 300)
	if err != nil {
		t.Fatalf("SyncPR: %v (errors=%+v)", err, sum.Errors)
	}

	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("a co-owned PR must never be auto-promoted; got SetDraft calls %+v", vp.setDraftCalls)
	}
	if sum.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", sum.DraftPromoted)
	}

	// Sanity-check the fixture actually produced "co-owned" (not "team"), so
	// this test is proven to exercise the case its name claims.
	row, err := db.GetPR(ctx, "foo/bar", 300)
	if err != nil || row == nil {
		t.Fatalf("GetPR: %v", err)
	}
	if row.Ownership != "co-owned" {
		t.Fatalf("test setup invalid: stored Ownership = %q, want co-owned", row.Ownership)
	}
}
