package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeEnv backs ResolveBinary/CLIRunner.Getenv in tests, mirroring
// cmd/pg-connector-issue-jira/internal/runner_test.go's identical fakeEnv
// pattern.
func fakeEnv(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// ----------------------------------------------------------------------
// ResolveBinary
// ----------------------------------------------------------------------

func TestResolveBinary_DefaultsToClaude(t *testing.T) {
	bin := ResolveBinary(fakeEnv(nil))
	if bin != "claude" {
		t.Fatalf("bin = %q, want claude", bin)
	}
}

func TestResolveBinary_EnvOverride(t *testing.T) {
	bin := ResolveBinary(fakeEnv(map[string]string{EnvBinary: "/custom/claude"}))
	if bin != "/custom/claude" {
		t.Fatalf("bin = %q, want /custom/claude", bin)
	}
}

func TestResolveBinary_BlankEnvTreatedAsUnset(t *testing.T) {
	bin := ResolveBinary(fakeEnv(map[string]string{EnvBinary: "   "}))
	if bin != "claude" {
		t.Fatalf("bin = %q, want claude for a blank override", bin)
	}
}

// ----------------------------------------------------------------------
// CLIRunner
// ----------------------------------------------------------------------

func TestCLIRunner_Binary_ExplicitOverride(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "/explicit/claude", Getenv: fakeEnv(map[string]string{EnvBinary: "/from/env"})}
	if got := r.Binary(); got != "/explicit/claude" {
		t.Fatalf("Binary() = %q, want /explicit/claude (explicit override wins over env)", got)
	}
}

func TestCLIRunner_Binary_ResolvesFromEnv(t *testing.T) {
	r := &CLIRunner{Getenv: fakeEnv(map[string]string{EnvBinary: "/from/env"})}
	if got := r.Binary(); got != "/from/env" {
		t.Fatalf("Binary() = %q, want /from/env", got)
	}
}

func TestCLIRunner_Command_SetsWaitDelay(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "claude"}
	cmd := r.command(context.Background(), "prompt text")
	if cmd.WaitDelay != scriptout.DefaultWaitDelay {
		t.Fatalf("cmd.WaitDelay = %v, want %v", cmd.WaitDelay, scriptout.DefaultWaitDelay)
	}
}

// TestCLIRunner_Command_ArgsAndStdin locks in claudeArgs' own fixed
// argument vector (this file's package doc comment explains each flag)
// and proves the prompt is delivered over stdin, never as a positional
// argument.
func TestCLIRunner_Command_ArgsAndStdin(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "claude"}
	cmd := r.command(context.Background(), "look up thread X")

	wantArgs := append([]string{"claude"}, claudeArgs()...)
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
	for i := range wantArgs {
		if cmd.Args[i] != wantArgs[i] {
			t.Fatalf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
		}
	}

	if cmd.Stdin == nil {
		t.Fatal("cmd.Stdin is nil, want the prompt reader command() set")
	}
	got, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if string(got) != "look up thread X" {
		t.Fatalf("stdin = %q, want the prompt verbatim", got)
	}
}

// claudeStubExitingWithStderr puts a fake executable named `claude` (or
// whatever name is given) on a temp dir, writing stderrMsg to stderr and
// exiting with exitCode — mirrors
// cmd/pg-connector-issue-jira/internal/runner_test.go's
// pjiraStubExitingWithStderr precedent.
func claudeStubExitingWithStderr(t *testing.T, name string, exitCode int, stderrMsg string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\ncat <<'CLAUDESTUBEOF' >&2\n" + stderrMsg + "\nCLAUDESTUBEOF\nexit " + fmt.Sprint(exitCode) + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestCLIRunner_Run_CapsStderr(t *testing.T) {
	huge := strings.Repeat("e", scriptout.MaxFoldedOutputBytes*3)
	stub := claudeStubExitingWithStderr(t, "claude", 1, huge)

	r := &CLIRunner{BinaryOverride: stub}
	_, err := r.Run(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error from the failing claude stub")
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
	script := "#!/bin/sh\ncat >/dev/null\necho '{\"result\":\"{}\",\"is_error\":false}'\n"
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	r := &CLIRunner{BinaryOverride: stub}
	out, err := r.Run(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, `"is_error":false`) {
		t.Fatalf("out = %q, want it to contain the envelope", out)
	}
}

func TestCLIRunner_Run_MissingBinary(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "/definitely/not/a/real/binary/claude"}
	_, err := r.Run(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error for a missing binary")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %v, want a not-found-style error", err)
	}
}
