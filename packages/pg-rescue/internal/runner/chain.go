package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/capture"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// runChain walks the handlers in order and stops at the first one that
// resolves (and passes verify) or defers the failure, or when the wrapper is
// signalled. When every handler declines or fails, the run is unhandled.
func (r *run) runChain(commandExit int) {
	for i, name := range r.p.Handlers {
		if r.pollSignal() {
			r.finishInterrupted(PhaseBetween, "", 0)
			return
		}
		if r.attempt(i+1, name) {
			return
		}
	}
	r.res.Kind = KindUnhandled
	r.res.ExitCode = commandExit
	if r.p.Options.Stdin {
		r.res.ExitCode = ExitStdinUnhandled
	}
	if r.sig != nil || r.pollSignal() {
		r.finishInterrupted(PhaseBetween, "", 0)
	}
}

// attempt runs handler number pos and records the attempt. It returns true
// when the chain must stop: the run was decided or interrupted.
func (r *run) attempt(pos int, name string) (stop bool) {
	h := r.p.Config.Handlers[name]
	a := report.Attempt{
		Handler:    name,
		Position:   pos,
		Tags:       append([]string{}, h.Tags...),
		Exit:       -1,
		StderrFile: r.attemptPath(pos, "stderr.log"),
	}
	binary := ""
	defer func() {
		r.rep.Attempts = append(r.rep.Attempts, a)
		r.res.Binaries = append(r.res.Binaries, binary)
	}()
	fail := func(format string, args ...any) bool {
		a.Outcome, a.Reason = contract.Failed, fmt.Sprintf(format, args...)
		return false
	}

	// The report as this handler will see it, with n-1 attempts.
	if err := r.writeReport(); err != nil {
		return fail("cannot write report.json: %v", err)
	}
	stderrCap, err := capture.Create(a.StderrFile, r.p.Limits)
	if err != nil {
		return fail("cannot create the stderr file: %v", err)
	}
	defer func() { _ = stderrCap.Close() }()

	vars := r.vars(name, pos)
	env, err := config.BuildEnv(r.baseEnv(), h, vars)
	if err != nil {
		return fail("%v", err)
	}
	path, err := r.resolve(h.Command[0])
	if err != nil {
		return fail("not found on PATH")
	}
	binary = path

	results := newResultSink()
	start := r.p.Now()
	pr := r.runProc(procSpec{
		path: path, argv: h.Command, dir: r.p.Cwd, env: env,
		stdout:   results,
		stderr:   stderrCap,
		ownGroup: true,
		timeout:  h.Timeout,
	})
	a.DurationMS = r.p.Now().Sub(start).Milliseconds()
	_ = stderrCap.Close()

	switch {
	case pr.startErr != nil:
		if errors.Is(pr.startErr, fs.ErrNotExist) {
			if strings.Contains(h.Command[0], "/") {
				return fail("not found: %s", h.Command[0])
			}
			return fail("not found on PATH")
		}
		return fail("cannot start: %v", pr.startErr)
	case pr.interrupted:
		fail("interrupted by %s", signalName(r.signal()))
		r.finishInterrupted(PhaseHandler, name, pos)
		return true
	case pr.timedOut:
		fail("timed out after %s", formatDuration(h.Timeout))
	case pr.signal != 0:
		fail("killed by signal %s", signalName(pr.signal))
	default:
		a.Exit = pr.exit
		out, trunc := results.snapshot()
		a.Outcome, a.Reason, a.Reported = contract.Classify(pr.exit, out, trunc)
	}
	r.finishReported(&a, pos)

	if a.Outcome == contract.Resolved {
		if r.verify(&a, h, name, pos, vars) {
			r.finishInterrupted(PhaseVerify, name, pos)
			return true
		}
	}
	switch a.Outcome {
	case contract.Resolved:
		r.res.Kind, r.res.ExitCode, r.res.ResolvedBy = KindResolved, 0, name
		return true
	case contract.Deferred:
		r.res.Kind, r.res.ExitCode, r.res.DeferredBy = KindDeferred, ExitDeferred, name
		return true
	}
	return false
}

// finishReported prepares the handler's claims for the report: the summary and
// details are redacted copies, details are capped (the full raw text goes to
// details_file).
func (r *run) finishReported(a *report.Attempt, pos int) {
	rep := &a.Reported
	rep.Summary = r.redact.apply(rep.Summary)
	if rep.Details == "" {
		return
	}
	full := rep.Details
	file := r.attemptPath(pos, "details")
	if err := writeAttemptFile(file, []byte(full)); err == nil {
		rep.DetailsFile = file
	}
	rep.Details = truncateBytes(r.redact.apply(full), DetailsBytes)
}

