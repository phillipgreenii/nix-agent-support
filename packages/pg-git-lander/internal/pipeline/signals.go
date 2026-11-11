package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// This file is the signal handling of a running operation (design section
// 6.4): the first SIGINT, SIGTERM or SIGHUP cancels the operation's context,
// which the runner turns into an interrupted stop; a second signal exits at
// once and leaves the record running for the next run to convert.

// DefaultGrace is how long a step's process group has between SIGTERM and
// SIGKILL.
const DefaultGrace = 5 * time.Second

// SecondSignalExit is the exit code of a run that got a second signal.
const SecondSignalExit = 4

// ErrInterrupted is the cause of a context cancelled by a signal.
var ErrInterrupted = errors.New("interrupted by a signal")

// Interrupter turns signals into the cancellation of an operation's context.
type Interrupter struct {
	// Signals delivers the signals; nil subscribes to SIGINT, SIGTERM and
	// SIGHUP of this process.
	Signals <-chan os.Signal
	// Exit ends the process at once on a second signal; nil is os.Exit.
	Exit func(code int)
}

// Context returns a child of parent that a first signal cancels with the
// cause ErrInterrupted, and the function that releases the subscription. Pass
// the context to Start (so renewal stops with it), Run and Resume, and run the
// git execution layer through GroupRunner so the step's process group gets
// SIGTERM, then SIGKILL after the grace.
//
// A fenced move already started is not cut short: Run runs the move itself
// detached from this context and stops at the next step boundary.
func (in Interrupter) Context(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	sigs := in.Signals
	unsub := func() {}
	if sigs == nil {
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		sigs, unsub = ch, func() { signal.Stop(ch) }
	}
	exit := in.Exit
	if exit == nil {
		exit = os.Exit
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			cancel(ErrInterrupted)
		case <-done:
			return
		}
		select {
		case <-sigs:
			exit(SecondSignalExit)
		case <-done:
		}
	}()
	return ctx, func() {
		close(done)
		unsub()
		cancel(nil)
	}
}

// GroupRunner is cmdrun.Exec with a graceful end for a step that owns its
// process group: when the context ends, the group gets SIGTERM and, after
// grace, SIGKILL. A call without OwnProcGroup runs as cmdrun.Exec does.
func GroupRunner(grace time.Duration) cmdrun.Runner {
	if grace <= 0 {
		grace = DefaultGrace
	}
	return func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		if !c.OwnProcGroup {
			return cmdrun.Exec(ctx, c)
		}
		cmd := exec.CommandContext(ctx, c.Name, c.Args...)
		cmd.Dir = c.Dir
		if len(c.Stdin) > 0 {
			cmd.Stdin = bytes.NewReader(c.Stdin)
		}
		if c.EnvMode == cmdrun.EnvExact {
			cmd.Env = append([]string{}, c.Env...)
		} else {
			cmd.Env = append(os.Environ(), c.Env...)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.WaitDelay = grace
		if c.WaitDelay > 0 {
			cmd.WaitDelay = c.WaitDelay
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			pid := cmd.Process.Pid
			err := syscall.Kill(-pid, syscall.SIGTERM)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			time.AfterFunc(grace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
			return err
		}
		runErr := cmd.Run()
		res := cmdrun.Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
		state := cmd.ProcessState
		if state == nil {
			res.Code, res.StartErr, res.CtxErr = -1, runErr, ctx.Err()
			return res
		}
		res.Code = state.ExitCode()
		if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal, res.Code = ws.Signal().String(), -1
		}
		if runErr != nil {
			res.CtxErr = ctx.Err()
		}
		return res
	}
}
