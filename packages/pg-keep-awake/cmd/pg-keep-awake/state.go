package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// exitStateDir is ADR 0042's "state directory unwritable" code. The other
// codes live in config_check.go.
const exitStateDir = 3

// defaultKey is the lease key used when --key is not given.
const defaultKey = "default"

// newFlagSet builds a flag set that reports parse errors on stderr and does
// not exit, so the subcommand can map them to exit 2. Its Usage is silent:
// parseFlags prints the command's help (stdout for --help) or its usage line
// (stderr for a mistake).
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() {}
	return set
}

// parseFlags parses args and reports (exit, done). done is true when the
// caller must return exit now: a parse failure (the error and the usage line
// go to stderr, exit 2) or -h/--help (the full help goes to stdout, exit 0).
func parseFlags(set *flag.FlagSet, args []string, stdout io.Writer) (exit int, done bool) {
	if err := set.Parse(args); err != nil {
		h := commandHelps[set.Name()]
		if errors.Is(err, flag.ErrHelp) {
			printHelp(stdout, h, set)
			return exitOK, true
		}
		fmt.Fprintln(set.Output(), h.usage)
		return exitUsage, true
	}
	return 0, false
}

// usageError prints a diagnostic and returns the usage exit code.
func usageError(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "pg-keep-awake: "+format+"\n", a...)
	return exitUsage
}

// flagSet collects the -d -i -m -s caffeinate-style flags.
type flagSet struct{ d, i, m, s bool }

func (f *flagSet) register(set *flag.FlagSet) {
	set.BoolVar(&f.d, "d", false, "prevent the display from sleeping")
	set.BoolVar(&f.i, "i", false, "prevent the system from idle sleeping (default when no flag is given)")
	set.BoolVar(&f.m, "m", false, "prevent the disk from idle sleeping")
	set.BoolVar(&f.s, "s", false, "prevent the system from sleeping on AC power")
}

// list returns the chosen flags in d i m s order; with none chosen it is the
// default, just "i".
func (f *flagSet) list() []string {
	var out []string
	for _, c := range []struct {
		on   bool
		name string
	}{{f.d, "d"}, {f.i, "i"}, {f.m, "m"}, {f.s, "s"}} {
		if c.on {
			out = append(out, c.name)
		}
	}
	if len(out) == 0 {
		return []string{"i"}
	}
	return out
}

// checkOwnerKey validates the id parts, returning an error message or "".
func checkOwnerKey(owner, key string) string {
	if !lease.ValidIDPart(owner) {
		return fmt.Sprintf("--owner %q must match ^[A-Za-z0-9_-]{1,64}$", owner)
	}
	if !lease.ValidIDPart(key) {
		return fmt.Sprintf("--key %q must match ^[A-Za-z0-9_-]{1,64}$", key)
	}
	return ""
}

// parseDuration parses a --for value; it must be a positive Go duration.
func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", s)
	}
	return d, nil
}

// isStateDirFailure reports whether err means the state directory cannot be
// written: the directory is refused, the lock cannot be taken in time, or the
// filesystem says permission denied or read-only.
func isStateDirFailure(err error) bool {
	return errors.Is(err, lease.ErrStateDir) ||
		errors.Is(err, lease.ErrLockTimeout) ||
		errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.EROFS)
}

// failState prints err and returns exit 3 for an unwritable state directory
// and exit 1 for anything else.
func failState(stderr io.Writer, cmd string, err error) int {
	fmt.Fprintf(stderr, "pg-keep-awake: %s: %v\n", cmd, err)
	if isStateDirFailure(err) {
		return exitStateDir
	}
	return exitUnexpected
}

// ensureStateDir resolves the state directory and creates it when missing.
func ensureStateDir() (*lease.Dir, error) {
	path, err := lease.ResolveStateDir()
	if err != nil {
		return nil, err
	}
	return lease.EnsureDir(path)
}

// existingStateDir resolves the state directory without creating it. A
// missing directory is fs.ErrNotExist.
func existingStateDir() (*lease.Dir, error) {
	path, err := lease.ResolveStateDir()
	if err != nil {
		return nil, err
	}
	return lease.CheckDir(path)
}
