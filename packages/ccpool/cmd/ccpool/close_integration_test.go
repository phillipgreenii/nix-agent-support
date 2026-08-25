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

func TestClose_endsTheSession(t *testing.T) {
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
	socket := "ccpool-closetest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
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

	if _, code := run("new", "alpha"); code != 0 {
		t.Fatal("new failed")
	}
	if out, code := run("close", "alpha"); code != 0 {
		t.Fatalf("close exit=%d:\n%s", code, out)
	}
	time.Sleep(300 * time.Millisecond)
	if exec.Command("tmux", "-L", socket, "has-session", "-t", "cc-alpha").Run() == nil {
		t.Error("cc-alpha still live after close")
	}
	// --purge drops the row: build a fresh one, purge, assert gone from --all.
	if _, code := run("new", "beta"); code != 0 {
		t.Fatal("new beta failed")
	}
	if _, code := run("close", "beta", "--purge"); code != 0 {
		t.Fatal("close --purge failed")
	}
	out, _ := run("list", "--all")
	if strings.Contains(out, "beta") {
		t.Errorf("--purge should remove beta from the store:\n%s", out)
	}
}
