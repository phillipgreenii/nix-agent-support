//go:build integration

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ffPool is a throwaway pool on its own tmux socket, driven through the real
// ccpool binary. The tests below prove the fail-fast launch wait (INV-POOL-8)
// against real tmux: a launch whose process has already died must fail at once,
// carrying the exit status or signal and the output tail, while the configured
// wait timeout is minutes.
type ffPool struct {
	t       *testing.T
	base    string
	ccpool  string
	socket  string
	env     []string
	cfgFile string
}

func newFFPool(t *testing.T, claudeBin, timeout string) *ffPool {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(bin, "fake-claude"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	// A short per-test socket name: a long one overflows the unix socket path.
	sum := sha256.Sum256([]byte(base))
	socket := "ccff-" + hex.EncodeToString(sum[:6])
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	p := &ffPool{t: t, base: base, ccpool: ccpool, socket: socket}
	if err := os.MkdirAll(filepath.Join(base, "cfg", "ccpool"), 0o700); err != nil {
		t.Fatal(err)
	}
	p.cfgFile = filepath.Join(base, "cfg", "ccpool", "config.toml")
	p.writeConfig(claudeBin, timeout)
	if err := os.MkdirAll(filepath.Join(base, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The ambient CCPOOL_* variables (CCPOOL_POOL above all) would address the
	// caller's own pool instead of this throwaway one, so none is inherited.
	var inherited []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CCPOOL_") {
			inherited = append(inherited, kv)
		}
	}
	p.env = append(
		inherited,
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"HOME="+base,
		"CCPOOL_BIN="+ccpool,
		"FAKE_CLAUDE_TRANSCRIPT="+filepath.Join(base, "t.jsonl"),
		"PATH="+bin+":"+os.Getenv("PATH"),
	)
	return p
}

func (p *ffPool) writeConfig(claudeBin, timeout string) {
	p.t.Helper()
	cfg := "[tmux]\nsocket = \"" + p.socket + "\"\n" +
		"[claude]\nbin = \"" + claudeBin + "\"\nplugin_dir = \"/unused-in-fake\"\n" +
		"[wait]\ntimeout = \"" + timeout + "\"\n"
	if err := os.WriteFile(p.cfgFile, []byte(cfg), 0o600); err != nil {
		p.t.Fatal(err)
	}
}

// run executes ccpool and returns its combined output, exit code and how long
// it took.
func (p *ffPool) run(args ...string) (string, int, time.Duration) {
	p.t.Helper()
	cmd := exec.Command(p.ccpool, args...)
	cmd.Env = p.env
	cmd.Dir = filepath.Join(p.base, "proj")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	took := time.Since(start)
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		p.t.Fatalf("run %v: %v\n%s", args, err, out)
	}
	return string(out), code, took
}

func (p *ffPool) script(name, body string) string {
	p.t.Helper()
	path := filepath.Join(p.base, "bin", name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		p.t.Fatal(err)
	}
	return path
}

// An instantly-exiting payload, 200 times: its exit status and a message always
// survive (remain-on-exit is armed in the creating tmux call, so even a process
// that dies within milliseconds leaves its pane), and each launch fails in
// seconds although the wait timeout is five minutes.
func TestFailFast_instantExitKeepsStatusAndMessage(t *testing.T) {
	p := newFFPool(t, "/usr/bin/false", "5m")
	for i := 0; i < 200; i++ {
		out, code, took := p.run("new", fmt.Sprintf("ff-%03d", i))
		if code == 0 {
			t.Fatalf("iteration %d: new exited 0, want a failure:\n%s", i, out)
		}
		if !strings.Contains(out, "exited with status 1") {
			t.Fatalf("iteration %d: message lacks the exit status (took %v):\n%s", i, took, out)
		}
		if took > 30*time.Second {
			t.Fatalf("iteration %d: took %v, want a fail-fast far under the 5m timeout", i, took)
		}
	}
}

// A process that exits 0 during launch destroys its session (remain-on-exit
// keeps only failed panes); the absent session reads as a clean exit.
func TestFailFast_cleanExitDuringLaunch(t *testing.T) {
	p := newFFPool(t, "/usr/bin/true", "5m")
	out, code, took := p.run("new", "clean")
	if code == 0 {
		t.Fatalf("new exited 0:\n%s", out)
	}
	if !strings.Contains(out, "exited cleanly during launch") {
		t.Errorf("message lacks %q:\n%s", "exited cleanly during launch", out)
	}
	if took > 30*time.Second {
		t.Errorf("took %v, want a fail-fast", took)
	}
}

// A payload killed by a signal reports the signal, not a status.
func TestFailFast_signalExit(t *testing.T) {
	p := newFFPool(t, "", "5m")
	p.writeConfig(p.script("sig-claude", "kill -TERM $$"), "5m")
	out, code, took := p.run("new", "sig")
	if code == 0 {
		t.Fatalf("new exited 0:\n%s", out)
	}
	if !strings.Contains(out, "killed by signal 15") {
		t.Errorf("message lacks the signal:\n%s", out)
	}
	if took > 30*time.Second {
		t.Errorf("took %v, want a fail-fast", took)
	}
}

// A payload that is alive but never becomes ready gives no death signal: the
// wait ends at its timeout, and the error carries the pane's output tail.
func TestFailFast_aliveButNeverReadyTimesOutWithTail(t *testing.T) {
	p := newFFPool(t, "", "3s")
	p.writeConfig(p.script("sleepy-claude", "echo login-prompt-marker-k19\nexec sleep 600"), "3s")
	out, code, took := p.run("new", "sleepy")
	if code == 0 {
		t.Fatalf("new exited 0:\n%s", out)
	}
	if !strings.Contains(out, "did not reach ready before timeout") {
		t.Errorf("message lacks the timeout text:\n%s", out)
	}
	if !strings.Contains(out, "login-prompt-marker-k19") {
		t.Errorf("timeout message lacks the pane tail:\n%s", out)
	}
	if took < 3*time.Second {
		t.Errorf("took %v, want it to wait out the 3s timeout", took)
	}
}

// reply's resume goes through the same launch wait: a session that was ready,
// lost its tmux session, and whose configured executable now fails, fails the
// resume at once rather than after the timeout.
func TestFailFast_replyResumeFailsFast(t *testing.T) {
	p := newFFPool(t, "", "30s")
	fake := filepath.Join(p.base, "bin", "fake-claude")
	p.writeConfig(fake, "30s")
	if out, code, _ := p.run("new", "rs"); code != 0 {
		t.Fatalf("new exit=%d:\n%s", code, out)
	}
	if out, err := exec.Command("tmux", "-L", p.socket, "kill-session", "-t", "cc-rs").CombinedOutput(); err != nil {
		t.Fatalf("kill-session: %v\n%s", err, out)
	}

	p.writeConfig("/usr/bin/false", "5m")
	out, code, took := p.run("reply", "rs", "hi")
	if code == 0 {
		t.Fatalf("reply exited 0, want a failed resume:\n%s", out)
	}
	if !strings.Contains(out, "exited with status 1") {
		t.Errorf("resume failure lacks the exit status:\n%s", out)
	}
	if took > 30*time.Second {
		t.Errorf("took %v, want a fail-fast far under the 5m timeout", took)
	}
}
