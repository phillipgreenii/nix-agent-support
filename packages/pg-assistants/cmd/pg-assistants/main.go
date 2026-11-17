// Command pg-assistants keeps configured, permanent Claude Code assistants
// running in tmux and recycles each session before its context fills. This
// build carries only `config check`; the other subcommands are added in turn.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/x/exitcode"
)

// Version is stamped by mkGoApp's default ldflag target (-X main.Version=...).
var Version = "dev"

const topHelp = `pg-assistants: a minimal supervisor for permanent Claude Code assistants.

usage: pg-assistants <command> [args]

commands:
  config check --config PATH   validate a configuration file

  --version                    print the version
  --help                       print this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the subcommands and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pg-assistants <command> [args]; try --help")
		return exitcode.Usage
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintf(stdout, "pg-assistants %s\n", Version)
		return exitcode.OK
	case "--help", "-h", "-help", "help":
		fmt.Fprint(stdout, topHelp)
		return exitcode.OK
	case "config":
		return runConfig(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "pg-assistants: unknown command %q; try --help\n", args[0])
	return exitcode.Usage
}
