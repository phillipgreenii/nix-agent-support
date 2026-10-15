package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// The options below are generic placeholders, never real values: the real
// working directory, model and MCP declaration come from the consuming
// flake's runtime config (this repo is public).
const (
	testCwd       = "/work/root"
	testModel     = "test-model"
	testMCPConfig = `{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.invalid/mcp"}}}`

	// wantAllowedTools is the exact --allowed-tools value, spelled out
	// literally (NOT built from the production readOnlyTools) so a change to
	// the allow-list has to change this test too.
	wantAllowedTools = "mcp__slack__slack_search_public,mcp__slack__slack_search_channels," +
		"mcp__slack__slack_read_thread,mcp__slack__slack_read_channel,mcp__slack__slack_read_user_profile"
)

// configCtx returns a context carrying config the way serveLoop does for a
// request's Config member (scriptout.WithConfig).
func configCtx(config string) context.Context {
	return scriptout.WithConfig(context.Background(), json.RawMessage(config))
}

// fullConfig is a backend config block with every claude option set,
// alongside the `queries` member a real block carries (the runner must
// ignore everything but `claude`).
func fullConfig() string {
	mcp, _ := json.Marshal(testMCPConfig)
	return `{"queries":{"mine":["in:#general"]},"claude":{"cwd":"` + testCwd +
		`","model":"` + testModel + `","mcp_config":` + string(mcp) + `}}`
}

// mustCommand builds r's command for ctx or fails the test.
func mustCommand(t *testing.T, r *CLIRunner, ctx context.Context, prompt, schema string) *exec.Cmd {
	t.Helper()
	cmd, err := r.command(ctx, prompt, schema)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	return cmd
}

func TestCLIRunner_Command_SetsWaitDelay(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "claude"}
	cmd := mustCommand(t, r, context.Background(), "prompt text", "")
	if cmd.WaitDelay != scriptout.DefaultWaitDelay {
		t.Fatalf("cmd.WaitDelay = %v, want %v", cmd.WaitDelay, scriptout.DefaultWaitDelay)
	}
}

// TestCLIRunner_Command_ConfiguredOptions is bead pg2-apllc's own contract:
// with every option supplied through the request Config on the context, the
// argv carries the stripped-down MCP flags, the configured model and
// EXACTLY the five read tools in ONE comma-separated --allowed-tools
// element; it carries neither --bare nor any send/schedule/draft/canvas
// tool; and cmd.Dir is the configured cwd. The whole vector is compared
// against a literal, so reordering or adding a flag fails here.
func TestCLIRunner_Command_ConfiguredOptions(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "claude"}
	cmd := mustCommand(t, r, configCtx(fullConfig()), "look up thread X", "")

	want := []string{
		"claude", "-p",
		"--output-format", "json",
		"--max-turns", "6",
		"--allowed-tools", wantAllowedTools,
		"--model", testModel,
		"--mcp-config", testMCPConfig,
		"--strict-mcp-config",
		"--setting-sources", "user",
		"--disable-slash-commands",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("cmd.Args =\n%q\nwant\n%q", cmd.Args, want)
	}
	if cmd.Dir != testCwd {
		t.Fatalf("cmd.Dir = %q, want the configured cwd %q", cmd.Dir, testCwd)
	}

	allowedFlags := 0
	for i, a := range cmd.Args {
		if a == "--allowed-tools" {
			allowedFlags++
			if cmd.Args[i+1] != wantAllowedTools {
				t.Fatalf("--allowed-tools value = %q, want %q", cmd.Args[i+1], wantAllowedTools)
			}
		}
		if a == "--bare" {
			t.Fatal("argv carries --bare, which ignores the OAuth/keychain credential the Slack MCP needs")
		}
	}
	if allowedFlags != 1 {
		t.Fatalf("%d --allowed-tools flags in %q, want exactly one", allowedFlags, cmd.Args)
	}
	// No write tool is pre-approved, whatever else changes.
	for _, a := range cmd.Args {
		for _, writer := range []string{"send", "schedule", "draft", "canvas"} {
			if strings.Contains(a, writer) {
				t.Fatalf("argv element %q mentions the write capability %q", a, writer)
			}
		}
	}
}

