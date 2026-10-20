// Command work-report records and reports the operator's work activity, as
// surfaced by pg-connector's activity list. It execs the pg-connector binary
// from PATH and nothing else, and it MUST contain no organization identifiers.
//
// # Adding a subcommand
//
// This file wires only the root command and the registration seam. Each verb
// (pull, report, query, status, config and its children) lives in its own file
// of this package and registers itself from init():
//
//	func init() { registerCommand(newPullCmd) }
//
// registerCommand takes a constructor rather than a command so every
// newRootCmd call (the real main and each test) gets fresh command instances.
// A verb reads the resolved global flag values with globalFlags(cmd), which
// returns the raw --config, --output and --store values ("" when unset) and
// performs no defaulting: defaulting is owned by internal/config, and
// validation of the --output value is owned by each verb (pull additionally
// accepts "pg-router"). Neither registering nor reading global flags requires
// editing this file.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags (mkGoApp's default target).
var Version = "dev"

var (
	registryMu sync.Mutex
	registry   []func() *cobra.Command
)

// registerCommand adds a subcommand constructor to the root command tree. Verb
// files call it from init().
func registerCommand(newCmd func() *cobra.Command) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, newCmd)
}

// globalFlags returns the raw values of the root persistent flags --config,
// --output and --store as seen by cmd. A flag that was not passed yields "".
func globalFlags(cmd *cobra.Command) (configPath, output, storePath string) {
	get := func(name string) string {
		f := cmd.Flag(name)
		if f == nil {
			return ""
		}
		return f.Value.String()
	}
	return get("config"), get("output"), get("store")
}

// newRootCmd builds the root command with the global flags and every
// registered subcommand.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "work-report",
		Short:         "Record and report work activity surfaced through pg-connector",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Runnable so that --help lists usage and the global flags even before
		// any verb is registered; a bare invocation prints that help.
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	pf := root.PersistentFlags()
	pf.String("config", "", "path to the config file (default: $XDG_CONFIG_HOME/work-report/config.yaml)")
	pf.String("output", "", "output format: json or human")
	pf.String("store", "", "path to the store database (overrides the config's store path)")

	registryMu.Lock()
	ctors := append([]func() *cobra.Command(nil), registry...)
	registryMu.Unlock()
	for _, newCmd := range ctors {
		root.AddCommand(newCmd())
	}
	wireUsageErrors(root)
	return root
}

// exitError makes a verb exit with a specific process status. A verb whose
// output was already written (pull's degraded outcome, for one) returns it
// with an empty msg, so main exits with code without printing anything more.
// cause, when set, is what errors.Is and errors.As see through it.
type exitError struct {
	code  int
	msg   string
	cause error
}

func (e *exitError) Error() string { return e.msg }
func (e *exitError) Unwrap() error { return e.cause }

// usageError marks a mistake in how a verb was invoked (a flag, an argument or
// a bad --range or --output value). report and config exit 2 for it; every
// other verb keeps the generic 1 (ADR 0042).
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// usageErrorf builds a usageError from a format.
func usageErrorf(format string, args ...any) error {
	return &usageError{err: fmt.Errorf(format, args...)}
}

// asUsage marks err as a usage error; nil stays nil.
func asUsage(err error) error {
	if err == nil {
		return nil
	}
	return &usageError{err: err}
}

// exitCode maps the error a command returned to the process exit status: the
// code of an exitError, 2 for a usageError, 1 for anything else (the generic
// "unexpected" status).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

// usageVerbs are the verbs whose flag and argument errors exit 2. pull keeps 1
// because it already uses 2 for "some sources degraded", and a scheduled pull
// with a bad flag must not read as degraded; errors at the root, outside any
// verb, keep 1 too.
var usageVerbs = map[string]bool{"report": true, "config": true}

// wireUsageErrors makes the flag errors and the argument errors (cobra's Args
// validation, including an unknown subcommand) of the usage verbs usage
// errors. cobra looks the flag-error hook up through the parent chain, so the
// verb's own subcommands inherit it; Args is per command, so it is wrapped on
// the whole subtree.
func wireUsageErrors(root *cobra.Command) {
	for _, verb := range root.Commands() {
		if !usageVerbs[verb.Name()] {
			continue
		}
		verb.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return asUsage(err) })
		wrapArgs(verb)
	}
}

func wrapArgs(c *cobra.Command) {
	orig := c.Args
	if orig == nil {
		// A command that only groups subcommands takes no argument of its
		// own: an unknown subcommand is an argument error.
		orig = cobra.NoArgs
	}
	c.Args = func(cmd *cobra.Command, args []string) error { return asUsage(orig(cmd, args)) }
	for _, sub := range c.Commands() {
		wrapArgs(sub)
	}
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		msg := err.Error()
		var ee *exitError
		if errors.As(err, &ee) {
			msg = ee.msg
		}
		if msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
		cancel()
		os.Exit(exitCode(err))
	}
}
