// Command pg-claude-control is the one way for a tool in any language to send submit, interrupt,
// run, stop and read requests to a running Claude's control inbox, resolving the target through
// the session registry. It reads only the registry files and the inbox; it never talks to the
// recorder.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// Version is stamped by mkGoApp's default ldflag (main.Version).
var Version = "dev"

const (
	// defaultWait is how long a missing inbox (ENOENT) is retried before exit 4: a healing
	// listener recreates its FIFO within that time, a Claude that never had the mod has none.
	defaultWait    = 3 * time.Second
	defaultTimeout = 10 * time.Second
)

// app carries what a subcommand touches, so tests inject it.
type app struct {
	out, errw io.Writer
	stdin     io.Reader
	getenv    func(string) string
	runner    cmdrun.Runner
	kill      func(pid int, sig syscall.Signal) error
	// wait overrides defaultWait when the --wait flag is absent (tests).
	wait time.Duration
	// poll is the liveness/reply polling step (100ms when zero).
	poll time.Duration
	// sleep replaces time.Sleep in the retry and polling loops (tests).
	sleep func(time.Duration)
}

func (a *app) pollEvery() time.Duration {
	if a.poll > 0 {
		return a.poll
	}
	return 100 * time.Millisecond
}

const usageText = `usage: pg-claude-control <command> [flags]

  submit    <target>                     type the text read from standard input and submit it
  interrupt <target>                     abort the running turn
  run       <target> <command> [args]    run an allowlisted slash command (exit, clear, compact)
  stop      <target> [--signal] [--escalate <duration>]
                                         abort the turn, then run exit; --signal sends SIGTERM at
                                         once (works without the mod); --escalate follows with
                                         SIGTERM if the Claude is still alive after the duration
  read      <target> --json              read the prompt draft
  list      [--json]                     every running Claude: pid, session, name, inbox present

<target> is a pid, a session id (UUID) or a name; pid:, session: and name: force one reading.
Flags: --json  --wait <duration> (inbox wait, default 3s)  --timeout <duration> (reply wait, default 10s)
       --reply (ask the mod to confirm; exit 6 on refusal, 8 on no reply)
Flags go before run's <command>; everything after it is passed to the command.
Exit codes: 0 written, 1 unexpected, 2 usage, 3 no such Claude process, 4 no control mod,
5 mod not accepting, 6 refused by the mod, 7 ambiguous target, 8 no reply within --timeout.
Environment: PG_CLAUDE_CONTROL_STATE_DIR, PG_CLAUDE_CONTROL_RUN_ALLOW, CLAUDE_CONFIG_DIR.
`

// opts are the flags shared by the subcommands.
type opts struct {
	json     bool
	reply    bool
	wait     time.Duration
	timeout  time.Duration
	signal   bool
	escalate time.Duration
}

// parseArgs parses flags interspersed with up to maxPos positionals. With verbatim, everything
// after the maxPos-th positional is returned untouched as the tail (run's command arguments).
func (a *app) parseArgs(name string, args []string, maxPos int, verbatim, stopFlags bool) (pos, tail []string, o opts, ce *cmdError) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o.wait, o.timeout = defaultWait, defaultTimeout
	if a.wait > 0 {
		o.wait = a.wait
	}
	fs.BoolVar(&o.json, "json", false, "")
	fs.DurationVar(&o.wait, "wait", o.wait, "")
	fs.DurationVar(&o.timeout, "timeout", o.timeout, "")
	if name != "list" {
		fs.BoolVar(&o.reply, "reply", false, "")
	}
	if stopFlags {
		fs.BoolVar(&o.signal, "signal", false, "")
		fs.DurationVar(&o.escalate, "escalate", 0, "")
	}
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, nil, o, fail(errUsage, "%v\n%s", err, usageText)
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		if len(pos) == maxPos {
			if verbatim {
				tail = rest
				break
			}
			return nil, nil, o, fail(errUsage, "unexpected argument %q\n%s", rest[0], usageText)
		}
		pos = append(pos, rest[0])
		rest = rest[1:]
		if verbatim && len(pos) == maxPos {
			tail = rest
			break
		}
	}
	return pos, tail, o, nil
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(a.errw, usageText)
		return exitUsage
	}
	switch args[0] {
	case "--help", "-h", "help":
		_, _ = fmt.Fprint(a.out, usageText)
		return exitOK
	case "--version", "version":
		_, _ = fmt.Fprintf(a.out, "pg-claude-control %s\n", Version)
		return exitOK
	case "submit", "interrupt", "run", "stop", "read", "list":
		return a.command(args[0], args[1:])
	}
	return a.emitError(false, fail(errUsage, "unknown command %q\n%s", args[0], usageText))
}

func main() {
	a := &app{
		out: os.Stdout, errw: os.Stderr, stdin: os.Stdin,
		getenv: os.Getenv,
		runner: cmdrun.Exec,
		kill:   func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) },
	}
	os.Exit(a.run(os.Args[1:]))
}
