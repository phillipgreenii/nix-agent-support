package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// enrichedPRsVCS embeds fakeVCS and adds the EnrichedPRsProvider capability so
// the BULK daemon path (enumerate -> tryEnumerateEnriched -> EnrichedPRs) is
// exercised. ep is returned verbatim for every EnrichedPRs call (one repo per
// test), letting a test inject a deliberately TRUNCATED statusCheckRollup
// (first:30 contexts + Truncated:["ciContexts"]) to reproduce the >30-context
// bug class.
type enrichedPRsVCS struct {
	fakeVCS
	eps []vcs.EnrichedPR
}

func (e *enrichedPRsVCS) EnrichedPRs(_ context.Context, _ string, _ string) ([]vcs.EnrichedPR, error) {
	return e.eps, nil
}

// truncatedCIRuns returns 30 completed+success CheckRun entries — the cap the
// bulk GraphQL statusCheckRollup.contexts(first:30) returns. The 31st (failing)
// check is intentionally NOT in this slice: it is beyond the page cap, so the
// bulk path never sees it. The dedicated CICD provider (which paginates fully)
// is the only source that carries the failing 31st check.
func truncatedCIRuns(headSHA string) []api.CIRun {
	runs := make([]api.CIRun, 0, 30)
	for i := 0; i < 30; i++ {
		runs = append(runs, api.CIRun{
			ID:         "ctx-" + itoa(i),
			Name:       "check-" + itoa(i),
			Status:     "completed",
			Conclusion: "success",
			Provider:   "github-actions",
			HeadSHA:    headSHA,
		})
	}
	return runs
}

// fullCIRuns is what the dedicated CICD provider returns: all 31 contexts,
// including the failing 31st check beyond the GraphQL first:30 cap.
func fullCIRuns(headSHA string) []api.CIRun {
	runs := truncatedCIRuns(headSHA)
	return append(runs, api.CIRun{
		ID:         "ctx-30-failing",
		Name:       "check-30-failing",
		Status:     "completed",
		Conclusion: "failure",
		Provider:   "github-actions",
		HeadSHA:    headSHA,
		URL:        "https://github.com/foo/bar/runs/30",
	})
}

// TestBulkCIFailureBeyondContext30_Ingested reproduces consequence (1): a
// failing check beyond statusCheckRollup.contexts(first:30) is MISSED by the
// bulk-path ci-failure ingestion because EnrichedPR.CIRuns is truncated at 30.
// After the fix (re-source CI from the CICD provider when ciContexts truncates)
// the failing 31st check produces a ci-failure feedback row.
func TestBulkCIFailureBeyondContext30_Ingested(t *testing.T) {
	ctx := realBDCtx(t)

	const headSHA = "head-sha-abc"
	// A team-authored open PR (ingestion runs for mine + team alike).
	pr := samplePR(77, "foo/bar", "feat/big-ci")
	pr.Author = "coworker"
	pr.HeadSHA = headSHA

	vp := &enrichedPRsVCS{}
	vp.fakeVCS = *newFakeVCS()
	// Bulk GraphQL returns the PR with a TRUNCATED rollup (only 30 green
	// contexts) and the ciContexts truncation flag — the failing 31st check is
	// beyond the cap and absent here.
	vp.eps = []vcs.EnrichedPR{{
		PR:        pr,
		CIRuns:    truncatedCIRuns(headSHA),
		Truncated: []string{"ciContexts"},
	}}

	ci := newFakeCICD()
	// The dedicated CICD provider paginates fully: it carries the failing 31st.
	ci.runs[keyOf("foo/bar", 77)] = fullCIRuns(headSHA)

	bd := newRealBDClient(t)
	db := store.OpenForTest(t)

	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vp},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "foo/bar", 77)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v", err)
	}
	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	var ciFailures int
	for _, r := range rows {
		if r.Kind == "ci-failure" && r.CheckName == "check-30-failing" {
			ciFailures++
		}
	}
	if ciFailures != 1 {
		t.Fatalf("expected the failing check beyond context 30 to produce 1 ci-failure feedback row; got %d (all rows: %+v)", ciFailures, rows)
	}
}

