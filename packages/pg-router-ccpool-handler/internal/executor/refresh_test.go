package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

// recordingRunner records every bd call so a test can prove no `bd show` ran.
type recordingRunner struct {
	calls [][]string
	out   string
	err   error
}

func (r *recordingRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return r.out, r.err
}

// TestRefreshItem_BeadVsNonBead covers pg2-rk7t9: non-bead items (PR,
// heartbeat) are not refreshed — no `bd show` and no WARN — while a genuine
// bead whose refresh fails still logs the WARN.
func TestRefreshItem_BeadVsNonBead(t *testing.T) {
	const warn = "could not refresh item metadata from bd"
	tests := []struct {
		name     string
		item     item.Item
		bdErr    error
		wantShow bool
		wantWarn bool
	}{
		{"PR item skips refresh", item.Item{ID: "ZR-Private/ziprecruiter#120058", Type: "pr"}, errors.New("bd down"), false, false},
		{"heartbeat item skips refresh", item.Item{ID: "2026-10-03T06:55:00Z", Type: "pg-router-probe-tick"}, errors.New("bd down"), false, false},
		{"empty id skips refresh", item.Item{}, errors.New("bd down"), false, false},
		{"bead refresh failure still warns", item.Item{ID: "pg2-abc12", Type: "task"}, errors.New("bd down"), true, true},
		{"hierarchical bead id refresh failure still warns", item.Item{ID: "pg2-abc12.3", Type: "review-pr"}, errors.New("bd down"), true, true},
		{"bead refresh success does not warn", item.Item{ID: "pg2-abc12", Type: "task"}, nil, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })

			bd := &recordingRunner{out: `{"data":[{"id":"x","status":"open"}]}`, err: tc.bdErr}
			got := RefreshItem(context.Background(), bd, tc.item)

			if got.ID != tc.item.ID {
				t.Fatalf("item must be returned unchanged, got %+v", got)
			}
			showed := false
			for _, c := range bd.calls {
				if len(c) > 0 && c[0] == "show" {
					showed = true
				}
			}
			if showed != tc.wantShow {
				t.Fatalf("bd show called = %v, want %v (calls %v)", showed, tc.wantShow, bd.calls)
			}
			if warned := strings.Contains(buf.String(), warn); warned != tc.wantWarn {
				t.Fatalf("WARN logged = %v, want %v; log: %q", warned, tc.wantWarn, buf.String())
			}
		})
	}
}
