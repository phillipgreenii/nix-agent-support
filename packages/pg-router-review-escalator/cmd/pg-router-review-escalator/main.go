// pg-router-review-escalator is the escalation path for unremovable pending
// reviews (bead pg2-kftf9.15, pending-review policy 5). A stale pending review
// that `pg-connector pr review submit` could not remove is reported as
// status blocked_human_pending; this binary is the DELIBERATE code path that
// turns that status into exactly one deduplicated `human` +
// `human-focus-required` bead per PR and a push notification, and closes the
// bead when the PR's review next resolves. It is not worker prose.
//
// It is an ordinary pg-router command-role binary, like pg-router-probe and
// pg-router-disk-watchdog: pg-router core, pg-desk and pg-connector are not
// changed, and it holds no state of its own (the escalation bead carries it).
//
// Subcommands:
//
//	submit [flags] <pr-id>   the verb the review role calls INSTEAD of
//	                         `pg-connector pr review submit <pr-id>`: it runs
//	                         that verb with the same stdin, prints its output
//	                         unchanged, then applies the outcome.
//	report [flags] <pr-id>   reads a submit output (the wire envelope or the
//	                         bare result) on stdin and applies it; for replay
//	                         and for a caller that ran the verb itself.
//
// Exit codes: 0 ok; 1 unexpected error (including a submit whose output is not
// a recognizable outcome, and pg-connector's own exit 1 or 4 passed through);
// 2 usage; 3 an escalation could not be delivered (the bead or the
// notification failed; both are always attempted and both are logged).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-review-escalator/internal/adapters"
	"github.com/phillipgreenii/pg-router-review-escalator/internal/escalate"
)

// Version is injected at build time by mkGoApp.
var Version = "dev"

const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitDelivery = 3
)

const prog = "pg-router-review-escalator"

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// deps are the process-level side effects, injected so tests start no process.
type deps struct {
	runner func(timeout time.Duration) adapters.Runner
	now    func() time.Time
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func realDeps() deps {
	return deps{
		runner: func(t time.Duration) adapters.Runner { return adapters.ExecRunner{Timeout: t} },
		now:    time.Now,
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], realDeps()))
}

const usageText = `usage: pg-router-review-escalator submit [flags] <pr-id>   (request JSON on stdin)
       pg-router-review-escalator report [flags] <pr-id>   (submit output JSON on stdin)
       pg-router-review-escalator --version

flags:
  --pg-connector-path P   pg-connector binary (default "pg-connector")
  --submit-backend B      pin the pr review submit call to this backend (submit only)
  --tracker-backend B     pg-connector issue backend for the escalation beads (default "pg-connector-issue-beads")
  --list-query Q          named issue query listing every OPEN escalation bead (default "pending-review-escalations")
  --label L               extra label on every created bead; repeatable
  --priority P            priority of created beads (tracker-native form; default: tracker default)
  --renotify-interval D   minimum time between push notifications for one PR or roll-up (default 12h)
  --rollup-threshold N    more than N PRs blocked for one systemic reason roll up into one bead (default 3; negative disables)
  --systemic-reason R     reason that can roll up; repeatable (default detection_failed, delete_refused, archive_failed)
  --notify-arg A          one element of the push command's argv; repeatable; {title} {body} {url} {key} are substituted
  --exec-timeout D        timeout for each tracker and notify call (default 30s)
  --submit-timeout D      timeout for the pr review submit call (default 2m)
`

