package main

import (
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
)

// TestRenderCapacityText exercises the pure renderer directly (mirrors
// state_test.go's TestRenderState_human): no config/store/tmux involved (the
// "Unit tests MUST be isolated" global constraint).
func TestRenderCapacityText(t *testing.T) {
	c := session.Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}
	want := "free=4 counted=2 preserved=1 live=3 max=6\n"
	if got := renderCapacityText(c); got != want {
		t.Fatalf("renderCapacityText(%+v) = %q, want %q", c, got, want)
	}
}

func TestRenderCapacityText_freeZero(t *testing.T) {
	c := session.Capacity{MaxSessions: 2, Live: 3, Preserved: 0, Counted: 3, Free: 0}
	want := "free=0 counted=3 preserved=0 live=3 max=2\n"
	if got := renderCapacityText(c); got != want {
		t.Fatalf("renderCapacityText(%+v) = %q, want %q", c, got, want)
	}
}
