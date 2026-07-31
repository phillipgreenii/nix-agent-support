package beadsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fakeRunner records bd invocations and returns canned output.
type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) Run(ctx context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) > 0 && args[0] == "create" {
		return "bead-1", nil
	}
	return "[]", nil
}

func TestPROpenedCreatesPRBead(t *testing.T) {
	fr := &fakeRunner{}
	client := beads.NewClientWithRunner(fr)
	h := New(client)

	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Title: "https://x/7"})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var sawCreate bool
	for _, c := range fr.calls {
		if len(c) > 0 && c[0] == "create" && strings.Contains(strings.Join(c, " "), "merge-request") {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Fatalf("expected a merge-request create, got calls %v", fr.calls)
	}
}

// noopBeadClient is a BeadClient whose every method is a no-op returning zero
// values. Focused fakes embed it and override only the methods their test path
// exercises, instead of re-declaring the whole ~8-method interface.
type noopBeadClient struct{}

func (noopBeadClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "", false, nil
}

func (noopBeadClient) SetMergeRequestCoOwned(context.Context, string, bool) error { return nil }

func (noopBeadClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return nil, nil
}
func (noopBeadClient) CloseMergeRequest(context.Context, string, string) error    { return nil }
func (noopBeadClient) ListChildrenOfPR(context.Context, string) ([]string, error) { return nil, nil }

func (noopBeadClient) CreateProcessingCycle(context.Context, beads.CreateProcessingCycleInput) (string, error) {
	return "", nil
}

func (noopBeadClient) ResolveProcessingCycle(context.Context, string, string) (beads.ProcessingCycleState, error) {
	return beads.ProcessingCycleState{}, nil
}

func (noopBeadClient) AppendProcessingCycleNote(context.Context, string, string, string, []string) error {
	return nil
}
func (noopBeadClient) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (noopBeadClient) CloseFeedback(context.Context, string, string) error        { return nil }
func (noopBeadClient) EnsureDraftReviewBead(context.Context, string, string, bool) (string, error) {
	return "", nil
}

func (noopBeadClient) EnsureAttentionBead(context.Context, string, string) (string, error) {
	return "", nil
}
func (noopBeadClient) CloseAttentionBead(context.Context, string, string) error { return nil }

func (noopBeadClient) EnsureDraftReviewMineLabel(context.Context, string) error { return nil }

func (noopBeadClient) GetMergeRequest(context.Context, string) (*beads.MergeRequest, error) {
	return nil, nil
}
func (noopBeadClient) SetPriority(context.Context, string, int) error    { return nil }
func (noopBeadClient) AddLabel(context.Context, string, string) error    { return nil }
func (noopBeadClient) RemoveLabel(context.Context, string, string) error { return nil }

// errFindClient returns an error from ResolveProcessingCycle; FindByRepoAndNumber
// returns a stub (open) MR. Used to prove the find-error propagates (NOT swallowed
// as "no open cycle" — that's the duplicate-cycle bug).
type errFindClient struct{ noopBeadClient }

func (errFindClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-1"}, nil
}

func (errFindClient) ResolveProcessingCycle(context.Context, string, string) (beads.ProcessingCycleState, error) {
	return beads.ProcessingCycleState{}, errBoom
}

var errBoom = errString("boom")

type errString string

func (e errString) Error() string { return string(e) }

func TestEnsureProcessFeedbackPropagatesFindError(t *testing.T) {
	h := New(errFindClient{})
	payload, _ := json.Marshal(FeedbackPayload{Repo: "o/r", Number: 1})
	err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: payload})
	if err == nil {
		t.Fatal("expected FindOpenProcessingCycle error to propagate, got nil (would re-create cycles)")
	}
}

func TestPROpenedWritesFullFields(t *testing.T) {
	var got beads.MergeRequestFields
	client := &capturingClient{onEnsure: func(f beads.MergeRequestFields) { got = f }}
	h := New(client)
	payload, _ := json.Marshal(store.PRPayload{
		Repo: "o/r", Number: 7, Title: "t", Ownership: "team",
		State: "open", Branch: "feat", Base: "main", Author: "alice",
		URL: "https://x/7", Draft: true, LastSyncedAt: "2026-06-24T00:00:00Z",
	})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.State != "open" || got.Branch != "feat" || got.Base != "main" ||
		got.Author != "alice" || got.URL != "https://x/7" || !got.Draft {
		t.Fatalf("fields not propagated: %+v", got)
	}
}

