// pg-pr-change is the composed command a `pr-change` role runs for one PR id:
// it takes a per-PR lock, hydrates the PR with `pg-desk pr refresh`, refuses an
// unhydrated view, and runs `pg-decider plan|apply pr <id>`. The logic is in
// internal/prchange; this file is argument parsing and process wiring only.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/prchange"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version").
var Version = "dev"

const lockDirEnv = "PG_PR_CHANGE_LOCK_DIR"

const usage = `usage: pg-pr-change [--lock-dir DIR] [--lock-wait DURATION] <plan|apply> <id>

Hydrate one PR with "pg-desk pr refresh <id>" and then decide it with
"pg-decider <mode> pr <id>", holding a per-PR lock for the whole run.

  <id>   <owner>/<repo>#<number>, or the router's retry-window form
         <owner>/<repo>#<number>@<12 hex digits> (the same PR, the same lock)
  plan   read-only: print what the decider would do
  apply  apply the decider's plan

  --lock-dir DIR        directory of the per-PR lock files
                        (env ` + lockDirEnv + `; default: $XDG_STATE_HOME/pg-pr-change/locks)
  --lock-wait DURATION  longest wait for the lock (default 30s)

Exit codes are published in docs/behavior/pg-decider/pr-change.md; none is 1 or 9.
`

// defaultLockDir is where lock files go when neither the flag nor the
// environment names a directory.
func defaultLockDir() string {
	if v := os.Getenv(lockDirEnv); v != "" {
		return v
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		if home, err := os.UserHomeDir(); err == nil {
			state = filepath.Join(home, ".local", "state")
		} else {
			state = os.TempDir()
		}
	}
	return filepath.Join(state, "pg-pr-change", "locks")
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pg-pr-change", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lockDir := fs.String("lock-dir", defaultLockDir(), "")
	lockWait := fs.Duration("lock-wait", prchange.DefaultLockWait, "")
	showVersion := fs.Bool("version", false, "")
	help := fs.Bool("help", false, "")
	fs.BoolVar(help, "h", false, "")
	// Flags may follow the positional arguments, so parse until none is left.
	var pos []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintf(stderr, "pg-pr-change: usage: %v: run pg-pr-change --help\n", err)
			return prchange.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	switch {
	case *help:
		fmt.Fprint(stdout, usage)
		return prchange.ExitOK
	case *showVersion:
		fmt.Fprintln(stdout, "pg-pr-change", Version)
		return prchange.ExitOK
	}
	if len(pos) != 2 {
		fmt.Fprintf(stderr, "pg-pr-change: usage: want <plan|apply> <id>, got %d argument(s): run pg-pr-change --help\n", len(pos))
		return prchange.ExitUsage
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "pg-pr-change: usage: %v: run pg-decider plan pr <id> to see the same configuration error\n", err)
		return prchange.ExitUsage
	}
	return prchange.Run(ctx, prchange.Options{
		Mode:     pos[0],
		RawID:    pos[1],
		LockDir:  *lockDir,
		LockWait: *lockWait,
		Config:   cfg,
		Stdout:   stdout,
		Stderr:   stderr,
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
