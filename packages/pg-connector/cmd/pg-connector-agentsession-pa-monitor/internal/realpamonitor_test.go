//go:build contract

package internal

import (
	"context"
	"testing"
	"time"
)

// TestRealPaMonitor_StatusAndInfoRoundTrip exercises the real pa-monitor
// binary (must be on PATH, daemon running) — skips (not fails) when
// unreachable, matching every other daemon-dependent test's convention.
// Picked up automatically by `nix run .#pg-connector-contract`'s existing
// `go test -tags contract ./...` (packages/pg-connector-wide) — no new nix
// wiring needed.
func TestRealPaMonitor_StatusAndInfoRoundTrip(t *testing.T) {
	r := NewCLIRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := r.Status(ctx)
	if err != nil {
		t.Skipf("pa-monitor daemon unreachable, skipping: %v", err)
	}

	b := New(r)
	list, err := b.List(ctx, nil, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	t.Logf("pa-monitor status --json returned %d bytes, %d sessions", len(raw), len(list.Entities))

	if len(list.PresentIDs) == 0 {
		t.Skip("no live sessions to Show(); round trip for status only")
		return
	}
	got, err := b.Show(ctx, list.PresentIDs[0])
	if err != nil {
		t.Fatalf("Show(%q): %v", list.PresentIDs[0], err)
	}
	if got.SessionID != list.PresentIDs[0] {
		t.Errorf("Show returned session %q, want %q", got.SessionID, list.PresentIDs[0])
	}
}
