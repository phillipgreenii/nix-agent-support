package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "dev"

// pickSubcommand inspects os.Args-style input and returns the subcommand
// name plus the remaining args (minus the subcommand token).
//
// Rules:
//   - If args[1] is a known subcommand name, that wins; the rest are its args.
//   - Otherwise the command is "tui" and args[1:] are its args.
//   - The flag-first case (e.g. --wait-until-idle) routes to tui because
//     no current TUI flags collide with a subcommand name.
func pickSubcommand(args []string) (cmd string, rest []string) {
	known := map[string]bool{
		"daemon":                     true,
		"status":                     true,
		"agents-busy-check":          true,
		"wait-until-agents-finished": true,
		"config":                     true,
		"caffeinate":                 true,
		"nudge":                      true,
		"info":                       true,
		"cmux-bridge":                true,
		"auto-resume":                true,
	}
	if len(args) < 2 {
		return "tui", nil
	}
	if known[args[1]] {
		return args[1], args[2:]
	}
	return "tui", args[1:]
}

func main() {
	cmd, rest := pickSubcommand(os.Args)
	switch cmd {
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
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
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
