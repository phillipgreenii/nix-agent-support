// runner.go: the claude -p exec seam. Mirrors
// cmd/pg-connector-issue-jira/internal/runner.go's "injectable Runner
// interface; production execs a real binary, tests inject a fake" seam
// (this bead's own Contract: the STRUCTURAL pattern this backend's own
// claude -p exec wrapper mirrors) — adapted for claude -p rather than
// pjira: the prompt is delivered over STDIN (mirroring
// packages/pg-ccaudit/internal/classify.execRunner's own real, landed
// `claude -p` precedent: prompt over stdin, `--output-format json` for a
// structured, cost-bearing envelope), never as a positional argument.
//
// Freedom boundary [design: "the exact claude -p invocation shape ...
// implementer's own call, documented in runner.go's doc comment the way
// pg-connector-issue-jira's own runner.go documents its pjira invocation
// choices"] — this backend's own choices, none independently verified
// against a live claude/Slack-MCP call (this packet's implementer has no
// live Slack credentials; see the "no Slack token" binding decision):
//
//   - `-p` (print/non-interactive mode) with `--output-format json`: the
//     envelope carries the model's final text in its own "result" field
//     plus cost/usage accounting, mirroring pg-ccaudit's own DefaultCommand
//     doc comment ("what makes the cost REPORTABLE").
//   - `--max-turns 6`: pg-ccaudit's own "--max-turns 1" pins a pure-text
//     classification with no tool use at all; this backend's prompt asks
//     the model to actually call the already-configured Slack MCP tool and
//     then reply with JSON, which needs at least one tool-call round trip
//     (a turn to invoke the tool, a turn carrying the tool's result, a
//     turn producing the final reply) — 6 leaves margin for the MCP tool
//     needing more than one call (e.g. a paginated search) without letting
//     a misbehaving run become an open-ended agent session.
//   - `--allowed-tools mcp__slack`: pre-approves ONLY the Slack MCP's own
//     tools (Claude Code's own server-level allow-list form — allowing the
//     bare `mcp__<server>` name approves every tool under that server,
//     without enumerating each one) so this non-interactive call needs no
//     permission prompt for exactly the one tool this backend's compute-
//     only contract permits, while leaving every OTHER tool (Bash, Edit,
//     ...) unapproved and therefore unusable even if the model attempted
//     one — deliberately narrower than `--permission-mode bypassPermissions`
//     (ccpool's own worker mode), which would approve everything.
//   - No `--model` pin: unlike pg-ccaudit's high-volume, cost-sensitive
//     classification loop, this backend is a low-volume connector read —
//     it defers to whatever default model this machine's own `claude`
//     configuration resolves, rather than pinning one here.
//   - No MCP-config flag of any kind (`--mcp-config` or similar): this
//     bead's own Files section is explicit that the Slack MCP is already
//     configured on the machine and this backend "passes no MCP-config
//     flags of its own."
//   - `--json-schema <schema>`, appended per-call (see claudeArgs below)
//     rather than baked into the fixed vector above: bead pg2-vkj77's own
//     reproduction (running this exact invocation live) proved the prior
//     no-schema shape's real failure mode — with no Slack MCP server
//     actually configured on the machine (contrary to this file's own
//     "already configured" assumption above), the model correctly and
//     truthfully declines in PROSE ("No Slack MCP tools are available in
//     this session ...", "I checked ... and there is no Slack server ...")
//     rather than emitting the JSON the prompt asked for — decodeClaudeEnvelope
//     still succeeds (the CLI's own `--output-format json` outer envelope is
//     always well-formed), but the inner env.Result string is that prose,
//     so decodeShowReply/decodeListReply then fail with exactly the
//     production symptom ("invalid character 'I'/'N' looking for beginning
//     of value"). `--json-schema` fixes the root cause rather than the
//     symptom: re-running the identical reproduction with `--json-schema`
//     set to this backend's own list-reply shape made the CLI validate the
//     model's final answer against that schema and forced a
//     schema-conformant `{"items":[]}` even though the underlying tool call
//     was still impossible — env.Result is then always valid JSON matching
//     the shape decodeShowReply/decodeListReply expect, so a conversational
//     decline can never again reach either decoder as raw prose.
package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Runner execs the resolved claude CLI binary with a prompt delivered over
// stdin. Production code uses CLIRunner; tests inject a fake runner that
// returns canned output without spawning a process — mirrors
// cmd/pg-connector-issue-jira/internal.Runner's identical seam, adapted to
// a single prompt-string argument (over stdin) rather than an argv vector
// (pjira is a plain CLI with subcommands; claude -p's own input is its
// prompt, not a subcommand vector).
type Runner interface {
	// Run invokes the resolved binary with claudeArgs(jsonSchema) (see
	// claudeArgs below) and prompt on stdin, returning stdout. jsonSchema
	// is the caller's own JSON Schema string for its expected reply shape
	// (showReplySchema/listReplySchema in backend.go) — passed through to
	// `--json-schema` so the CLI itself validates/forces the model's final
	// answer to conform, rather than relying on prompt wording alone (see
	// this file's own package doc comment for why). On failure, the
	// returned error wraps the underlying exec error and includes a
	// trimmed stderr tail so callers can classify it.
	Run(ctx context.Context, prompt string, jsonSchema string) (stdout string, err error)

	// Binary reports the resolved CLI binary name Run would exec next,
	// without invoking it — mirrors issue-jira's Runner.Binary() precedent.
	Binary() string
}