// TestBulkDraftPromoteWithFailingCheckBeyond30_DoesNotPromote reproduces
// consequence (2): a self-authored draft PR is WRONGLY promoted to ready when a
// check beyond statusCheckRollup.contexts(first:30) is failing, because
// maybePromoteDraft sees only the truncated "all green" first-30 set and does
// NOT consult the aggregate rollup .state. After the fix (re-source CI from the
// CICD provider when ciContexts truncates) the failing 31st check blocks the
// promotion.
func TestBulkDraftPromoteWithFailingCheckBeyond30_DoesNotPromote(t *testing.T) {
	ctx := realBDCtx(t)

	const headSHA = "head-sha-draft"
	pr := selfDraftPR(88, "foo/bar", "feat/draft-big-ci")
	pr.State = "open"
	pr.HeadSHA = headSHA

	vp := &enrichedPRsVCS{}
	vp.fakeVCS = *newFakeVCS()
	vp.eps = []vcs.EnrichedPR{{
		PR:        pr,
		CIRuns:    truncatedCIRuns(headSHA), // 30 green; failing 31st is beyond cap
		Truncated: []string{"ciContexts"},
	}}

	ci := newFakeCICD()
	ci.runs[keyOf("foo/bar", 88)] = fullCIRuns(headSHA) // includes the failing 31st

	bd := newRealBDClient(t)
	db := store.OpenForTest(t)

	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vp},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("draft must NOT be promoted while a check beyond context 30 is failing; got SetDraft calls %+v", vp.setDraftCalls)
	}
	if sum.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0 (a >30 check is failing)", sum.DraftPromoted)
	}
}

func TestReconcileTruncatedCI(t *testing.T) {
	ctx := context.Background()
	rcfg := cfgWithCICD().Repos[0] // CICD: []string{"ci"}

	t.Run("ciContexts truncated re-sources from CICD and clears flag", func(t *testing.T) {
		ci := newFakeCICD()
		ci.runs[keyOf("foo/bar", 1)] = []api.CIRun{{Name: "from-cicd", Conclusion: "failure"}}
		e := &Engine{deps: Deps{CICD: map[string]CICDProvider{"ci": ci}}}
		ep := &vcs.EnrichedPR{
			PR:        api.PR{Repo: "foo/bar", Number: 1, Branch: ""}, // empty branch -> ListRuns path
			CIRuns:    []api.CIRun{{Name: "from-graphql-truncated"}},
			Truncated: []string{"reviewThreads", "ciContexts"},
		}
		e.reconcileTruncatedCI(ctx, ep, rcfg)
		if len(ep.CIRuns) != 1 || ep.CIRuns[0].Name != "from-cicd" {
			t.Errorf("CI should be re-sourced from CICD, got %+v", ep.CIRuns)
		}
		if len(ep.Truncated) != 1 || ep.Truncated[0] != "reviewThreads" {
			t.Errorf("ciContexts flag should be cleared (other flags kept), got %v", ep.Truncated)
		}
	})

	t.Run("non-truncated CI is left untouched (happy path)", func(t *testing.T) {
		ci := newFakeCICD()
		ci.runs[keyOf("foo/bar", 2)] = []api.CIRun{{Name: "from-cicd"}}
		e := &Engine{deps: Deps{CICD: map[string]CICDProvider{"ci": ci}}}
		ep := &vcs.EnrichedPR{
			PR:        api.PR{Repo: "foo/bar", Number: 2},
			CIRuns:    []api.CIRun{{Name: "from-graphql"}},
			Truncated: nil,
		}
		e.reconcileTruncatedCI(ctx, ep, rcfg)
		if len(ep.CIRuns) != 1 || ep.CIRuns[0].Name != "from-graphql" {
			t.Errorf("non-truncated CI must NOT be replaced, got %+v", ep.CIRuns)
		}
	})

	t.Run("no CICD provider leaves partial CI + flag intact", func(t *testing.T) {
		e := &Engine{deps: Deps{}} // no CICD configured
		ep := &vcs.EnrichedPR{
			PR:        api.PR{Repo: "foo/bar", Number: 3},
			CIRuns:    []api.CIRun{{Name: "partial"}},
			Truncated: []string{"ciContexts"},
		}
		e.reconcileTruncatedCI(ctx, ep, rcfg)
		if len(ep.CIRuns) != 1 || ep.CIRuns[0].Name != "partial" {
			t.Errorf("with no CICD provider, partial CI must be left intact, got %+v", ep.CIRuns)
		}
		if len(ep.Truncated) != 1 || ep.Truncated[0] != "ciContexts" {
			t.Errorf("with no CICD provider, ciContexts flag must remain, got %v", ep.Truncated)
		}
	})

	t.Run("nil enriched is a no-op", func(t *testing.T) {
		e := &Engine{deps: Deps{}}
		e.reconcileTruncatedCI(ctx, nil, rcfg) // must not panic
	})
}

