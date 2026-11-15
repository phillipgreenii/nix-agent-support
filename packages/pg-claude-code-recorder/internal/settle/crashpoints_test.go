//go:build crashsweep

package settle

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The crash-point tests below arm a panic at each named point of one operation,
// restart, and check the operation converges. TestCrashSweep covers the same
// operations at every file-system call, so these run with the crashsweep tier
// (its own check), not in the unit gate.

func TestExtractCrashPointsConverge(t *testing.T) {
	for _, point := range []string{"extract.after-register", "extract.after-mtime", "extract.after-path", "extract.after-partial"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t, false)
			content, _ := e.removedConversation()
			disarm := faults.ArmPanic(point)
			crashed := faults.Recover(func() { _, _ = e.sett.Extract(sess, "") })
			disarm()
			if !crashed {
				t.Fatalf("%s not reached", point)
			}
			e.crash()
			if _, err := e.sett.Extract(sess, ""); err != nil {
				t.Fatalf("running the extract again: %v", err)
			}
			if got := e.readOrig(e.transcript()); got != content {
				t.Fatal("bytes differ after the retried extract")
			}
			if e.stage() != StageQuiet {
				t.Fatalf("stage %s", e.stage())
			}
			matches, _ := filepath.Glob(e.path("projects/example-project/*.tmp.pgrec"))
			if len(matches) != 0 {
				t.Fatalf("a temporary file is left: %v", matches)
			}
		})
	}
}

func TestCrashAtEveryRemovalPointConverges(t *testing.T) {
	for _, point := range []string{"remove.after-pending", "remove.after-rename", "remove.after-quarantined", "remove.after-unlink", "remove.before-removed", "remove.after-removed"} {
		t.Run(point, func(t *testing.T) {
			e := newRemoveEnv(t, false)
			content := e.oldSettled(6, removeAge+time.Hour)
			file := e.fileID()
			disarm := faults.ArmPanic(point)
			crashed := faults.Recover(func() { _, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}}) })
			disarm()
			if !crashed {
				t.Fatalf("%s not reached", point)
			}
			e.crash()
			if got := e.stage(); got != store.StageRemoved {
				t.Fatalf("stage %s after recovery", got)
			}
			if e.exists(e.transcript()) {
				t.Fatal("the file is back or was never removed")
			}
			if got := e.raw(file, len(content)); got != content {
				t.Fatal("bytes lost")
			}
		})
	}
}
