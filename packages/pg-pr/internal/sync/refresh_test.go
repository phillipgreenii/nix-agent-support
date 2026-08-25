package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fakeDepBeads embeds noopBeads (which already satisfies the BeadClient
// interface) and adds the depTreeReader methods so buildPRInput's dep path runs
// without a *beads.Client / real bd workspace.
type fakeDepBeads struct {
	noopBeads
	mrID  string
	deps  []beads.DepNode
	human map[string]bool
}

func (f *fakeDepBeads) FindByRepoAndNumber(_ context.Context, _ string, _ int) (*beads.MergeRequest, error) {
	if f.mrID == "" {
		return nil, nil
	}
	return &beads.MergeRequest{ID: f.mrID}, nil
}

func (f *fakeDepBeads) DepTreeUp(_ context.Context, _ string) ([]beads.DepNode, error) {
	return f.deps, nil
}

func (f *fakeDepBeads) HumanLabeledBeads(_ context.Context) (map[string]bool, error) {
	return f.human, nil
}

func TestBuildPRInput_AppliesHumanLabelWithoutCache(t *testing.T) {
	bdc := &fakeDepBeads{
		mrID: "mr-1",
		deps: []beads.DepNode{{ID: "fb-1", Status: "open"}},
	}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The human-label overlay now comes from the engine's atomic set, not a
	// per-PR HumanLabeledBeads call.
	e.humanLabels.Store(&map[string]map[string]bool{"o/r": {"fb-1": true}})
	pr := api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}

	in := e.buildPRInput(context.Background(), pr, nil, bdc, nil, config.RepoConfig{Remote: "o/r"}, "")

	if len(in.BeadsDeps) != 1 {
		t.Fatalf("want 1 dep, got %d", len(in.BeadsDeps))
	}
	found := false
	for _, l := range in.BeadsDeps[0].Labels {
		if l == "human" {
			found = true
		}
	}
	if !found {
		t.Fatal("human label not applied on cache-less path; WaitingOnMe will regress")
	}
}

// TestBuildPRInput_OverlaysMergeability guards against the pg2-dwfld daemon
// gap: on the daemon refresh path, `pr` comes from REST GetPR and never
// carries GitHub's merge-state fields, while `enriched.PR` (from GraphQL, via
// enrichOnePR) does. buildPRInput must overlay those fields onto in.PR so
// MineRow.NeedsMergeReminder can fire from the live snapshot, not just from
// one-shot `pg-pr sync`.
func TestBuildPRInput_OverlaysMergeability(t *testing.T) {
	bdc := &fakeDepBeads{}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pr := api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}
	enriched := &vcs.EnrichedPR{PR: api.PR{MergeStateStatus: "CLEAN", AutoMergeEnabled: false}}

	in := e.buildPRInput(context.Background(), pr, enriched, bdc, nil, config.RepoConfig{Remote: "o/r"}, "")

	if in.PR.MergeStateStatus != "CLEAN" {
		t.Errorf("expected in.PR.MergeStateStatus overlaid from enriched.PR, got %q", in.PR.MergeStateStatus)
	}
}

// ----------------------------------------------------------------------
// refreshPR tests
// ----------------------------------------------------------------------

// refreshFakeBeads is a minimal sync.BeadClient that satisfies the slim
// {ListMergeRequests} interface. It is used by the refreshPR tests.
//
// After the pg2-4c5i.18 slim, EnsureMergeRequest/CloseMergeRequest are gone
// from sync.BeadClient — the engine cannot call them inline. Regression guards
// that previously checked bdc.closed / bdc.lastState are now event-based:
// tests assert that the correct outbox event was emitted (collectOutboxEvents).
//
// existing, when non-nil, is returned by ListMergeRequests so the engine's
// pre-existing-bead index (listExistingByKey) finds a bead for this PR.
type refreshFakeBeads struct {
	existing *beads.MergeRequest
}

func (f *refreshFakeBeads) ListMergeRequests(_ context.Context, _ bool) ([]beads.MergeRequest, error) {
	if f.existing == nil {
		return nil, nil
	}
	return []beads.MergeRequest{*f.existing}, nil
}

