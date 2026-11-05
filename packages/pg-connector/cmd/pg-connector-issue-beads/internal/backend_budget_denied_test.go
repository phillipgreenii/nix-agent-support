package internal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal/budget"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestBackend_HeldFullBudget_DeniesReadWithTheWireTextTheRouterAdapterMatches
// fills every read slot of a real budget directory (real slot files, no mock),
// then reads through the real CLIRunner: the call is refused as the retryable
// unavailable error, before any bd process starts, and its text contains the
// marker pg-router-source-pg-connector recognises as a skipped poll. A write
// still gets a reserved slot, so the denial is read-only.
func TestBackend_HeldFullBudget_DeniesReadWithTheWireTextTheRouterAdapterMatches(t *testing.T) {
	started := fakeBD(t)
	stateDir := t.TempDir()
	cfg := budget.Config{MaxConcurrent: 3, WriteReserve: 1, StateDir: stateDir, WaitBound: 100 * time.Millisecond}
	holder, err := budget.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the 2 read slots
		s, err := holder.Acquire(context.Background(), false)
		if err != nil {
			t.Fatalf("hold read slot %d: %v", i, err)
		}
		t.Cleanup(s.Release)
	}

	reader, err := budget.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := New(&CLIRunner{Dir: t.TempDir(), Budget: reader})
	_, err = b.Show(context.Background(), "x-1")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if !strings.Contains(err.Error(), "bd budget denied (retryable)") {
		t.Fatalf("err = %q, want the marker the router adapter matches", err)
	}
	if !strings.Contains(err.Error(), BudgetDeniedMessage) {
		t.Fatalf("err = %q, want BudgetDeniedMessage", err)
	}
	if n := countStarted(t, started, ""); n != 0 {
		t.Fatalf("a denied call started %d bd processes, want 0", n)
	}
	w, err := reader.Acquire(context.Background(), true)
	if err != nil {
		t.Fatalf("write with the reserve slot free: %v", err)
	}
	w.Release()
}