// ----------------------------------------------------------------------
// Gate-state preservation across the three ingest paths (pg2-4dz88.2.7)
//
// The bead's own acceptance criteria name three paths and ask for proof that
// each is "not silently satisfied and not vanished":
//
//   - P1 (bulk, <=30 contexts): already correct pre-existing behaviour --
//     TestBulkGateCheckObserved_PersistsGateState pins it as a regression
//     guard.
//   - P2 (bulk, >30 contexts / reconcileTruncatedCI): the real gap this bead
//     fixes -- TestReconcileTruncatedCI_PreservesClaimedGateRun proves the
//     merge in isolation, TestBulkTruncatedGateCheck_StillPersistsGateState
//     proves it end-to-end through the store.
//   - P3 (per-PR / enrichOnePR path): already correct by construction --
//     TestPerPRPath_GateCheckNeverObserved_PreservesUnknown pins both halves
//     of "preserved, never falsely satisfied".
// ----------------------------------------------------------------------

// TestBulkGateCheckObserved_PersistsGateState is P1: a regression test for
// the ALREADY-CORRECT bulk (<=30 contexts, no truncation) happy path. The
// bulk GraphQL fetch's enriched.CIRuns carries the gate's real, Description-
// bearing commit-status run, so gateStateFromSync classifies it and
// ingestFeedbackToStore persists a real gate_state -- this predates
// pg2-4dz88.2.7 (landed by pg2-4dz88.2.6) and needed no code change here.
func TestBulkGateCheckObserved_PersistsGateState(t *testing.T) {
	ctx := realBDCtx(t)

	const headSHA = "head-sha-p1"
	pr := samplePR(51, "foo/bar", "feat/gate")
	pr.Author = "coworker"
	pr.HeadSHA = headSHA

	gateRun := api.CIRun{
		ID: "gate-run-1", Name: "gate-bot: approval required", Status: "completed",
		Conclusion: "failure", Description: "0/1 rules approved",
		Provider: "github-actions", HeadSHA: headSHA,
	}

	vp := &enrichedPRsVCS{}
	vp.fakeVCS = *newFakeVCS()
	// Well under the 30-context page cap -- no truncation flag, so
	// reconcileTruncatedCI never engages on this path.
	vp.eps = []vcs.EnrichedPR{{
		PR:     pr,
		CIRuns: []api.CIRun{gateRun},
	}}

	cfg := cfgWithCICD()
	cfg.Repos[0].CheckInterpreters = []config.CheckInterpreterConfig{
		{Patterns: []string{"^gate-bot"}, Type: "approval-gate"},
	}

	bd := newRealBDClient(t)
	db := store.OpenForTest(t)

	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": vp},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "foo/bar", 51)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v", err)
	}
	rev, err := db.LatestRevision(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("LatestRevision: %v", err)
	}
	if rev == nil {
		t.Fatal("LatestRevision: got nil, want a revision")
	}
	if rev.GateState != "unsatisfied" || rev.GateStateN != 0 || rev.GateStateM != 1 {
		t.Fatalf("gate state = %q (n=%d m=%d), want unsatisfied (n=0 m=1)",
			rev.GateState, rev.GateStateN, rev.GateStateM)
	}
}