// TestCLIRunner_Command_UnsetOptionsAddNothing proves every option is
// optional: with no Config at all, or a block with no `claude` member (every
// real block carries `queries`), cmd.Dir is empty (inherited), no --model or
// MCP flag is passed, and the read-only allow-list is still there.
func TestCLIRunner_Command_UnsetOptionsAddNothing(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no config":         context.Background(),
		"queries only":      configCtx(`{"queries":{"mine":["in:#general"]}}`),
		"empty claude":      configCtx(`{"claude":{}}`),
		"blank claude vals": configCtx(`{"claude":{"cwd":"  ","model":"\t","mcp_config":" "}}`),
	} {
		t.Run(name, func(t *testing.T) {
			r := &CLIRunner{BinaryOverride: "claude"}
			cmd := mustCommand(t, r, ctx, "p", "")
			want := []string{
				"claude", "-p",
				"--output-format", "json",
				"--max-turns", "6",
				"--allowed-tools", wantAllowedTools,
			}
			if !reflect.DeepEqual(cmd.Args, want) {
				t.Fatalf("cmd.Args =\n%q\nwant\n%q", cmd.Args, want)
			}
			if cmd.Dir != "" {
				t.Fatalf("cmd.Dir = %q, want empty (inherited)", cmd.Dir)
			}
		})
	}
}

