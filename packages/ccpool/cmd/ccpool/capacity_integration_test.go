//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
)

// TestCapacity_emptyPoolFreeEqualsMax starts the real ccpool binary against a
// temp, empty pool and asserts `ccpool capacity --json` parses and reports
// free == max_sessions (nothing live, nothing counted).
func TestCapacity_emptyPoolFreeEqualsMax(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	_ = os.MkdirAll(bin, 0o755)
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	socket := "ccpool-capacitytest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
[pool]
max_sessions = 6
[tmux]
socket = "`+socket+`"
`), 0o600)
	env := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"HOME="+base, "PATH="+bin+":"+os.Getenv("PATH"),
	)
	run := func(args ...string) (string, int) {
		cmd := exec.Command(ccpool, args...)
		cmd.Env = env
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v\n%s", args, err, out)
		}
		return string(out), code
	}

	out, code := run("capacity", "--json")
	if code != 0 {
		t.Fatalf("capacity --json exited %d:\n%s", code, out)
	}
	var c session.Capacity
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("parse capacity --json output %q: %v", out, err)
	}
	if c.MaxSessions != 6 {
		t.Fatalf("max_sessions = %d, want 6", c.MaxSessions)
	}
	if c.Free != c.MaxSessions {
		t.Errorf("free = %d, want %d (empty pool: nothing live, nothing counted)", c.Free, c.MaxSessions)
	}
	if c.Live != 0 || c.Counted != 0 || c.Preserved != 0 {
		t.Errorf("got %+v, want all zero besides max_sessions/free on an empty pool", c)
	}
}
