package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// TestPollResultClearsLastErr guards against a latched offline screen: a single
// transient poll failure sets lastErr, and the offline view renders while
// tree==nil && lastErr!=nil. Once a poll succeeds again, lastErr MUST be cleared
// so a later tree-reset (or any lastErr-gated UI) cannot resurrect the stale
// "Daemon offline" state after the daemon has recovered.
func TestPollResultClearsLastErr(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}, Interval: time.Second})

	m.Update(pollErrMsg{err: errors.New("dial timeout")})
	if m.lastErr == nil {
		t.Fatal("precondition: pollErrMsg should set lastErr")
	}

	m.Update(pollResultMsg{tree: &aggregate.Tree{}})
	if m.lastErr != nil {
		t.Fatalf("a successful poll must clear lastErr, got %v", m.lastErr)
	}
	if !m.daemonConnected {
		t.Fatal("a successful poll must mark the daemon connected")
	}
}
