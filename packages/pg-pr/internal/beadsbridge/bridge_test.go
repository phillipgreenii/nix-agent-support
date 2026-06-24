package beadsbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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

// errFindClient returns an error from FindOpenProcessingCycle; FindByRepoAndNumber
// returns a stub MR; everything else is a no-op. Used to prove the find-error
// propagates (NOT swallowed as "no open cycle" — that's the duplicate-cycle bug).
type errFindClient struct{}

func (errFindClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "mr-1", false, nil
}
func (errFindClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-1"}, nil
}
func (errFindClient) CloseMergeRequest(context.Context, string, string) error    { return nil }
func (errFindClient) ListChildrenOfPR(context.Context, string) ([]string, error) { return nil, nil }
func (errFindClient) CreateProcessingCycle(context.Context, string, string, bool) (string, error) {
	return "", nil
}
func (errFindClient) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "", false, errBoom
}
func (errFindClient) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (errFindClient) CloseFeedback(context.Context, string, string) error        { return nil }

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

// capturingClient is a minimal BeadClient capturing EnsureMergeRequest fields.
type capturingClient struct {
	onEnsure func(beads.MergeRequestFields)
}

func (c *capturingClient) EnsureMergeRequest(_ context.Context, _ string, f beads.MergeRequestFields) (string, bool, error) {
	if c.onEnsure != nil {
		c.onEnsure(f)
	}
	return "mr-1", false, nil
}
func (c *capturingClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return nil, nil
}
func (c *capturingClient) CloseMergeRequest(context.Context, string, string) error { return nil }
func (c *capturingClient) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return nil, nil
}
func (c *capturingClient) CreateProcessingCycle(context.Context, string, string, bool) (string, error) {
	return "", nil
}
func (c *capturingClient) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (c *capturingClient) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (c *capturingClient) CloseFeedback(context.Context, string, string) error        { return nil }

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

// closedParentClient returns a closed merge-request from FindByRepoAndNumber.
type closedParentClient struct{ createInc func() }

func (c *closedParentClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "mr-1", true, nil
}
func (c *closedParentClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-1", Status: "closed"}, nil
}
func (c *closedParentClient) CloseMergeRequest(context.Context, string, string) error { return nil }
func (c *closedParentClient) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return nil, nil
}
func (c *closedParentClient) CreateProcessingCycle(context.Context, string, string, bool) (string, error) {
	c.createInc()
	return "cycle-1", nil
}
func (c *closedParentClient) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (c *closedParentClient) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (c *closedParentClient) CloseFeedback(context.Context, string, string) error        { return nil }

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

// cascadeClient is a minimal BeadClient fake for cascade-close tests.
type cascadeClient struct {
	find         *beads.MergeRequest
	children     []string
	onCloseMR    func(string)
	onCloseChild func()
}

func (c *cascadeClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "mr-1", false, nil
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
func (c *cascadeClient) CreateProcessingCycle(context.Context, string, string, bool) (string, error) {
	return "", nil
}
func (c *cascadeClient) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (c *cascadeClient) CloseProcessingCycle(context.Context, string, string) error {
	if c.onCloseChild != nil {
		c.onCloseChild()
	}
	return nil
}
func (c *cascadeClient) CloseFeedback(context.Context, string, string) error { return nil }
