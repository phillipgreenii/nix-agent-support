//go:build integration

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

func runCC(t *testing.T, bin, xdgData, xdgState, externalID, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = runCCEnv(os.Environ(), xdgData, xdgState)
	if externalID != "" {
		cmd.Env = append(cmd.Env, "CCPOOL_EXTERNAL_ID="+externalID)
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

// runCCEnv builds the subprocess environment for runCC. HOME is replaced with
// the test's sandbox base (the parent of xdgData) so Claude trust writes land
// in <base>/.claude.json rather than the operator's real ~/.claude.json
// (pg2-b74hc). The subprocess is the ccpool binary, not `go build`, but the Go
// cache locations are pinned to the real ones anyway so any go tooling it (or
// a future helper) runs still reuses the real module/build caches.
func runCCEnv(base []string, xdgData, xdgState string) []string {
	sandbox := filepath.Join(xdgData, "..")
	env := append([]string{}, base...)
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if os.Getenv(k) != "" {
			continue
		}
		if v, err := exec.Command("go", "env", k).Output(); err == nil {
			env = append(env, k+"="+strings.TrimSpace(string(v)))
		}
	}
	return append(
		env,
		"HOME="+sandbox,
		"XDG_DATA_HOME="+xdgData,
		"XDG_STATE_HOME="+xdgState,
		"XDG_CONFIG_HOME="+filepath.Join(sandbox, "cfg"),
	)
}

// envWithoutOTel returns os.Environ() minus every OTEL_* variable, so a
// subprocess ccpool (and the tmux server/hooks it starts) never exports
// telemetry to a collector inherited from the developer's shell.
func envWithoutOTel() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "OTEL_") {
			out = append(out, kv)
		}
	}
	return out
}

// narrationLine returns the first stderr text-handler slog line whose msg is
// msg and whose external_id is exactly externalID ("" when none matches).
func narrationLine(out, msg, externalID string) string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `msg="`+msg+`"`) {
			continue
		}
		if strings.Contains(line, " external_id="+externalID+" ") || strings.HasSuffix(line, " external_id="+externalID) {
			return line
		}
	}
	return ""
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

	// SessionStart: upserts row by CCPOOL_EXTERNAL_ID, sets ready.
	if _, code := runCC(t, bin, data, state, "alpha", start, "hook", "start"); code != 0 {
		t.Fatalf("hook start exit = %d, want 0", code)
	}
	// Stop: resolves by claude_session_id, sets idle.
	if _, code := runCC(t, bin, data, state, "", stop, "hook", "stop"); code != 0 {
		t.Fatalf("hook stop exit = %d, want 0", code)
	}
	// list reflects it (cc-alpha not live → derived not-live; young idle → shown).
	out, code := runCC(t, bin, data, state, "", "", "list")
	if code != 0 {
		t.Fatalf("list exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "idle") {
		t.Fatalf("list missing alpha/idle:\n%s", out)
	}
	if !strings.Contains(out, " no ") {
		t.Errorf("expected alpha to read not-live (no), got:\n%s", out)
	}
}

// TestRunCCEnv_sandboxesHome asserts runCC never hands the subprocess the
// operator's real HOME (pg2-b74hc), while Go cache vars still resolve to the
// real caches.
func TestRunCCEnv_sandboxesHome(t *testing.T) {
	base := t.TempDir()
	realHome := os.Getenv("HOME")
	env := runCCEnv(os.Environ(), filepath.Join(base, "data"), filepath.Join(base, "state"))
	got := map[string]string{}
	for _, kv := range env { // later entries win, as in exec
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if got["HOME"] == realHome || filepath.Clean(got["HOME"]) != filepath.Clean(base) {
		t.Fatalf("HOME = %q, want sandbox %q (real %q)", got["HOME"], base, realHome)
	}
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if want, err := exec.Command("go", "env", k).Output(); err == nil && got[k] != strings.TrimSpace(string(want)) {
			t.Errorf("%s = %q, want real %q", k, got[k], strings.TrimSpace(string(want)))
		}
	}
}
