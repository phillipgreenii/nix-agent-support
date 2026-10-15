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
// choices"] — this backend's own choices. The first version of them assumed
// the machine's ambient `claude` configuration already declared a Slack MCP
// server. That was never true from the working directory a launchd-run poll
// inherits: the model saw no mcp__ tool at all and every poll still logged
// "list ok" with an empty result (bead pg2-s417i). Bead pg2-apllc therefore
// makes the working directory, the model and the MCP server declaration this
// backend's own config, and narrows the allow-list to reads.
//
// Options. The backend's own `backends.<name>.claude` config block reaches
// this runner as the Config of the request (the umbrella copies the block
// into every show and list request; scriptout.ConfigFromContext hands it
// over), never through a new env var or a wider Runner interface. Every key
// is optional; a blank value counts as unset:
//
//   - `cwd`: the directory the claude child runs in (cmd.Dir). Unset leaves
//     it inherited. claude finds project settings and a project's .mcp.json
//     from its working directory, so where it runs decides what it loads.
//   - `model`: passed as `--model <model>`. Unset passes no `--model`, so
//     claude's own default model applies.
//   - `mcp_config`: an inline JSON string or the path of a JSON file,
//     passed as `--mcp-config <value>`. When set, the child also gets
//     `--strict-mcp-config` (use ONLY this declaration, ignore every other
//     MCP source), `--setting-sources user` and `--disable-slash-commands`,
//     so it neither loads a project's hooks, plugins and skills (that
//     startup work dominated the wall clock of a measured search) nor
//     depends on a project's MCP approvals. The declaration MUST name the
//     Slack server `slack`: the allow-list below spells mcp__slack__...
//     tool names. Unset passes none of these four flags and claude
//     resolves MCP servers from its own ambient configuration (what this
//     backend did before bead pg2-apllc). `--bare` is never passed: it
//     ignores OAuth and keychain reads, which the Slack credential needs.
//   - `max_turns`: the `--max-turns` value, an integer from 1 to 30 (see
//     the `--max-turns` note below). Unset or 0 uses the default of 6; a
//     negative value or one above 30 is an invalid_argument error rather than
//     a silent clamp, so a typo cannot lift the cap on a runaway agent run.
//
// Fixed flags:
//
//   - `-p` (print/non-interactive mode) with `--output-format json`: the
//     envelope carries the model's final text in its own "result" field
//     plus cost/usage accounting, mirroring pg-ccaudit's own DefaultCommand
//     doc comment ("what makes the cost REPORTABLE").
//   - `--max-turns <n>`, 6 unless `max_turns` is configured: pg-ccaudit's own "--max-turns 1" pins a pure-text
//     classification with no tool use at all; this backend's prompt asks
//     the model to actually call a Slack MCP tool and then reply with JSON,
//     which needs at least one tool-call round trip (a turn to invoke the
//     tool, a turn carrying the tool's result, a turn producing the final
//     reply) — 6 leaves margin for the MCP tool needing more than one call
//     (e.g. a paginated search) without letting a misbehaving run become an
//     open-ended agent session. 6 is enough for a single show lookup but NOT
//     for the full list prompt, whose measured runs needed 6, 12 and 13 turns
//     (bead pg2-apllc), so a deployment that polls with list configures a
//     higher `max_turns`; the cap of 30 keeps a misbehaving run bounded.
//   - `--allowed-tools <list>`: ONE argv element holding exactly the five
//     read tools (readOnlyTools), comma-separated (claude accepts comma- or
//     space-separated; the flag is variadic, which is harmless here because
//     the prompt travels on stdin and no operand follows it). This backend's
//     contract is read-only, so the send, schedule, draft and canvas tools
//     are never pre-approved — the earlier server-level `mcp__slack` token
//     approved every tool under the server, writes included — and every
//     OTHER tool (Bash, Edit, ...) stays unapproved and unusable even if the
//     model attempted one: deliberately narrower than `--permission-mode
//     bypassPermissions` (ccpool's own worker mode), which would approve
//     everything.
//   - `--json-schema <schema>`, appended per-call (see claudeArgs below)
//     rather than baked into the fixed vector above: bead pg2-vkj77's own
//     reproduction (running this exact invocation live) proved the prior
//     no-schema shape's real failure mode — with no Slack MCP server
//     actually reachable from the child (contrary to the ambient-
//     configuration assumption this backend used to make), the model
//     correctly and truthfully declines in PROSE ("No Slack MCP tools are
//     available in this session ...", "I checked ... and there is no Slack
//     server ...") rather than emitting the JSON the prompt asked for —
//     decodeClaudeEnvelope still succeeds (the CLI's own `--output-format
//     json` outer envelope is always well-formed), but the inner env.Result
//     string is that prose, so decodeShowReply/decodeListReply then fail
//     with exactly the production symptom ("invalid character 'I'/'N'
//     looking for beginning of value"). `--json-schema` fixes the root
//     cause rather than the symptom: re-running the identical reproduction
//     with `--json-schema` set to this backend's own list-reply shape made
//     the CLI validate the model's final answer against that schema and
//     forced a schema-conformant `{"items":[]}` even though the underlying
//     tool call was still impossible — env.Result is then always valid JSON
//     matching the shape decodeShowReply/decodeListReply expect, so a
//     conversational decline can never again reach either decoder as raw
//     prose.
package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
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
	// Run invokes the resolved binary with claudeArgs (see claudeArgs
	// below) and prompt on stdin, returning stdout. jsonSchema is the
	// caller's own JSON Schema string for its expected reply shape
	// (showReplySchema/listReplySchema in backend.go) — passed through to
	// `--json-schema` so the CLI itself validates/forces the model's final
	// answer to conform, rather than relying on prompt wording alone (see
	// this file's own package doc comment for why). The call's own options
	// (working directory, model, MCP declaration) come from the request
	// Config carried on ctx (claudeOptionsFromContext), so this signature
	// stays unchanged. On failure, the returned error wraps the underlying
	// exec error and includes a trimmed stderr tail so callers can
	// classify it.
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

