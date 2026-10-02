// Package claudehandler is pg-rescue-claude: a failure handler that runs
// `claude -p` synchronously in the failed command's working directory, with a
// prompt rendered from the failure report, and returns the agent's verdict
// under the handler contract.
//
// It adds no policy: no tool denylist, no nesting guard, no rules in the
// prompt. Everything the agent may do comes from the instance's arguments and
// the agent's own rules and skills.
package claudehandler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/orphan"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
)

// Defaults of the documented arguments.
const (
	defaultModel          = "sonnet"
	defaultTimeLimit      = 5 * time.Minute
	defaultPermissionMode = "acceptEdits"
	defaultTailLines      = 200
)

// exitFailed is the handler's generic failure code. It is outside the outcome
// table, so the wrapper records the attempt as failed.
const exitFailed = 1

// maxEnvelopeBytes caps what is read from the CLI's stdout. A result object is
// small; this only stops a runaway process from filling memory.
const maxEnvelopeBytes = 16 << 20

// defaultKillGrace is how long the CLI gets between SIGTERM and SIGKILL.
const defaultKillGrace = 5 * time.Second

// claudeChildMarkers are the variables Claude Code uses to flag a nested
// session. A caller that is itself an agent exports them, and a child that
// inherits them does not persist its transcript. They are blanked (an empty
// value is read as "not a child") for the claude this handler launches. This
// is the same set ccpool blanks when it launches a session; it is plumbing and
// changes nothing about what the agent may do.
var claudeChildMarkers = []string{
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDECODE",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_EXECPATH",
}

// Runtime is everything Main takes from the outside world.
type Runtime struct {
	// Environ is the process environment.
	Environ []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// Template supplies the template data's outside dependencies.
	Template tmpldata.Env
	// KillGrace is the SIGTERM to SIGKILL grace for the claude process group.
	KillGrace time.Duration
	// Watch starts the orphan watch; cleanup runs once if the wrapper is gone.
	Watch func(cleanup func()) (stop func())
}

// DefaultRuntime is backed by the real process.
func DefaultRuntime() Runtime {
	return Runtime{
		Environ:   os.Environ(),
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Template:  tmpldata.DefaultEnv(),
		KillGrace: defaultKillGrace,
		Watch:     orphan.Watch,
	}
}

// Usage is the synopsis printed for --help.
const Usage = `usage: pg-rescue-claude [--model M] [--time-limit D] [--permission-mode M]
                        [--allowed-tools LIST] [--disallowed-tools LIST]
                        [--mcp-config F]... [--strict-mcp-config] [--claude-arg ARG]...
                        [--prompt-template TEXT | --prompt-template-file F]
                        [--append-instructions TEXT | --append-instructions-file F]
                        [--output-tail-lines N]
       pg-rescue-claude --print-default-template | --print-template-vars
`

type options struct {
	model, permissionMode    string
	timeLimit                time.Duration
	allowedTools, disallowed string
	mcpConfigs, claudeArgs   []string
	strictMCP                bool
	promptTemplate           string
	appendInstructions       string
	tailLines                int
	printTemplate, printVars bool
}