// TestReconcileTruncatedCI_PreservesClaimedGateRun is P2's isolated proof:
// reconcileTruncatedCI's wholesale re-source from the CICD provider must not
// discard a run a configured check-interpreter claims from the ORIGINAL
// (pre-reconcile) CIRuns -- e.g. an approval gate whose Description only
// GraphQL's commit-status rollup can carry. The CICD provider's own
// re-sourced runs are Actions-only and structurally can never reproduce such
// a run, so without the merge the gate observation would be silently
// dropped on every truncated tick.
func TestReconcileTruncatedCI_PreservesClaimedGateRun(t *testing.T) {
	ctx := context.Background()
	rcfg := config.RepoConfig{
		Remote: "foo/bar",
		CICD:   []string{"ci"},
		CheckInterpreters: []config.CheckInterpreterConfig{
			{Patterns: []string{"^gate-bot"}, Type: "approval-gate"},
		},
	}

	const headSHA = "head-sha-p2-isolated"
	gateRun := api.CIRun{
		Name: "gate-bot: approval required", Status: "completed",
		Conclusion: "failure", Description: "1/2 rules approved",
		HeadSHA: headSHA,
	}

	ci := newFakeCICD()
	// The CICD re-source is Actions-only: green + one failing, no gate-bot
	// entry at all -- matching reality (a CICD provider cannot produce one).
	ci.runs[keyOf("foo/bar", 61)] = fullCIRuns(headSHA)

	e := &Engine{deps: Deps{CICD: map[string]CICDProvider{"ci": ci}}}
	ep := &vcs.EnrichedPR{
		PR: api.PR{Repo: "foo/bar", Number: 61, Branch: ""}, // empty branch -> ListRuns path
		CIRuns: append(
			append([]api.CIRun{}, truncatedCIRuns(headSHA)...),
			gateRun,
		),
		Truncated: []string{"ciContexts"},
	}

	e.reconcileTruncatedCI(ctx, ep, rcfg)

	if len(ep.Truncated) != 0 {
		t.Errorf("ciContexts flag should be cleared, got %v", ep.Truncated)
	}

	var found *api.CIRun
	for i := range ep.CIRuns {
		if ep.CIRuns[i].Name == "gate-bot: approval required" {
			found = &ep.CIRuns[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("gate-bot run must survive the CICD re-source, got %+v", ep.CIRuns)
	}
	if found.Description != "1/2 rules approved" {
		t.Errorf("gate-bot run's original Description must be preserved intact, got %q", found.Description)
	}

	// Additive, not exclusive: every CICD-provided run (fullCIRuns: 30 green
	// + 1 failing) must still be present alongside the preserved gate run.
	want := fullCIRuns(headSHA)
	seen := map[string]bool{}
	for _, r := range ep.CIRuns {
		seen[r.Name] = true
	}
	for _, r := range want {
		if !seen[r.Name] {
			t.Errorf("expected CICD-provided run %q to survive the merge, got %+v", r.Name, ep.CIRuns)
		}
	}
}

// TestBulkTruncatedGateCheck_StillPersistsGateState is P2's full-pipeline
// proof, exercising the bead's core fix end-to-end through the store: a PR
// genuinely over the 30-context cap whose original bulk-fetched CIRuns DID
// carry the gate's real Description-bearing run must still get a real
// gate_state persisted after reconcileTruncatedCI re-sources CI from the
// CICD provider. Before the fix, this PR would be starved at "unknown"
// forever on every tick (the CICD provider can never reproduce a
// commit-status Description) -- exactly the class of PR the parent bead's
// own measurement sampled at 70-139 checks.
func TestBulkTruncatedGateCheck_StillPersistsGateState(t *testing.T) {
	ctx := realBDCtx(t)

	const headSHA = "head-sha-p2-full"
	pr := samplePR(52, "foo/bar", "feat/gate-truncated")
	pr.Author = "coworker"
	pr.HeadSHA = headSHA

	gateRun := api.CIRun{
		ID: "gate-run-2", Name: "gate-bot: approval required", Status: "completed",
		Conclusion: "failure", Description: "0/1 rules approved",
		Provider: "github-actions", HeadSHA: headSHA,
	}

	vp := &enrichedPRsVCS{}
	vp.fakeVCS = *newFakeVCS()
	// Original bulk fetch: the 30-context page cap PLUS the gate run --
	// genuinely 31 total, over the cap -- with the ciContexts truncation flag
	// set exactly as GitHub's real statusCheckRollup would report it.
	vp.eps = []vcs.EnrichedPR{{
		PR:        pr,
		CIRuns:    append(truncatedCIRuns(headSHA), gateRun),
		Truncated: []string{"ciContexts"},
	}}

	ci := newFakeCICD()
	// The dedicated CICD provider paginates fully but is Actions-only: it
	// never carries the gate-bot commit-status run.
	ci.runs[keyOf("foo/bar", 52)] = fullCIRuns(headSHA)

	cfg := cfgWithCICD()
	cfg.Repos[0].CheckInterpreters = []config.CheckInterpreterConfig{
		{Patterns: []string{"^gate-bot"}, Type: "approval-gate"},
	}

	bd := newRealBDClient(t)
	db := store.OpenForTest(t)

	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": vp},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "foo/bar", 52)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v", err)
	}
	rev, err := db.LatestRevision(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("LatestRevision: %v", err)
	}
	if rev == nil {
		t.Fatal("LatestRevision: got nil, want a revision")
	}
	if rev.GateState != "unsatisfied" || rev.GateStateN != 0 || rev.GateStateM != 1 {
		t.Fatalf("gate state on a truncated tick = %q (n=%d m=%d), want unsatisfied (n=0 m=1) -- truncation must not starve the gate observation",
			rev.GateState, rev.GateStateN, rev.GateStateM)
	}

	// The CICD-sourced failing 31st check must still produce its own
	// ci-failure row, and the gate-bot run (excluded from the rollup by its
	// own CheckInterpreters entry, like any claimed check) must not --
	// proving the merge is additive to, not a replacement of, the sibling
	// truncation-repair fix (pg2-4dz88.2 / TestBulkCIFailureBeyondContext30_Ingested).
	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{Kind: "ci-failure"})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(rows) != 1 || rows[0].CheckName != "check-30-failing" {
		t.Fatalf("expected exactly 1 ci-failure row (check-30-failing; gate-bot excluded), got %d: %+v", len(rows), rows)
	}
}