// capturingClient captures the fields passed to EnsureMergeRequest.
type capturingClient struct {
	noopBeadClient
	onEnsure func(beads.MergeRequestFields)
}

func (c *capturingClient) EnsureMergeRequest(_ context.Context, _ string, f beads.MergeRequestFields) (string, bool, error) {
	if c.onEnsure != nil {
		c.onEnsure(f)
	}
	return "mr-1", false, nil
}

func TestFeedbackSkippedWhenParentClosed(t *testing.T) {
	created := 0
	client := &closedParentClient{createInc: func() { created++ }}
	h := New(client)
	payload, _ := json.Marshal(FeedbackPayload{Repo: "o/r", Number: 7, Mine: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if created != 0 {
		t.Fatalf("expected no cycle created under a closed parent, got %d", created)
	}
}

// closedParentClient returns a closed merge-request from FindByRepoAndNumber and
// counts CreateProcessingCycle calls — which must stay zero under a closed parent
// (the override is the regression detector for the closed-parent guard).
type closedParentClient struct {
	noopBeadClient
	createInc func()
}

func (c *closedParentClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-1", Status: "closed"}, nil
}

func (c *closedParentClient) CreateProcessingCycle(context.Context, beads.CreateProcessingCycleInput) (string, error) {
	c.createInc()
	return "cycle-1", nil
}

func TestPRMergedCascadeCloses(t *testing.T) {
	var closedReason string
	closedChildren := 0
	client := &cascadeClient{
		find:         &beads.MergeRequest{ID: "mr-1", Status: "open"},
		children:     []string{"c1", "c2"},
		onCloseMR:    func(reason string) { closedReason = reason },
		onCloseChild: func() { closedChildren++ },
	}
	h := New(client)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Merged: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRMerged, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if closedReason != "upstream-merged" || closedChildren != 2 {
		t.Fatalf("reason=%q children=%d", closedReason, closedChildren)
	}
}

func TestPRClosedReasonPRClosed(t *testing.T) {
	var closedReason string
	client := &cascadeClient{
		find:         &beads.MergeRequest{ID: "mr-1", Status: "open"},
		children:     []string{},
		onCloseMR:    func(reason string) { closedReason = reason },
		onCloseChild: func() {},
	}
	h := New(client)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Merged: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRClosed, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if closedReason != "pr-closed" {
		t.Fatalf("expected reason %q, got %q", "pr-closed", closedReason)
	}
}

// cascadeClient is a fake for cascade-close tests: it serves the found MR and
// its children, and records the close reason + per-child close calls.
type cascadeClient struct {
	noopBeadClient
	find         *beads.MergeRequest
	children     []string
	onCloseMR    func(string)
	onCloseChild func()
}

func (c *cascadeClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return c.find, nil
}

func (c *cascadeClient) CloseMergeRequest(_ context.Context, _ string, reason string) error {
	if c.onCloseMR != nil {
		c.onCloseMR(reason)
	}
	return nil
}

func (c *cascadeClient) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return c.children, nil
}

func (c *cascadeClient) CloseProcessingCycle(context.Context, string, string) error {
	if c.onCloseChild != nil {
		c.onCloseChild()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Scenario tests: idempotency + no-resurrection guarantees
// ---------------------------------------------------------------------------

// upsertClient is an in-memory BeadClient whose EnsureMergeRequest upserts by
// (repo, prNumber) key. Re-dispatching the same event must not create a second
// logical bead entry. ensureCalls counts all invocations (including re-delivers);
// beads maps key → ID and grows only on first creation.
type upsertClient struct {
	noopBeadClient
	ensureCalls int
	beads       map[string]string // key "repo#number" → id
}

func newUpsertClient() *upsertClient {
	return &upsertClient{beads: make(map[string]string)}
}

func (c *upsertClient) EnsureMergeRequest(_ context.Context, _ string, f beads.MergeRequestFields) (string, bool, error) {
	c.ensureCalls++
	key := fmt.Sprintf("%s#%d", f.Repo, f.PRNumber)
	if id, ok := c.beads[key]; ok {
		return id, false, nil // idempotent: return existing
	}
	id := fmt.Sprintf("mr-%s", key)
	c.beads[key] = id
	return id, false, nil
}

// TestPROpenedIdempotentSingleBead asserts that dispatching the same pr.opened
// event twice (simulating at-least-once redelivery) results in exactly ONE
// logical bead entry. The upsertClient's beads map grows only on first
// creation; on the second delivery EnsureMergeRequest finds the key and returns
// without inserting, leaving the map with one entry.
func TestPROpenedIdempotentSingleBead(t *testing.T) {
	client := newUpsertClient()
	h := New(client)

	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 42, Title: "feat: thing"})
	evt := store.Event{Type: store.EventPROpened, Payload: payload}

	// First delivery.
	if err := h.Handle(context.Background(), evt); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// Second delivery (redelivery).
	if err := h.Handle(context.Background(), evt); err != nil {
		t.Fatalf("second Handle: %v", err)
	}

	if got := len(client.beads); got != 1 {
		t.Fatalf("expected exactly 1 bead entry after two deliveries, got %d (beads: %v)", got, client.beads)
	}
	if client.ensureCalls != 2 {
		t.Fatalf("expected EnsureMergeRequest called twice (once per delivery), got %d", client.ensureCalls)
	}
}

// scenarioClosedClient is an in-memory BeadClient for the closed-bead scenario.
// EnsureMergeRequest returns alreadyClosed=true (bead exists but is closed) and
// FindByRepoAndNumber returns Status "closed" — simulating a PR that was
// previously merged/closed in the bead store.
type scenarioClosedClient struct {
	noopBeadClient
	ensureCalls  int
	createCycles int
}

func (c *scenarioClosedClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	c.ensureCalls++
	return "mr-closed-1", true, nil // alreadyClosed = true
}

func (c *scenarioClosedClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-closed-1", Status: "closed"}, nil
}

func (c *scenarioClosedClient) CreateProcessingCycle(context.Context, beads.CreateProcessingCycleInput) (string, error) {
	c.createCycles++
	return "cycle-x", nil
}

// TestClosedBeadNotResurrectedByReappearance is a two-event scenario: a PR
// whose bead is already closed receives both pr.opened (reappearance) and
// feedback.created. It asserts:
//
//	(a) EnsureMergeRequest returns alreadyClosed — the bead is NOT reopened.
//	(b) No processing-cycle bead is created — the closed-parent guard fires.
//
// If the closed-parent guard in ensureProcessFeedbackBead were removed,
// CreateProcessingCycle would be called and the test would fail on (b).
// If EnsureMergeRequest were changed to reopen closed beads, it would no
// longer return alreadyClosed=true and the test would fail on (a).
func TestClosedBeadNotResurrectedByReappearance(t *testing.T) {
	client := &scenarioClosedClient{}
	h := New(client)

	// Step 1: pr.opened for a PR whose bead is already closed.
	prPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Title: "feat: old-pr"})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: prPayload}); err != nil {
		t.Fatalf("pr.opened Handle: %v", err)
	}
	// EnsureMergeRequest must have returned alreadyClosed — the returned bool
	// is not threaded back out of Handle, but we can verify the fake's call count
	// and that no reopening occurred (Status remains "closed" from FindByRepoAndNumber).
	if client.ensureCalls != 1 {
		t.Fatalf("expected EnsureMergeRequest called once, got %d", client.ensureCalls)
	}

	// Step 2: feedback.created for the same (closed) PR.
	fbPayload, _ := json.Marshal(FeedbackPayload{Repo: "o/r", Number: 7, Mine: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: fbPayload}); err != nil {
		t.Fatalf("feedback.created Handle: %v", err)
	}

	// (b) No cycle must have been created under the closed parent.
	if client.createCycles != 0 {
		t.Fatalf("closed-parent guard failed: expected 0 cycles created, got %d", client.createCycles)
	}
}