// defaultBinary is "claude" — the real Claude Code CLI already installed on
// this machine, resolved from PATH. Which MCP server it talks to is this
// backend's own `claude` config (see the package doc comment), not whatever
// the CLI happens to have configured.
const defaultBinary = "claude"

// readOnlyTools are the Slack MCP tools this backend may call, exactly: the
// server is named `slack` (see the package doc comment), so each is
// mcp__slack__<tool>. All five only read; none sends, schedules, drafts or
// edits anything.
var readOnlyTools = []string{
	"mcp__slack__slack_search_public",
	"mcp__slack__slack_search_channels",
	"mcp__slack__slack_read_thread",
	"mcp__slack__slack_read_channel",
	"mcp__slack__slack_read_user_profile",
}

// allowedToolsArg is the one `--allowed-tools` argv element: readOnlyTools,
// comma-separated.
var allowedToolsArg = strings.Join(readOnlyTools, ",")

// defaultMaxTurns is the `--max-turns` value this backend passes when no
// `max_turns` is configured (see this file's package doc comment for the
// reasoning), and maxMaxTurns is the largest `max_turns` it accepts.
const (
	defaultMaxTurns = 6
	maxMaxTurns     = 30
)

// claudeOptions are the per-call options a `claude` config block carries
// (see the package doc comment for what each does). The zero value is the
// all-unset default.
type claudeOptions struct {
	Cwd       string `json:"cwd"`
	Model     string `json:"model"`
	MCPConfig string `json:"mcp_config"`
	// MaxTurns is the `--max-turns` value; 0 means unset (defaultMaxTurns).
	// claudeOptionsFromContext rejects a negative value or one above
	// maxMaxTurns.
	MaxTurns int `json:"max_turns"`
}

