//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReap_closesOverCap(t *testing.T) {
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
	src, _ := os.ReadFile("testdata/fake-claude")
	fake := filepath.Join(bin, "fake-claude")
	_ = os.WriteFile(fake, src, 0o755)
	socket := "ccpool-reaptest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
[pool]
max_sessions = 1
idle_ttl = "0s"
[tmux]
socket = "`+socket+`"
[claude]
bin = "`+fake+`"
plugin_dir = "/unused"
[wait]
timeout = "10s"
`), 0o600)
	env := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"HOME="+base, "CCPOOL_BIN="+ccpool, "PATH="+bin+":"+os.Getenv("PATH"),
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

	run("new", "first")
	time.Sleep(50 * time.Millisecond)
	run("new", "second") // newer activity than "first"
	if _, code := run("reap"); code != 0 {
		t.Fatal("reap failed")
	}
	time.Sleep(300 * time.Millisecond)
	// cap=1 → the LRU ("first") is closed; "second" survives.
	if exec.Command("tmux", "-L", socket, "has-session", "-t", "cc-first").Run() == nil {
		t.Error("cc-first (LRU) should have been reaped under cap=1")
	}
	if exec.Command("tmux", "-L", socket, "has-session", "-t", "cc-second").Run() != nil {
		t.Error("cc-second (most recent) should survive")
	}
	out, _ := run("doctor")
	if !strings.Contains(out, "second") {
		t.Errorf("doctor should report 'second':\n%s", out)
	}
}