// newRefreshEngine builds an Engine over a single repo "o/r" with the given
// self login, the supplied fake bead client, and a fakeVCS whose GetPR
// returns pr.
func newRefreshEngine(t *testing.T, self string, bdc BeadClient, pr api.PR) *Engine {
	t.Helper()
	vcs := newFakeVCS()
	vcs.views[keyOf("o/r", pr.Number)] = pr
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: self,
			Repos: []config.RepoConfig{
				{Remote: "o/r", VCS: "github", TeamMembers: []string{"teammate"}},
			},
		},
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bdc,
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// TestRefreshPR_ClosedMergedTeam_ClosesAndRemoves: a merged TEAM-authored PR
// (own != Mine) is genuinely detected as merged and removed from the
// dashboard immediately (nil input) — pg2-ew4kf's retention grace period is
// deliberately scoped to PRs authored by ME; a teammate's merge is unchanged.
// The bead close still happens via an emitted pr.merged event (the bridge
// cascade-closes), NOT an inline CloseMergeRequest on the engine's bd client.
func TestRefreshPR_ClosedMergedTeam_ClosesAndRemoves(t *testing.T) {
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{
		existing: &beads.MergeRequest{
			ID:     "mr-1",
			Fields: beads.MergeRequestFields{Repo: "o/r", PRNumber: 1},
		},
	}
	pr := api.PR{
		Repo: "o/r", Number: 1, State: "merged", Merged: true,
		Author: "teammate", URL: "https://github.com/o/r/pull/1",
	}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	in, err := e.refreshPR(context.Background(), "o/r", 1)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Fatalf("a team-authored merged PR must still be removed immediately (nil input); got %+v", in)
	}
	// Structural guarantee: sync.BeadClient = {ListMergeRequests} so
	// refreshPR cannot call CloseMergeRequest on the engine's bd client.
	// Event-based regression guard: a pr.merged event must have been emitted.
	var sawMerged bool
	for _, ev := range collectOutboxEvents(t, db) {
		if ev.Type == store.EventPRMerged {
			sawMerged = true
		}
	}
	if !sawMerged {
		t.Fatal("expected a pr.merged event to be emitted for the merged PR")
	}
}

// TestRefreshPR_TeamDraft_MarksDraftKeepsBeadHidden: a team-authored draft is
// genuinely detected as draft and kept off the dashboard (nil input). The
// draft bead-state now propagates via an emitted pr.updated event (State=draft)
// rather than an inline EnsureMergeRequest on the engine's bd client.
func TestRefreshPR_TeamDraft_MarksDraftKeepsBeadHidden(t *testing.T) {
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 2, State: "open", Draft: true,
		Author: "teammate", URL: "https://github.com/o/r/pull/2",
	}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	in, err := e.refreshPR(context.Background(), "o/r", 2)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Fatalf("hidden team draft must be removed (nil input); got %+v", in)
	}
	// Structural guarantee: sync.BeadClient = {ListMergeRequests} so refreshPR
	// cannot call EnsureMergeRequest or CloseMergeRequest on the engine's bd
	// client. Event-based regression guard: a pr.updated event with State=="draft"
	// must have been emitted (the bead state the bridge will project).
	var sawDraft bool
	for _, ev := range collectOutboxEvents(t, db) {
		if ev.Type != store.EventPRUpdated {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.State == "draft" {
			sawDraft = true
		}
	}
	if !sawDraft {
		t.Fatal("expected a pr.updated event with State=draft for the hidden team draft")
	}
}

func TestRefreshPR_ActiveMine_UpsertsSnapshot(t *testing.T) {
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 3, State: "open",
		Author: "me", URL: "https://github.com/o/r/pull/3",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	in, err := e.refreshPR(context.Background(), "o/r", 3)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil {
		t.Fatal("active self PR must yield a non-nil snapshot input")
	}
	if in.PR.Number != 3 {
		t.Fatalf("input PR.Number: got %d want 3", in.PR.Number)
	}
	// Structural guarantee: sync.BeadClient = {ListMergeRequests} so
	// refreshPR cannot close the bead inline on the engine's bd client.
}

func TestRefreshPR_ActiveMine_EnrichmentReused(t *testing.T) {
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 9, State: "open",
		Author: "me", URL: "https://github.com/o/r/pull/9",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	in, err := e.refreshPR(context.Background(), "o/r", 9)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil {
		t.Fatal("active self PR must yield a non-nil snapshot input")
	}
	// The snapshot input is built from the per-PR enrichment bundle the active
	// path fetched once; the PR identity must round-trip.
	if in.PR.Number != 9 || in.PR.Repo != "o/r" {
		t.Fatalf("input PR mismatch: got %s#%d", in.PR.Repo, in.PR.Number)
	}
}