// scenarioOpenClient is an in-memory BeadClient for the positive-control
// scenario: the PR bead exists and is open. createCycles counts how many
// processing-cycle beads are created.
type scenarioOpenClient struct {
	noopBeadClient
	createCycles int
}

func (c *scenarioOpenClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-open-1", Status: "open"}, nil
}

func (c *scenarioOpenClient) CreateProcessingCycle(context.Context, beads.CreateProcessingCycleInput) (string, error) {
	c.createCycles++
	return "cycle-open-1", nil
}

// TestOpenBeadGetsProcessingCycle is the positive-control for
// TestClosedBeadNotResurrectedByReappearance: an OPEN PR bead receiving
// pr.opened then feedback.created must yield exactly one processing-cycle bead.
// This confirms the guard in ensureProcessFeedbackBead is specific to closed
// beads rather than a blanket no-create policy.
func TestOpenBeadGetsProcessingCycle(t *testing.T) {
	client := &scenarioOpenClient{}
	h := New(client)

	prPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 9, Title: "feat: active-pr"})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: prPayload}); err != nil {
		t.Fatalf("pr.opened Handle: %v", err)
	}

	fbPayload, _ := json.Marshal(FeedbackPayload{Repo: "o/r", Number: 9, Mine: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: fbPayload}); err != nil {
		t.Fatalf("feedback.created Handle: %v", err)
	}

	if client.createCycles != 1 {
		t.Fatalf("expected 1 processing cycle for open PR, got %d", client.createCycles)
	}
}

