package main

import (
	"flag"
	"fmt"
	"io"
)

// commandHelp is the --help text of one command: a one-line summary, the
// usage line and free-form notes. The flag list is generated from the flag set
// so it cannot drift from the code.
type commandHelp struct {
	summary string
	usage   string
	notes   []string
}

// Exit codes (ADR 0042) as one help paragraph, shared by the top-level help
// and the commands whose codes go beyond 0, 1 and 2.
const exitCodesNote = "Exit codes: 0 ok, 1 unexpected failure, 2 usage error, 3 state directory unwritable, 4 config invalid, 5 keeper down (status --check), 6 degraded (status --check)."

// Notes that label the two long-running commands a person does not run by
// hand.
const (
	keeperRunNote = "Long-running mode, not for interactive use: launchd starts and restarts it; it is not typed at a prompt."
	anchorRunNote = "Long-running mode, not for interactive use: the keeper starts it; it is not typed at a prompt."
)

// commandHelps holds the help of every command, keyed by the flag set's name
// ("config check" for the nested command).
var commandHelps = map[string]commandHelp{
	"acquire": {
		summary: "Create or refresh a keep-awake lease and print its id.",
		usage:   acquireUsage,
		notes: []string{
			"A lease is one reason to keep the Mac awake, named by --owner and --key. Repeating the command refreshes the lease.",
			"With neither --pid nor --for the lease is bound to the caller's parent process; --for alone is a pure timer. A --pid that is not running creates no lease, warns and exits 0.",
			"-d, -i, -m and -s follow caffeinate; -i is the default.",
		},
	},
	"release": {
		summary: "Remove one lease of an owner, or every lease of the owner with --all.",
		usage:   releaseUsage,
		notes:   []string{"A lease that does not exist is not an error: the exit status is 0."},
	},
	"run": {
		summary: "Hold a lease for the life of one command.",
		usage:   runUsage,
		notes: []string{
			"The command's exit status passes through; a command killed by a signal gives 128 plus the signal number, and a command not found gives 127. The lease ends when the command does.",
		},
	},
	"status": {
		summary: "Show what holds the Mac awake: the keeper, the assertion, the battery, every lease and every external holder.",
		usage:   "usage: pg-keep-awake status [--json] [--check]",
		notes: []string{
			"It works with the keeper down. --json prints exactly one JSON object; --check turns the exit status into a health check.",
			exitCodesNote,
		},
	},
	"pause": {
		summary: "Drop the keep-awake assertion without removing any lease.",
		usage:   "usage: pg-keep-awake pause [--for DUR]",
		notes:   []string{"Without --for the pause lasts until `pg-keep-awake resume`."},
	},
	"resume": {
		summary: "End a pause; the assertion returns while leases are live.",
		usage:   "usage: pg-keep-awake resume",
		notes:   []string{"With no pause in effect it does nothing and exits 0."},
	},
	"config check": {
		summary: "Validate a configuration file without changing anything.",
		usage:   "usage: pg-keep-awake config check [--config PATH]",
		notes: []string{
			"Without --config it checks the keeper's own config.toml; an absent file is valid there (the built-in defaults apply). An explicit --config PATH that does not exist is invalid.",
			"Exit 0 when valid, 4 when invalid.",
		},
	},
	"keeper": {
		summary: "Run the keeper: the one process that holds the keep-awake assertion.",
		usage:   "usage: pg-keep-awake keeper",
		notes: []string{
			keeperRunNote,
			"It takes no flags: the state and config locations come from XDG_STATE_HOME and XDG_CONFIG_HOME. A second keeper in the same state directory exits at once. SIGTERM ends it cleanly.",
		},
	},
	"anchor": {
		summary: "Run the watchdog that the keeper's caffeinate follows.",
		usage:   "usage: pg-keep-awake anchor --release-pct N --blind-pct N --watchdog DUR --heartbeat FILE --interval DUR --state-dir DIR",
		notes: []string{
			anchorRunNote,
			"It gets every parameter on its command line; it never reads config.toml. Every flag is required.",
		},
	},
}

// configGroupHelp is the help of `config` itself.
var configGroupHelp = commandHelp{
	summary: "Configuration commands.",
	usage:   "usage: pg-keep-awake config check [--config PATH]",
	notes:   []string{"check validates a configuration file; try `pg-keep-awake config check --help`."},
}

// topHelp is the help of the command without a subcommand.
const topHelp = `pg-keep-awake: the single holder of the Mac keep-awake assertion

usage: pg-keep-awake <command> [args]
       pg-keep-awake <command> --help

The command line is the only supported interface. The state files are private.

Commands for clients:
  acquire       create or refresh a keep-awake lease and print its id
  release       remove a lease (or every lease of an owner with --all)
  run           hold a lease for the life of one command

Commands for the operator:
  status        show what holds the Mac awake (--json, --check)
  pause         drop the assertion for a while, keeping every lease
  resume        end a pause
  config check  validate a configuration file

Long-running modes, not for interactive use (started by launchd and the keeper):
  keeper        the one process that holds the assertion
  anchor        the watchdog the keeper's caffeinate follows

Other:
  help [command]   same as <command> --help
  --version        print the version

` + exitCodesNote + "\n"

// printHelp writes a command's help, then its flags from set, to out.
func printHelp(out io.Writer, h commandHelp, set *flag.FlagSet) {
	fmt.Fprintln(out, h.summary)
	fmt.Fprintln(out)
	fmt.Fprintln(out, h.usage)
	for _, n := range h.notes {
		fmt.Fprintln(out)
		fmt.Fprintln(out, n)
	}
	if set == nil {
		return
	}
	n := 0
	set.VisitAll(func(*flag.Flag) { n++ })
	if n == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "flags:")
	prev := set.Output()
	set.SetOutput(out)
	set.PrintDefaults()
	set.SetOutput(prev)
}

// isHelpFlag reports whether arg asks for help, as package flag does.
func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}
