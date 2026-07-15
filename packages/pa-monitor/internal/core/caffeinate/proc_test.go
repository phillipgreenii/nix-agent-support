package caffeinate

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestProcSpawnKillRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("caffeinate is macOS only")
	}
	// caffeinate is a macOS system binary (/usr/bin/caffeinate) that is not on
	// PATH inside the nix build sandbox. Skip when absent — matching the repo's
	// tool-absent skip idiom (pb's bd/pn tests) — so the pa-monitor-go-tests
	// gate stays green while a dev machine still exercises the real spawn/kill.
	if _, err := exec.LookPath("caffeinate"); err != nil {
		t.Skip("caffeinate not on PATH (e.g. nix build sandbox)")
	}
	p := &Proc{}
	if err := p.Spawn(123); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("double Kill: %v", err)
	}
}