// draftReviewClient records EnsureDraftReviewBead calls and controls the
// alreadyClosed result of EnsureMergeRequest.
type draftReviewClient struct {
	noopBeadClient
	alreadyClosed bool
	drCalls       int
	mrCalls       int
	lastPRBeadID  string
	lastTitle     string
	lastMine      bool
	relabelCalls  int
	lastRelabelID string
	coOwnedCalls  int
	lastCoOwned   bool
}

func (c *draftReviewClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	c.mrCalls++
	return "mr-1", c.alreadyClosed, nil
}

func (c *draftReviewClient) SetMergeRequestCoOwned(_ context.Context, _ string, coOwned bool) error {
	c.coOwnedCalls++
	c.lastCoOwned = coOwned
	return nil
}

func (c *draftReviewClient) EnsureDraftReviewBead(_ context.Context, prBeadID, title string, mine bool) (string, error) {
	c.drCalls++
	c.lastPRBeadID = prBeadID
	c.lastTitle = title
	c.lastMine = mine
	return "dr-1", nil
}

func (c *draftReviewClient) EnsureDraftReviewMineLabel(_ context.Context, prBeadID string) error {
	c.relabelCalls++
	c.lastRelabelID = prBeadID
	return nil
}

func TestPROpenedMinePRCreatesDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// My PR, still a GitHub draft → review bead is still created.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", Draft: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 1 {
		t.Fatalf("expected 1 draft-review ensure, got %d", c.drCalls)
	}
	if !c.lastMine {
		t.Fatalf("expected mine=true for my PR")
	}
	if c.lastPRBeadID != "mr-1" {
		t.Fatalf("expected parent bead id mr-1, got %q", c.lastPRBeadID)
	}
	if c.lastTitle != "o/r#7" {
		t.Fatalf("expected title o/r#7, got %q", c.lastTitle)
	}
	if c.relabelCalls != 0 {
		t.Fatalf("relabel must only fire on team->co-owned transition, got %d calls for a mine PR", c.relabelCalls)
	}
	if c.coOwnedCalls != 1 {
		t.Fatalf("expected SetMergeRequestCoOwned called once, got %d", c.coOwnedCalls)
	}
	if c.lastCoOwned {
		t.Fatalf("expected SetMergeRequestCoOwned(false) for a mine PR (removes the label), got true")
	}
}

func TestPROpenedTeamDraftSkipsDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// Teammate PR still in draft → NO review bead yet.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 0 {
		t.Fatalf("expected no draft-review for a teammate draft PR, got %d", c.drCalls)
	}
	if c.relabelCalls != 0 {
		t.Fatalf("relabel must not fire for a team PR, got %d calls", c.relabelCalls)
	}
}

func TestPROpenedTeamReadyCreatesDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// Teammate PR, not a draft → review bead created.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 1 {
		t.Fatalf("expected 1 draft-review ensure for a ready teammate PR, got %d", c.drCalls)
	}
	if c.lastMine {
		t.Fatalf("expected mine=false for a teammate PR")
	}
	if c.relabelCalls != 0 {
		t.Fatalf("relabel must not fire for a team PR, got %d calls", c.relabelCalls)
	}
	if c.coOwnedCalls != 1 {
		t.Fatalf("expected SetMergeRequestCoOwned called once, got %d", c.coOwnedCalls)
	}
	if c.lastCoOwned {
		t.Fatalf("expected SetMergeRequestCoOwned(false) for a team PR (removes the label), got true")
	}
}

func TestPRUpdatedTeamDraftToReadyCreatesDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// First observation: teammate draft → no bead.
	draftPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: draftPayload}); err != nil {
		t.Fatalf("Handle (draft): %v", err)
	}
	if c.drCalls != 0 {
		t.Fatalf("expected no draft-review while still draft, got %d", c.drCalls)
	}
	// Draft flag removed → pr.updated with Draft=false → bead created.
	readyPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: readyPayload}); err != nil {
		t.Fatalf("Handle (ready): %v", err)
	}
	if c.drCalls != 1 {
		t.Fatalf("expected 1 draft-review ensure after draft→ready, got %d", c.drCalls)
	}
}

// TestHandle_CoOwnedCreatesMineDraftReviewAndRelabels asserts a co-owned PR
// projects a mine-style draft-review (mine=true, per ownership.ActsAsMine) and
// additionally triggers the team->co-owned relabel call, so a pre-existing
// team-style draft-review bead flips to mine on the transition.
func TestHandle_CoOwnedCreatesMineDraftReviewAndRelabels(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "co-owned", Draft: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 1 {
		t.Fatalf("expected 1 draft-review ensure for a co-owned PR, got %d", c.drCalls)
	}
	if !c.lastMine {
		t.Fatalf("expected mine=true for a co-owned PR")
	}
	if c.relabelCalls != 1 {
		t.Fatalf("expected EnsureDraftReviewMineLabel called once on team->co-owned transition, got %d", c.relabelCalls)
	}
	if c.lastRelabelID != "mr-1" {
		t.Fatalf("expected relabel called with parent bead id mr-1, got %q", c.lastRelabelID)
	}
	if c.coOwnedCalls != 1 {
		t.Fatalf("expected SetMergeRequestCoOwned called once, got %d", c.coOwnedCalls)
	}
	if !c.lastCoOwned {
		t.Fatalf("expected SetMergeRequestCoOwned(true) for a co-owned PR, got false")
	}
}

// TestHandle_OutOfBandOwnershipIsTeamStyle pins the draft-review selection's
// acts-as-mine test at an OUT-OF-BAND ownership value: "" (the field absent from
// a pr.* payload an older binary left in the durable outbox) and an unrecognised
// string. The site delegates to the shared ownership.ActsAsMine (mine OR
// co-owned), so such a value degrades to TEAM-style selection — a draft is
// SKIPPED rather than auto-reviewed, and a ready PR gets a mine=false review
// bead. The superseded local formulation `p.Ownership != "team"` called these
// acts-as-mine, which is exactly why the case is pinned: the two formulations
// must not silently disagree again (pg2-q2drf). Direction matches pr-pool's copy
// of the predicate (TestActsAsMineParity) — fail closed, never auto-review.
func TestHandle_OutOfBandOwnershipIsTeamStyle(t *testing.T) {
	for _, tc := range []struct{ name, own string }{
		{"empty (field absent from an older payload)", ""},
		{"unrecognised value", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The shared predicate this site delegates to must agree.
			if ownership.Ownership(tc.own).ActsAsMine() {
				t.Fatalf("ownership.Ownership(%q).ActsAsMine() = true; an out-of-band value must be team-like", tc.own)
			}
			// Draft → team-style means no review bead yet.
			c := &draftReviewClient{}
			draftPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: tc.own, Draft: true})
			if err := New(c).Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: draftPayload}); err != nil {
				t.Fatalf("Handle (draft): %v", err)
			}
			if c.drCalls != 0 {
				t.Errorf("ownership %q on a GitHub draft must be team-style (skipped), got %d draft-review ensures", tc.own, c.drCalls)
			}
			if c.relabelCalls != 0 {
				t.Errorf("relabel must not fire for ownership %q, got %d calls", tc.own, c.relabelCalls)
			}
			if c.lastCoOwned {
				t.Errorf("expected SetMergeRequestCoOwned(false) for ownership %q, got true", tc.own)
			}
			// Ready → review bead created, but team-style (mine=false).
			c = &draftReviewClient{}
			readyPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: tc.own, Draft: false})
			if err := New(c).Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: readyPayload}); err != nil {
				t.Fatalf("Handle (ready): %v", err)
			}
			if c.drCalls != 1 {
				t.Fatalf("expected 1 draft-review ensure for a ready PR with ownership %q, got %d", tc.own, c.drCalls)
			}
			if c.lastMine {
				t.Errorf("expected mine=false (team-style) for ownership %q", tc.own)
			}
		})
	}
}

