// Package doctor implements the environment checks of `pg-review job doctor`:
// one PASS, FAIL or SKIP line per check. Every external effect (commands, the
// ledger, ccpool, the clock) is injected so tests run the checks with fakes.
package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
)

// Check names, in the order they run and print.
const (
	CheckConfig    = "config"
	CheckLedger    = "ledger"
	CheckProviders = "providers"
	CheckRecorder  = "recorder"
	CheckCcpool    = "ccpool"
	CheckHook      = "hook"
	CheckSession   = "session"
)

// Status is the outcome of one check.
type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
)

// Result is one check's outcome. Code is the exit code a failure maps to.
type Result struct {
	Name   string
	Status Status
	Reason string
	Code   int
}

// String renders the doctor line.
func (r Result) String() string {
	if r.Status == Fail {
		return fmt.Sprintf("FAIL %s: %s", r.Name, r.Reason)
	}
	return fmt.Sprintf("%s %s", r.Status, r.Name)
}

// Exec runs one command: exit is the process exit code, err is non-nil only
// when the command could not be run at all (not found, timed out).
type Exec func(ctx context.Context, name string, args []string, stdin string, env []string, dir string) (stdout string, exit int, err error)

// IntegrityLedger is the part of the ledger the doctor needs.
type IntegrityLedger interface {
	IntegrityCheck(ctx context.Context) error
	Close() error
}

// Deps is everything the checks touch.
type Deps struct {
	// Config is the effective configuration, or nil with ConfigErr set when
	// it could not be loaded.
	Config    *config.Effective
	ConfigErr error

	LookPath     func(name string) (string, error)
	Exec         Exec
	OpenLedger   func(ctx context.Context) (IntegrityLedger, error)
	Ccpool       ccpool.Client
	Now          func() time.Time
	Sleep        func(ctx context.Context, d time.Duration)
	SkipSession  bool
	ProbeTimeout time.Duration // per provider/recorder/hook command; default 10s
	// SessionTimeout bounds the in-session check; default 3 minutes.
	SessionTimeout time.Duration
	PollInterval   time.Duration // default 3s
	// Approver is the approval hook binary; default claude-extended-tool-approver.
	Approver string
	// RecorderCmd is the recorder index tool; default pg-ccaudit.
	RecorderCmd string
}

// HookSample is the command the hook check submits to the approver.
const HookSample = "pg-review agent q ccaudit error-rate-by-tool"

func (d *Deps) probeTimeout() time.Duration {
	if d.ProbeTimeout > 0 {
		return d.ProbeTimeout
	}
	return 10 * time.Second
}

func fail(name string, code int, format string, a ...any) Result {
	return Result{Name: name, Status: Fail, Code: code, Reason: fmt.Sprintf(format, a...)}
}

func pass(name string) Result { return Result{Name: name, Status: Pass} }

// Run executes every check and returns the results in order.
func Run(ctx context.Context, d *Deps) []Result {
	return []Result{
		checkConfig(d),
		checkLedger(ctx, d),
		checkProviders(ctx, d),
		checkRecorder(ctx, d),
		checkCcpool(d),
		checkHook(ctx, d),
		checkSession(ctx, d),
	}
}

// ExitCode is 0 when no check failed, else the highest failing code.
func ExitCode(rs []Result) int {
	code := exitcode.OK
	for _, r := range rs {
		if r.Status == Fail && r.Code > code {
			code = r.Code
		}
	}
	return code
}

func checkConfig(d *Deps) Result {
	if d.ConfigErr != nil || d.Config == nil {
		return fail(CheckConfig, exitcode.ValidationRejected, "%v", d.ConfigErr)
	}
	probs := d.Config.Validate()
	if len(probs) == 0 {
		return pass(CheckConfig)
	}
	parts := make([]string, len(probs))
	for i, p := range probs {
		parts[i] = p.String()
	}
	return fail(CheckConfig, exitcode.ValidationRejected, "%d problem(s): %s", len(probs), strings.Join(parts, "; "))
}

func checkLedger(ctx context.Context, d *Deps) Result {
	l, err := d.OpenLedger(ctx)
	if err != nil {
		return fail(CheckLedger, exitcode.Ledger, "open: %v", err)
	}
	defer l.Close()
	if err := l.IntegrityCheck(ctx); err != nil {
		return fail(CheckLedger, exitcode.Ledger, "%v", err)
	}
	return pass(CheckLedger)
}

// run runs a command under the probe timeout.
func (d *Deps) run(ctx context.Context, name string, args []string, stdin string, env []string, dir string) (string, int, error) {
	cctx, cancel := context.WithTimeout(ctx, d.probeTimeout())
	defer cancel()
	return d.Exec(cctx, name, args, stdin, env, dir)
}

