// Package handlerkit is the shared entrypoint skeleton of the handler and
// source mains (Template Method): Main fixes the order of process start-up
// (logger, adapter setup, run, exit) and the adapter supplies the varying
// steps. It lives under api/ so the handler and source members may both
// import it without importing each other's subtree (import-boundary rule
// SD-3, shared set).
package handlerkit

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/phillipgreenii/x/jsonllogger"
)

// Exit codes the subcommand dispatcher returns for usage problems and
// success; they equal the participant contract's coarse codes.
const (
	exitOK    = 0
	exitUsage = 2
)

// Adapter is everything that differs between mains. Name is required.
type Adapter struct {
	// Name is the binary name, used in diagnostics and as the log service.
	Name string
	// Version is printed by the version subcommand of a Commands adapter.
	Version string
	// JSONLog installs a jsonllogger default logger on stderr when true.
	JSONLog bool
	// Setup runs once after the logger is installed and before Run
	// (optional).
	Setup func()

	// Run executes the adapter with the process arguments and streams and
	// returns the exit code. When Run is nil, Commands is dispatched.
	Run func(args []string, stdout, stderr io.Writer) int

	// Usage and Help are the short usage line and help text of a Commands
	// adapter.
	Usage string
	Help  string
	// Commands maps a subcommand to its handler, which receives the
	// arguments after the subcommand name.
	Commands map[string]func(args []string) int
}

// Main runs the adapter and exits the process with its code.
func Main(a Adapter) {
	os.Exit(Execute(a, os.Args[1:], os.Stdout, os.Stderr))
}

// Execute is Main without the process exit, so tests and in-process callers
// can drive the skeleton.
func Execute(a Adapter, args []string, stdout, stderr io.Writer) int {
	if a.JSONLog {
		slog.SetDefault(jsonllogger.NewLogger(stderr, jsonllogger.WithService(a.Name)))
	}
	if a.Setup != nil {
		a.Setup()
	}
	if a.Run != nil {
		return a.Run(args, stdout, stderr)
	}
	return dispatch(a, args, stdout, stderr)
}

func dispatch(a Adapter, args []string, stdout, stderr io.Writer) int {
	usageErr := func(msg string) int {
		fmt.Fprintln(stderr, msg)
		fmt.Fprintln(stderr, a.Usage)
		return exitUsage
	}
	if len(args) == 0 {
		return usageErr(a.Name + ": a subcommand is required")
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, a.Help)
		return exitOK
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, a.Version)
		return exitOK
	}
	if h, ok := a.Commands[args[0]]; ok {
		return h(args[1:])
	}
	return usageErr(fmt.Sprintf("%s: unknown subcommand %q", a.Name, args[0]))
}