// parseArgs parses the handler's arguments. help is true when --help was asked.
func parseArgs(args []string) (o options, help bool, err error) {
	var (
		timeLimit                        string
		tmpl, tmplFile, appendT, appendF string
	)
	fs := pflag.NewFlagSet("pg-rescue-claude", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.model, "model", defaultModel, "passed through as claude --model")
	fs.StringVar(&timeLimit, "time-limit", defaultTimeLimit.String(), "wall-clock limit for the agent")
	fs.StringVar(&o.permissionMode, "permission-mode", defaultPermissionMode, "passed through as claude --permission-mode")
	fs.StringVar(&o.allowedTools, "allowed-tools", "", "passed through as claude --allowed-tools")
	fs.StringVar(&o.disallowed, "disallowed-tools", "", "passed through as claude --disallowed-tools")
	fs.StringArrayVar(&o.mcpConfigs, "mcp-config", nil, "passed through as claude --mcp-config (repeatable)")
	fs.BoolVar(&o.strictMCP, "strict-mcp-config", false, "passed through as claude --strict-mcp-config")
	fs.StringArrayVar(&o.claudeArgs, "claude-arg", nil, "extra claude argument, passed through verbatim (repeatable)")
	fs.StringVar(&tmpl, "prompt-template", "", "replaces the whole prompt")
	fs.StringVar(&tmplFile, "prompt-template-file", "", "like --prompt-template, read from a file")
	fs.StringVar(&appendT, "append-instructions", "", "text appended to the prompt")
	fs.StringVar(&appendF, "append-instructions-file", "", "like --append-instructions, read from a file")
	fs.IntVar(&o.tailLines, "output-tail-lines", defaultTailLines, "lines of output tail put in the prompt")
	fs.BoolVar(&o.printTemplate, "print-default-template", false, "print the built-in prompt template and exit")
	fs.BoolVar(&o.printVars, "print-template-vars", false, "print the template data model and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return o, true, nil
		}
		return o, false, err
	}
	if fs.NArg() > 0 {
		return o, false, fmt.Errorf("unexpected argument %q (the prompt is rendered from the report, not given as an argument)", fs.Arg(0))
	}
	if o.printTemplate || o.printVars {
		return o, false, nil
	}

	d, err := time.ParseDuration(timeLimit)
	if err != nil || d <= 0 {
		return o, false, fmt.Errorf("--time-limit %q: want a positive duration such as 5m or 200ms", timeLimit)
	}
	o.timeLimit = d
	if o.tailLines < 0 {
		return o, false, fmt.Errorf("--output-tail-lines %d: must not be negative", o.tailLines)
	}
	if o.model == "" {
		return o, false, errors.New("--model: the model name is empty")
	}
	if o.promptTemplate, err = textOrFile(fs, "prompt-template", tmpl, "prompt-template-file", tmplFile); err != nil {
		return o, false, err
	}
	if o.appendInstructions, err = textOrFile(fs, "append-instructions", appendT, "append-instructions-file", appendF); err != nil {
		return o, false, err
	}
	return o, false, nil
}

// textOrFile resolves an inline-or-file flag pair. The result is "" when
// neither was given. Giving both is an error.
func textOrFile(fs *pflag.FlagSet, textFlag, text, fileFlag, file string) (string, error) {
	hasText, hasFile := fs.Changed(textFlag), fs.Changed(fileFlag)
	switch {
	case hasText && hasFile:
		return "", fmt.Errorf("--%s and --%s are mutually exclusive", textFlag, fileFlag)
	case hasFile:
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("--%s: %w", fileFlag, err)
		}
		return string(b), nil
	default:
		return text, nil
	}
}

// Main runs the handler and returns its exit code.
func Main(args []string, rt Runtime) int {
	o, help, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(rt.Stderr, "pg-rescue-claude: %v\n%s", err, Usage)
		return exitFailed
	}
	if help {
		fmt.Fprint(rt.Stdout, Usage)
		return 0
	}
	if o.printTemplate {
		if err := tmpldata.PrintDefaultTemplates(rt.Stdout, []tmpldata.Template{{Name: "prompt", Text: DefaultPromptTemplate}}); err != nil {
			fmt.Fprintf(rt.Stderr, "pg-rescue-claude: %v\n", err)
			return exitFailed
		}
		return 0
	}
	if o.printVars {
		if err := tmpldata.PrintVars(rt.Stdout); err != nil {
			fmt.Fprintf(rt.Stderr, "pg-rescue-claude: %v\n", err)
			return exitFailed
		}
		return 0
	}

	h := &handler{o: o, rt: rt}
	stop := rt.Watch(h.killChild)
	defer stop()

	prompt, err := h.prompt()
	if err != nil {
		fmt.Fprintf(rt.Stderr, "pg-rescue-claude: %v\n", err)
		return exitFailed
	}
	return h.run(prompt)
}

type handler struct {
	o  options
	rt Runtime

	mu   sync.Mutex
	pgid int // the claude process group; 0 when none is running
}