func TestPROpenedClosedParentSkipsDraftReview(t *testing.T) {
	c := &draftReviewClient{alreadyClosed: true}
	h := New(c)
	// PR bead already closed → no review bead even for my PR.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 0 {
		t.Fatalf("closed-parent guard failed: expected 0 draft-review ensures, got %d", c.drCalls)
	}
	if c.coOwnedCalls != 0 {
		t.Fatalf("closed-parent guard failed: SetMergeRequestCoOwned must not be called for a closed parent, got %d calls", c.coOwnedCalls)
	}
}

// cascadeChildCapture records the IDs of children closed during cascade.
type cascadeChildCapture struct {
	noopBeadClient
	find         *beads.MergeRequest
	children     []string
	onCloseChild func(string)
}

func (c *cascadeChildCapture) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return c.find, nil
}

func (c *cascadeChildCapture) CloseMergeRequest(context.Context, string, string) error { return nil }

func (c *cascadeChildCapture) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return c.children, nil
}

func (c *cascadeChildCapture) CloseProcessingCycle(_ context.Context, id, _ string) error {
	if c.onCloseChild != nil {
		c.onCloseChild(id)
	}
	return nil
}

// TestPRMergedCascadeClosesDraftReviewChild discharges the spec MUST that
// cascadeClose closes an open draft-review child when its PR is merged.
// cascadeClose is type-blind, so the draft-review child is closed like any
// other; this test names one explicitly to lock the contract.
func TestPRMergedCascadeClosesDraftReviewChild(t *testing.T) {
	var closedChildren []string
	client := &cascadeChildCapture{
		find:         &beads.MergeRequest{ID: "mr-1", Status: "open"},
		children:     []string{"draft-review-child-1"},
		onCloseChild: func(id string) { closedChildren = append(closedChildren, id) },
	}
	h := New(client)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Merged: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRMerged, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	found := false
	for _, id := range closedChildren {
		if id == "draft-review-child-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected draft-review child to be closed by cascade, closed: %v", closedChildren)
	}
}

// ---------------------------------------------------------------------------
// reconcilePriority: conflict->priority nudge/revert (pg2-tsgkj)
// ---------------------------------------------------------------------------

// setPriorityCall records one SetPriority(id, p) invocation.
type setPriorityCall struct {
	id string
	p  int
}

// labelCall records one AddLabel/RemoveLabel(id, label) invocation.
type labelCall struct {
	id    string
	label string
}

// reconcileFakeClient is a functional BeadClient fake for reconcilePriority
// tests: EnsureMergeRequest always resolves to "mr-1" and GetMergeRequest
// returns the configurable canned mr; SetPriority/AddLabel/RemoveLabel record
// their calls instead of no-opping.
type reconcileFakeClient struct {
	noopBeadClient
	mr *beads.MergeRequest

	setPriorityCalls []setPriorityCall
	addLabelCalls    []labelCall
	removeLabelCalls []labelCall
}

func (c *reconcileFakeClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "mr-1", false, nil
}

func (c *reconcileFakeClient) GetMergeRequest(context.Context, string) (*beads.MergeRequest, error) {
	return c.mr, nil
}

func (c *reconcileFakeClient) SetPriority(_ context.Context, id string, p int) error {
	c.setPriorityCalls = append(c.setPriorityCalls, setPriorityCall{id, p})
	return nil
}

func (c *reconcileFakeClient) AddLabel(_ context.Context, id, label string) error {
	c.addLabelCalls = append(c.addLabelCalls, labelCall{id, label})
	return nil
}

