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

	payload, _ := json.Marshal(PRPayload{Repo: "o/r", Number: 7, Title: "https://x/7"})
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
