package runner

import (
	"context"
	"fmt"
)

// SkippedError ends a run that was gated off: the run row is Skipped with the
// reason and the command exits 3.
type SkippedError struct {
	RunID  int64
	Reason string
}

func (e *SkippedError) Error() string { return "skipped: " + e.Reason }

// gateStage skips the run, gathering nothing, when the pending cap is reached
// or the lens is paused. It first promotes Waiting topics to Pending.
type gateStage struct{}

func (gateStage) Name() string { return "gate" }

func (gateStage) Run(ctx context.Context, s *State) error {
	limit := s.Deps.Cfg.Limits.ItemsMaxPending
	if err := s.Deps.Ledger.WithTx(ctx, func(t *ledgerTx) error {
		_, err := t.PromoteWaiting(ctx, limit)
		return err
	}); err != nil {
		return err
	}
	c, err := s.Deps.Ledger.Counts(ctx)
	if err != nil {
		return err
	}
	if limit > 0 && c.Pending >= limit {
		return &SkippedError{RunID: s.Run.ID, Reason: fmt.Sprintf("pending cap reached: %d items pending (items_max_pending %d)", c.Pending, limit)}
	}
	paused, err := s.Deps.Ledger.IsLensPaused(ctx, s.Run.Lens)
	if err != nil {
		return err
	}
	if paused {
		return &SkippedError{RunID: s.Run.ID, Reason: fmt.Sprintf("lens %s is paused: its last runs were all rejected (resume with job resume %s)", s.Run.Lens, s.Run.Lens)}
	}
	return nil
}