// TestRefreshPR_ActiveTeam_FlushesOutboxOnce is the FB-3 flush-collapse guard:
// one refresh of an active (non-draft) TEAM PR must flush the outbox EXACTLY
// ONCE. Before FB-3 the active path flushed twice — once right after
// applyFetchedPR and again after emitAttention — churning bd connections.
// Collapsing them to a single drain (emitAttention now enqueues BEFORE the one
// flush) drops the count to 1 without changing which events are delivered or
// their order. A team PR is used because own != Mine engages the attention
// emit, which is the second (now-removed) flush's trigger.
func TestRefreshPR_ActiveTeam_FlushesOutboxOnce(t *testing.T) {
	// Read the process-wide call counter before/after and assert the DELTA,
	// rather than swapping the package-level flushOutbox func for a counting
	// closure: flushOutbox is called from production code on every tick, so a
	// reassignment would race a concurrent call under -race. The atomic
	// counter is race-safe by construction — see its doc comment in sync.go.
	before := flushOutboxCalls.Load()

	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 4, State: "open",
		Author: "teammate", URL: "https://github.com/o/r/pull/4",
	}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	if _, err := e.refreshPR(context.Background(), "o/r", pr.Number); err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if got := flushOutboxCalls.Load() - before; got != 1 {
		t.Fatalf("expected exactly 1 flushOutbox invocation per active team refresh (collapsed), got %d", got)
	}
}

// outboxFakeBeads is the bead client shared between the engine (Deps.Beads,
// used by buildPRInput's dep path via FindByRepoAndNumber) and the beadsbridge
// (which projects the PR bead at outbox flush). It records every
// ReconcileMergeRequest call so the test can prove the bead is created by the
// OUTBOX (the bridge) and NOT inline by applyFetchedPR.
//
// It satisfies both sync.BeadClient (via embedded noopBeads, which provides
// ListMergeRequests) and beadsbridge.BeadClient (all bridge write methods
// are defined directly below — after the sync.BeadClient slim, noopBeads no
// longer provides the bridge-side methods). It also adds depTreeReader
// (FindByRepoAndNumber/DepTreeUp/HumanLabeledBeads) so buildPRInput's dep
// path engages and the bead lookup runs.
type outboxFakeBeads struct {
	noopBeads
	ensureFields []beads.MergeRequestFields // every ReconcileMergeRequest call's fields
	created      bool                       // set once ReconcileMergeRequest has run
}

// FindByRepoAndNumberUncached is the read-once (pg2-pz7y8) fetch the bridge
// issues before ReconcileMergeRequest. It shares FindByRepoAndNumber's
// created-gated result, so the pre-create read observes nil exactly like the
// real uncached lookup would for a not-yet-projected bead.
func (f *outboxFakeBeads) FindByRepoAndNumberUncached(ctx context.Context, repo string, number int) (*beads.MergeRequest, error) {
	return f.FindByRepoAndNumber(ctx, repo, number)
}

func (f *outboxFakeBeads) ReconcileMergeRequest(_ context.Context, _ *beads.MergeRequest, _ string, fields beads.MergeRequestFields, _, _, _ bool) (string, bool, error) {
	f.ensureFields = append(f.ensureFields, fields)
	f.created = true
	return "mr-1", false, nil
}

// FindByRepoAndNumber returns the projected bead only AFTER ReconcileMergeRequest
// has run — modelling that the bead exists once the bridge projected it from
// the outbox event. This proves buildPRInput finds the bead only because the
// outbox was flushed BEFORE buildPRInput ran.
func (f *outboxFakeBeads) FindByRepoAndNumber(_ context.Context, _ string, _ int) (*beads.MergeRequest, error) {
	if !f.created {
		return nil, nil
	}
	return &beads.MergeRequest{ID: "mr-1"}, nil
}

func (f *outboxFakeBeads) CloseMergeRequest(context.Context, string, string) error { return nil }
func (f *outboxFakeBeads) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *outboxFakeBeads) CreateProcessingCycle(context.Context, beads.CreateProcessingCycleInput) (string, error) {
	return "", nil
}

