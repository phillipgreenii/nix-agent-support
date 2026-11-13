// Package cli is the pg-review command tree: three audience groups (human,
// agent, job), each holding one file per command so later work never
// collides. This file wires the root command, the group registry and the
// shared flag and exit-code helpers.
//
// # Adding a command
//
// A command file registers a constructor from init():
//
//	func init() { registerCommand(groupJob, newJobRunCmd) }
//
// The constructor takes no arguments so every NewRoot call (the real main and
// each test) gets fresh command instances.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/x/exitcode"
)

// Group names and their default --output formats.
const (
	groupHuman = "human"
	groupAgent = "agent"
	groupJob   = "job"

	outputText = "text"
	outputJSON = "json"

	defaultOutputAnnotation = "pg-review/default-output"
)

var (
	registryMu sync.Mutex
	registry   = map[string][]func() *cobra.Command{}
)

// registerCommand adds a command constructor to a group. Command files call it
// from init().
func registerCommand(group string, newCmd func() *cobra.Command) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[group] = append(registry[group], newCmd)
}

func newGroup(name, short, defaultOutput string) *cobra.Command {
	g := &cobra.Command{
		Use:         name,
		Short:       short,
		Annotations: map[string]string{defaultOutputAnnotation: defaultOutput},
		RunE:        func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	registryMu.Lock()
	ctors := append([]func() *cobra.Command(nil), registry[name]...)
	registryMu.Unlock()
	for _, newCmd := range ctors {
		g.AddCommand(newCmd())
	}
	return g
}

// NewRoot builds the root command with the global --output flag and every
// registered command.
func NewRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "pg-review",
		Short:         "Self-review: find, rule on and track improvements to how agents work",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.PersistentFlags().String("output", "", "output format: text or json (default: text for human and job, json for agent)")
	root.AddCommand(
		newGroup(groupHuman, "Commands for the operator: list, show and rule on review items", outputText),
		newGroup(groupAgent, "Commands for the analyst agent: query evidence, file findings", outputJSON),
		newGroup(groupJob, "Commands for scheduled and on-demand runs and upkeep", outputText),
	)
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		_, err := OutputFormat(cmd)
		return err
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return asUsage(err) })
	wrapArgs(root)
	return root
}

// wrapArgs makes argument errors (cobra's Args validation, including an
// unknown subcommand) usage errors across the whole tree.
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

// OutputFormat resolves the effective output format of cmd: the --output
// value when given, else its group's default. There is no terminal
// detection. A value other than text or json is a usage error.
func OutputFormat(cmd *cobra.Command) (string, error) {
	if f := cmd.Flag("output"); f != nil && f.Value.String() != "" {
		switch v := f.Value.String(); v {
		case outputText, outputJSON:
			return v, nil
		default:
			return "", usageErrorf("--output must be text or json, got %q", v)
		}
	}
	for c := cmd; c != nil; c = c.Parent() {
		if d, ok := c.Annotations[defaultOutputAnnotation]; ok {
			return d, nil
		}
	}
	return outputText, nil
}

// exitError makes a command exit with a specific status. A command whose
// output is already written returns it with an empty msg.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// ExitErrorf returns an error that makes the process exit with code after
// printing the formatted message to stderr.
func ExitErrorf(code int, format string, args ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...)}
}

// usageError marks a mistake in how a command was invoked.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usageErrorf(format string, args ...any) error {
	return &usageError{err: fmt.Errorf(format, args...)}
}

func asUsage(err error) error {
	if err == nil {
		return nil
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return err
	}
	return &usageError{err: err}
}

// ExitCode maps the error a command returned to the process exit status: the
// code of an exitError, Usage for a usageError, Failure for anything else.
func ExitCode(err error) int {
	if err == nil {
		return exitcode.OK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return exitcode.Usage
	}
	return exitcode.Error
}

// Execute runs the CLI with args (without the program name) and returns the
// process exit status.
func Execute(ctx context.Context, version string, args []string, stdout, stderr io.Writer) int {
	root := NewRoot(version)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitcode.OK
	}
	msg := err.Error()
	var ee *exitError
	if errors.As(err, &ee) {
		msg = ee.msg
	}
	if msg != "" {
		fmt.Fprintln(stderr, msg)
	}
	return ExitCode(err)
}
