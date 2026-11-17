package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/x/exitcode"
)

const configCheckUsage = "usage: pg-assistants config check --config PATH"

const configHelp = `pg-assistants config check: validate a configuration file without changing anything.

` + configCheckUsage + `

Strict: every problem fails the check, including one the daemon would only
log (a global field falling back to its default, or one assistant that would
not be started). Exit 0 prints "ok: <n> assistants"; exit 7 prints each
problem on its own line; exit 2 is a usage error.
`

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

// runConfig dispatches `pg-assistants config <subcommand>`.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && isHelpFlag(args[0]) {
		fmt.Fprint(stdout, configHelp)
		return exitcode.OK
	}
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, configCheckUsage)
		return exitcode.Usage
	}
	return runConfigCheck(args[1:], stdout, stderr)
}

// runConfigCheck is `config check`: Load, then Strict.
func runConfigCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "the configuration file to validate (required)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, configHelp)
			return exitcode.OK
		}
		return exitcode.Usage
	}
	if fs.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, configCheckUsage)
		return exitcode.Usage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "pg-assistants: %s: %v\n", *path, err)
		return exitcode.Config
	}
	if err := cfg.Strict(); err != nil {
		fmt.Fprintln(stderr, err)
		return exitcode.Config
	}
	fmt.Fprintf(stdout, "ok: %d assistants\n", len(cfg.Assistants))
	return exitcode.OK
}
