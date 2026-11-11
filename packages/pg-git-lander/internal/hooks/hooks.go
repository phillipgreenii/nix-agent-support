// Package hooks runs the configured hook entries of a hook point, applies the
// exit-code contract and the effect a failure has at each point, and
// implements autofix and the retrying commit helper (design section 13).
package hooks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Hook points (design section 13.1).
const (
	PointBefore      = "before"
	PointAfterRebase = "after_rebase"
	PointBeforeMove  = "before_move"
	PointAfter       = "after"
)

// Exit codes of the contract (design section 13.3).
const (
	ExitTempFail   = 75
	ExitNotExec    = 126
	ExitNotFound   = 127
	DefaultTimeout = 10 * time.Minute
)

// Tail sizes: the outcome's log_tail and a skipped hook's stderr.
const (
	LogTailLines     = 50
	SkippedTailLines = 10
)

// Per-command results.
const (
	ResultPass    = "pass"
	ResultSkipped = "skipped"
	ResultAutofix = "autofix"
	ResultFailed  = "failed"
)

// CommandRecord is one command run, for the operation log and events. It
// carries variable NAMES only, never values (design section 14.7).
type CommandRecord struct {
	Point    string        `json:"point"`
	Index    int           `json:"index"`
	Argv0    string        `json:"argv0"`
	Code     int           `json:"code"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timed_out"`
	Stdout   string        `json:"-"`
	Stderr   string        `json:"-"`
	// AddedVars are the names of the variables added to the base environment.
	AddedVars []string `json:"added_vars"`
	// Result is pass, skipped, autofix or failed.
	Result string `json:"result"`
}

// LogLine renders the record for a log; it never contains a variable value.
func (r CommandRecord) LogLine() string {
	return fmt.Sprintf("hook %s[%d] %s exit=%d result=%s duration=%s env=%s",
		r.Point, r.Index, r.Argv0, r.Code, r.Result, r.Duration.Round(time.Millisecond),
		strings.Join(r.AddedVars, ","))
}

// Skipped is a hook whose exit code is listed in skip_exit_codes.
type Skipped struct {
	Point      string `json:"point"`
	Index      int    `json:"index"`
	Code       int    `json:"code"`
	StderrTail string `json:"stderr_tail"`
}

// FailKind classifies a failure.
type FailKind int

// Failure kinds.
const (
	FailedExit FailKind = iota + 1 // any other non-zero (incl. 126, 127)
	Transient                      // exit 75
	TimedOut
	AutofixExhausted // autofix.attempts runs without a clean pass
	CommitRejected   // an autofix commit a git commit hook kept rejecting
)

// Failure is a hook failure; Effect says what the pipeline does with it.
type Failure struct {
	Kind       FailKind
	Point      string
	Index      int
	Code       int
	StderrTail string
	// LogTail is the last 50 lines of the failing run's output, without git's
	// hint: lines.
	LogTail string
}

// Effect is what a failure means at its point.
type Effect struct {
	// Exactly one is set.
	Refusal *outcome.RefusalReason
	Stop    *outcome.StopReason
	Warning *outcome.Warning
}

// Effect applies design sections 13.1 and 13.3.
func (f *Failure) Effect() Effect {
	switch f.Point {
	case PointBefore:
		r := outcome.RefBeforeHookRefused
		return Effect{Refusal: &r}
	case PointAfter:
		w := outcome.WarnAfterHookFailed
		return Effect{Warning: &w}
	}
	s := outcome.StopHookFailed
	switch f.Kind {
	case Transient:
		s = outcome.StopTransient
	case TimedOut:
		s = outcome.StopTimeout
	case CommitRejected:
		s = outcome.StopCommitRejected
	}
	return Effect{Stop: &s}
}

// PointResult is the result of running one hook point.
type PointResult struct {
	// Failure is nil when every hook passed or was skipped.
	Failure *Failure
	Skipped []Skipped
	Records []CommandRecord
	// Untracked lists untracked files a hook left in the work worktree; they
	// are never committed.
	Untracked []string
}