// maxTurnsArg returns the value following --max-turns in args, failing the
// test if the flag is missing or appears more than once.
func maxTurnsArg(t *testing.T, args []string) string {
	t.Helper()
	got, n := "", 0
	for i, a := range args {
		if a == "--max-turns" && i+1 < len(args) {
			got = args[i+1]
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d --max-turns flags in %q, want exactly one", n, args)
	}
	return got
}

// TestCLIRunner_Command_MaxTurns: claude.max_turns sets --max-turns; unset or
// 0 keeps the default of 6 (the full list prompt needs more, so a deployment
// raises it); the bounds 1 and 30 are accepted.
func TestCLIRunner_Command_MaxTurns(t *testing.T) {
	for name, tc := range map[string]struct{ config, want string }{
		"unset":      {`{"claude":{}}`, "6"},
		"no config":  {``, "6"},
		"zero":       {`{"claude":{"max_turns":0}}`, "6"},
		"configured": {`{"claude":{"max_turns":16}}`, "16"},
		"lower edge": {`{"claude":{"max_turns":1}}`, "1"},
		"upper edge": {`{"claude":{"max_turns":30}}`, "30"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if tc.config != "" {
				ctx = configCtx(tc.config)
			}
			cmd := mustCommand(t, &CLIRunner{BinaryOverride: "claude"}, ctx, "p", "")
			if got := maxTurnsArg(t, cmd.Args); got != tc.want {
				t.Fatalf("--max-turns = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCLIRunner_Command_MaxTurnsOutOfRangeIsInvalidArgument: a negative value,
// one above the cap of 30 and a non-integer are invalid_argument (never a
// silent clamp), both from command and from Run, which must not exec.
func TestCLIRunner_Command_MaxTurnsOutOfRangeIsInvalidArgument(t *testing.T) {
	for name, config := range map[string]string{
		"negative":       `{"claude":{"max_turns":-1}}`,
		"just over cap":  `{"claude":{"max_turns":31}}`,
		"far over cap":   `{"claude":{"max_turns":1000000}}`,
		"not an integer": `{"claude":{"max_turns":"many"}}`,
		"fractional":     `{"claude":{"max_turns":6.5}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := &CLIRunner{BinaryOverride: "/definitely/not/a/real/binary/claude"}
			_, err := r.command(configCtx(config), "p", "")
			if !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("command err = %v, want ErrInvalidArgument", err)
			}
			if _, err := r.Run(configCtx(config), "p", ""); !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("Run err = %v, want ErrInvalidArgument", err)
			}
		})
	}
	// The message names the key so config validate output is actionable.
	_, err := (&CLIRunner{BinaryOverride: "claude"}).command(configCtx(`{"claude":{"max_turns":31}}`), "p", "")
	if err == nil || !strings.Contains(err.Error(), "max_turns") {
		t.Fatalf("err = %v, want it to name max_turns", err)
	}
}

// TestCLIRunner_Command_MalformedClaudeBlockIsInvalidArgument: a `claude`
// member of the wrong shape must fail loudly rather than fall back to
// running with no MCP declaration (which would reproduce the silent
// empty-result failure).
func TestCLIRunner_Command_MalformedClaudeBlockIsInvalidArgument(t *testing.T) {
	for name, config := range map[string]string{
		"claude is a string": `{"claude":"oops"}`,
		"cwd is a number":    `{"claude":{"cwd":7}}`,
		"config not object":  `"oops"`,
	} {
		t.Run(name, func(t *testing.T) {
			r := &CLIRunner{BinaryOverride: "/definitely/not/a/real/binary/claude"}
			if _, err := r.command(configCtx(config), "p", ""); !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("command err = %v, want ErrInvalidArgument", err)
			}
			// Run reports the same error without ever exec'ing (the binary
			// above does not exist, which would be a different error).
			if _, err := r.Run(configCtx(config), "p", ""); !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("Run err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

// TestCLIRunner_Command_ArgsAndStdin locks in claudeArgs' own fixed
// argument vector (this file's package doc comment explains each flag)
// and proves the prompt is delivered over stdin, never as a positional
// argument. jsonSchema is "" here — no --json-schema flag — leaving the
// with-schema case to TestCLIRunner_Command_AppendsJSONSchemaWhenProvided
// below.
func TestCLIRunner_Command_ArgsAndStdin(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "claude"}
	cmd := mustCommand(t, r, configCtx(fullConfig()), "look up thread X", "")

	wantArgs := append([]string{"claude"}, claudeArgs("", claudeOptions{Model: testModel, MCPConfig: testMCPConfig})...)
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

// TestCLIRunner_Command_AppendsJSONSchemaWhenProvided locks in this bead's
// own fix (pg2-vkj77, runner.go's package doc comment): a non-empty
// jsonSchema must append exactly "--json-schema <jsonSchema>" to the end
// of claudeArgs' own fixed vector, so the CLI validates/forces the model's
// final answer against it rather than relying on prompt wording alone.
func TestCLIRunner_Command_AppendsJSONSchemaWhenProvided(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "claude"}
	schema := `{"type":"object","required":["items"]}`
	cmd := mustCommand(t, r, configCtx(fullConfig()), "look up thread X", schema)

	wantArgs := append([]string{"claude"}, claudeArgs(schema, claudeOptions{Model: testModel, MCPConfig: testMCPConfig})...)
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
	for i := range wantArgs {
		if cmd.Args[i] != wantArgs[i] {
			t.Fatalf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
		}
	}
	if got := cmd.Args[len(cmd.Args)-2]; got != "--json-schema" {
		t.Fatalf("cmd.Args[-2] = %q, want --json-schema", got)
	}
	if got := cmd.Args[len(cmd.Args)-1]; got != schema {
		t.Fatalf("cmd.Args[-1] = %q, want the schema verbatim", got)
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
	_, err := r.Run(context.Background(), "prompt", "")
	if err == nil {
		t.Fatal("expected error from the failing claude stub")
	}
	// The allowance covers the message's own argv prefix (the five read-tool
	// names alone are about 170 bytes), not the stderr fold.
	if got := len(err.Error()); got > scriptout.MaxFoldedOutputBytes+1024 {
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
	out, err := r.Run(context.Background(), "prompt", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, `"is_error":false`) {
		t.Fatalf("out = %q, want it to contain the envelope", out)
	}
}

// execStub writes a fake `claude` that records how the real exec delivered
// it: its argv, one argument per line, into <dir>/argv, and its working
// directory into <dir>/cwd, then answers with a success envelope. It proves
// the options survive an actual exec, which asserting on *exec.Cmd fields
// alone cannot. Arguments here are single-line, so line splitting is exact.
func execStub(t *testing.T) (stub, dir string) {
	t.Helper()
	dir = t.TempDir()
	script := "#!/bin/sh\n" +
		"cat >/dev/null\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + dir + "/argv'\n" +
		"pwd -P > '" + dir + "/cwd'\n" +
		"echo '{\"result\":\"{}\",\"is_error\":false}'\n"
	stub = filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return stub, dir
}

// TestCLIRunner_Run_ExecDeliversConfiguredOptions runs a real child and
// checks, from the child's own side, that it started in the configured cwd and
// received the configured argv verbatim (the inline MCP JSON, with its
// quotes, arrives as ONE argument).
func TestCLIRunner_Run_ExecDeliversConfiguredOptions(t *testing.T) {
	stub, recDir := execStub(t)
	cwd := t.TempDir()
	mcp, _ := json.Marshal(testMCPConfig)
	config := `{"claude":{"cwd":"` + cwd + `","model":"` + testModel + `","mcp_config":` + string(mcp) + `}}`

	r := &CLIRunner{BinaryOverride: stub}
	if _, err := r.Run(configCtx(config), "prompt", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}

	gotArgv, err := os.ReadFile(filepath.Join(recDir, "argv"))
	if err != nil {
		t.Fatalf("read argv record: %v", err)
	}
	wantArgv := strings.Join(claudeArgs("", claudeOptions{Model: testModel, MCPConfig: testMCPConfig}), "\n") + "\n"
	if string(gotArgv) != wantArgv {
		t.Fatalf("child argv =\n%s\nwant\n%s", gotArgv, wantArgv)
	}
	if !strings.Contains(string(gotArgv), "--mcp-config\n"+testMCPConfig+"\n--strict-mcp-config\n") {
		t.Fatalf("child argv did not carry the inline MCP declaration as one argument:\n%s", gotArgv)
	}

	gotCwd, err := os.ReadFile(filepath.Join(recDir, "cwd"))
	if err != nil {
		t.Fatalf("read cwd record: %v", err)
	}
	wantCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if strings.TrimSpace(string(gotCwd)) != wantCwd {
		t.Fatalf("child cwd = %q, want the configured %q", strings.TrimSpace(string(gotCwd)), wantCwd)
	}
}

// TestCLIRunner_Run_NoConfigInheritsCwd is the negative control for the test
// above: with no cwd configured the child keeps this process's own working
// directory.
func TestCLIRunner_Run_NoConfigInheritsCwd(t *testing.T) {
	stub, recDir := execStub(t)
	r := &CLIRunner{BinaryOverride: stub}
	if _, err := r.Run(context.Background(), "prompt", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	gotCwd, err := os.ReadFile(filepath.Join(recDir, "cwd"))
	if err != nil {
		t.Fatalf("read cwd record: %v", err)
	}
	here, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	want, err := filepath.EvalSymlinks(here)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if strings.TrimSpace(string(gotCwd)) != want {
		t.Fatalf("child cwd = %q, want the inherited %q", strings.TrimSpace(string(gotCwd)), want)
	}
}

// TestCLIRunner_Run_MissingCwdNamesTheDirectory: a configured cwd that does
// not exist (an unmounted volume, a moved checkout) fails before claude
// starts, and the error must name the directory rather than blame PATH.
func TestCLIRunner_Run_MissingCwdNamesTheDirectory(t *testing.T) {
	stub, _ := execStub(t)
	missing := filepath.Join(t.TempDir(), "not-mounted")
	r := &CLIRunner{BinaryOverride: stub}
	_, err := r.Run(configCtx(`{"claude":{"cwd":"`+missing+`"}}`), "prompt", "")
	if err == nil {
		t.Fatal("expected an error for a missing cwd")
	}
	for _, want := range []string{"chdir", "claude.cwd", missing} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "on PATH") {
		t.Fatalf("err = %q blames PATH for a missing working directory", err)
	}
}

// TestCLIRunner_Run_ErrorElidesMCPConfig: error text reaches the event log
// and alerts, and an inline MCP declaration may carry a header or token, so
// the failure message must not repeat its value.
func TestCLIRunner_Run_ErrorElidesMCPConfig(t *testing.T) {
	stub := claudeStubExitingWithStderr(t, "claude", 1, "boom")
	secret := "test-secret-value-12345"
	mcp, _ := json.Marshal(`{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.invalid/mcp","headers":{"X-Test":"` + secret + `"}}}}`)
	r := &CLIRunner{BinaryOverride: stub}
	_, err := r.Run(configCtx(`{"claude":{"mcp_config":`+string(mcp)+`}}`), "prompt", "")
	if err == nil {
		t.Fatal("expected error from the failing claude stub")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %q leaks the MCP declaration's value", err)
	}
	if !strings.Contains(err.Error(), "--mcp-config <mcp-config>") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %q, want the elided flag and the stderr tail", err)
	}
}

func TestCLIRunner_Run_MissingBinary(t *testing.T) {
	r := &CLIRunner{BinaryOverride: "/definitely/not/a/real/binary/claude"}
	_, err := r.Run(context.Background(), "prompt", "")
	if err == nil {
		t.Fatal("expected error for a missing binary")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %v, want a not-found-style error", err)
	}
}
