package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

var version = "dev"

// subcommandNames is the ordered, canonical list of top-level subcommands.
// It is the single source of truth for both dispatch (see knownSubcommands)
// and the top-level usage text (see usageText). "tui" is included because it
// can be requested explicitly, and it is also the default when no subcommand
// is given.
var subcommandNames = []string{
	"daemon",
	"status",
	"agents-busy-check",
	"wait-until-agents-finished",
	"config",
	"caffeinate",
	"nudge",
	"info",
	"cmux-bridge",
	"auto-resume",
	"tui",
}

// knownSubcommands is the set form of subcommandNames for O(1) lookup.
var knownSubcommands = func() map[string]bool {
	m := make(map[string]bool, len(subcommandNames))
	for _, n := range subcommandNames {
		m[n] = true
	}
	return m
}()

// usageText returns the top-level help/usage listing every subcommand.
func usageText() string {
	var b strings.Builder
	b.WriteString("pa-monitor - monitor Personal Agents\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  pa-monitor [<subcommand>] [flags]\n\n")
	b.WriteString("With no subcommand, pa-monitor launches the TUI.\n\n")
	b.WriteString("Subcommands:\n")
	for _, n := range subcommandNames {
		b.WriteString("  " + n + "\n")
	}
	b.WriteString("\nExamples:\n")
	b.WriteString("  pa-monitor nudge <session:<id>|path:<p>|cmux:<ws>> [--text=...] [--cancel]\n")
	return b.String()
}

// pickSubcommand inspects os.Args-style input and returns the subcommand
// name plus the remaining args (minus the subcommand token).
//
// Rules:
//   - No args: the command is "tui" (the default when invoked bare).
//   - "-h"/"--help": the command is "help" (top-level usage).
//   - If args[1] is a known subcommand name, that wins; the rest are its args.
//   - A leading flag other than -h/--help (e.g. --version) routes to tui,
//     since no current TUI flag collides with a subcommand name. Note this
//     routes UNKNOWN leading flags there too, where runTUI's flag set rejects
//     them with exit 2 — including the removed `--wait-until-idle` (ADR 0011
//     replaced it with the `wait-until-agents-finished` subcommand).
//   - Any other first arg is unrecognized: it is returned as the command
//     itself so main can emit an "unknown subcommand" error (it MUST NOT fall
//     through to the TUI).
func pickSubcommand(args []string) (cmd string, rest []string) {
	if len(args) < 2 {
		return "tui", nil
	}
	if args[1] == "-h" || args[1] == "--help" {
		return "help", args[2:]
	}
	if knownSubcommands[args[1]] {
		return args[1], args[2:]
	}
	if strings.HasPrefix(args[1], "-") {
		return "tui", args[1:]
	}
	return args[1], args[2:]
}

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// run dispatches os.Args-style input and returns a process exit code. The
// pure meta-commands (help/unknown) are handled here against the provided
// writers so they are testable without shelling out; known subcommands are
// delegated to their runners (which manage their own exit behavior).
func run(args []string, stdout, stderr io.Writer) int {
	cmd, rest := pickSubcommand(args)
	switch cmd {
	case "help":
		fmt.Fprint(stdout, usageText())
		return 0
	case "daemon":
		runDaemon(rest)
	case "status":
		runStatus(rest)
	case "agents-busy-check":
		runAgentsBusyCheck(rest)
	case "wait-until-agents-finished":
		runWaitUntilAgentsFinished(rest)
	case "config":
		runConfigSubcommand(rest)
	case "caffeinate":
		runCaffeinate(rest)
	case "nudge":
		runNudge(rest)
	case "info":
		runInfo(rest)
	case "cmux-bridge":
		runCmuxBridge(rest)
	case "auto-resume":
		runAutoResume(rest)
	case "tui":
		runTUI(rest)
	default:
		fmt.Fprintf(stderr, "unknown subcommand: %s\n", cmd)
		fmt.Fprintf(stderr, "Run 'pa-monitor --help' for usage.\n")
		return 2
	}
	return 0
}

// runConfigSubcommand dispatches `config show` (only "show" supported v1).
func runConfigSubcommand(args []string) {
	if len(args) == 0 || args[0] == "show" {
		runConfigShow(args)
		return
	}
	fmt.Fprintf(os.Stderr, "config: unknown action %q (only 'show' supported)\n", args[0])
	os.Exit(2)
}

func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print version")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if *showVersion {
		fmt.Println("pa-monitor", version)
		return
	}

	runTUIRemote()
}