// EnvBinary is the env var this backend checks to override the resolved
// claude CLI binary name/path — a dedicated, backend-scoped override in
// the same env-var-driven-config style as
// cmd/pg-connector-issue-jira/internal.EnvBinary, and exactly the
// "resolved via $PATH or an env-var override" seam this bead's own
// Contract names for the conformance suite's ExecBackend run against a
// fake claude executable.
const EnvBinary = "PG_CONNECTOR_THREAD_SLACK_BINARY"

// defaultBinary is "claude" — the real Claude Code CLI already installed
// and configured (including its Slack MCP server) on this machine, per
// this bead's own Objective/Files sections.
const defaultBinary = "claude"

// defaultAllowedTools is the Slack MCP's own server-level allow-list token
// (see this file's package doc comment for why the bare `mcp__slack` form
// is used rather than enumerating individual tool names).
const defaultAllowedTools = "mcp__slack"

// defaultMaxTurns is the `--max-turns` value this backend passes (see this
// file's package doc comment for the reasoning).
const defaultMaxTurns = "6"

// ResolveBinary resolves the claude CLI binary name Run execs, using
// getenv (production passes os.Getenv; tests inject a fixed lookup so
// resolution never depends on this process's real environment).
func ResolveBinary(getenv func(string) string) string {
	if bin := strings.TrimSpace(getenv(EnvBinary)); bin != "" {
		return bin
	}
	return defaultBinary
}

// claudeArgs returns the `claude -p ...` argument vector this backend
// passes, ahead of the prompt (delivered separately, over stdin) — see
// this file's package doc comment for each flag's reasoning. jsonSchema is
// this call's own expected-reply JSON Schema (showReplySchema/
// listReplySchema in backend.go); when non-empty, `--json-schema
// <jsonSchema>` is appended so the CLI validates/forces the model's final
// answer against it. A caller with no schema of its own (there is none
// today, but the empty case is kept cheap and explicit) passes "" and gets
// the fixed vector unchanged.
func claudeArgs(jsonSchema string) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--max-turns", defaultMaxTurns,
		"--allowed-tools", defaultAllowedTools,
	}
	if jsonSchema != "" {
		args = append(args, "--json-schema", jsonSchema)
	}
	return args
}

// CLIRunner is the default Runner. It execs the resolved claude CLI binary
// from PATH (or BinaryOverride/EnvBinary), writing prompt to its stdin.
type CLIRunner struct {
	// BinaryOverride pins the binary name/path directly, bypassing env
	// resolution entirely. Optional — tests set this to a disposable fake
	// binary; when empty (the production default via NewCLIRunner), Run
	// resolves it itself via ResolveBinary on every call.
	BinaryOverride string
	// Env overrides the exec'd process's env block. Nil means the process
	// env — where a real claude binary reads its own ambient
	// configuration (including its MCP server registrations) from; tests
	// use this to avoid leaking real configuration into a fake-binary
	// invocation.
	Env []string
	// Getenv resolves EnvBinary when BinaryOverride is unset. Optional —
	// nil means os.Getenv.
	Getenv func(string) string
}

// NewCLIRunner returns a CLIRunner using the process env.
func NewCLIRunner() *CLIRunner { return &CLIRunner{} }

func (r *CLIRunner) resolveBinary() string {
	if r.BinaryOverride != "" {
		return r.BinaryOverride
	}
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return ResolveBinary(getenv)
}

// Binary implements Runner.Binary.
func (r *CLIRunner) Binary() string { return r.resolveBinary() }

// command builds the *exec.Cmd Run below actually executes, split out
// (mirroring the pjira/bd/gh backends' own Command/command choke points)
// so a test can assert on WaitDelay/Env/Args directly without spawning a
// real process. jsonSchema is forwarded to claudeArgs (see its own doc
// comment).
func (r *CLIRunner) command(ctx context.Context, prompt string, jsonSchema string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.resolveBinary(), claudeArgs(jsonSchema)...)
	if r.Env != nil {
		cmd.Env = r.Env
	}
	cmd.Stdin = strings.NewReader(prompt)
	// See scriptout.DefaultWaitDelay's doc comment for why this is needed
	// even though ctx already carries a deadline: it bounds Cmd.Wait's own
	// residual wait for the stdout/stderr pipes to close, independent of
	// killing the direct child — the same hang guard
	// cmd/pg-connector-issue-jira/internal/runner.go applies to `pjira`.
	cmd.WaitDelay = scriptout.DefaultWaitDelay
	return cmd
}

// Run execs the resolved binary with claudeArgs(jsonSchema) and prompt on
// stdin, returning stdout. On failure it wraps the underlying exec error
// with a trimmed, capped stderr tail so callers can classify it without an
// unbounded error string.
func (r *CLIRunner) Run(ctx context.Context, prompt string, jsonSchema string) (string, error) {
	bin := r.resolveBinary()
	cmd := r.command(ctx, prompt, jsonSchema)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("%s %s: %w: %s",
				bin, strings.Join(claudeArgs(jsonSchema), " "), err, scriptout.TruncateForFold(stderr.Bytes()))
		}
		return stdout.String(), fmt.Errorf("%s %s: %w (is %s on PATH?)",
			bin, strings.Join(claudeArgs(jsonSchema), " "), err, bin)
	}
	return stdout.String(), nil
}
