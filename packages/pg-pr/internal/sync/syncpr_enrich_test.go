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

// newEnrichSyncEngine builds an engine with a store but NO dispatcher, so
// flushOutbox is a no-op and emitted events stay in the raw outbox for direct
// inspection (mirrors TestRefreshPR_ConflictingTeamPR_DampensAttention). The
// provider is an enricherVCS: GetPR serves views (empty merge-state, like the
// production REST path) while EnrichPR (GraphQL) is the ONLY source of
// merge-state and comments.
func newEnrichSyncEngine(t *testing.T, vp *enricherVCS) (*Engine, *store.DB) {
	t.Helper()
	db := store.OpenForTest(t)
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos:     []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
		},
		VCS:      map[string]VCSProvider{"github": vp},
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e, db
}

// TestSyncPR_CarriesConflictFromEnrichment proves the CLI single-sync path
// (SyncPR) enriches the PR before applyFetchedPR, so GitHub's merge-state —
// available ONLY from GraphQL enrichment, never the REST GetPR the CLI uses —
// reaches the emitted pr.opened/updated payload. Pre-fix SyncPR passed
// enriched==nil, so overlayMergeState no-oped and PRPayload.HasConflict was
// always false, which flapped a daemon-stashed conflict priority in the bridge
// (reconcilePriority's clear branch). (pg2-ic3nh)
func TestSyncPR_CarriesConflictFromEnrichment(t *testing.T) {
	ctx := context.Background()

	// GetPR returns an OPEN PR with EMPTY merge-state (production REST omits it).
	pr := api.PR{
		Repo: "o/r", Number: 7, State: "open", Draft: false,
		Author: "me", HeadSHA: "h1", URL: "https://github.com/o/r/pull/7",
	}
	// Merge-state comes ONLY from EnrichPR. If GetPR carried MergeStateStatus,
	// the test would pass pre-fix and prove nothing.
	vp := &enricherVCS{
		fakeVCS: fakeVCS{views: map[string]api.PR{keyOf("o/r", pr.Number): pr}},
		ep:      &vcs.EnrichedPR{PR: api.PR{MergeStateStatus: "DIRTY"}},
	}
	e, db := newEnrichSyncEngine(t, vp)

	if _, err := e.SyncPR(ctx, "o/r", pr.Number); err != nil {
		t.Fatalf("SyncPR: %v", err)
	}
	if !vp.called {
		t.Fatal("expected SyncPR to enrich the PR via EnrichPR (GraphQL)")
	}

	var sawPR bool
	for _, ev := range collectOutboxEvents(t, db) {
		if ev.Type != store.EventPROpened && ev.Type != store.EventPRUpdated {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.Repo != "o/r" || p.Number != pr.Number {
			continue
		}
		sawPR = true
		if !p.HasConflict {
			t.Errorf("SyncPR must carry GitHub merge-conflict signal into the PR payload; got HasConflict=false")
		}
	}
	if !sawPR {
		t.Fatalf("expected a pr.opened/updated event for o/r#%d", pr.Number)
	}
}

// TestSyncPR_IngestsFeedbackFromEnrichment covers the (intended) behavior
// expansion the enrich fix brings to the CLI single-sync path: with a non-nil
// enriched, applyFetchedPR->processFeedback->ingestFeedbackToStore no longer
// early-returns, so SyncPR now ingests the PR's comments and emits
// feedback.created — matching the daemon per-PR path (the SinglePREnricher
// contract). Pre-fix (enriched==nil) SyncPR produced no feedback event. (pg2-ic3nh)
func TestSyncPR_IngestsFeedbackFromEnrichment(t *testing.T) {
	ctx := context.Background()

	pr := api.PR{
		Repo: "o/r", Number: 8, State: "open", Draft: false,
		Author: "me", HeadSHA: "h1", URL: "https://github.com/o/r/pull/8",
	}
	// A top-level (Path == "") comment becomes a pr-comments feedback row +
	// feedback.created event on ingestion. It is ingested because it is not
	// "ours" — ingest classifies ours by a body marker (pg-pr posts under the
	// user's own login), not by author login — and the plain body here carries
	// no such marker.
	vp := &enricherVCS{
		fakeVCS: fakeVCS{views: map[string]api.PR{keyOf("o/r", pr.Number): pr}},
		ep: &vcs.EnrichedPR{
			Comments: []api.Comment{{ID: "c1", Author: "teammate", Body: "please fix this"}},
		},
	}
	e, db := newEnrichSyncEngine(t, vp)

	if _, err := e.SyncPR(ctx, "o/r", pr.Number); err != nil {
		t.Fatalf("SyncPR: %v", err)
	}

	var sawFeedback bool
	for _, ev := range collectOutboxEvents(t, db) {
		if ev.Type == store.EventFeedbackCreated {
			sawFeedback = true
		}
	}
	if !sawFeedback {
		t.Fatalf("expected SyncPR to emit a feedback.created event from enrichment comments")
	}
}