func (f *outboxFakeBeads) ResolveProcessingCycle(context.Context, string, string) (beads.ProcessingCycleState, error) {
	return beads.ProcessingCycleState{}, nil
}

func (f *outboxFakeBeads) AppendProcessingCycleNote(context.Context, string, string, string, []string) error {
	return nil
}
func (f *outboxFakeBeads) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (f *outboxFakeBeads) CloseFeedback(context.Context, string, string) error        { return nil }

func (f *outboxFakeBeads) DepTreeUp(_ context.Context, _ string) ([]beads.DepNode, error) {
	return nil, nil
}

func (f *outboxFakeBeads) HumanLabeledBeads(_ context.Context) (map[string]bool, error) {
	return nil, nil
}

// compile-time check: the same fake serves the bridge interface too.
var _ beadsbridge.BeadClient = (*outboxFakeBeads)(nil)

// recordingHandler records the event types it is dispatched, proving a
// pr.opened/updated event reached the dispatcher via the outbox.
type recordingHandler struct{ types []string }

func (h *recordingHandler) Handle(_ context.Context, e store.Event) error {
	h.types = append(h.types, e.Type)
	return nil
}

// TestRefreshPREmitsOpen drives the daemon's per-PR refresh for an active,
// non-draft, open PR through the REAL store → outbox → dispatcher → beadsbridge
// chain. It proves the event-ownership conversion of the daemon create path:
//
//   - applyFetchedPR does NOT create the bead inline; the bead is created by
//     the OUTBOX (the bridge's EnsureMergeRequest, projecting pr.opened).
//   - a pr.opened event is enqueued and dispatched.
//   - refreshPR still yields a non-nil snapshot input whose dep path found the
//     bead — proving the outbox was flushed BEFORE buildPRInput ran.
func TestRefreshPREmitsOpen(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	bdc := &outboxFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 7, State: "open",
		Branch: "feat/x", Base: "main",
		Author: "me", URL: "https://github.com/o/r/pull/7",
	}
	vcs := newFakeVCS()
	vcs.views[keyOf("o/r", pr.Number)] = pr
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos: []config.RepoConfig{
				{Remote: "o/r", VCS: "github", TeamMembers: []string{"teammate"}},
			},
		},
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bdc,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Wire the REAL dispatcher: the beadsbridge (projects the bead) + a
	// recording handler (proves the event type was dispatched).
	rec := &recordingHandler{}
	dispatcher := event.New()
	dispatcher.Register(beadsbridge.New(bdc).Handle)
	dispatcher.Register(rec.Handle)
	e.SetStoreAndDispatch(db, dispatcher.Dispatch)

	in, err := e.refreshPR(ctx, "o/r", pr.Number)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}

	// The bead must be created exactly once, and only by the bridge (outbox).
	if len(bdc.ensureFields) != 1 {
		t.Fatalf("EnsureMergeRequest call count: got %d want 1 (bead must be created only by the outbox bridge)", len(bdc.ensureFields))
	}
	if got := bdc.ensureFields[0].State; got != "open" {
		t.Fatalf("projected bead State: got %q want \"open\"", got)
	}
	if bdc.ensureFields[0].PRNumber != 7 {
		t.Fatalf("projected bead PRNumber: got %d want 7", bdc.ensureFields[0].PRNumber)
	}

	// A pr.opened event must have been enqueued and dispatched.
	sawOpened := false
	for _, ty := range rec.types {
		if ty == store.EventPROpened {
			sawOpened = true
		}
	}
	if !sawOpened {
		t.Fatalf("expected a %s event to be dispatched; got %v", store.EventPROpened, rec.types)
	}

	// The snapshot input must still be produced and the dep path must have
	// found the bead (only possible because the outbox flushed before
	// buildPRInput ran).
	if in == nil {
		t.Fatal("active self PR must yield a non-nil snapshot input")
	}
	if in.PR.Number != 7 {
		t.Fatalf("input PR.Number: got %d want 7", in.PR.Number)
	}

	// The authoritative store row must have been written.
	row, err := db.GetPR(ctx, "o/r", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if row == nil {
		t.Fatal("expected the authoritative store row to be written by applyFetchedPR")
	}
	if row.Ownership != "mine" {
		t.Fatalf("store row Ownership: got %q want \"mine\"", row.Ownership)
	}
}

