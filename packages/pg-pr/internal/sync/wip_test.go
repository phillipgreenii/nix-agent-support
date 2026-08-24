package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ----------------------------------------------------------------------
// ApplyWIP: the WIP-ON / WIP-OFF transitions (pg2-4dz88.4.4).
// ----------------------------------------------------------------------

func TestApplyWIP_ReadyPRConvertsToDraft(t *testing.T) {
	vp := newFakeVCS()
	pr := api.PR{Repo: "foo/bar", Number: 10, State: "open"}

	converted, err := ApplyWIP(context.Background(), vp, "foo/bar", pr, true)
	if err != nil {
		t.Fatalf("ApplyWIP: %v", err)
	}
	if !converted {
		t.Fatal("expected converted=true for a ready PR")
	}
	if len(vp.setDraftCalls) != 1 || !vp.setDraftCalls[0].Draft {
		t.Fatalf("expected exactly one SetDraft(true) call; got %+v", vp.setDraftCalls)
	}
	if vp.setDraftCalls[0].Repo != "foo/bar" || vp.setDraftCalls[0].Number != 10 {
		t.Fatalf("SetDraft called with the wrong (repo, number): %+v", vp.setDraftCalls[0])
	}
}

func TestApplyWIP_AlreadyDraftPR_NoUpstreamCall(t *testing.T) {
	vp := newFakeVCS()
	pr := api.PR{Repo: "foo/bar", Number: 11, State: "open", Draft: true}

	converted, err := ApplyWIP(context.Background(), vp, "foo/bar", pr, true)
	if err != nil {
		t.Fatalf("ApplyWIP: %v", err)
	}
	if converted {
		t.Fatal("expected converted=false; the PR is already draft")
	}
	if len(vp.setDraftCalls) != 0 {
		t.Fatalf("expected NO SetDraft calls for an already-draft PR; got %+v", vp.setDraftCalls)
	}
}

// TestApplyWIP_MergedOrClosedPR_NoUpstreamCall pins the acceptance
// criterion: "a merged or closed PR carrying wip=true receives no upstream
// draft-toggle call".
func TestApplyWIP_MergedOrClosedPR_NoUpstreamCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   api.PR
	}{
		{"merged", api.PR{Repo: "foo/bar", Number: 12, State: "open", Merged: true}},
		{"closed", api.PR{Repo: "foo/bar", Number: 13, State: "closed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vp := newFakeVCS()
			converted, err := ApplyWIP(context.Background(), vp, "foo/bar", tc.pr, true)
			if err != nil {
				t.Fatalf("ApplyWIP: %v", err)
			}
			if converted {
				t.Fatalf("expected converted=false for a %s PR", tc.name)
			}
			if len(vp.setDraftCalls) != 0 {
				t.Fatalf("expected NO upstream draft-toggle call for a %s PR; got %+v", tc.name, vp.setDraftCalls)
			}
		})
	}
}

// TestApplyWIP_TurningOff_NeverCallsUpstream is the negative proof required
// by this leaf's own acceptance criteria: toggling WIP off — on a draft PR
// as much as a ready one — must never itself call SetDraft(false). The
// eventual return to ready is the (not-yet-rebuilt) promotion predicate's
// job, out of scope here.
func TestApplyWIP_TurningOff_NeverCallsUpstream(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   api.PR
	}{
		{"draft PR", api.PR{Repo: "foo/bar", Number: 14, State: "open", Draft: true}},
		{"ready PR", api.PR{Repo: "foo/bar", Number: 15, State: "open"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vp := newFakeVCS()
			converted, err := ApplyWIP(context.Background(), vp, "foo/bar", tc.pr, false)
			if err != nil {
				t.Fatalf("ApplyWIP: %v", err)
			}
			if converted {
				t.Fatal("expected converted=false; WIP-off never calls anything upstream")
			}
			if len(vp.setDraftCalls) != 0 {
				t.Fatalf("expected NO upstream call when turning WIP off; got %+v", vp.setDraftCalls)
			}
		})
	}
}

func TestApplyWIP_ProviderErrorPropagates(t *testing.T) {
	vp := newFakeVCS()
	vp.setDraftErr = errors.New("boom")
	pr := api.PR{Repo: "foo/bar", Number: 16, State: "open"}

	if _, err := ApplyWIP(context.Background(), vp, "foo/bar", pr, true); err == nil {
		t.Fatal("expected the provider's SetDraft error to propagate")
	}
}

