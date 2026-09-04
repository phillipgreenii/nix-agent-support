package main

import (
	"os"
	"strings"
	"testing"
)

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
