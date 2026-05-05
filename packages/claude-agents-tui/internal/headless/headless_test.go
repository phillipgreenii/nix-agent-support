package headless

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)

type stubPoller struct {
	calls   int
	working []bool
}

func (s *stubPoller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	w := s.working[0]
	s.working = s.working[1:]
	s.calls++
	return &aggregate.Tree{}, w, nil
}

func TestExitsAfterConsecutiveIdleChecks(t *testing.T) {
	p := &stubPoller{working: []bool{true, false, false, false}}
	out := &bytes.Buffer{}
	code := Run(context.Background(), Opts{
		Poller:                p,
		Interval:              1 * time.Millisecond,
		ConsecutiveIdleChecks: 3,
		Maximum:               1 * time.Second,
		Writer:                out,
	})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if p.calls < 4 {
		t.Errorf("calls = %d, want ≥4", p.calls)
	}
}

func TestTimeoutReturns1(t *testing.T) {
	p := &stubPoller{working: []bool{true, true, true, true, true, true}}
	out := &bytes.Buffer{}
	code := Run(context.Background(), Opts{
		Poller:                p,
		Interval:              1 * time.Millisecond,
		ConsecutiveIdleChecks: 3,
		Maximum:               5 * time.Millisecond,
		Writer:                out,
	})
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
