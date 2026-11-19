// Command pg-assistants keeps configured, permanent Claude Code assistants
// running in tmux and recycles each session before its context fills (ADR
// 0128). `daemon` is the supervisor launchd keeps alive; `status`, `attach`,
// `exit`, `trust` and `config check` are its command line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/exitcode"
	"github.com/phillipgreenii/x/osdirs"
)

// Version is stamped by mkGoApp's default ldflag target (-X main.Version=...).
var Version = "dev"

const topHelp = `pg-assistants: a minimal supervisor for permanent Claude Code assistants.

usage: pg-assistants <command> [args]

commands:
  daemon --config PATH         run the supervisor (started by launchd)
  status [--json] [--check]    show each assistant's state; --check is the health probe
  attach <id>                  attach to an assistant's tmux session (detach with C-b d)
  exit                         ask the daemon to stop this session (run inside a session)
  trust --claude-json PATH <cwd>...
                               mark directories trusted in Claude's .claude.json
  config check --config PATH   validate a configuration file

  --version                    print the version
  --help                       print this help

status and attach read the configuration at --config, by default
$XDG_CONFIG_HOME/pg-assistants/config.toml.
`

// tmuxSocket is the assistants' own tmux server: tmux -L pg-assistants.
const tmuxSocket = "pg-assistants"

// cli is the process as the subcommands see it. main fills it with the real
// process; the tests replace every seam.
type cli struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	environ        func() []string
	// run makes every tmux and pg-claude-control call.
	run cmdrun.Runner
	// exec replaces the process image (syscall.Exec); attach's last step.
	exec func(argv0 string, argv, env []string) error
	// notify and exit are the daemon's lifecycle.Options.Notify and Exit; nil
	// selects signal.NotifyContext and os.Exit.
	notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	exit   func(code int)
	pid    int
	newID  func() string // a fresh session id, a UUID v4
}

func main() {
	c := &cli{
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		getenv:  os.Getenv,
		environ: os.Environ,
		run:     cmdrun.Exec,
		exec:    syscall.Exec,
		pid:     os.Getpid(),
		newID:   newUUID,
	}
	// The root context: the daemon's lifecycle group derives the signal
	// context from it, and every call below takes it from there.
	os.Exit(c.main(context.Background(), os.Args[1:]))
}

// main dispatches the subcommands and returns the process exit code.
func (c *cli) main(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr, "usage: pg-assistants <command> [args]; try --help")
		return exitcode.Usage
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintf(c.stdout, "pg-assistants %s\n", Version)
		return exitcode.OK
	case "--help", "-h", "-help", "help":
		fmt.Fprint(c.stdout, topHelp)
		return exitcode.OK
	case "config":
		return runConfig(args[1:], c.stdout, c.stderr)
	case "daemon":
		return c.daemon(ctx, args[1:])
	case "status":
		return c.status(args[1:])
	case "attach":
		return c.attach(ctx, args[1:])
	case "exit":
		return c.exitCmd(args[1:])
	case "trust":
		return c.trust(args[1:])
	}
	fmt.Fprintf(c.stderr, "pg-assistants: unknown command %q; try --help\n", args[0])
	return exitcode.Usage
}

// newFlagSet is a subcommand's flag set: errors to stderr, never exiting.
func (c *cli) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	return fs
}

// parse parses fs over args and returns the positional arguments, allowing a
// flag after one (attach <id> --config PATH). done is set when the command
// has finished: help was printed (exit 0) or a flag was wrong (exit 2).
func (c *cli) parse(fs *flag.FlagSet, args []string, help string) (pos []string, code int, done bool) {
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(c.stdout, help)
				return nil, exitcode.OK, true
			}
			return nil, exitcode.Usage, true
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, 0, false
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
}

// configFlag registers --config with the default path shown in help.
func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", "", "the configuration file (default $XDG_CONFIG_HOME/pg-assistants/config.toml)")
}

// loadConfig loads the configuration at path, or at the default path when it
// is empty. A failure is printed and returns exit 7.
func (c *cli) loadConfig(cmd, path string) (config.Config, int, bool) {
	cfg, _, code, ok := c.loadConfigRaw(cmd, path)
	return cfg, code, ok
}

// loadConfigRaw is loadConfig that also returns the file's bytes, for the
// caller that hashes them.
func (c *cli) loadConfigRaw(cmd, path string) (config.Config, []byte, int, bool) {
	if path == "" {
		dir, err := osdirs.ConfigDir("pg-assistants")
		if err != nil {
			fmt.Fprintf(c.stderr, "pg-assistants %s: %v\n", cmd, err)
			return config.Config{}, nil, exitcode.Config, false
		}
		path = filepath.Join(dir, "config.toml")
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var cfg config.Config
		if cfg, err = config.Parse(data); err == nil {
			return cfg, data, exitcode.OK, true
		}
	} else {
		err = fmt.Errorf("read config: %w", err)
	}
	fmt.Fprintf(c.stderr, "pg-assistants %s: %s: %v\n", cmd, path, err)
	return config.Config{}, nil, exitcode.Config, false
}

// The files in the state directory the subcommands share.
func statusPath(stateDir string) string   { return filepath.Join(stateDir, "status.json") }
func tmuxConfPath(stateDir string) string { return filepath.Join(stateDir, "tmux.conf") }
func eventsPath(stateDir string) string   { return filepath.Join(stateDir, "events.jsonl") }
