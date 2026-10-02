package app

import (
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/capture"
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
	return res.ExitCode
}
