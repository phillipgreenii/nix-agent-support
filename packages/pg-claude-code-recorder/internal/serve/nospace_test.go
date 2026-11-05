package serve

import (
	"fmt"
	"syscall"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// A failure that says the volume is full is counted by op with the free bytes
// the state directory has; any other failure is not a space signal.
func TestNoteNoSpaceCountsOnlyFullVolumeFailures(t *testing.T) {
	d := &Daemon{obs: obs.New(nil), cfg: config.Config{StateDir: t.TempDir()}}
	d.noteNoSpace(obs.OpCopy, fmt.Errorf("append: %w", syscall.ENOSPC))
	d.noteNoSpace(obs.OpCopy, fmt.Errorf("permission denied"))
	if v, _ := d.obs.Reg.Counter(obs.MBlockedNoSpace, map[string]string{"op": obs.OpCopy}); v != 1 {
		t.Fatalf("copy count %v, want 1", v)
	}
	evs := d.obs.EventsNamed(obs.EvBlockedNoSpace)
	if len(evs) != 1 || evs[0].Attrs["op"] != obs.OpCopy || evs[0].Attrs["had_bytes"] == "unknown" {
		t.Fatalf("events %v", evs)
	}
}
