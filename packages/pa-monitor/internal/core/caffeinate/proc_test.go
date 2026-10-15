package caffeinate

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestProcSpawnUsesAbsolutePath proves Spawn execs /usr/bin/caffeinate by
// absolute path: a fake caffeinate earlier on PATH must NOT be run.
func TestProcSpawnUsesAbsolutePath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("caffeinate is macOS only")
	}
	if _, err := os.Stat("/usr/bin/caffeinate"); err != nil {
		t.Skip("/usr/bin/caffeinate missing")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "fake-ran")
	script := "#!/bin/sh\necho ran > '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "caffeinate"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := &Proc{}
	if err := p.Spawn(os.Getpid()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if p.cmd.Path != "/usr/bin/caffeinate" {
		t.Errorf("spawned %q, want /usr/bin/caffeinate", p.cmd.Path)
	}
	if !p.IsAlive() {
		t.Error("real caffeinate should be alive (-w own pid)")
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("fake caffeinate on PATH was executed; spawn must use the absolute path")
	}
}
