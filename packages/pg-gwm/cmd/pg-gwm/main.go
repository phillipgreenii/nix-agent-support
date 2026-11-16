// pg-gwm manages worktrees of a git repository: a bounded set of leased,
// reusable checkouts. This build carries only the metrics exporter (serve)
// and version; the lease commands arrive with the manager.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/x/exitcode"
)

// Version is injected at build time by mkGoApp.
var Version = "dev"

const exitUsage = exitcode.Usage

func main() {
	os.Exit(newApp(os.Stdout, os.Stderr).run(os.Args[1:]))
}

type app struct {
	stdout, stderr io.Writer
}

func newApp(stdout, stderr io.Writer) *app { return &app{stdout: stdout, stderr: stderr} }

const usage = `usage: pg-gwm <command> [flags]

commands:
  serve    [--addr 127.0.0.1:9148]   loopback /metrics
  version

serve opens the opt-in pprof endpoint only when ` + pprofEnv + ` is set (absolute unix-socket path or loopback host:port).`

func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "pg-gwm: %v\n", err)
	return exitcode.Error
}

func (a *app) run(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(a.stderr, usage)
		if len(args) == 0 {
			return exitUsage
		}
		return 0
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(a.stdout, Version)
		return 0
	case "serve":
		return a.serve(args[1:], nil)
	}
	fmt.Fprintf(a.stderr, "pg-gwm: unknown command %q\n", args[0])
	return exitUsage
}
