package render_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/render"
)

func TestCmuxWindowProgress(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fp := func(v float64) *float64 { return &v }
	zero := time.Time{}
	const staleAfter = 10 * time.Minute

	t.Run("latched window is exhausted regardless of pct/cost", func(t *testing.T) {
		frac, label, ok := render.CmuxWindowProgress(now.Add(5*time.Hour), fp(50), now, 30, true, now, staleAfter)
		if !ok || frac != 1.0 || label != "5h block exhausted — waiting for reset" {
			t.Errorf("latched: got (%v, %q, %v), want (1, exhausted label, true)", frac, label, ok)
		}
	})

	t.Run("not latched falls back to authoritative 5h pct", func(t *testing.T) {
		frac, label, ok := render.CmuxWindowProgress(zero, fp(50), now, 30, true, now, staleAfter)
		if !ok || label != "block 50%" {
			t.Errorf("not latched (auth): got (%v, %q, %v), want block 50%%", frac, label, ok)
		}
	})

	t.Run("not latched with no auth reading falls back to cost/cap", func(t *testing.T) {
		frac, label, ok := render.CmuxWindowProgress(zero, nil, zero, 42, true, now, staleAfter)
		if !ok || label != "block 42% of cap" {
			t.Errorf("not latched (cost): got (%v, %q, %v), want block 42%% of cap", frac, label, ok)
		}
	})

	t.Run("delegates staleness marking when not latched", func(t *testing.T) {
		_, label, ok := render.CmuxWindowProgress(zero, fp(50), now.Add(-1*time.Hour), 0, false, now, staleAfter)
		if !ok || label != "block 50% (stale)" {
			t.Errorf("stale auth: got (%q, %v), want block 50%% (stale)", label, ok)
		}
	})
}
