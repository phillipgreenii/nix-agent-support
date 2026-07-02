package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// fakeCostPricer is a port fake proving the poller depends only on the
// CostPricer interface — never on the concrete ccusage provider (ADR 0021 §3
// "port fakes prove consumers depend only on interfaces"). It returns a
// preset block and probe state with no ccusage subprocess or byte parsing.
type fakeCostPricer struct {
	block  *usage.Block
	err    error
	probed bool
	stErr  error
}

func (f *fakeCostPricer) ActiveBlock(context.Context) (*usage.Block, error) {
	return f.block, f.err
}
func (f *fakeCostPricer) Probed() (bool, error) { return f.probed, f.stErr }

// TestSnapshotConsumesCostPricerBlock proves the poller folds the block handed
// to it by the CostPricer port into the tree (via aggregate.Build cost share)
// and threads the port's probe state onto the tree.
func TestSnapshotConsumesCostPricerBlock(t *testing.T) {
	block := &usage.Block{
		ID:        "2026-06-01T10Z",
		StartTime: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		IsActive:  true,
		CostUSD:   12.0,
	}
	stErr := errors.New("probe failed")
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
		Pricer:      &fakeCostPricer{block: block, probed: true, stErr: stErr},
	}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tree.ActiveBlock == nil || tree.ActiveBlock.CostUSD != 12.0 {
		t.Errorf("tree.ActiveBlock = %+v, want the pricer's block (cost 12)", tree.ActiveBlock)
	}
	if !tree.CCUsageProbed {
		t.Error("tree.CCUsageProbed = false, want true (from pricer state)")
	}
	if tree.CCUsageErr == nil {
		t.Error("tree.CCUsageErr = nil, want the pricer's probe error")
	}
}

// TestSnapshotNilPricerIsSafe proves a nil CostPricer leaves the block absent
// and probe state zero, matching the pre-port "no CCUsageFn" behavior.
func TestSnapshotNilPricerIsSafe(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
		// Pricer left nil.
	}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tree.ActiveBlock != nil {
		t.Errorf("tree.ActiveBlock = %+v, want nil with nil pricer", tree.ActiveBlock)
	}
	if tree.CCUsageProbed {
		t.Error("tree.CCUsageProbed = true, want false with nil pricer")
	}
}
