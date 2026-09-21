//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCrash_leavesInspectablePane proves the pg2-olchz fix: NewSession's
// remain-on-exit=failed keeps a CRASHED session's tmux pane (and the stderr it
// wrote) around on the socket for forensic inspection, instead of the pane —
// and, since it is a single-window session, the whole tmux session — vanishing
// the instant the foreground process dies (pg2-5sirm's finding). It also
// proves the matching HasSession fix (querying #{pane_dead}, not plain
// has-session) so `ccpool list`'s own liveness reporting is NOT fooled by the
// preserved dead pane: the crashed session must still read as not-live, or
// every caller that trusts Live as "the process is running" (reuse-live,
// phantom-prune reap, pg-router-ccpool-handler's active()) would wrongly treat
// it as still working.
func TestCrash_leavesInspectablePane(t *testing.T) {
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
	src, err := os.ReadFile("testdata/fake-claude-crash")
	if err != nil {
		t.Fatal(err)
	}
	fakeClaude := filepath.Join(bin, "fake-claude-crash")
	if err := os.WriteFile(fakeClaude, src, 0o755); err != nil {
		t.Fatal(err)
	}

	socket := "ccpool-crashtest"
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

	env := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"HOME="+base,
		"CCPOOL_BIN="+ccpool,
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

	// new alpha → fake-claude-crash fires SessionStart hook → ready/live,
	// exactly like the happy-path fake-claude.
	if out, code := run("new", "alpha"); code != 0 {
		t.Fatalf("new exit=%d:\n%s", code, out)
	}

	tmuxSession := "cc-alpha"

	// Deliver one line directly (bypassing `ccpool reply`, which would block on
	// a Stop hook that a crash never fires): fake-claude-crash reads it, prints
	// its forensic marker to stderr, and exits 7.
	if out, err := exec.Command("tmux", "-L", socket, "send-keys", "-t", tmuxSession, "trigger", "Enter").CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v\n%s", err, out)
	}

	// Poll (bounded) until the pane reports dead, rather than a fixed sleep.
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", tmuxSession, "#{pane_dead}").CombinedOutput()
		if strings.TrimSpace(string(out)) == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never reported dead after crash; last #{pane_dead}=%q", string(out))
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 1. Raw tmux must still know about the session: the pane (and, since this
	// is single-window, the whole session) survived the crash instead of being
	// destroyed. This is the behavior remain-on-exit=failed changed.
	if out, err := exec.Command("tmux", "-L", socket, "has-session", "-t", tmuxSession).CombinedOutput(); err != nil {
		t.Fatalf("has-session after crash: %v\n%s (session should have survived for forensic inspection)", err, out)
	}

	// 2. The forensic evidence (stderr) must still be visible in the dead pane.
	captured, err := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", tmuxSession).CombinedOutput()
	if err != nil {
		t.Fatalf("capture-pane after crash: %v\n%s", err, captured)
	}
	if !strings.Contains(string(captured), "FAKE-CLAUDE-CRASH-MARKER") {
		t.Errorf("captured dead pane missing crash stderr marker:\n%s", captured)
	}

	// 3. ccpool's OWN liveness reporting must NOT be fooled by the preserved
	// dead pane: `list --json` must still report live=false for alpha, or the
	// crash would be silently treated as "still working" everywhere that
	// trusts Live (session.go's reuse-live branch, reap's phantom-prune,
	// pg-router-ccpool-handler's active()).
	out, code := run("list", "--all", "--json")
	if code != 0 {
		t.Fatalf("list --all --json exit=%d:\n%s", code, out)
	}
	var rows []struct {
		ExternalID string `json:"external_id"`
		Live       bool   `json:"live"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal list --json: %v\n%s", err, out)
	}
	found := false
	for _, r := range rows {
		if r.ExternalID == "alpha" {
			found = true
			if r.Live {
				t.Errorf("list --json reports alpha live=true after a crash; want false (HasSession must treat a dead pane as not-live)")
			}
		}
	}
	if !found {
		t.Fatalf("alpha missing from list --all --json:\n%s", out)
	}
}
