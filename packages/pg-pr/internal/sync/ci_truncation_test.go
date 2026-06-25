package sync

import (
	"context"
	"testing"

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
	ctx := context.Background()

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
	ctx := context.Background()

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
