// Command pg-keep-awake is the single holder of the Mac keep-awake assertion
// and its command line, the only supported interface to it. Every command
// answers --help (help.go).
package main

import (
	"fmt"
	"io"
	"os"
)

// Version is stamped by mkGoApp's default ldflag target (-X main.Version=...).
var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the subcommands and returns the process exit code. It is the
// extension point: each later packet adds a case for its subcommand.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pg-keep-awake <command> [args]; try --help")
		return 2
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintf(stdout, "pg-keep-awake %s\n", Version)
		return 0
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "acquire":
		return runAcquire(args[1:], stdout, stderr)
	case "release":
		return runRelease(args[1:], stdout, stderr)
	case "run":
		return runCommand(args[1:], stdout, stderr)
	case "pause":
		return runPause(args[1:], stdout, stderr)
	case "resume":
		return runResume(args[1:], stdout, stderr)
	case "keeper":
		return runKeeper(args[1:], stdout, stderr)
	case "anchor":
		return runAnchor(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "--help", "-h", "-help":
		fmt.Fprint(stdout, topHelp)
		return 0
	case "help":
		if len(args) == 1 {
			fmt.Fprint(stdout, topHelp)
			return 0
		}
		// `help <command...>` is `<command...> --help`.
		return run(append(append([]string{}, args[1:]...), "--help"), stdout, stderr)
	}
	fmt.Fprintf(stderr, "pg-keep-awake: unknown command %q\n", args[0])
	return 2
}