func checkProviders(ctx context.Context, d *Deps) Result {
	if d.Config == nil {
		return fail(CheckProviders, exitcode.ValidationRejected, "no effective configuration")
	}
	var bad []string
	for _, p := range d.Config.Providers {
		if len(p.Command) == 0 {
			bad = append(bad, fmt.Sprintf("%s: no command", p.Name))
			continue
		}
		path, err := d.LookPath(p.Command[0])
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %q not found on PATH", p.Name, p.Command[0]))
			continue
		}
		args := append(append([]string{}, p.Command[1:]...), "--help")
		if _, code, err := d.run(ctx, path, args, "", nil, ""); err != nil {
			bad = append(bad, fmt.Sprintf("%s: --help failed: %v", p.Name, err))
		} else if code != 0 {
			bad = append(bad, fmt.Sprintf("%s: --help exited %d", p.Name, code))
		}
	}
	if len(bad) > 0 {
		return fail(CheckProviders, exitcode.Provider, "%s", strings.Join(bad, "; "))
	}
	return pass(CheckProviders)
}

// checkRecorder asks the index tool for its status (never the coverage query,
// which reads zero-filled columns under feed ingest) and requires at least one
// indexed file.
func checkRecorder(ctx context.Context, d *Deps) Result {
	cmd := d.RecorderCmd
	if cmd == "" {
		cmd = "pg-ccaudit"
	}
	path, err := d.LookPath(cmd)
	if err != nil {
		return fail(CheckRecorder, exitcode.Provider, "%q not found on PATH", cmd)
	}
	out, code, err := d.run(ctx, path, []string{"status", "--json"}, "", nil, "")
	if err != nil {
		return fail(CheckRecorder, exitcode.Provider, "%s status: %v", cmd, err)
	}
	if code != 0 {
		return fail(CheckRecorder, exitcode.Provider, "%s status exited %d", cmd, code)
	}
	var st struct {
		FilesIndexed *int `json:"files_indexed"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.FilesIndexed == nil {
		return fail(CheckRecorder, exitcode.Provider, "%s status: unreadable output", cmd)
	}
	if *st.FilesIndexed <= 0 {
		return fail(CheckRecorder, exitcode.Provider, "the index holds no data (files_indexed is %d)", *st.FilesIndexed)
	}
	return pass(CheckRecorder)
}

func checkCcpool(d *Deps) Result {
	if _, err := d.LookPath("ccpool"); err != nil {
		return fail(CheckCcpool, exitcode.Provider, "ccpool not found on PATH")
	}
	return pass(CheckCcpool)
}

// checkHook feeds the approval hook a PreToolUse event for a sample analyst
// command in a hermetic home, so the decision log of the real machine is not
// touched, and requires an allow.
func checkHook(ctx context.Context, d *Deps) Result {
	bin := d.Approver
	if bin == "" {
		bin = "claude-extended-tool-approver"
	}
	path, err := d.LookPath(bin)
	if err != nil {
		return fail(CheckHook, exitcode.Provider, "%q not found on PATH", bin)
	}
	tmp, err := os.MkdirTemp("", "pg-review-doctor-")
	if err != nil {
		return fail(CheckHook, exitcode.Provider, "%v", err)
	}
	defer os.RemoveAll(tmp)
	ti, _ := json.Marshal(map[string]string{"command": HookSample})
	payload, _ := json.Marshal(map[string]any{
		"session_id": "pg-review-doctor", "transcript_path": filepath.Join(tmp, "t.jsonl"),
		"cwd": tmp, "permission_mode": "dontAsk", "hook_event_name": "PreToolUse",
		"tool_name": "Bash", "tool_input": json.RawMessage(ti), "tool_use_id": "pg-review-doctor",
	})
	env := append(baseEnv(), "HOME="+tmp, "XDG_CONFIG_HOME="+filepath.Join(tmp, "c"),
		"XDG_STATE_HOME="+filepath.Join(tmp, "s"), "XDG_DATA_HOME="+filepath.Join(tmp, "d"),
		"XDG_CACHE_HOME="+filepath.Join(tmp, "k"))
	out, code, err := d.run(ctx, path, nil, string(payload), env, tmp)
	if err != nil {
		return fail(CheckHook, exitcode.Provider, "%s: %v", bin, err)
	}
	if code != 0 {
		return fail(CheckHook, exitcode.Provider, "%s exited %d", bin, code)
	}
	var o struct {
		Hook struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &o)
	if o.Hook.Decision != "allow" {
		got := o.Hook.Decision
		if got == "" {
			got = "no decision"
		}
		return fail(CheckHook, exitcode.Provider, "the approval hook did not approve %q (got %s)", HookSample, got)
	}
	return pass(CheckHook)
}

func baseEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "LANG", "LC_ALL", "TZ", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// checkSession starts a minimal ccpool session the way the runner starts an
// analyst, asks it to print PG_REVIEW_RUN and the path of pg-review (no
// analysis), and requires both to be visible. The session is always closed.
func checkSession(ctx context.Context, d *Deps) (res Result) {
	if d.SkipSession {
		return Result{Name: CheckSession, Status: Skip}
	}
	if d.Config == nil || d.Ccpool == nil {
		return fail(CheckSession, exitcode.ValidationRejected, "no effective configuration or ccpool client")
	}
	cp, err := d.Ccpool.Capacity(ctx)
	if err != nil {
		return fail(CheckSession, exitcode.Provider, "ccpool capacity: %v", err)
	}
	if cp.UsageLimit != nil {
		return fail(CheckSession, exitcode.Provider, "ccpool usage limit reached (%s window); rerun later or skip with --skip-session-check", cp.UsageLimit.Window)
	}
	if cp.Free < 1 {
		return fail(CheckSession, exitcode.Provider, "ccpool has no free slot; rerun later or skip with --skip-session-check")
	}

	token := fmt.Sprintf("doctor-%d", d.Now().UnixNano())
	extID := token
	e := d.Config.Env
	neutral := d.Config.Ccpool.NeutralDir
	if neutral != "" {
		_ = os.MkdirAll(neutral, 0o700)
	}
	var model, effort string
	if len(d.Config.Profiles) > 0 {
		model, effort = d.Config.Profiles[0].Model, d.Config.Profiles[0].Effort
	}
	opts := ccpool.StartOpts{
		ExternalID: extID, Cwd: neutral, Model: model, Effort: effort,
		PermissionMode: "dontAsk",
		AllowedTools:   []string{"Bash(printenv PG_REVIEW_RUN)", "Bash(command -v pg-review)"},
		Env: []string{
			"PG_REVIEW_RUN=" + token,
			"XDG_CONFIG_HOME=" + e.ConfigHome, "XDG_STATE_HOME=" + e.StateHome, "XDG_CACHE_HOME=" + e.CacheHome,
		},
	}
	if err := d.Ccpool.Start(ctx, opts); err != nil {
		return fail(CheckSession, exitcode.Provider, "ccpool new: %v", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Ccpool.Close(cctx, extID, "doctor"); err != nil && res.Status == Pass {
			res = fail(CheckSession, exitcode.Provider, "the session resolved but could not be closed: %v", err)
		}
	}()

	turn, err := d.Ccpool.Reply(ctx, extID, sessionPrompt)
	if err != nil {
		return fail(CheckSession, exitcode.Provider, "ccpool reply: %v", err)
	}
	timeout, poll := d.SessionTimeout, d.PollInterval
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	if poll <= 0 {
		poll = 3 * time.Second
	}
	deadline := d.Now().Add(timeout)
	for {
		text, st, err := d.Ccpool.Result(ctx, turn)
		if err != nil {
			return fail(CheckSession, exitcode.Provider, "ccpool result: %v", err)
		}
		if st == ccpool.ResultDone {
			return judgeSession(text, token)
		}
		if !d.Now().Before(deadline) {
			return fail(CheckSession, exitcode.Provider, "no reply from the session within %s", timeout)
		}
		d.Sleep(ctx, poll)
		if ctx.Err() != nil {
			return fail(CheckSession, exitcode.Provider, "interrupted: %v", ctx.Err())
		}
	}
}

const sessionPrompt = "Run exactly these two shell commands, one at a time, and then reply with only " +
	"their output, one per line, and nothing else. Do not run anything else.\n" +
	"printenv PG_REVIEW_RUN\ncommand -v pg-review"

func judgeSession(text, token string) Result {
	var missing []string
	if !strings.Contains(text, token) {
		missing = append(missing, "PG_REVIEW_RUN")
	}
	if !strings.Contains(text, "pg-review") {
		missing = append(missing, "pg-review")
	}
	if len(missing) > 0 {
		return fail(CheckSession, exitcode.Provider, "not visible inside the session: %s (reply: %q)", strings.Join(missing, ", "), truncate(text, 200))
	}
	return pass(CheckSession)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// OSExec is the real Exec.
func OSExec(ctx context.Context, name string, args []string, stdin string, env []string, dir string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0, nil
	}
	var xe *exec.ExitError
	if errors.As(err, &xe) && ctx.Err() == nil {
		return out.String(), xe.ExitCode(), nil
	}
	if ctx.Err() != nil {
		return out.String(), -1, fmt.Errorf("timed out or cancelled: %w", ctx.Err())
	}
	return out.String(), -1, err
}
