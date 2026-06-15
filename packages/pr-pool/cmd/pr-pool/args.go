package main

import (
	"errors"
	"flag"
	"io"
	"strings"
)

// usageLine is the short synopsis printed to stderr on a usage error.
const usageLine = "usage: pr-pool [--version | --help] [drain]"

// helpText is the full help printed to stdout for --help/help. pr-pool takes no
// config flags — its entire configuration surface is PR_POOL_* environment
// variables — so --help is the only place an operator can discover them.
const helpText = usageLine + `

pr-pool runs one drain pass: it discovers ready beads, dispatches a Claude
session per role (feedback, then worker) up to each role's cap, waits for
completion, then tears down every pr-pool-* tmux session. Bare "pr-pool" is
equivalent to "pr-pool drain".

Subcommands:
  drain              run one drain pass (the default when omitted)
  version            print the version and exit
  help               print this help and exit

Configuration is via PR_POOL_* environment variables (there are no flags).
Common ones (see internal/config for the full set and defaults):
  PR_POOL_MAX_WORKER       max concurrent worker dispatches (default 1)
  PR_POOL_MAX_FEEDBACK     max concurrent feedback dispatches (default 1)
  PR_POOL_BUDGET_TOKENS    per-worker token budget; 0 = unlimited (default 0)
  PR_POOL_BUDGET_COST      per-worker cost budget in cents; 0 = unlimited (default 0)
  PR_POOL_BUDGET_TIME      per-worker wall-clock budget in seconds (default 1500)
  PR_POOL_MODEL            claude model override (default: ccpool's default)
  PR_POOL_EFFORT           claude --effort value (default max)
  PR_POOL_DANGEROUS        run workers with --dangerously-skip-permissions (default on)
  PR_POOL_REPO_ROOT        monorepo root the drain operates in (default: cwd)
  PR_POOL_BEADS_PREFIX     expected bead prefix, asserted at precheck (default zr)`

// routeKind enumerates what the program should do after parsing argv. Keeping
// the decision pure (no I/O, no side effects) is what guarantees a help/version
// request or a parse error can never fall through to a real drain — a drain
// dispatches Claude sessions and tears down every pr-pool-* tmux session, so
// fail-open on a parse error is a real foot-gun (pg2-52rn).
type routeKind int

const (
	routeDrain    routeKind = iota // run a drain with .rest as the subcommand args
	routeVersion                   // print the version and exit 0
	routeHelp                      // print usage and exit 0
	routeUsageErr                  // print .msg + usage to stderr and exit 2
)

type routeResult struct {
	kind routeKind
	rest []string // drain subcommand args (routeDrain only)
	msg  string   // diagnostic for routeUsageErr
}

// route inspects the full argv and decides what to do, without side effects. No
// subcommand ⇒ drain. Top-level --version/-v and --help/-h are recognised here
// so they cannot be misrouted to a drain; any other leading flag, or any unknown
// subcommand, is a usage error rather than a silent fall-through to drain.
//
// Per CLI convention (GNU Coding Standards §4.7; clap/cobra/click), a leading
// --version/-v or --help/-h SHORT-CIRCUITS: it is honoured and exits 0,
// deliberately ignoring any trailing arguments (so `pr-pool --version drain`
// prints the version, not a usage error). A subcommand-scoped flag like
// `pr-pool drain --version` is NOT a global request — drain defines no such
// flag, so it is reported as an unknown flag (exit 2), matching cobra/docker.
func route(argv []string) routeResult {
	args := argv[1:] // strip program name
	if len(args) == 0 {
		return routeResult{kind: routeDrain}
	}
	switch args[0] {
	case "version", "--version", "-v":
		return routeResult{kind: routeVersion}
	case "help", "--help", "-h":
		return routeResult{kind: routeHelp}
	case "drain":
		return routeResult{kind: routeDrain, rest: args[1:]}
	}
	if strings.HasPrefix(args[0], "-") {
		return routeResult{kind: routeUsageErr, msg: "unknown flag: " + args[0]}
	}
	return routeResult{kind: routeUsageErr, msg: "unknown subcommand: " + args[0]}
}

// parseDrainArgs validates the drain subcommand's own args. It is pure: it
// reports a routeKind and never runs the drain itself, so the caller can refuse
// to touch config/precheck/DrainOnce on a parse error or help request. The drain
// subcommand accepts no positionals; a help flag yields routeHelp and any other
// parse failure (or an unexpected positional) yields routeUsageErr.
func parseDrainArgs(args []string) routeResult {
	fs := flag.NewFlagSet("drain", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves; suppress flag's defaults
	pos, err := parseInterspersed(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return routeResult{kind: routeHelp}
	case err != nil:
		// drain defines no flags, so the first dash-prefixed token is the
		// offender. Report it in the same "unknown flag: X" phrasing as the
		// top-level route (the stdlib's "flag provided but not defined: -x"
		// single-dashes the flag and reads differently).
		return routeResult{kind: routeUsageErr, msg: "unknown flag: " + firstFlag(args)}
	case len(pos) > 0:
		return routeResult{kind: routeUsageErr, msg: "unexpected argument: " + pos[0]}
	}
	return routeResult{kind: routeDrain}
}

// firstFlag returns the first dash-prefixed token in args (the offending flag on
// a drain parse error), or "flag" if none is found.
func firstFlag(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return a
		}
	}
	return "flag"
}

// parseInterspersed parses a FlagSet allowing flags to appear before, after, or
// between positional arguments. Go's stdlib flag stops at the first positional,
// silently dropping any flags after it. This walks the args, collecting
// positionals and re-parsing the remainder. It returns the positionals and the
// first parse error (including flag.ErrHelp); callers MUST inspect the error —
// swallowing it is what let -h/--help and unknown flags trigger a drain.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return positionals, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
