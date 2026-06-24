package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNew_launchesFakeClaude_reachesReadyAndLive(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Build ccpool and place fake-claude beside it.
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	src, err := os.ReadFile("testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	fakeClaude := filepath.Join(bin, "fake-claude")
	if err := os.WriteFile(fakeClaude, src, 0o755); err != nil {
		t.Fatal(err)
	}

	socket := "ccpool-newtest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	// Config: point claude.bin at fake-claude, dedicated test socket.
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	cfg := `
[tmux]
socket = "` + socket + `"
[claude]
bin = "` + fakeClaude + `"
plugin_dir = "/unused-in-fake"
[wait]
timeout = "10s"
`
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600)

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"HOME="+base,         // trust writes to $base/.claude.json
		"CCPOOL_BIN="+ccpool, // fake-claude uses this to call `ccpool hook start`
		"PATH="+bin+":"+os.Getenv("PATH"),
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

	// new alpha → fake-claude fires SessionStart hook → ready.
	out, code := run("new", "alpha")
	if code != 0 {
		t.Fatalf("new exit=%d:\n%s", code, out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "ready") {
		t.Fatalf("new output missing alpha/ready:\n%s", out)
	}

	// list shows alpha ready + live.
	out, _ = run("list")
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "ready") || !strings.Contains(out, "yes") {
		t.Fatalf("list missing alpha ready live:\n%s", out)
	}

	// kill the tmux session → list shows not-live.
	_ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "cc-alpha").Run()
	time.Sleep(300 * time.Millisecond)
	out, _ = run("list", "--all")
	if !strings.Contains(out, "alpha") {
		t.Fatalf("alpha vanished from --all:\n%s", out)
	}
	// Liveness column should now read "no" for alpha.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "alpha") && strings.Contains(line, " yes ") {
			t.Errorf("alpha still live after kill:\n%s", line)
		}
	}
}

// TestNew_metaFlag_setsMetadataAtomically proves the --meta flag plumbs through the
// real CLI: `ccpool new --meta` sets metadata with NO separate `meta set` call,
// queryable via `ccpool meta get`. Mirrors the harness in
// TestNew_launchesFakeClaude_reachesReadyAndLive; tmux-gated.
func TestNew_metaFlag_setsMetadataAtomically(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	src, err := os.ReadFile("testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	fakeClaude := filepath.Join(bin, "fake-claude")
	if err := os.WriteFile(fakeClaude, src, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := "ccpool-metatest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	cfg := `
[tmux]
socket = "` + socket + `"
[claude]
bin = "` + fakeClaude + `"
plugin_dir = "/unused-in-fake"
[wait]
timeout = "10s"
`
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600)
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"HOME="+base,
		"CCPOOL_BIN="+ccpool,
		"PATH="+bin+":"+os.Getenv("PATH"),
	)
	proj := filepath.Join(base, "proj")
	_ = os.MkdirAll(proj, 0o755)

	// new WITH --meta, no separate `meta set`.
	newCmd := exec.Command(ccpool, "new", "ext-meta", "--cwd", proj,
		"--meta", "prpool.bead=zr-1", "--meta", "prpool.role=worker")
	newCmd.Env = env
	if out, err := newCmd.CombinedOutput(); err != nil {
		t.Fatalf("new --meta: %v\n%s", err, out)
	}

	// meta get must return the value set by `new`.
	getCmd := exec.Command(ccpool, "meta", "get", "ext-meta", "prpool.bead")
	getCmd.Env = env
	out, err := getCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("meta get: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "zr-1" {
		t.Errorf("meta get prpool.bead = %q, want zr-1", got)
	}
}
