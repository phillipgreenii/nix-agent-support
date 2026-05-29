package tui

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// TestPollResultMsg_AdoptsCaffeinateFromDaemon: a fresh model adopts
// CaffeinateActive=true from the first successful poll, mirroring how
// AutoResumeEnabled is adopted at update.go:60-62. Guards against the
// regression where the TUI never reads daemon caffeinate state at startup.
func TestPollResultMsg_AdoptsCaffeinateFromDaemon(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	if m.caffeinateOn {
		t.Fatal("precondition: caffeinateOn must start false")
	}

	now := time.Unix(1_700_000_000, 0)
	m.Update(pollResultMsg{
		tree: &aggregate.Tree{},
		meta: DaemonMeta{
			CaffeinateActive: true,
			DaemonNow:        now,
		},
	})

	if !m.caffeinateOn {
		t.Error("first pollResultMsg with CaffeinateActive=true should set caffeinateOn=true")
	}
}

// TestPollResultMsg_StaleSnapshotDoesNotOverwriteCaffeinateUserAction:
// the same DaemonNow race guard auto-resume uses protects caffeinate. A
// snapshot whose daemon timestamp predates the user's last C-press must not
// undo their optimistic flip.
func TestPollResultMsg_StaleSnapshotDoesNotOverwriteCaffeinateUserAction(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})

	handleToggleCaffeinate(m)
	if !m.caffeinateOn {
		t.Fatal("precondition: optimistic flip should set caffeinateOn=true")
	}

	stale := m.caffeinateUserAt.Add(-time.Second)
	m.Update(pollResultMsg{
		tree: &aggregate.Tree{},
		meta: DaemonMeta{
			CaffeinateActive: false,
			DaemonNow:        stale,
		},
	})

	if !m.caffeinateOn {
		t.Error("stale snapshot must not overwrite optimistic flip")
	}
}

// TestPollResultMsg_FreshSnapshotAfterUserToggleStillAdoptsDaemon: when a
// snapshot's DaemonNow is AFTER the user's last toggle, the daemon value
// wins (it reflects post-RPC truth). Matches autoResume behaviour.
func TestPollResultMsg_FreshSnapshotAfterUserToggleStillAdoptsDaemon(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})

	handleToggleCaffeinate(m) // sets caffeinateOn=true, stamps userAt

	fresh := m.caffeinateUserAt.Add(time.Second)
	m.Update(pollResultMsg{
		tree: &aggregate.Tree{},
		meta: DaemonMeta{
			CaffeinateActive: false,
			DaemonNow:        fresh,
		},
	})

	if m.caffeinateOn {
		t.Error("post-user-action snapshot reporting CaffeinateActive=false should be adopted")
	}
}

// TestHandleToggleCaffeinate_StampsUserAt: the C keybinding must stamp
// caffeinateUserAt so the race guard has something to compare against.
func TestHandleToggleCaffeinate_StampsUserAt(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	if !m.caffeinateUserAt.IsZero() {
		t.Fatal("precondition: caffeinateUserAt must start zero")
	}
	before := time.Now()
	handleToggleCaffeinate(m)
	after := time.Now()

	if m.caffeinateUserAt.IsZero() {
		t.Error("caffeinateUserAt should be stamped after handleToggleCaffeinate")
	}
	if m.caffeinateUserAt.Before(before) || m.caffeinateUserAt.After(after) {
		t.Errorf("caffeinateUserAt out of range: %v not in [%v, %v]", m.caffeinateUserAt, before, after)
	}
}