func run(ctx context.Context, args []string, d deps) int {
	if len(args) == 0 {
		fmt.Fprint(d.stderr, usageText)
		return exitUsage
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintln(d.stdout, Version)
		return exitOK
	case "-h", "--help", "help":
		fmt.Fprint(d.stdout, usageText)
		return exitOK
	case "submit", "report":
	default:
		fmt.Fprintf(d.stderr, "%s: unknown subcommand %q\n%s", prog, args[0], usageText)
		return exitUsage
	}
	sub := args[0]

	fs := flag.NewFlagSet(prog+" "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	connector := fs.String("pg-connector-path", adapters.DefaultConnectorBinary, "")
	submitBackend := fs.String("submit-backend", "", "")
	trackerBackend := fs.String("tracker-backend", adapters.DefaultTrackerBackend, "")
	listQuery := fs.String("list-query", adapters.DefaultListQuery, "")
	var labels, systemic, notifyArgv multiFlag
	fs.Var(&labels, "label", "")
	fs.Var(&systemic, "systemic-reason", "")
	fs.Var(&notifyArgv, "notify-arg", "")
	priority := fs.String("priority", "", "")
	renotify := fs.Duration("renotify-interval", escalate.DefaultRenotifyInterval, "")
	threshold := fs.Int("rollup-threshold", escalate.DefaultRollupThreshold, "")
	execTimeout := fs.Duration("exec-timeout", 30*time.Second, "")
	submitTimeout := fs.Duration("submit-timeout", 2*time.Minute, "")

	// Flags and the one positional PR id may be interleaved.
	var positional []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintf(d.stderr, "%s: %v\n%s", prog, err, usageText)
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) != 1 {
		fmt.Fprintf(d.stderr, "%s: %s takes exactly one <pr-id>, got %d\n%s", prog, sub, len(positional), usageText)
		return exitUsage
	}
	pr := positional[0]
	if *renotify <= 0 {
		fmt.Fprintf(d.stderr, "%s: --renotify-interval must be positive\n", prog)
		return exitUsage
	}

	toolRunner := d.runner(*execTimeout)
	esc := escalate.New(
		adapters.ConnectorTracker{Runner: toolRunner, Binary: *connector, Backend: *trackerBackend, ListQuery: *listQuery},
		adapters.ExecNotifier{Runner: toolRunner, Argv: notifyArgv},
		d.now,
		escalate.Config{
			RenotifyInterval: *renotify,
			RollupThreshold:  *threshold,
			SystemicReasons:  systemic,
			Labels:           labels,
			Priority:         *priority,
		},
	)

	switch sub {
	case "report":
		raw, err := io.ReadAll(d.stdin)
		if err != nil {
			fmt.Fprintf(d.stderr, "%s: read stdin: %v\n", prog, err)
			return exitError
		}
		return apply(ctx, esc, pr, raw, d)
	default: // submit
		req, err := io.ReadAll(d.stdin)
		if err != nil {
			fmt.Fprintf(d.stderr, "%s: read request from stdin: %v\n", prog, err)
			return exitError
		}
		cargs := []string{"pr", "review", "submit", pr}
		if *submitBackend != "" {
			cargs = append(cargs, "--backend", *submitBackend)
		}
		res, err := d.runner(*submitTimeout).Run(ctx, *connector, cargs, req, nil)
		if err != nil {
			fmt.Fprintf(d.stderr, "%s: pr review submit: %v\n", prog, err)
			return exitError
		}
		// The worker's view of the submit is unchanged, byte for byte.
		_, _ = d.stdout.Write(res.Stdout)
		_, _ = d.stderr.Write(res.Stderr)
		if res.ExitCode != 0 {
			// A failed submit has no outcome to escalate; pg-connector's own
			// coarse code (1 or 4) is passed through, anything else is generic.
			if res.ExitCode == 4 {
				return 4
			}
			return exitError
		}
		return apply(ctx, esc, pr, res.Stdout, d)
	}
}

// apply parses a submit output and applies it. Every escalation failure is
// logged on stderr, one line each, and turns into exit 3.
func apply(ctx context.Context, esc *escalate.Escalator, pr string, raw []byte, d deps) int {
	o, err := escalate.ParseOutcome(pr, raw)
	if err != nil {
		fmt.Fprintf(d.stderr, "%s: %v\n", prog, err)
		return exitError
	}
	rep, err := esc.Handle(ctx, o)
	for _, a := range rep.Actions {
		fmt.Fprintf(d.stderr, "%s: %s: %s\n", prog, pr, a)
	}
	if err != nil {
		for _, line := range splitErrors(err) {
			fmt.Fprintf(d.stderr, "%s: ESCALATION FAILED for %s (%s): %s\n", prog, pr, o.Status, line)
		}
		return exitDelivery
	}
	return exitOK
}

// splitErrors flattens an errors.Join into its member messages.
func splitErrors(err error) []string {
	var multi interface{ Unwrap() []error }
	if errors.As(err, &multi) {
		var out []string
		for _, e := range multi.Unwrap() {
			out = append(out, splitErrors(e)...)
		}
		return out
	}
	return []string{err.Error()}
}