// TestPerPRPath_GateCheckNeverObserved_PreservesUnknown is P3: a regression
// test proving the ALREADY-CORRECT per-PR path (refreshPR -> enrichOnePR ->
// applyFetchedPR -> processFeedback -> ingestFeedbackToStore). enrichOnePR's
// CI always comes from the dedicated CICD provider (Actions-only), never
// from GraphQL's statusCheckRollup, so it can structurally never carry a
// commit-status run with a Description -- gateStateFromSync therefore
// always reports ok==false on this path, and ingestFeedbackToStore's
// write-only-when-ok design (see gateStateFromSync's doc comment,
// revision.go) skips SetRevisionGateState entirely. Both halves of the
// bead's "not silently satisfied and not vanished" pair are asserted: a
// never-yet-observed revision stays at the DB default "unknown" (never a
// false "satisfied"), and a revision that already carries a REAL prior gate
// observation (e.g. from an earlier bulk tick) is left UNCHANGED by a
// subsequent per-PR-only tick.
func TestPerPRPath_GateCheckNeverObserved_PreservesUnknown(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{}

	pr := api.PR{
		Repo: "o/r", Number: 41, State: "open",
		Branch: "feat/w", Base: "main", Author: "me",
		URL: "https://github.com/o/r/pull/41", HeadSHA: "sha-per-pr-1",
	}

	vp := newFakeVCS()
	vp.views[keyOf("o/r", pr.Number)] = pr

	ci := newFakeCICD()
	// The CICD provider is Actions-only -- structurally it can never carry a
	// gate-shaped commit-status run, matching reality.
	ci.runs[keyOf("o/r", pr.Number)] = []api.CIRun{
		{Name: "build", Status: "completed", Conclusion: "success", Provider: "github-actions", HeadSHA: pr.HeadSHA},
	}

	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos: []config.RepoConfig{{
				Remote: "o/r", VCS: "github", CICD: []string{"ci"},
				CheckInterpreters: []config.CheckInterpreterConfig{
					{Patterns: []string{"^gate-bot"}, Type: "approval-gate"},
				},
			}},
		},
		VCS:      map[string]VCSProvider{"github": vp},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bdc,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := e.refreshPR(ctx, "o/r", pr.Number); err != nil {
		t.Fatalf("refreshPR: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "o/r", pr.Number)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: %v", err)
	}
	rev, err := db.LatestRevision(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("LatestRevision: %v", err)
	}
	if rev == nil {
		t.Fatal("LatestRevision: got nil, want a revision")
	}
	if rev.GateState != "unknown" {
		t.Fatalf("gate state on a per-PR-only observed revision: got %q, want %q (never a false satisfied/other value)", rev.GateState, "unknown")
	}

	// Seed a prior REAL gate observation (as an earlier bulk tick would have
	// recorded) and prove a subsequent per-PR-only tick leaves it untouched.
	if err := db.SetRevisionGateState(ctx, rev.ID, store.GateState{State: "satisfied", CapturedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("seed SetRevisionGateState: %v", err)
	}

	if _, err := e.refreshPR(ctx, "o/r", pr.Number); err != nil {
		t.Fatalf("refreshPR (second tick): %v", err)
	}
	rev2, err := db.LatestRevision(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("LatestRevision (after 2nd tick): %v", err)
	}
	if rev2 == nil {
		t.Fatal("LatestRevision (after 2nd tick): got nil")
	}
	if rev2.GateState != "satisfied" {
		t.Fatalf("a prior real gate_state must be PRESERVED across a per-PR-only tick that observes no gate-claimed run; got %q, want %q", rev2.GateState, "satisfied")
	}
}
