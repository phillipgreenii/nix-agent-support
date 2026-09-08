package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeEnv backs ResolveBinary/CLIRunner.Getenv in tests, mirroring
// cmd/pg-connector-issue-beads/internal/runner_test.go's identical fakeEnv
// pattern.
func fakeEnv(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// ----------------------------------------------------------------------
// ResolveBinary
// ----------------------------------------------------------------------

func TestResolveBinary_DefaultsToPjira(t *testing.T) {
	bin := ResolveBinary(fakeEnv(nil))
	if bin != "pjira" {
		t.Fatalf("bin = %q, want pjira (verified as the real installed generic Jira CLI on this workspace's PATH — see defaultBinary's doc comment)", bin)
	}
}

func TestResolveBinary_EnvOverride(t *testing.T) {
	bin := ResolveBinary(fakeEnv(map[string]string{EnvBinary: "/custom/pjira"}))
	if bin != "/custom/pjira" {
		t.Fatalf("bin = %q, want /custom/pjira", bin)
	}
}

func TestResolveBinary_BlankEnvTreatedAsUnset(t *testing.T) {
	bin := ResolveBinary(fakeEnv(map[string]string{EnvBinary: "   "}))
	if bin != "pjira" {
		t.Fatalf("bin = %q, want pjira for a blank override", bin)
	}
}

// ----------------------------------------------------------------------
// CLIRunner
// ----------------------------------------------------------------------

func TestCLIRunner_Binary_ExplicitOverride(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "/explicit/pjira", Getenv: fakeEnv(map[string]string{EnvBinary: "/from/env"})}
	if got := r.Binary(); got != "/explicit/pjira" {
		t.Fatalf("Binary() = %q, want /explicit/pjira (explicit override wins over env)", got)
	}
}

func TestCLIRunner_Binary_ResolvesFromEnv(t *testing.T) {
	r := &CLIRunner{Getenv: fakeEnv(map[string]string{EnvBinary: "/from/env"})}
	if got := r.Binary(); got != "/from/env" {
		t.Fatalf("Binary() = %q, want /from/env", got)
	}
}

func TestCLIRunner_Command_SetsWaitDelay(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "pjira"}
	cmd := r.command(context.Background(), []string{"issue", "PROJ-1"})
	if cmd.WaitDelay != scriptout.DefaultWaitDelay {
		t.Fatalf("cmd.WaitDelay = %v, want %v", cmd.WaitDelay, scriptout.DefaultWaitDelay)
	}
}

// pjiraStubExitingWithStderr puts a fake executable named `pjira` (or
// whatever name is given) on a temp dir, writing stderrMsg to stderr and
// exiting with exitCode — mirroring the bd/gh backends' own
// bdStubExitingWithStderr/ghStubExitingWithStderr helpers.
func pjiraStubExitingWithStderr(t *testing.T, name string, exitCode int, stderrMsg string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'PJIRASTUBEOF' >&2\n" + stderrMsg + "\nPJIRASTUBEOF\nexit " + fmt.Sprint(exitCode) + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestCLIRunner_Run_CapsStderr(t *testing.T) {
	huge := strings.Repeat("e", scriptout.MaxFoldedOutputBytes*3)
	stub := pjiraStubExitingWithStderr(t, "pjira", 1, huge)

	r := &CLIRunner{BinaryOverride: stub}
	_, err := r.Run(context.Background(), "issue", "PROJ-1")
	if err == nil {
		t.Fatal("expected error from the failing pjira stub")
	}
	if got := len(err.Error()); got > scriptout.MaxFoldedOutputBytes+256 {
		t.Fatalf("error message is %d bytes; stderr fold was not capped", got)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncation marker in the error, got a %d-byte message", len(err.Error()))
	}
}

func TestCLIRunner_Run_Success(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho '{\"key\":\"PROJ-1\"}'\n"
	stub := filepath.Join(dir, "pjira")
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	r := &CLIRunner{BinaryOverride: stub}
	out, err := r.Run(context.Background(), "issue", "PROJ-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "PROJ-1") {
		t.Fatalf("out = %q, want it to contain PROJ-1", out)
	}
}

func TestCLIRunner_Run_MissingBinary(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "/definitely/not/a/real/binary/pjira"}
	_, err := r.Run(context.Background(), "issue", "PROJ-1")
	if err == nil {
		t.Fatal("expected error for a missing binary")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %v, want a not-found-style error", err)
	}
}