// claudeOptionsFromContext reads the `claude` member of the request Config
// threaded onto ctx (scriptout.ConfigFromContext). A request with no Config,
// or none carrying a `claude` member, yields the zero options. A `claude`
// member of the wrong shape is an invalid_argument error rather than a silent
// fallback: running without the configured MCP declaration would reproduce
// the empty-result failure this config exists to prevent.
func claudeOptionsFromContext(ctx context.Context) (claudeOptions, error) {
	var cfg struct {
		Claude claudeOptions `json:"claude"`
	}
	if err := scriptout.Decode(scriptout.ConfigFromContext(ctx), &cfg); err != nil {
		return claudeOptions{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
			"pg-connector-thread-slack: decode config.claude: "+err.Error())
	}
	if t := cfg.Claude.MaxTurns; t < 0 || t > maxMaxTurns {
		return claudeOptions{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("pg-connector-thread-slack: config.claude.max_turns %d out of range (0 for the default of %d, or 1 to %d)",
				t, defaultMaxTurns, maxMaxTurns))
	}
	return claudeOptions{
		Cwd:       strings.TrimSpace(cfg.Claude.Cwd),
		Model:     strings.TrimSpace(cfg.Claude.Model),
		MCPConfig: strings.TrimSpace(cfg.Claude.MCPConfig),
		MaxTurns:  cfg.Claude.MaxTurns,
	}, nil
}

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
// this file's package doc comment for each flag's reasoning. opts carries
// the call's configured model, MCP declaration and turn limit: `--model` is added only
// when a model is configured, and `--mcp-config <value>
// --strict-mcp-config --setting-sources user --disable-slash-commands` only
// when an MCP declaration is. jsonSchema is this call's own expected-reply
// JSON Schema (showReplySchema/listReplySchema in backend.go); when
// non-empty, `--json-schema <jsonSchema>` is appended LAST so the CLI
// validates/forces the model's final answer against it. A caller with no
// schema of its own (there is none today, but the empty case is kept cheap
// and explicit) passes "" and gets the vector without it.
func claudeArgs(jsonSchema string, opts claudeOptions) []string {
	maxTurns := defaultMaxTurns
	if opts.MaxTurns > 0 {
		maxTurns = opts.MaxTurns
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--max-turns", strconv.Itoa(maxTurns),
		"--allowed-tools", allowedToolsArg,
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.MCPConfig != "" {
		args = append(
			args,
			"--mcp-config", opts.MCPConfig,
			"--strict-mcp-config",
			"--setting-sources", "user",
			"--disable-slash-commands",
		)
	}
	if jsonSchema != "" {
		args = append(args, "--json-schema", jsonSchema)
	}
	return args
}

// describeArgs renders args for an error message with the --mcp-config value
// elided: an inline declaration may carry a header or token of its own, and
// error text reaches the event log and alerts.
func describeArgs(args []string) string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--mcp-config" {
			out[i+1] = "<mcp-config>"
		}
	}
	return strings.Join(out, " ")
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
	// configuration (its settings, and its MCP server registrations when
	// no `mcp_config` is configured) from; tests use this to avoid
	// leaking real configuration into a fake-binary invocation.
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
// so a test can assert on WaitDelay/Env/Args/Dir directly without spawning a
// real process. The call's options are read from the request Config on ctx
// (claudeOptionsFromContext) and forwarded to claudeArgs along with
// jsonSchema; cmd.Dir is the configured `cwd`, left empty (inherited) when
// none is. A malformed `claude` config block is the only error.
func (r *CLIRunner) command(ctx context.Context, prompt string, jsonSchema string) (*exec.Cmd, error) {
	opts, err := claudeOptionsFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, r.resolveBinary(), claudeArgs(jsonSchema, opts)...)
	cmd.Dir = opts.Cwd
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
	return cmd, nil
}

// Run execs the resolved binary with claudeArgs and prompt on stdin,
// returning stdout. On failure it wraps the underlying exec error with a
// trimmed, capped stderr tail so callers can classify it without an
// unbounded error string.
func (r *CLIRunner) Run(ctx context.Context, prompt string, jsonSchema string) (string, error) {
	bin := r.resolveBinary()
	cmd, err := r.command(ctx, prompt, jsonSchema)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		argv := describeArgs(cmd.Args[1:])
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("%s %s: %w: %s",
				bin, argv, err, scriptout.TruncateForFold(stderr.Bytes()))
		}
		// A configured cwd that does not exist (an unmounted volume, a moved
		// checkout) fails before claude starts; naming the directory beats
		// the PATH hint below, which would send the operator the wrong way.
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) && pathErr.Op == "chdir" {
			return stdout.String(), fmt.Errorf("%s %s: working directory %q (config claude.cwd): %w",
				bin, argv, cmd.Dir, err)
		}
		return stdout.String(), fmt.Errorf("%s %s: %w (is %s on PATH?)",
			bin, argv, err, bin)
	}
	return stdout.String(), nil
}
