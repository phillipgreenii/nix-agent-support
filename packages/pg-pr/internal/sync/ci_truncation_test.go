package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

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
//   - P2 (bulk, >30 contexts / reconcileTruncatedCI): the real gap
//     pg2-4dz88.2.7 fixed -- TestReconcileTruncatedCI_PreservesClaimedGateRun
//     proves the merge in isolation, TestBulkTruncatedGateCheck_StillPersistsGateState
//     proves it end-to-end through the store.
//   - P3 (per-PR / enrichOnePR path): had a SEPARATE, wider gap of its own
//     (pg2-g9fu0) -- enrichOnePR discarded GraphQL's statusCheckRollup CIRuns
//     entirely on every call, not just on truncation, so a classic
//     commit-Status-API gate check (e.g. policy-bot) could NEVER be observed
//     via this path, regardless of the 30-context cap. This is the path the
//     daemon's per-PR refresh (refreshPR) actually runs on every tick, which
//     is why gate_state never left "unknown" in production.
//     TestPerPRPath_GateCheckNeverObserved_PreservesUnknown pins the
//     genuinely-unclaimable case (no GraphQL enrichment available at all);
//     TestPerPRPath_ClassicStatusGateCheck_PersistsGateState proves the fix:
//     when GraphQL enrichment IS available and carries a claimed classic
//     status, gate_state now populates through refreshPR exactly like the
//     bulk path.
// ----------------------------------------------------------------------

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

// TestPerPRPath_GateCheckNeverObserved_PreservesUnknown is P3's negative
// case: a regression test proving the per-PR path (refreshPR -> enrichOnePR
// -> applyFetchedPR -> processFeedback -> ingestFeedbackToStore) behaves
// correctly when NO GraphQL enrichment is available at all (the VCS fake
// here, plain fakeVCS, does not implement SinglePREnricher, so enrichOnePR's
// GraphQL branch never fires and CI comes solely from the CICD-only
// provider -- matching a real single-PR GraphQL failure). gateStateFromSync
// therefore reports ok==false, and ingestFeedbackToStore's
// write-only-when-ok design (see gateStateFromSync's doc comment,
// revision.go) skips SetRevisionGateState entirely. Both halves of the
// bead's "not silently satisfied and not vanished" pair are asserted: a
// never-yet-observed revision stays at the DB default "unknown" (never a
// false "satisfied"), and a revision that already carries a REAL prior gate
// observation (e.g. from an earlier bulk tick) is left UNCHANGED by a
// subsequent per-PR-only tick that observes no gate-claimed run. Contrast
// with TestPerPRPath_ClassicStatusGateCheck_PersistsGateState below, which
// covers the case GraphQL enrichment IS available (pg2-g9fu0's actual fix).
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

// TestPerPRPath_ClassicStatusGateCheck_PersistsGateState is P3's fix proof
// (pg2-g9fu0): the per-PR daemon path (refreshPR -> enrichOnePR) must
// observe a classic commit-Status-API gate check (e.g. policy-bot) exactly
// like the bulk path already does, even though CI otherwise still comes
// from the dedicated (Actions-only) CICD provider. Before the fix,
// enrichOnePR unconditionally discarded GraphQL's statusCheckRollup-derived
// CIRuns -- the only source that can ever carry such a run -- so gate_state
// never left "unknown" for a PR observed exclusively through this path,
// matching the bead's live-DB evidence of 0/1541 rows ever populated.
func TestPerPRPath_ClassicStatusGateCheck_PersistsGateState(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{}

	pr := api.PR{
		Repo: "o/r", Number: 42, State: "open",
		Branch: "feat/w", Base: "main", Author: "me",
		URL: "https://github.com/o/r/pull/42", HeadSHA: "sha-per-pr-classic-status",
	}

	// GraphQL's single-PR enrichment (the ONLY source of a classic
	// commit-Status-API run) carries the policy-bot-shaped status alongside
	// an ordinary check-run, exactly as GitHub's real statusCheckRollup
	// would.
	vp := &enricherVCS{ep: &vcs.EnrichedPR{
		CIRuns: []api.CIRun{
			{Name: "build", Status: "completed", Conclusion: "success", Provider: "github-actions", HeadSHA: pr.HeadSHA},
			{
				Name:        "policy-bot: approval required (click for details): main",
				Status:      "completed",
				Conclusion:  "failure",
				Description: "0/1 rules approved",
				Provider:    "github-status",
				HeadSHA:     pr.HeadSHA,
			},
		},
	}}
	vp.fakeVCS = *newFakeVCS()
	vp.views[keyOf("o/r", pr.Number)] = pr

	ci := newFakeCICD()
	// The dedicated CICD provider is Actions-only: it never carries the
	// policy-bot commit-status run, matching reality.
	ci.runs[keyOf("o/r", pr.Number)] = []api.CIRun{
		{Name: "build", Status: "completed", Conclusion: "success", Provider: "github-actions", HeadSHA: pr.HeadSHA},
	}

	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos: []config.RepoConfig{{
				Remote: "o/r", VCS: "github", CICD: []string{"ci"},
				CheckInterpreters: []config.CheckInterpreterConfig{
					{Patterns: []string{"^policy-bot"}, Type: "approval-gate"},
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
	if rev.GateState != "unsatisfied" || rev.GateStateN != 0 || rev.GateStateM != 1 {
		t.Fatalf("gate state via the per-PR path = %q (n=%d m=%d), want unsatisfied (n=0 m=1) -- a classic commit-Status-API check must be observed exactly like the bulk path",
			rev.GateState, rev.GateStateN, rev.GateStateM)
	}

	// The merge must be additive: the CICD-sourced "build" check-run's own
	// CI rollup must still reflect success, and the gate-bot run (excluded
	// from the rollup by its own CheckInterpreters entry, like any claimed
	// check) must not produce a ci-failure row despite its own "failure"
	// conclusion -- proving the merge feeds gate classification without also
	// corrupting the ordinary CI-failure ingestion path.
	rows, err := db.ListFeedback(ctx, storedPR.ID, store.ListFilter{Kind: "ci-failure"})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no ci-failure rows (build succeeded; policy-bot is a claimed/excluded gate check), got %d: %+v", len(rows), rows)
	}
}
