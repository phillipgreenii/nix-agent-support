package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/config"
)

// Exit codes (ADR 0042) used by the config subcommand.
const (
	exitOK            = 0
	exitUnexpected    = 1
	exitUsage         = 2
	exitConfigInvalid = 4
)

// runConfig dispatches `pg-keep-awake config <subcommand>`.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, "usage: pg-keep-awake config check [--config PATH]")
		return exitUsage
	}
	return runConfigCheck(args[1:], stdout, stderr)
}

// runConfigCheck validates a config file: exit 0 when it is valid, 4 when it
// is not, 2 for a usage error. Without --config it checks the default path,
// where an absent file is valid (it means the built-in defaults); an explicit
// --config PATH that does not exist is invalid.
func runConfigCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "config file to validate (default: the keeper's config.toml)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: pg-keep-awake config check [--config PATH]")
		return exitUsage
	}
	explicit := *path != ""
	if !explicit {
		p, err := config.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "pg-keep-awake: %v\n", err)
			return exitUnexpected
		}
		*path = p
	}
	if err := config.Check(*path, explicit); err != nil {
		fmt.Fprintf(stderr, "pg-keep-awake: %s: %v\n", *path, err)
		return exitConfigInvalid
	}
	fmt.Fprintf(stdout, "%s: ok\n", *path)
	return exitOK
}