// ----------------------------------------------------------------------
// Head-advance / lifecycle interaction with the ingestion pipeline.
// ----------------------------------------------------------------------

// newWIPIngestEngine builds a minimal Engine wired to db for the
// ingestFeedbackToStore-driven tests below, mirroring
// revision_test.go's newApprovalIngestEngine.
func newWIPIngestEngine(t *testing.T, db *store.DB) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos:     []config.RepoConfig{{Remote: "foo/bar", VCS: "github"}},
		},
		VCS:      map[string]VCSProvider{"github": newFakeVCS()},
		Beads:    &noopBeads{},
		StateDir: t.TempDir(),
		Store:    db,
		Now:      func() time.Time { return time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// TestIngestHeadAdvance_PreservesWIP proves the acceptance criterion "WIP
// persists across a head advance": a new revision recorded via the real
// ingestion path (ingestFeedbackToStore, internal/sync/ingest.go) while WIP
// is already true must leave the flag untouched (the store's no-clobber
// guarantee, pg2-4dz88.4.2, exercised end-to-end through this leaf's own
// call path rather than a raw store call).
func TestIngestHeadAdvance_PreservesWIP(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	e := newWIPIngestEngine(t, db)

	pr := api.PR{Repo: "foo/bar", Number: 20, State: "open", Draft: true, Author: "me", HeadSHA: "sha1"}
	if err := e.ingestFeedbackToStore(ctx, "foo/bar", pr, &vcs.EnrichedPR{PR: pr}); err != nil {
		t.Fatalf("ingest #1: %v", err)
	}
	if err := db.SetWIP(ctx, "foo/bar", 20, true); err != nil {
		t.Fatalf("SetWIP: %v", err)
	}

	// Head advance: a new commit lands while WIP is still true.
	pr.HeadSHA = "sha2"
	if err := e.ingestFeedbackToStore(ctx, "foo/bar", pr, &vcs.EnrichedPR{PR: pr}); err != nil {
		t.Fatalf("ingest #2 (head advance): %v", err)
	}

	got, err := db.GetPR(ctx, "foo/bar", 20)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if !got.WIP {
		t.Fatalf("WIP reset across head advance: %+v", got)
	}
	if got.HeadSHA != "sha2" {
		t.Fatalf("head advance not recorded: %+v", got)
	}
	revs, err := db.ListRevisions(ctx, got.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 revisions across the head advance, got %d: %+v", len(revs), revs)
	}
}

// TestIngestMergedPR_RetainsWIP proves the acceptance criterion "a merged
// or closed PR carrying wip=true receives no upstream draft-toggle call":
// ingesting a PR that goes from open+WIP to merged must retain the WIP
// flag, and nothing in the ingestion path (which never touches a VCS
// provider at all) attempts an upstream write for it.
func TestIngestMergedPR_RetainsWIP(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	e := newWIPIngestEngine(t, db)

	pr := api.PR{Repo: "foo/bar", Number: 21, State: "open", Draft: true, Author: "me", HeadSHA: "sha1"}
	if err := e.ingestFeedbackToStore(ctx, "foo/bar", pr, &vcs.EnrichedPR{PR: pr}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := db.SetWIP(ctx, "foo/bar", 21, true); err != nil {
		t.Fatalf("SetWIP: %v", err)
	}

	pr.Merged = true
	pr.State = "closed"
	if err := e.ingestFeedbackToStore(ctx, "foo/bar", pr, &vcs.EnrichedPR{PR: pr}); err != nil {
		t.Fatalf("ingest (merged): %v", err)
	}

	got, err := db.GetPR(ctx, "foo/bar", 21)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if !got.WIP {
		t.Fatalf("WIP flag lost across merge: %+v", got)
	}
	if got.State != "merged" {
		t.Fatalf("state = %q, want merged", got.State)
	}

	// ApplyWIP itself must also refuse to act on the now-merged PR, matching
	// the store-level observation above (belt-and-suspenders: this is the
	// same guard TestApplyWIP_MergedOrClosedPR_NoUpstreamCall exercises in
	// isolation, re-asserted here against the PR shape ingestion produced).
	converted, err := ApplyWIP(ctx, newFakeVCS(), "foo/bar", pr, true)
	if err != nil {
		t.Fatalf("ApplyWIP: %v", err)
	}
	if converted {
		t.Fatal("expected converted=false for a merged PR")
	}
}
