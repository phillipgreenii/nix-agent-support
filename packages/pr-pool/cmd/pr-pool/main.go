package main

import (
	"fmt"
	"os"
)

var version = "dev"

// pickSubcommand returns the subcommand and remaining args. No subcommand ⇒ "drain".
func pickSubcommand(args []string) (cmd string, rest []string) {
	known := map[string]bool{"drain": true, "version": true}
	if len(args) < 2 {
		return "drain", nil
	}
	if known[args[1]] {
		return args[1], args[2:]
	}
	return "drain", args[1:]
}

func main() {
	cmd, rest := pickSubcommand(os.Args)
	switch cmd {
	case "drain":
		os.Exit(runDrain(rest))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
}