// prompt renders the prompt from the report the wrapper wrote.
func (h *handler) prompt() (string, error) {
	path := lookupEnv(h.rt.Environ, "PG_RESCUE_REPORT")
	if path == "" {
		return "", errors.New("PG_RESCUE_REPORT is not set; this program is a pg-rescue handler")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the report: %w", err)
	}
	var r report.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("cannot parse the report %s: %w", path, err)
	}
	if r.SchemaVersion != report.SchemaVersion {
		return "", fmt.Errorf("unsupported report schema_version %d (this handler reads %d)", r.SchemaVersion, report.SchemaVersion)
	}
	data, err := tmpldata.New(&r, h.rt.Template)
	if err != nil {
		return "", err
	}
	data.OutputTail = lastLines(data.OutputTail, h.o.tailLines)

	text := h.o.promptTemplate
	name := "prompt-template"
	if text == "" {
		text, name = DefaultPromptTemplate, "prompt"
	}
	out, err := tmpldata.Render(name, text, data)
	if err != nil {
		return "", err
	}
	if h.o.appendInstructions != "" {
		out = strings.TrimRight(out, "\n") + "\n\n" + h.o.appendInstructions
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
	}
	return out, nil
}

// claudeArgv is the argument vector of the claude invocation. The prompt is
// NOT in it: it goes on stdin, because --mcp-config, --allowed-tools and
// --disallowed-tools take a variable number of values and would swallow a
// trailing positional prompt.
func (h *handler) claudeArgv() []string {
	a := []string{"-p", "--output-format", "json", "--model", h.o.model, "--permission-mode", h.o.permissionMode, "--json-schema", resultSchema}
	if h.o.allowedTools != "" {
		a = append(a, "--allowed-tools", h.o.allowedTools)
	}
	if h.o.disallowed != "" {
		a = append(a, "--disallowed-tools", h.o.disallowed)
	}
	for _, f := range h.o.mcpConfigs {
		a = append(a, "--mcp-config", f)
	}
	if h.o.strictMCP {
		a = append(a, "--strict-mcp-config")
	}
	return append(a, h.o.claudeArgs...)
}

// childEnv is environ with the nested-session markers blanked.
func childEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+len(claudeChildMarkers))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !isMarker(k) {
			out = append(out, kv)
		}
	}
	for _, k := range claudeChildMarkers {
		out = append(out, k+"=")
	}
	return out
}

func isMarker(k string) bool {
	for _, m := range claudeChildMarkers {
		if k == m {
			return true
		}
	}
	return false
}

func lookupEnv(environ []string, key string) string {
	val := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

// errTimeLimit is the cancellation cause of the wall-clock limit.
var errTimeLimit = errors.New("time limit exceeded")

// signalCause is the cancellation cause of a termination signal.
type signalCause struct{ sig syscall.Signal }

func (c signalCause) Error() string { return "interrupted by " + c.sig.String() }

// run launches claude with prompt on stdin, waits for it within the time limit
// and turns its answer into the handler's result.
func (h *handler) run(prompt string) int {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	ctx, cancelT := context.WithTimeoutCause(ctx, h.o.timeLimit, errTimeLimit)
	defer cancelT()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		select {
		case s := <-sigs:
			sig, _ := s.(syscall.Signal)
			cancel(signalCause{sig})
		case <-ctx.Done():
		}
	}()

	stdout := &contract.LimitedBuffer{Max: maxEnvelopeBytes}
	cmd := exec.CommandContext(ctx, "claude", h.claudeArgv()...)
	cmd.Env = childEnv(h.rt.Environ)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = stdout
	cmd.Stderr = h.rt.Stderr
	// Its own process group, so the whole tree (MCP servers, tool children)
	// can be signalled at once and never outlives this handler.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return h.signalChild(syscall.SIGTERM) }
	cmd.WaitDelay = h.rt.KillGrace

	h.mu.Lock()
	err := cmd.Start()
	if err == nil {
		h.pgid = cmd.Process.Pid
	}
	h.mu.Unlock()
	if err != nil {
		fmt.Fprintf(h.rt.Stderr, "pg-rescue-claude: cannot run claude: %v\n", err)
		return h.fail(nil, kindSpawn, fmt.Sprintf("cannot run claude: %v", err))
	}

	waitErr := cmd.Wait()
	// Whatever claude left behind goes with it.
	h.reapChild()

	if cause := context.Cause(ctx); cause != nil && waitErr != nil {
		var sc signalCause
		switch {
		case errors.Is(cause, errTimeLimit):
			fmt.Fprintf(h.rt.Stderr, "pg-rescue-claude: the agent exceeded the %s time limit and was stopped\n", h.o.timeLimit)
			return h.fail(nil, kindTimeLimit, fmt.Sprintf("agent exceeded the %s time limit", h.o.timeLimit))
		case errors.As(cause, &sc):
			return 128 + int(sc.sig)
		}
	}
	return h.finish(stdout.Bytes(), waitErr)
}

