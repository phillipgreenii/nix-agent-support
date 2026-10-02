package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/phillipgreenii/pg-rescue/internal/capture"
	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/display"
	"github.com/phillipgreenii/pg-rescue/internal/runlog"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// ChainExecutor is the production Executor: it hands the plan to the runner,
// which runs the command and the handler chain.
type ChainExecutor struct {
	// Signals and Foreground are test seams; leave them nil in production
	// (the runner then subscribes to the process's own signals and asks the
	// terminal).
	Signals    chan os.Signal
	Foreground func() bool
	// Limits sizes the capture files; the zero value is the 32 MiB cap.
	Limits capture.Limits
	// Last is the most recent run's in-memory result, kept for tests and for
	// the display and run log that build on it.
	Last *runner.Result
}

// Execute implements Executor.
func (e *ChainExecutor) Execute(p *Plan) int {
	rt := p.Runtime
	res := runner.Run(runner.Params{
		Options:    p.Options,
		Config:     p.Config,
		Chain:      p.Chain,
		Handlers:   p.Handlers,
		Cwd:        p.Cwd,
		RunID:      p.RunID,
		RunDir:     p.RunDir,
		StateRoot:  p.StateRoot,
		StartedAt:  p.StartedAt,
		Host:       p.Host,
		Version:    rt.Version,
		Stdin:      rt.Stdin,
		Stdout:     p.Stdout,
		Stderr:     p.Stderr,
		Now:        rt.Now,
		KillGrace:  rt.KillGrace,
		Getenv:     rt.Getenv,
		Environ:    rt.Environ,
		LookPath:   rt.LookPath,
		Signals:    e.Signals,
		Foreground: e.Foreground,
		Limits:     e.Limits,
	})
	e.Last = &res
	e.finish(p, &res)
	return res.ExitCode
}

// finish does everything that comes after the run and must never change its
// exit code: the display (to stderr, and always at -vv to display.log when a
// handler ran), the run log line, and --result-file. A failed write is a
// warning at most.
func (e *ChainExecutor) finish(p *Plan, res *runner.Result) {
	opts := p.Options
	redact := runner.RedactWith(p.Config.Redact)
	in := display.Input{
		Result:   res,
		Handlers: p.Config.Handlers,
		RunDir:   p.RunDir,
		Home:     p.Runtime.Home,
		Redact:   redact,
	}
	if res.Kept && display.Shown(res) {
		text := display.Render(in, cli.VeryVerbose)
		if err := writeDisplayLog(filepath.Join(p.RunDir, "display.log"), text); err != nil {
			warnf(p, cli.Verbose, "cannot write display.log: %v", err)
		}
	}
	if opts.Verbosity != cli.Quiet {
		if text := display.Render(in, opts.Verbosity); text != "" {
			fmt.Fprint(p.Stderr, display.Lead(res)+text)
		}
	}

	entry := runlog.Build(runlog.Input{
		Result: res, Options: opts, Config: p.Config, Chain: p.Chain, Handlers: p.Handlers,
		Cwd: p.Cwd, RunID: p.RunID, Host: p.Host, Version: p.Runtime.Version,
		StartedAt: p.StartedAt, Redact: redact,
	})
	line, err := entry.Line()
	if err != nil {
		warnf(p, cli.Verbose, "cannot encode the run log line: %v", err)
		return
	}
	if err := runlog.Append(p.StateRoot, line); err != nil {
		warnf(p, cli.Verbose, "cannot write the run log: %v", err)
	}
	if opts.ResultFile != "" {
		if err := runlog.WriteResultFile(opts.ResultFile, line); err != nil {
			warnf(p, cli.Normal, "cannot write --result-file %s: %v", opts.ResultFile, err)
		}
	}
}

// warnf prints a warning to stderr when the verbosity is at least min (so a
// warning that "-q" silences is printed from the default level up).
func warnf(p *Plan, min cli.Verbosity, format string, a ...any) {
	if p.Options.Verbosity >= min {
		fmt.Fprintf(p.Stderr, "pg-rescue: warning: "+format+"\n", a...)
	}
}

// writeDisplayLog writes display.log (mode 0600), never following a symlink at
// its name.
func writeDisplayLog(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
