package main

import (
	"fmt"
	"os"
)

var version = "dev"

// pickSubcommand returns the subcommand name and the remaining args.
// With no subcommand, defaults to "list".
func pickSubcommand(args []string) (cmd string, rest []string) {
	known := map[string]bool{
		"attach":  true,
		"attend":  true,
		"cancel":  true,
		"close":   true,
		"doctor":  true,
		"hook":    true,
		"list":    true,
		"new":     true,
		"reap":    true,
		"reply":   true,
		"tail":    true,
		"version": true,
	}
	if len(args) < 2 {
		return "list", nil
	}
	if known[args[1]] {
		return args[1], args[2:]
	}
	return "list", args[1:]
}

func main() {
	cmd, rest := pickSubcommand(os.Args)
	switch cmd {
	case "attach":
		os.Exit(runAttach(rest))
	case "attend":
		os.Exit(runAttend(rest))
	case "cancel":
		os.Exit(runCancel(rest))
	case "close":
		os.Exit(runClose(rest))
	case "doctor":
		os.Exit(runDoctor(rest))
	case "hook":
		os.Exit(runHook(rest))
	case "list":
		os.Exit(runList(rest))
	case "new":
		os.Exit(runNew(rest))
	case "reap":
		os.Exit(runReap(rest))
	case "reply":
		os.Exit(runReply(rest))
	case "tail":
		os.Exit(runTail(rest))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
}