// finish turns the CLI's exit and output into the handler's result.
func (h *handler) finish(stdout []byte, waitErr error) int {
	env, envErr := parseEnvelope(stdout)
	if envErr != nil {
		fmt.Fprintf(h.rt.Stderr, "pg-rescue-claude: claude printed no result object (%v); its output follows\n%s\n", envErr, stdout)
		reason := "claude printed no result object"
		if waitErr != nil {
			reason = fmt.Sprintf("claude failed (%v) and printed no result object", waitErr)
		}
		return h.fail(nil, kindBadOutput, reason)
	}
	if waitErr != nil || env.IsError {
		why := env.Result
		if why == "" {
			why = env.Subtype
		}
		fmt.Fprintf(h.rt.Stderr, "pg-rescue-claude: claude reported an error (%v): %s\n", waitErr, why)
		return h.fail(env, kindCLIError, "claude reported an error: "+oneLine(why))
	}

	msg := env.finalMessage()
	v, err := interpret(msg)
	if err != nil {
		fmt.Fprintf(h.rt.Stderr, "pg-rescue-claude: the agent's final message is not a result object: %v\n%s\n", err, msg)
		return h.fail(env, kindUnparsable, "the agent's final message is not a result object: "+err.Error())
	}
	code, _ := v.Outcome.ExitCode()
	return h.emit(output{Outcome: v.Outcome, Summary: v.Summary, Details: v.Details, Meta: env.meta()}, code)
}

// fail emits a result with no outcome (the exit code carries the verdict:
// failed) and the summary reason, and returns exit 1. meta still carries
// whatever the CLI reported.
func (h *handler) fail(env *envelope, kind, summary string) int {
	m := env.meta()
	m["error_kind"] = kind
	return h.emit(output{Summary: summary, Meta: m}, exitFailed)
}

// output is the result JSON printed on stdout. Outcome is omitted when the
// handler exits 1: that exit code is outside the outcome table, and a present
// outcome there would be read as a disagreement.
type output struct {
	Outcome contract.Outcome `json:"outcome,omitempty"`
	Summary string           `json:"summary,omitempty"`
	Details string           `json:"details,omitempty"`
	Meta    map[string]any   `json:"meta,omitempty"`
}

func (h *handler) emit(o output, code int) int {
	b, err := json.Marshal(o)
	if err != nil {
		fmt.Fprintf(h.rt.Stderr, "pg-rescue-claude: cannot encode the result: %v\n", err)
		return exitFailed
	}
	if _, err := h.rt.Stdout.Write(append(b, '\n')); err != nil {
		return exitFailed
	}
	return code
}

// signalChild sends sig to the claude process group, if one is running.
func (h *handler) signalChild(sig syscall.Signal) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pgid == 0 {
		return os.ErrProcessDone
	}
	return syscall.Kill(-h.pgid, sig)
}

// killChild force-kills the claude process group. It is safe to call at any
// time and more than once; it is also the orphan watch's cleanup.
func (h *handler) killChild() { _ = h.signalChild(syscall.SIGKILL) }

// reapChild force-kills what is left of the claude process group after claude
// itself has been waited for, then forgets the group.
func (h *handler) reapChild() {
	h.killChild()
	h.mu.Lock()
	h.pgid = 0
	h.mu.Unlock()
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + "…"
	}
	return s
}