// Pass reports that no hook failed.
func (r PointResult) Pass() bool { return r.Failure == nil }

// Runner runs hook points.
type Runner struct {
	Git *gitexec.Git
	// AutofixAttempts is autofix.attempts (runs per hook, tries per commit).
	AutofixAttempts int
	// RunGitHooks is run_git_hooks: false passes --no-verify to the tool's own
	// commits.
	RunGitHooks bool
}

// PointInput is one hook point to run.
type PointInput struct {
	Point string
	Hooks []config.Hook
	Facts Facts
}

// RunPoint runs the hooks of one point in list order and stops at the first
// failure.
func (r *Runner) RunPoint(ctx context.Context, in PointInput) (PointResult, error) {
	switch in.Point {
	case PointBefore, PointAfterRebase, PointBeforeMove, PointAfter:
	default:
		return PointResult{}, fmt.Errorf("hooks: unknown hook point %q", in.Point)
	}
	autofix := in.Point == PointAfterRebase || in.Point == PointBeforeMove
	var res PointResult
	for i, h := range in.Hooks {
		if err := r.runEntry(ctx, in, i, h, autofix, &res); err != nil {
			return res, err
		}
		if res.Failure != nil {
			return res, nil
		}
	}
	return res, nil
}

func (r *Runner) runEntry(ctx context.Context, in PointInput, idx int, h config.Hook, autofix bool, res *PointResult) error {
	attempts := r.AutofixAttempts
	if attempts < 1 {
		attempts = 1
	}
	for run := 1; ; run++ {
		rec, fail := r.runOnce(ctx, in, idx, h)
		if rec.Result == ResultSkipped {
			res.Skipped = append(res.Skipped, Skipped{Point: in.Point, Index: idx, Code: rec.Code, StderrTail: Tail(rec.Stderr, SkippedTailLines)})
		}
		if ctx.Err() != nil {
			res.Records = append(res.Records, rec)
			return ctx.Err()
		}
		// Transient and timeout stop at once, whatever the tree holds.
		if fail != nil && (fail.Kind == Transient || fail.Kind == TimedOut) {
			res.Records = append(res.Records, rec)
			res.Failure = fail
			return nil
		}
		if !autofix {
			res.Records = append(res.Records, rec)
			res.Failure = fail
			return nil
		}
		changed, untracked, err := r.status(ctx, in.Facts.WorkWorktree, false)
		if err != nil {
			res.Records = append(res.Records, rec)
			return err
		}
		res.Untracked = appendUnique(res.Untracked, untracked)
		if !changed {
			res.Records = append(res.Records, rec)
			res.Failure = fail // nil on pass or skip; a failure that changed nothing stops at once
			return nil
		}
		rec.Result = ResultAutofix
		res.Records = append(res.Records, rec)
		msg := FixMessage(in.Point, in.Facts.Operation)
		if cr := r.Commit(ctx, CommitSpec{Dir: in.Facts.WorkWorktree, Message: msg, StageTracked: true}); cr.Err != nil || cr.Stop != nil {
			if cr.Err != nil {
				return cr.Err
			}
			res.Failure = r.failureFrom(rec, CommitRejected)
			res.Failure.LogTail = cr.LogTail
			return nil
		}
		if run >= attempts {
			f := r.failureFrom(rec, AutofixExhausted)
			res.Failure = f
			return nil
		}
	}
}

func (r *Runner) failureFrom(rec CommandRecord, k FailKind) *Failure {
	return &Failure{
		Kind: k, Point: rec.Point, Index: rec.Index, Code: rec.Code,
		StderrTail: Tail(rec.Stderr, SkippedTailLines),
		LogTail:    LogTail(rec.Stdout, rec.Stderr),
	}
}

// FixMessage is the autofix commit message. It never carries --context
// values: commit messages are durable history.
func FixMessage(point, operation string) string {
	return fmt.Sprintf("style: apply %s fixes made during %s", point, operation)
}

