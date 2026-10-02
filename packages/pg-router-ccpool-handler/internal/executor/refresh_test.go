package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// pinnedHeadPromptBody is a review prompt that pins the head sha both as the
// checkout instruction and as the sha the review is posted against — the two
// places the stale head showed up in pg2-1pt7r.
const pinnedHeadPromptBody = `Checkout {{index .Item.Metadata "head_sha"}} and review {{index .Item.Metadata "repo"}}#{{index .Item.Metadata "pr_number"}} pinned at head_sha={{index .Item.Metadata "head_sha"}}.`

func pinnedHeadReviewRole(cfg config.Config) roles.Role {
	return roles.Role{
		Name: "review", Type: "ccpool",
		CCPool: &roles.CCPoolConfig{
			Actor: "pgii-pool__review", Completion: roles.CloseOrHandback,
			OnFailure: roles.AddHuman, OnDispatchFail: roles.DispatchLeave,
			PromptBody: pinnedHeadPromptBody, Prompt: mustParsePrompt("review", pinnedHeadPromptBody),
			Budget: cfg.WorkerBudget(),
		},
	}
}

// TestRefreshItem_HeadAdvanceBetweenFirstReviewAndRedispatch covers the
// pg2-1pt7r scenario: the queued event's payload still carries the head the
// FIRST review ran at (old), the bead was reopened with the advanced head
// (new), and the dispatch prompt MUST pin the bead's current head_sha.
func TestRefreshItem_HeadAdvanceBetweenFirstReviewAndRedispatch(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{
		"zr-r": `{"data":[{"id":"zr-r","status":"open","metadata":{"repo":"o/r","pr_number":120058,"head_sha":"0cbb75e","branch":"b"}}]}`,
	}}
	r := newExec(&dtest.FakeCC{}, bd, cfg)
	role := pinnedHeadReviewRole(cfg)
	stale := item.Item{ID: "zr-r", Type: "review-pr", Metadata: map[string]any{
		"repo": "o/r", "pr_number": float64(120058), "head_sha": "96278d3", "branch": "b", "payload_only": "kept",
	}}

	// Without the refresh the prompt names the stale head (the defect).
	before := r.renderNudge(role.CCPool, DispatchContext{Role: role, Item: stale}, "/wt")
	if !strings.Contains(before, "96278d3") {
		t.Fatalf("precondition: payload item should render the stale head, got %q", before)
	}

	fresh := RefreshItem(context.Background(), bd, stale)
	got := r.renderNudge(role.CCPool, DispatchContext{Role: role, Item: fresh}, "/wt")
	if strings.Contains(got, "96278d3") {
		t.Fatalf("prompt still pins the stale head: %q", got)
	}
	if want := "Checkout 0cbb75e and review o/r#120058 pinned at head_sha=0cbb75e."; !strings.Contains(got, want) {
		t.Fatalf("prompt = %q, want it to contain %q", got, want)
	}
	if fresh.Metadata["payload_only"] != "kept" {
		t.Fatalf("payload-only metadata key must survive the overlay, got %v", fresh.Metadata)
	}
	if stale.Metadata["head_sha"] != "96278d3" {
		t.Fatalf("RefreshItem must not mutate the caller's metadata map, got %v", stale.Metadata)
	}
}

type failingRunner struct{}

func (failingRunner) Run(context.Context, ...string) (string, error) {
	return "", errors.New("bd down")
}

func TestRefreshItem_BestEffortKeepsPayloadOnBDFailure(t *testing.T) {
	in := item.Item{ID: "zr-r", Metadata: map[string]any{"head_sha": "old"}}
	got := RefreshItem(context.Background(), failingRunner{}, in)
	if got.Metadata["head_sha"] != "old" {
		t.Fatalf("bd failure must leave the payload item, got %v", got.Metadata)
	}
}

func TestRefreshItem_BeadWithoutMetadataKeepsPayload(t *testing.T) {
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-r": `{"data":[{"id":"zr-r","status":"open"}]}`}}
	in := item.Item{ID: "zr-r", Metadata: map[string]any{"head_sha": "old"}}
	if got := RefreshItem(context.Background(), bd, in); got.Metadata["head_sha"] != "old" {
		t.Fatalf("got %v", got.Metadata)
	}
}