// verify checks a "resolved" claim: --verify through sh -c, or a re-run of the
// original argv, in the command's cwd with the command's execution rules and
// the PG_RESCUE_* variables. Its output goes to a per-attempt file, never to
// the wrapper's stdout. A failure turns the attempt into failed. It reports
// whether a wrapper signal interrupted it.
func (r *run) verify(a *report.Attempt, h *config.Handler, name string, pos int, vars map[string]string) (interrupted bool) {
	failf := func(format string, args ...any) {
		a.Outcome, a.Reason = contract.Failed, "resolved → verify "+fmt.Sprintf(format, args...)
	}
	file := r.attemptPath(pos, "verify.log")
	a.VerifyOutputFile = file
	vcap, err := capture.Create(file, r.p.Limits)
	if err != nil {
		failf("could not start: cannot create the output file: %v", err)
		return false
	}
	defer func() { _ = vcap.Close() }()

	var argv []string
	if r.p.Options.HasVerify {
		argv = []string{"sh", "-c", r.p.Options.Verify}
	} else {
		argv = r.p.Options.Argv
	}
	path, err := r.resolveShell(argv[0])
	if err != nil {
		failf("could not start: %v", err)
		return false
	}
	env, _ := config.BuildEnv(r.baseEnv(), &config.Handler{Name: name}, vars)
	start := r.p.Now()
	pr := r.runProc(procSpec{
		path: path, argv: argv, dir: r.p.Cwd, env: env,
		stdin:  r.p.Stdin,
		stdout: vcap, merge: true,
		timeout: h.Timeout,
	})
	ms := r.p.Now().Sub(start).Milliseconds()
	a.VerifyMS = &ms
	_ = vcap.Close()

	switch {
	case pr.startErr != nil:
		failf("could not start: %v", pr.startErr)
	case pr.interrupted:
		failf("interrupted by %s", signalName(r.signal()))
		return true
	case pr.timedOut:
		failf("timed out after %s", formatDuration(h.Timeout))
	case pr.signal != 0:
		failf("killed by signal %s", signalName(pr.signal))
	case pr.exit != 0:
		failf("failed (exit %d)", pr.exit)
	default:
		a.Reason = "verify passed"
	}
	return false
}

// resolveShell is resolve, with a fallback for "sh" so a stripped PATH still
// finds the system shell.
func (r *run) resolveShell(name string) (string, error) {
	p, err := r.resolve(name)
	if err != nil && name == "sh" {
		return "/bin/sh", nil
	}
	return p, err
}

func (r *run) signal() syscall.Signal {
	s, _ := r.sig.(syscall.Signal)
	return s
}

func (r *run) attemptPath(pos int, suffix string) string {
	return filepath.Join(r.p.RunDir, fmt.Sprintf("attempt-%d.%s", pos, suffix))
}

// baseEnv is the wrapper's environment minus PG_RESCUE_PARENT_RUN_ID, which
// is set (to the inherited run id) only when this run is nested.
func (r *run) baseEnv() []string {
	var out []string
	for _, kv := range r.p.Environ() {
		if strings.HasPrefix(kv, "PG_RESCUE_PARENT_RUN_ID=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// vars are the PG_RESCUE_* variables for the handler at position pos.
func (r *run) vars(handler string, pos int) map[string]string {
	rep := r.rep
	m := map[string]string{
		"PG_RESCUE_REPORT":      filepath.Join(r.p.RunDir, "report.json"),
		"PG_RESCUE_CMD":         "",
		"PG_RESCUE_EXIT":        "",
		"PG_RESCUE_OUTPUT_FILE": rep.OutputFile,
		"PG_RESCUE_CONTEXT":     rep.Context,
		"PG_RESCUE_FINGERPRINT": rep.Fingerprint,
		"PG_RESCUE_RUN_ID":      rep.RunID,
		"PG_RESCUE_RUN_DIR":     r.p.RunDir,
		"PG_RESCUE_RUN_LOG":     filepath.Join(r.p.StateRoot, "runs.jsonl"),
		"PG_RESCUE_HANDLER":     handler,
		"PG_RESCUE_POSITION":    strconv.Itoa(pos),
		"PG_RESCUE_DEPTH":       strconv.Itoa(rep.Depth),
	}
	if rep.Command != nil {
		m["PG_RESCUE_CMD"] = report.QuoteArgv(rep.Command.Argv)
		m["PG_RESCUE_EXIT"] = strconv.Itoa(rep.Command.Exit)
	}
	if rep.ParentRunID != nil {
		m["PG_RESCUE_PARENT_RUN_ID"] = *rep.ParentRunID
	}
	return m
}

// resultSink collects a handler's stdout up to the contract's cap and drops
// the rest, so the handler never blocks on a full pipe.
type resultSink struct {
	mu  sync.Mutex
	buf *contract.LimitedBuffer
}

func newResultSink() *resultSink { return &resultSink{buf: contract.NewResultBuffer()} }

func (s *resultSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *resultSink) snapshot() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...), s.buf.Truncated()
}

// formatDuration writes a timeout the way a config author wrote it: "10m",
// "90s", "1h".
func formatDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}