// runOnce runs the hook command once and classifies it.
func (r *Runner) runOnce(ctx context.Context, in PointInput, idx int, h config.Hook) (CommandRecord, *Failure) {
	rec := CommandRecord{Point: in.Point, Index: idx}
	if len(h.Run) == 0 {
		rec.Code, rec.Result = ExitNotFound, ResultFailed
		return rec, r.failureFrom(rec, FailedExit)
	}
	rec.Argv0 = h.Run[0]
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	dir := in.Facts.WorkWorktree
	cwd := h.Cwd
	if cwd == "" && in.Point == PointBefore {
		cwd = "repo"
	}
	if cwd == "repo" {
		dir = in.Facts.Repo
	}
	env, added := BuildEnv(r.Git.Env.Environ(), in.Facts.Vars(in.Point), h.Env)
	rec.AddedVars = added

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cr := r.Git.Runner(cctx, cmdrun.Call{
		Name: h.Run[0], Args: h.Run[1:], Dir: dir,
		Env: env, EnvMode: cmdrun.EnvExact, OwnProcGroup: true,
	})
	rec.Duration = time.Since(start)
	rec.Stdout, rec.Stderr = string(cr.Stdout), string(cr.Stderr)
	rec.Code = cr.Code
	if cr.StartErr != nil {
		rec.Code = startCode(cr.StartErr)
	}

	switch {
	case ctx.Err() == nil && errors.Is(cr.CtxErr, context.DeadlineExceeded):
		rec.TimedOut, rec.Result = true, ResultFailed
		return rec, r.failureFrom(rec, TimedOut)
	case rec.Code == 0 && cr.OK():
		rec.Result = ResultPass
		return rec, nil
	case containsInt(h.SkipExitCodes, rec.Code):
		rec.Result = ResultSkipped
		return rec, nil
	case rec.Code == ExitTempFail:
		rec.Result = ResultFailed
		return rec, r.failureFrom(rec, Transient)
	}
	rec.Result = ResultFailed
	return rec, r.failureFrom(rec, FailedExit)
}

// startCode maps a start error as a shell would: 126 for a command that could
// not be executed, 127 for one that could not be found.
func startCode(err error) int {
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOEXEC) {
		return ExitNotExec
	}
	var ee *exec.Error
	if errors.As(err, &ee) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return ExitNotFound
	}
	return ExitNotExec
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func appendUnique(dst, src []string) []string {
	for _, s := range src {
		if !containsStr(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Tail returns the last n lines of s.
func Tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// LogTail is the last 50 lines of a command's output (stdout then stderr),
// without the hint: lines git prints, which would send an agent to the wrong
// command.
func LogTail(stdout, stderr string) string {
	var kept []string
	for _, l := range strings.Split(stdout+stderr, "\n") {
		if strings.HasPrefix(l, "hint:") {
			continue
		}
		kept = append(kept, l)
	}
	return Tail(strings.Join(kept, "\n"), LogTailLines)
}

// status reports whether the work worktree has tracked changes and lists its
// untracked files.
// With unstagedOnly, only changes the index does not hold count.
func (r *Runner) status(ctx context.Context, dir string, unstagedOnly bool) (changed bool, untracked []string, err error) {
	g := r.Git.Run(ctx, dir, "status", "--porcelain")
	if !g.OK() {
		return false, nil, fmt.Errorf("git status: exit %d: %s", g.Code, strings.TrimSpace(string(g.Stderr)))
	}
	for _, l := range strings.Split(string(g.Stdout), "\n") {
		switch {
		case l == "":
		case strings.HasPrefix(l, "?? "):
			untracked = append(untracked, strings.TrimPrefix(l, "?? "))
		case strings.HasPrefix(l, "!! "):
		case unstagedOnly && (len(l) < 2 || l[1] == ' '):
		default:
			changed = true
		}
	}
	return changed, untracked, nil
}
