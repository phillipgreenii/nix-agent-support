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
		"hook":    true,
		"list":    true,
		"new":     true,
		"reply":   true,
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
	case "hook":
		os.Exit(runHook(rest))
	case "list":
		os.Exit(runList(rest))
	case "new":
		os.Exit(runNew(rest))
	case "reply":
		os.Exit(runReply(rest))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
}
