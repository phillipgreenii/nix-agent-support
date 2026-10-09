package engine_test

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// Probe and StoreSize back the store_writable and store_size_bytes gauges.

func TestProbeReportsWhetherTheDirectoryTakesAWrite(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	if err := h.e.Probe(); err != nil {
		t.Fatalf("Probe on a healthy directory: %v", err)
	}
	h.fs.Inject(storefault.Rule{Op: storefault.OpCreateTemp})
	if err := h.e.Probe(); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("Probe = %v, want the injected failure", err)
	}
	h.requireNoPending()
	if hl := h.e.Health(); hl.ReadOnly {
		t.Errorf("a failed Probe changed Health: %+v", hl)
	}
	if err := h.e.Probe(); err != nil {
		t.Errorf("Probe after the fault was spent: %v", err)
	}
}

func TestStoreSizeIsTheLengthOfTheLog(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.e.StoreSize(); got != 0 {
		t.Errorf("StoreSize of an empty log = %d, want 0", got)
	}
	h.bootstrap()
	h.at(local(9, 5), command.CompleteTask{TaskID: taskOf(7, "plan-day")})
	if got, want := h.e.StoreSize(), int64(len(h.logBytes())); got != want {
		t.Errorf("StoreSize = %d, want the file's %d bytes", got, want)
	}
	h.reopen()
	if got, want := h.e.StoreSize(), int64(len(h.logBytes())); got != want {
		t.Errorf("after reopening StoreSize = %d, want %d", got, want)
	}
}
