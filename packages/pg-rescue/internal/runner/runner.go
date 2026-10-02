// Package runner is pg-rescue's runtime core. Given a Plan the wrapper already
// validated, it runs the command (or reads the failure from stdin), records
// its output, and on failure walks the handler chain: it writes the report
// before each handler, invokes the handler under the handler contract,
// classifies its result with contract.Classify, verifies a "resolved" claim,
// and decides the wrapper's exit code. It reacts to SIGINT, SIGTERM and SIGHUP
// in every phase.
//
// What it does not do: show anything to the user beyond passing the command's
// own output through (the display), manage the run directory beyond its
// minimal keep-or-remove rule, or write the run log. Those belong to later
// work and build on the Result returned here.
package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/phillipgreenii/pg-rescue/internal/capture"
	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// Exit codes owned by the wrapper (mirrored by package app).
const (
	ExitWrapperError = 70
	ExitDeferred     = 75
	// ExitStdinUnhandled is the exit code in --stdin mode when no handler
	// resolved or deferred the failure.
	ExitStdinUnhandled = 1
)

// Kind is the run's overall result, in the words of the run log.
type Kind string

const (
	KindSuccess     Kind = "success"
	KindResolved    Kind = "resolved"
	KindDeferred    Kind = "deferred"
	KindUnhandled   Kind = "unhandled"
	KindError       Kind = "error"
	KindInterrupted Kind = "interrupted"
)

// Phase says which part of the run a signal interrupted.
type Phase string

const (
	PhaseCommand Phase = "command"
	PhaseStdin   Phase = "stdin"
	PhaseHandler Phase = "handler"
	PhaseVerify  Phase = "verify"
	// PhaseBetween is a signal noticed between steps, when no child was running.
	PhaseBetween Phase = "between"
)

// Params is everything Run needs. The caller (package app) has already
// validated the command line and the config and created the run directory.
type Params struct {
	Options  *cli.Options
	Config   *config.Config
	Chain    string   // chain name; "" under --handlers
	Handlers []string // resolved instance names, in order
	Cwd      string   // absolute directory the command runs in
	RunID    string
	RunDir   string
	// StateRoot is the pg-rescue state directory; runs.jsonl lives in it.
	StateRoot string
	StartedAt time.Time
	Host      string
	Version   string

	// Stdin is the wrapper's stdin: inherited by the command and by verify,
	// and the failure text in --stdin mode.
	Stdin io.Reader
	// Stdout and Stderr receive the command's own output, passed through.
	Stdout, Stderr io.Writer

	Now       func() time.Time
	KillGrace time.Duration
	Getenv    func(string) string
	Environ   func() []string
	LookPath  func(string) (string, error)

	// Signals, when set, replaces the process-wide signal subscription (tests
	// inject signals here). When nil, Run subscribes to SIGINT, SIGTERM and
	// SIGHUP for its duration.
	Signals chan os.Signal
	// Foreground reports whether the wrapper is in its terminal's foreground
	// process group; nil means "ask the terminal".
	Foreground func() bool
	// Limits sizes the capture files; the zero value takes the 32 MiB cap.
	Limits capture.Limits
}

// Interruption records a wrapper signal.
type Interruption struct {
	Signal syscall.Signal
	Phase  Phase
	// Handler and Position name the handler that was running (or whose verify
	// was); empty and 0 outside the chain.
	Handler  string
	Position int
}

// Result is what happened, held in memory. The report and the run directory
// are the durable record; this is what the display and the run log are built
// from.
type Result struct {
	Kind Kind
	// ExitCode is the wrapper's exit code.
	ExitCode int
	// CommandExit is the command's exit code (128+signo if a signal killed
	// it); -1 in --stdin mode.
	CommandExit int
	// ResolvedBy / DeferredBy name the handler that decided the run.
	ResolvedBy string
	DeferredBy string
	// Interrupted is set when the run ended on a wrapper signal.
	Interrupted *Interruption
	// Report is the final report (nil only for KindError).
	Report *report.Report
	// Binaries holds the resolved command[0] of each attempt, aligned with
	// Report.Attempts; "" when it could not be resolved.
	Binaries []string
	Duration time.Duration
	// Kept says the run directory was kept (a handler ran or the run was
	// interrupted); otherwise Run removed it.
	Kept bool
}

type run struct {
	p    Params
	sigs chan os.Signal
	sig  os.Signal // the first wrapper signal received

	outMu  sync.Mutex // serializes writes to the wrapper's own stdout/stderr
	rep    *report.Report
	res    *Result
	redact *redactor
}

