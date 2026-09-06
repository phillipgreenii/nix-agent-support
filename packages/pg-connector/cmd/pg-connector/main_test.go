package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// captureRealStdio redirects the REAL os.Stdout/os.Stderr (not cobra's own
// buffer, which executePr swaps in instead — see pr_test.go) around
// calling f, restoring both before returning. Used to reproduce bug
// pg2-njx27 exactly as reported: a Tier-1 CLI-level failure that used to
// print prose on the real stderr with an empty real stdout, rather than a
// JSON error envelope on stdout — a distinction executePr's shared
// in-memory buffer cannot observe, since main's own run() (which does the
// actual os.Stderr fallback print) is what's under test here, not
// cobra's Execute().
func captureRealStdio(t *testing.T, f func() int) (stdout, stderr string, code int) {
	t.Helper()
	origStdout, origStderr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = origStdout, origStderr }()

	code = f()

	outW.Close()
	errW.Close()
	var outBuf, errBuf bytes.Buffer
	if _, err := io.Copy(&outBuf, outR); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	if _, err := io.Copy(&errBuf, errR); err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	return outBuf.String(), errBuf.String(), code
}

// TestRun_PrShow_EmptyRegistry_JSONEnvelopeOnRealStdoutNotStderr
// reproduces bug pg2-njx27's own cited repro command verbatim:
// `PG_PR_CONFIG=<file with connector: {}> pg-connector pr show 1`. Before
// the fix, this printed prose to the real stderr with an empty real
// stdout (main.go's generic fallback, reached because output.go's
// writeTargetedResult returned a bare error for a nil resp); after the
// fix it must print a JSON error envelope to the real stdout with
// nothing on the real stderr, matching the same "only stdout JSON is the
// contract" convention a backend-reported failure already gets.
func TestRun_PrShow_EmptyRegistry_JSONEnvelopeOnRealStdoutNotStderr(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, stderr, code := captureRealStdio(t, func() int {
		return run([]string{"pr", "show", "1"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty — the failure must be reported as a JSON envelope on stdout, not stderr prose", stderr)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "no backend registered") {
		t.Fatalf("resp.Error = %+v, want a message naming the no-backend-registered failure", resp.Error)
	}
}

func TestRun_AuthStatus_ExitCodeMatchesOutcome(t *testing.T) {
	writeFakeBackend(t, "backend-ok", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`)

	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-ok\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	if code := run([]string{"auth", "status"}); code != 0 {
		t.Fatalf("run(auth status) = %d, want 0", code)
	}
}

func TestRun_AuthStatus_NoConfigIsGenericFailure(t *testing.T) {
	t.Setenv("PG_PR_CONFIG", "/does/not/exist.yaml")
	// A missing/invalid config is a CLI-level failure before any
	// well-formed fan-out response was produced — the generic exit-1
	// path, never one of the fan-out/targeted taxonomy codes.
	if code := run([]string{"auth", "status"}); code != 1 {
		t.Fatalf("run(auth status) = %d, want 1", code)
	}
}

func TestRun_AuthStatus_HumanOutput(t *testing.T) {
	writeFakeBackend(t, "backend-ok-human", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`)

	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-ok-human\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, code := executePr(t, []string{"--output", "human", "auth", "status"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "auth status:") || !strings.Contains(stdout, "backend-ok-human: succeeded") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_ConfigValidate_HumanOutput(t *testing.T) {
	writeFakeBackend(t, "backend-bad-human", `{"protocolVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`)

	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-bad-human\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, code := executePr(t, []string{"--output", "human", "config", "validate"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "config validate:") || !strings.Contains(stdout, "backend-bad-human: degraded") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_ConfigValidate_DegradedExitCode(t *testing.T) {
	writeFakeBackend(t, "backend-bad", `{"protocolVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`)

	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-bad\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	if code := run([]string{"config", "validate"}); code != 3 {
		t.Fatalf("run(config validate) = %d, want 3 (single degraded source is total failure)", code)
	}
}