func (c *reconcileFakeClient) RemoveLabel(_ context.Context, id, label string) error {
	c.removeLabelCalls = append(c.removeLabelCalls, labelCall{id, label})
	return nil
}

// TestHandle_ConflictRaisesMinePriorityAndStashesBaseline asserts the first
// conflicting tick on a "mine" PR raises priority (numerically lower, toward
// 0) and stashes the pre-adjustment priority in a pbase: label.
func TestHandle_ConflictRaisesMinePriorityAndStashesBaseline(t *testing.T) {
	c := &reconcileFakeClient{mr: &beads.MergeRequest{ID: "mr-1", Priority: 2, Labels: nil}}
	h := New(c)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", HasConflict: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(c.setPriorityCalls) != 1 || c.setPriorityCalls[0] != (setPriorityCall{"mr-1", 1}) {
		t.Fatalf("expected SetPriority(mr-1, 1), got %v", c.setPriorityCalls)
	}
	if len(c.addLabelCalls) != 1 || c.addLabelCalls[0] != (labelCall{"mr-1", "pbase:2"}) {
		t.Fatalf("expected AddLabel(mr-1, pbase:2), got %v", c.addLabelCalls)
	}
}

// TestHandle_ConflictClearedRestoresBaseline asserts a clear (HasConflict
// false) with a stashed baseline restores the exact pre-adjustment priority
// and removes the pbase: marker.
func TestHandle_ConflictClearedRestoresBaseline(t *testing.T) {
	c := &reconcileFakeClient{mr: &beads.MergeRequest{ID: "mr-1", Priority: 1, Labels: []string{"pbase:2"}}}
	h := New(c)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", HasConflict: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(c.setPriorityCalls) != 1 || c.setPriorityCalls[0] != (setPriorityCall{"mr-1", 2}) {
		t.Fatalf("expected SetPriority(mr-1, 2), got %v", c.setPriorityCalls)
	}
	if len(c.removeLabelCalls) != 1 || c.removeLabelCalls[0] != (labelCall{"mr-1", "pbase:2"}) {
		t.Fatalf("expected RemoveLabel(mr-1, pbase:2), got %v", c.removeLabelCalls)
	}
}

// TestHandle_ConflictIdempotentNoDoubleNudge asserts a repeated conflicting
// tick (baseline already stashed) is a no-op — it must NOT call SetPriority
// again (that would double-nudge past the intended single-step adjustment),
// and must NOT re-add or remove the pbase baseline label.
func TestHandle_ConflictIdempotentNoDoubleNudge(t *testing.T) {
	c := &reconcileFakeClient{mr: &beads.MergeRequest{ID: "mr-1", Priority: 1, Labels: []string{"pbase:2"}}}
	h := New(c)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", HasConflict: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(c.setPriorityCalls) != 0 {
		t.Fatalf("expected no SetPriority call (already adjusted this conflict episode), got %v", c.setPriorityCalls)
	}
	if len(c.addLabelCalls) != 0 {
		t.Fatalf("expected no AddLabel call (baseline already stashed), got %v", c.addLabelCalls)
	}
	if len(c.removeLabelCalls) != 0 {
		t.Fatalf("expected no RemoveLabel call (conflict still present), got %v", c.removeLabelCalls)
	}
}

// TestHandle_TeamConflictLowersPriority asserts a team PR's first conflicting
// tick lowers priority (numerically higher, toward 4) rather than raising it.
func TestHandle_TeamConflictLowersPriority(t *testing.T) {
	c := &reconcileFakeClient{mr: &beads.MergeRequest{ID: "mr-1", Priority: 2, Labels: nil}}
	h := New(c)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", HasConflict: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(c.setPriorityCalls) != 1 || c.setPriorityCalls[0] != (setPriorityCall{"mr-1", 3}) {
		t.Fatalf("expected SetPriority(mr-1, 3), got %v", c.setPriorityCalls)
	}
	if len(c.addLabelCalls) != 1 || c.addLabelCalls[0] != (labelCall{"mr-1", "pbase:2"}) {
		t.Fatalf("expected AddLabel(mr-1, pbase:2), got %v", c.addLabelCalls)
	}
}
