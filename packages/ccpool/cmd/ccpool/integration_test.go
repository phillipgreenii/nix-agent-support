package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildCCPool compiles the binary once into the test's temp dir.
func buildCCPool(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ccpool")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build ccpool: %v\n%s", err, out)
	}
	return bin
}

func runCC(t *testing.T, bin, xdgData, xdgState, ccpoolName, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"XDG_DATA_HOME="+xdgData,
		"XDG_STATE_HOME="+xdgState,
		"XDG_CONFIG_HOME="+filepath.Join(xdgData, "..", "cfg"),
	)
	if ccpoolName != "" {
		cmd.Env = append(cmd.Env, "CCPOOL_NAME="+ccpoolName)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	code := 0
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run %v: %v\n%s", args, err, out.String())
		}
	}
	return out.String(), code
}

func TestEndToEnd_hookLifecycleReflectedInList(t *testing.T) {
	bin := buildCCPool(t)
	base := t.TempDir()
	data := filepath.Join(base, "data")
	state := filepath.Join(base, "state")

	// Isolate liveness onto a dedicated tmux socket (mirrors TestReap_closesOverCap)
	// so a real ccpool session on the shared default "ccpool" socket can never bleed
	// into this test's has-session checks (nas-a95.5). runCC points XDG_CONFIG_HOME
	// at <base>/cfg, so the override lives at <base>/cfg/ccpool/config.toml.
	const socket = "ccpool-hooktest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("[tmux]\nsocket = \""+socket+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const start = `{"session_id":"11111111-1111-1111-1111-111111111111","transcript_path":"/p/x.jsonl","cwd":"/tmp/x","hook_event_name":"SessionStart","source":"startup"}`
	const stop = `{"session_id":"11111111-1111-1111-1111-111111111111","transcript_path":"/p/x.jsonl","hook_event_name":"Stop"}`

	// SessionStart: upserts row by CCPOOL_NAME, sets ready.
	if _, code := runCC(t, bin, data, state, "alpha", start, "hook", "start"); code != 0 {
		t.Fatalf("hook start exit = %d, want 0", code)
	}
	// Stop: resolves by uuid, sets done.
	if _, code := runCC(t, bin, data, state, "", stop, "hook", "stop"); code != 0 {
		t.Fatalf("hook stop exit = %d, want 0", code)
	}
	// list reflects it (cc-alpha not live → derived cold; young done → shown).
	out, code := runCC(t, bin, data, state, "", "", "list")
	if code != 0 {
		t.Fatalf("list exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "done") {
		t.Fatalf("list missing alpha/done:\n%s", out)
	}
	if !strings.Contains(out, " no ") {
		t.Errorf("expected alpha to read not-live (no), got:\n%s", out)
	}
}