// newRefreshEngineWithStore builds a refreshPR engine over repo "o/r" wired to
// a real store (so emitted pr.* events land in the outbox) but WITHOUT a bridge
// dispatcher, so tests can inspect the raw outbox and prove the engine emits an
// event rather than closing/ensuring the bead inline on its own bd client.
func newRefreshEngineWithStore(t *testing.T, self string, bdc BeadClient, pr api.PR, db *store.DB) *Engine {
	t.Helper()
	vcs := newFakeVCS()
	vcs.views[keyOf("o/r", pr.Number)] = pr
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: self,
			Repos: []config.RepoConfig{
				{Remote: "o/r", VCS: "github", TeamMembers: []string{"teammate"}},
			},
		},
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bdc,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Intentionally do NOT wire a dispatcher: with Deps.Dispatch nil, the
	// engine's flushOutbox is a no-op so emitted rows stay pending and can be
	// inspected via the raw outbox below. The engine's bd client is also left
	// untouched, so an inline CloseMergeRequest/EnsureMergeRequest would be
	// detectable (mirrors the Task 8/9 raw-outbox-inspection style).
	return e
}

// drainOutboxTypes runs the outbox and collects every event into the slice,
// returning the decoded PRPayloads keyed by event type for assertion.
func collectOutboxEvents(t *testing.T, db *store.DB) []store.Event {
	t.Helper()
	var events []store.Event
	if err := db.RunOutbox(context.Background(), func(_ context.Context, ev store.Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	return events
}

// TestRefreshPRClosedEmitsClose proves a closed (un-merged) PR causes refreshPR
// to emit a store.EventPRClosed (Merged=false), return (nil,nil), and NOT call
// CloseMergeRequest/EnsureMergeRequest inline on the engine's bd client (the
// bridge is now the sole bead writer).
func TestRefreshPRClosedEmitsClose(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{
		existing: &beads.MergeRequest{
			ID:     "mr-1",
			Fields: beads.MergeRequestFields{Repo: "o/r", PRNumber: 1},
		},
	}
	pr := api.PR{
		Repo: "o/r", Number: 1, State: "closed",
		Author: "me", URL: "https://github.com/o/r/pull/1",
	}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	in, err := e.refreshPR(ctx, "o/r", 1)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Fatalf("closed PR must be removed (nil input); got %+v", in)
	}
	// Structural guarantee: sync.BeadClient = {ListMergeRequests} so
	// refreshPR cannot call CloseMergeRequest on the engine's bd client.
	// Event-based regression guard: a pr.closed event must have been emitted.
	events := collectOutboxEvents(t, db)
	var found bool
	for _, ev := range events {
		if ev.Type != store.EventPRClosed {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.Repo == "o/r" && p.Number == 1 {
			found = true
			if p.Merged {
				t.Errorf("pr.closed payload should have Merged=false; got %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("expected a pr.closed event for o/r#1; events: %+v", events)
	}
}

// TestRefreshPRMergedEmitsMerge proves a merged PR causes refreshPR to emit a
// store.EventPRMerged (Merged=true) instead of closing the bead inline, AND
// (pg2-ew4kf) that because this PR is authored by ME, refreshPR now hands
// back a retained snapshot input (Ownership=Mine) instead of signalling
// dashboard removal — the bead lifecycle (this event) is unchanged; only the
// dashboard/snapshot outcome differs from the pre-pg2-ew4kf behavior.
func TestRefreshPRMergedEmitsMerge(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{
		existing: &beads.MergeRequest{
			ID:     "mr-2",
			Fields: beads.MergeRequestFields{Repo: "o/r", PRNumber: 2},
		},
	}
	pr := api.PR{
		Repo: "o/r", Number: 2, State: "merged", Merged: true,
		Author: "me", URL: "https://github.com/o/r/pull/2",
	}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	in, err := e.refreshPR(ctx, "o/r", 2)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil {
		t.Fatal("a merged PR of MINE must be retained (non-nil snapshot input), not removed")
	}
	if in.Ownership != ownership.Mine {
		t.Errorf("retained input Ownership: got %q want %q", in.Ownership, ownership.Mine)
	}
	if !in.PR.Merged {
		t.Errorf("retained input PR.Merged must be true, got %+v", in.PR)
	}
	// Structural guarantee: sync.BeadClient = {ListMergeRequests} so
	// refreshPR cannot call CloseMergeRequest on the engine's bd client.
	// Event-based regression guard: a pr.merged event must have been emitted.
	events := collectOutboxEvents(t, db)
	var found bool
	for _, ev := range events {
		if ev.Type != store.EventPRMerged {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.Repo == "o/r" && p.Number == 2 {
			found = true
			if !p.Merged {
				t.Errorf("pr.merged payload should have Merged=true; got %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("expected a pr.merged event for o/r#2; events: %+v", events)
	}
}

// TestRefreshPRHiddenDraftEmitsUpdate proves a team-authored draft causes
// refreshPR to emit a store.EventPRUpdated whose payload has State=="draft",
// return (nil,nil), and NOT call EnsureMergeRequest inline.
func TestRefreshPRHiddenDraftEmitsUpdate(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 3, State: "open", Draft: true,
		Author: "teammate", URL: "https://github.com/o/r/pull/3",
	}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	in, err := e.refreshPR(ctx, "o/r", 3)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Fatalf("hidden team draft must be removed (nil input); got %+v", in)
	}
	// Structural guarantee: sync.BeadClient = {ListMergeRequests} so
	// refreshPR cannot call EnsureMergeRequest on the engine's bd client.
	// Event-based regression guard: a pr.updated event with State=="draft" must
	// have been emitted (the bead state the bridge will project).
	events := collectOutboxEvents(t, db)
	var found bool
	for _, ev := range events {
		if ev.Type != store.EventPRUpdated {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.Repo == "o/r" && p.Number == 3 {
			found = true
			if p.State != "draft" {
				t.Errorf("pr.updated payload State: got %q want \"draft\"; payload %+v", p.State, p)
			}
		}
	}
	if !found {
		t.Fatalf("expected a pr.updated event with State=draft for o/r#3; events: %+v", events)
	}
}

// TestRefreshPR_TeamToCoOwned_Surfaces: a draft PR authored by a teammate
// ("you") whose enriched CommitAuthors include SelfLogin ("me") classifies as
// CoOwned (ownership.Classify: authored-by-self wins Mine; else a
// self-authored commit wins CoOwned; else Team) — NOT Team. So the draft-hide
// guard (own == ownership.Team && pr.Draft) must NOT hide it: refreshPR must
// surface it (non-nil snapshot input) rather than treating it as a hidden
// team draft.
//
// (This test used to also assert a pr.attention{Need:false} clearing event on
// this transition; that bead-projection mechanism was removed by pg2-ynhr.5 —
// see internal/beadsbridge's package doc. The dashboard's own attention
// verdict, unaffected, is covered by internal/snapshot's NeedsAttention tests.)
func TestRefreshPR_TeamToCoOwned_Surfaces(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 6, State: "open", Draft: true,
		Author: "you", URL: "https://github.com/o/r/pull/6",
	}
	// enricherVCS (sync_test.go) embeds fakeVCS and adds the SinglePREnricher
	// capability, so enrichOnePR routes through EnrichPR and returns ep as the
	// enrichment bundle — giving us control over CommitAuthors, which the plain
	// REST fallback path (fakeVCS alone) never populates.
	vp := &enricherVCS{
		fakeVCS: fakeVCS{views: map[string]api.PR{keyOf("o/r", pr.Number): pr}},
		ep:      &vcs.EnrichedPR{CommitAuthors: []string{"you", "me"}},
	}
	bdc := &refreshFakeBeads{}
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos: []config.RepoConfig{
				{Remote: "o/r", VCS: "github", TeamMembers: []string{"you"}},
			},
		},
		VCS:      map[string]VCSProvider{"github": vp},
		Beads:    bdc,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Intentionally no dispatcher wired (Deps.Dispatch nil): flushOutbox becomes
	// a no-op and emitted events stay in the raw outbox for direct inspection,
	// mirroring newRefreshEngineWithStore's pattern used by the sibling
	// hidden-draft/closed/merged tests above.

	in, err := e.refreshPR(ctx, "o/r", pr.Number)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil {
		t.Fatal("co-owned draft must be surfaced (non-nil snapshot input), not hidden as a team draft")
	}
	if in.PR.Number != pr.Number {
		t.Fatalf("input PR.Number: got %d want %d", in.PR.Number, pr.Number)
	}
}

// TestRefreshPR_AssignmentRemoved_RowDisappearsBeadUntouched is the sync-layer
// half of the EXIT rule (pg2-4dz88.11.4, decided fact #2 and acceptance
// criterion 4): a PR open+non-draft whose ONLY qualifying reason is
// "assigned to me" (author "zara" is on no team, requests no review, carries
// no watch label) loses that qualifier on a later refresh with no other
// change. Two things must hold:
//   - the SNAPSHOT input flips AssignedToMe true->false across the two
//     refreshes, and feeding each into snapshot.Build shows the dashboard Team
//     row present, then absent;
//   - the merge-request BEAD is untouched by that second refresh: bead
//     closure/cascade fires only on a pr.closed/pr.merged event
//     (internal/beadsbridge/bridge.go's project(), case
//     store.EventPRClosed/EventPRMerged -> cascadeClose), and a PR that merely
//     lost a snapshot match reason while staying open never emits either.
func TestRefreshPR_AssignmentRemoved_RowDisappearsBeadUntouched(t *testing.T) {
	db := store.OpenForTest(t)
	bdc := &refreshFakeBeads{}
	vp := newFakeVCS()
	pr := api.PR{
		Repo: "o/r", Number: 30, State: "open", Draft: false,
		Author: "zara", URL: "https://github.com/o/r/pull/30",
		Assignees: []string{"me"},
	}
	vp.views[keyOf("o/r", pr.Number)] = pr
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos: []config.RepoConfig{
				{Remote: "o/r", VCS: "github", TeamMembers: []string{"teammate"}},
			},
		},
		VCS:      map[string]VCSProvider{"github": vp},
		Beads:    bdc,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No dispatcher wired: flushOutbox is a no-op, so every emitted event stays
	// in the raw outbox for direct inspection across BOTH refreshes below.

	in1, err := e.refreshPR(context.Background(), "o/r", pr.Number)
	if err != nil {
		t.Fatalf("refreshPR (assigned): %v", err)
	}
	if in1 == nil || !in1.PR.AssignedToMe {
		t.Fatalf("expected an active input with AssignedToMe=true while assigned, got %+v", in1)
	}
	snap1 := snapshot.Build(snapshot.BuilderInput{Self: "me", PRs: []snapshot.PRInput{*in1}})
	if len(snap1.Team) != 1 {
		t.Fatalf("expected a Team row while assigned, got %+v", snap1.Team)
	}

	// Remove the assignment; nothing else about the PR changes.
	pr.Assignees = nil
	vp.views[keyOf("o/r", pr.Number)] = pr

	in2, err := e.refreshPR(context.Background(), "o/r", pr.Number)
	if err != nil {
		t.Fatalf("refreshPR (unassigned): %v", err)
	}
	if in2 == nil || in2.PR.AssignedToMe {
		t.Fatalf("expected AssignedToMe=false after the assignment is removed, got %+v", in2)
	}
	snap2 := snapshot.Build(snapshot.BuilderInput{Self: "me", PRs: []snapshot.PRInput{*in2}})
	if len(snap2.Team) != 0 {
		t.Fatalf("expected the Team row to disappear once the assignment is removed, got %+v", snap2.Team)
	}

	// Bead-lifecycle half: across BOTH refreshes, no pr.closed/pr.merged event
	// was ever emitted — the merge-request bead is untouched by a
	// match-reason-only change.
	for _, ev := range collectOutboxEvents(t, db) {
		if ev.Type == store.EventPRClosed || ev.Type == store.EventPRMerged {
			t.Fatalf("assignment removal must NOT emit a close/merge event (the merge-request bead must stay untouched); got %v", ev.Type)
		}
	}
}

func TestEngineCfg_AtomicSwap(t *testing.T) {
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "old"},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: &refreshFakeBeads{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.cfg().SelfLogin != "old" {
		t.Fatalf("cfg() initial = %q", e.cfg().SelfLogin)
	}
	e.ReplaceCfg(&config.Config{SelfLogin: "new"})
	if e.cfg().SelfLogin != "new" {
		t.Fatalf("cfg() after swap = %q", e.cfg().SelfLogin)
	}
}
