package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const progName = "pg-connector-issue-beads"

// errUsage wraps every argument-parse failure so run can map it to exit
// code 2 without confusing it with flag.ErrHelp (exit 0).
var errUsage = errors.New("usage")

// Budget flag defaults: the outputs of the phase-1 spike (design 4.4).
const (
	defaultBDMaxConcurrent = 9
	defaultBDMaxPerMinute  = 92
	defaultBDWaitBound     = 5 * time.Second
	defaultBDWriteReserve  = 2
)

// options is the parsed command line: the tracker this instance serves and the
// settings of the bd budget (design 4.4), which are read from the backend's own
// rendered flags rather than from per-request config because the runner is
// built before any request.
type options struct {
	// beadsDir is the --beads-dir value, "" when the flag is absent.
	beadsDir string
	// maxConcurrent is --bd-max-concurrent, the number of bd processes in flight.
	maxConcurrent int
	// maxPerMinute is --bd-max-per-minute, the cap on bd starts per minute.
	maxPerMinute int
	// waitBound is --bd-wait-bound, how long a call waits before it is denied;
	// zero means no wait.
	waitBound time.Duration
	// writeReserve is --bd-write-reserve, the slots only writes may take.
	writeReserve int
	// budgetGroup is --budget-group: instances naming the same group share one
	// semaphore (and window); "" means one group per tracker directory.
	budgetGroup string
}

// parseArgs parses the command's own arguments (os.Args[1:]): the registry
// command of an instance is `pg-connector-issue-beads --beads-dir DIR` plus the
// optional bd budget flags (--bd-max-concurrent N, --bd-max-per-minute N,
// --bd-wait-bound DURATION, --bd-write-reserve N, --budget-group NAME), and
// the umbrella passes the command's arguments first and the JSON request on
// stdin (bead pg2-91y12, ADR 0062 amendment). Every flag also takes the
// --flag=value form. --beads-dir's value must be an absolute path: unlike the
// env vars (left unvalidated for compatibility) a flag is new surface, and a
// relative path would resolve against whatever cwd the umbrella was launched
// from, the defect ErrWorkspaceNotConfigured exists to prevent. The budget
// counts must be at least 1 (--bd-write-reserve at least 0 and below
// --bd-max-concurrent) and --bd-wait-bound a non-negative duration. Giving a
// flag twice is not an error: the last one wins. A parse failure is returned
// (wrapping errUsage), never printed here: the caller prints it exactly once.
// Only -h prints, the single usage line on stderr.
func parseArgs(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet(progName, flag.ContinueOnError)
	// The flag package would print its own copy of a parse error (and the
	// usage text) to its output; the caller reports the returned error
	// once, so keep the package silent and print usage only for -h.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	opts := options{
		maxConcurrent: defaultBDMaxConcurrent,
		maxPerMinute:  defaultBDMaxPerMinute,
		waitBound:     defaultBDWaitBound,
		writeReserve:  defaultBDWriteReserve,
	}
	given := false
	fs.Func("beads-dir", "absolute path of the bd tracker this instance serves (beats $PG_CONNECTOR_ISSUE_BEADS_DIR and $BEADS_DIR)", func(v string) error {
		given = true
		opts.beadsDir = v
		return nil
	})
	fs.Func("bd-max-concurrent", "bd processes allowed in flight at once (default 9)", func(v string) error {
		n, err := parseCount("--bd-max-concurrent", v, 1)
		opts.maxConcurrent = n
		return err
	})
	fs.Func("bd-max-per-minute", "bd processes allowed to start per minute (default 92)", func(v string) error {
		n, err := parseCount("--bd-max-per-minute", v, 1)
		opts.maxPerMinute = n
		return err
	})
	fs.Func("bd-wait-bound", "how long a call waits for the budget before it is denied as unavailable (default 5s; 0s waits not at all)", func(v string) error {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("--bd-wait-bound %q is not a duration: %v", v, err)
		}
		if d < 0 {
			return fmt.Errorf("--bd-wait-bound %q must not be negative", v)
		}
		opts.waitBound = d
		return nil
	})
	fs.Func("bd-write-reserve", "slots only writes may take (default 2)", func(v string) error {
		n, err := parseCount("--bd-write-reserve", v, 0)
		opts.writeReserve = n
		return err
	})
	fs.Func("budget-group", "name of the shared bd budget; instances using one dolt server share it (default: one per tracker directory)", func(v string) error {
		opts.budgetGroup = strings.TrimSpace(v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "usage: %s [--beads-dir DIR] [--bd-max-concurrent N] [--bd-max-per-minute N] [--bd-wait-bound DURATION] [--bd-write-reserve N] [--budget-group NAME]\n", progName)
			return options{}, err
		}
		return options{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("%w: %s: unexpected argument %q; the options are --beads-dir DIR and the --bd-*/--budget-group budget flags", errUsage, progName, fs.Arg(0))
	}
	if opts.writeReserve >= opts.maxConcurrent {
		return options{}, fmt.Errorf("%w: %s: --bd-write-reserve %d must be below --bd-max-concurrent %d", errUsage, progName, opts.writeReserve, opts.maxConcurrent)
	}
	if !given {
		return opts, nil
	}
	opts.beadsDir = strings.TrimSpace(opts.beadsDir)
	if opts.beadsDir == "" {
		return options{}, fmt.Errorf("%w: %s: --beads-dir must not be empty", errUsage, progName)
	}
	if !filepath.IsAbs(opts.beadsDir) {
		return options{}, fmt.Errorf("%w: %s: --beads-dir %q must be an absolute path (a relative path would resolve against the caller's cwd)", errUsage, progName, opts.beadsDir)
	}
	return opts, nil
}

// parseCount parses a whole number of at least min for the named flag.
func parseCount(name, v string, min int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a whole number", name, v)
	}
	if n < min {
		return 0, fmt.Errorf("%s %d must be at least %d", name, n, min)
	}
	return n, nil
}
