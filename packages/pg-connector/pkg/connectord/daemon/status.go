package daemon

import (
	"context"
	"fmt"
	"io"
)

// StatusReport is a daemon's health report as the kernel's status skeleton
// sees it: how to print it and which process exit code its health maps to.
type StatusReport interface {
	// WriteText prints the operator-facing text form.
	WriteText(w io.Writer) error
	// WriteJSON prints the JSON document.
	WriteJSON(w io.Writer) error
	// ExitCode is the process exit code of the report's health.
	ExitCode() int
}

// RunStatus is the `status [--json]` skeleton every connector daemon shares:
// parse the arguments (--json is the only one), ask collect for the report (the
// daemon's own collector answers from the daemon, or builds the local one when
// it does not answer), print it as text or as the JSON document, and exit by its
// health. name is the daemon's command name and usage the text appended to an
// argument error.
func RunStatus(ctx context.Context, name, usage string, args []string, stdout, stderr io.Writer, collect func(context.Context) StatusReport) int {
	asJSON, err := ParseStatusArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%s status: %v\n\n%s", name, err, usage)
		return ExitUsage
	}
	r := collect(ctx)
	write := r.WriteText
	if asJSON {
		write = r.WriteJSON
	}
	if err := write(stdout); err != nil {
		fmt.Fprintf(stderr, "%s status: %v\n", name, err)
		return 1
	}
	return r.ExitCode()
}

// FuncReport is a StatusReport assembled from a daemon's own renderers, so a
// daemon with its own report type needs no adapter type.
type FuncReport struct {
	Text func(io.Writer) error
	JSON func(io.Writer) error
	Code int
}

// WriteText implements StatusReport.
func (f FuncReport) WriteText(w io.Writer) error { return f.Text(w) }

// WriteJSON implements StatusReport.
func (f FuncReport) WriteJSON(w io.Writer) error { return f.JSON(w) }

// ExitCode implements StatusReport.
func (f FuncReport) ExitCode() int { return f.Code }

// ParseStatusArgs reads the arguments after the status mode word: --json is the
// only one. A daemon whose command line reports a usage error before it reads
// its config calls this itself.
func ParseStatusArgs(args []string) (asJSON bool, err error) {
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		default:
			return false, fmt.Errorf("unknown argument %q", a)
		}
	}
	return asJSON, nil
}
