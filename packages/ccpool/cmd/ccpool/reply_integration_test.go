package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReply_returnsAssistantReply(t *testing.T) {
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

	socket := "ccpool-replytest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	transcript := filepath.Join(base, "t.jsonl")
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

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"HOME="+base,
		"CCPOOL_BIN="+ccpool,
		"FAKE_CLAUDE_TRANSCRIPT="+transcript, // both new + reply share this transcript
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

	if _, code := run("new", "alpha"); code != 0 {
		t.Fatal("new failed")
	}
	out, code := run("reply", "alpha", "say something")
	if code != 0 {
		t.Fatalf("reply exit=%d:\n%s", code, out)
	}
	if !strings.Contains(out, "OK-DONE") {
		t.Fatalf("reply did not return the assistant text; got:\n%s", out)
	}
}