// Run executes the plan. It prints a message to p.Stderr only for a wrapper
// error (the command could not be spawned); everything else is the command's
// own output, passed through.
func Run(p Params) Result {
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.KillGrace <= 0 {
		p.KillGrace = 5 * time.Second
	}
	if p.Getenv == nil {
		p.Getenv = os.Getenv
	}
	if p.Environ == nil {
		p.Environ = os.Environ
	}
	if p.LookPath == nil {
		p.LookPath = exec.LookPath
	}
	if p.Foreground == nil {
		p.Foreground = foregroundOfTerminal
	}
	r := &run{p: p, res: &Result{CommandExit: -1}, redact: newRedactor(p.Config.Redact)}
	r.sigs = p.Signals
	if r.sigs == nil {
		r.sigs = make(chan os.Signal, 8)
		signal.Notify(r.sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer signal.Stop(r.sigs)
	}
	started := p.Now()
	r.execute()
	r.res.Duration = p.Now().Sub(started)
	return *r.res
}

// noteSignal remembers the first wrapper signal.
func (r *run) noteSignal(sig os.Signal) {
	if r.sig == nil {
		r.sig = sig
	}
}

// pollSignal notices a signal that arrived while no child was being waited on.
func (r *run) pollSignal() bool {
	for {
		select {
		case sig := <-r.sigs:
			r.noteSignal(sig)
		default:
			return r.sig != nil
		}
	}
}

func (r *run) wrapperError(format string, a ...any) {
	fmt.Fprintf(r.p.Stderr, "pg-rescue: "+format+"\n", a...)
	r.res.Kind = KindError
	r.res.ExitCode = ExitWrapperError
	_ = os.RemoveAll(r.p.RunDir)
}

func (r *run) execute() {
	argvMode := !r.p.Options.Stdin
	out, err := capture.Create(filepath.Join(r.p.RunDir, "output.log"), r.p.Limits)
	if err != nil {
		r.wrapperError("cannot create the output log in %s: %v", r.p.RunDir, err)
		return
	}

	commandExit := -1
	if r.pollSignal() {
		_ = out.Close()
		r.finishInterrupted(PhaseBetween, "", 0)
		return
	}
	if argvMode {
		code, ok := r.runCommand(out)
		if !ok {
			_ = out.Close()
			return
		}
		commandExit = code
	} else {
		r.readStdin(out)
	}
	_ = out.Close()
	r.res.CommandExit = commandExit

	if r.sig != nil || r.pollSignal() {
		r.res.Report = r.buildReport(out, commandExit)
		r.finishInterrupted(phaseOfInput(argvMode), "", 0)
		return
	}
	if argvMode && commandExit == 0 {
		r.res.Kind = KindSuccess
		r.res.ExitCode = 0
		_ = os.RemoveAll(r.p.RunDir)
		return
	}

	r.rep = r.buildReport(out, commandExit)
	r.res.Report = r.rep
	r.res.Kept = true
	r.runChain(commandExit)
	r.writeReport() // the final state, for "what happened in run X"
}

func phaseOfInput(argvMode bool) Phase {
	if argvMode {
		return PhaseCommand
	}
	return PhaseStdin
}

// finishInterrupted ends the run on a wrapper signal: the chain stops, the
// run directory is kept and the wrapper exits 128+signo.
func (r *run) finishInterrupted(phase Phase, handler string, position int) {
	sig, _ := r.sig.(syscall.Signal)
	r.res.Kind = KindInterrupted
	r.res.ExitCode = 128 + int(sig)
	r.res.Interrupted = &Interruption{Signal: sig, Phase: phase, Handler: handler, Position: position}
	r.res.Kept = true
}

// runCommand spawns the command with the wrapper's stdin, in the wrapper's
// process group, tees its two streams to the wrapper's own and to out, and
// waits. ok is false when it could not be spawned (a wrapper error, already
// reported).
func (r *run) runCommand(out *capture.File) (exit int, ok bool) {
	argv := r.p.Options.Argv
	path, err := r.resolve(argv[0])
	if err != nil {
		r.wrapperError("cannot run %q: %v", argv[0], err)
		return 0, false
	}
	res := r.runProc(procSpec{
		path: path, argv: argv, dir: r.p.Cwd, env: r.p.Environ(),
		stdin:   r.p.Stdin,
		stdout:  &tee{c: out, out: r.p.Stdout, mu: &r.outMu},
		stderr:  &tee{c: out, out: r.p.Stderr, mu: &r.outMu},
		forward: true,
	})
	if res.startErr != nil {
		r.wrapperError("cannot run %q: %v", argv[0], res.startErr)
		return 0, false
	}
	if res.signal != 0 {
		return 128 + int(res.signal), true
	}
	return res.exit, true
}

// readStdin reads the failure text from stdin to EOF into out, without echoing
// it. A wrapper signal abandons the read.
func (r *run) readStdin(out *capture.File) {
	if r.p.Stdin == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(out, r.p.Stdin)
	}()
	select {
	case <-done:
	case sig := <-r.sigs:
		r.noteSignal(sig)
	}
}

// resolve finds an executable the way a shell would: a name with a slash is
// used as written (relative to the child's directory), a bare name is looked
// up on PATH.
func (r *run) resolve(name string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	return r.p.LookPath(name)
}

// tee writes a child's stream to its capture file and, best effort, to the
// wrapper's own stream. A failing passthrough (a closed pipe) never stops the
// capture or blocks the child.
type tee struct {
	c      *capture.File
	out    io.Writer
	mu     *sync.Mutex
	failed bool
}

func (t *tee) Write(p []byte) (int, error) {
	_, _ = t.c.Write(p)
	if t.out != nil && !t.failed {
		t.mu.Lock()
		_, err := t.out.Write(p)
		t.mu.Unlock()
		if err != nil {
			t.failed = true
		}
	}
	return len(p), nil
}

// foregroundOfTerminal reports whether this process is in the foreground
// process group of its controlling terminal.
func foregroundOfTerminal() bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	defer tty.Close()
	var pgrp int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&pgrp)))
	return errno == 0 && int(pgrp) == syscall.Getpgrp()
}

func itoa(n int) string { return strconv.Itoa(n) }
