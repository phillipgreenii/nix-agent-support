// pg-gwm manages worktrees of a git repository: a bounded set of leased,
// reusable checkouts. This build carries lease, renew, release, expire, info, list, reap, completion, the metrics
// exporter (serve) and version; the other commands arrive with the manager.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/state"
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
	// isTerminal decides whether stdout is a terminal (text output) or not
	// (JSON); tests replace it.
	isTerminal func(*os.File) bool
	// git is the runner every git call goes through; tests give it a
	// hermetic one.
	git gitrun.Git
	// counters and label count refusals once a repository is open.
	counters state.Counters
	label    string
}

func newApp(stdout, stderr io.Writer) *app {
	return &app{stdout: stdout, stderr: stderr, isTerminal: defaultIsTerminal}
}

const usage = `usage: pg-gwm <command> [flags]

commands:
  lease    [--name N [--or-renew]] [--owner O] [--metadata k=v]... [--ttl D|none]
           [-b B | -B B | --detach] [<commit-ish>] [--wait D] [--print=path]
  renew    <lease id> | --name N [--ttl D] [--metadata k=v]... [--print=path]
  release  <lease id> | --name N
  expire   <lease id> | --name N
  info     <key> | --path P | --id W | --lease-id L | --name N [--print=path]
  list     [--include-unmanaged]
  reap     [--all-repos] [--dry-run] [--deadline D]
  preserved list
  preserved prune [--older-than D] [--dry-run]
  config check [--config PATH]   validate the configuration file
  completion <bash|zsh>
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
	case "lease":
		return a.lease(args[1:])
	case "renew":
		return a.renew(args[1:])
	case "release":
		return a.release(args[1:])
	case "expire":
		return a.expire(args[1:])
	case "info":
		return a.info(args[1:])
	case "list":
		return a.list(args[1:])
	case "reap":
		return a.reap(args[1:])
	case "preserved":
		return a.preserved(args[1:])
	case "config":
		return a.configCmd(args[1:])
	case "completion":
		return a.completion(args[1:])
	case "serve":
		return a.serve(args[1:], nil)
	}
	fmt.Fprintf(a.stderr, "pg-gwm: unknown command %q\n", args[0])
	return exitUsage
}
