// main.go: pg-decider's cobra root wiring and process entry point.
//
// pg-decider reads the composite view ONLY through `pg-desk <type> show <id>
// --json` (never pg-connector data) and holds no compile-time dependency on
// pg-desk. The CLI shell here owns argument parsing, the view read and the
// exit-code mapping; `plan` and `apply` behavior live behind the planFn and
// applyFn seams (plan.go, apply.go) that sibling packets replace.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version").
var Version = "dev"

// errFailed is returned by RunE once its own diagnostic has been written to
// stderr; run must not print it again.
var errFailed = errors.New("pg-decider: command failed")

// errExit carries the exit code RunE wants run to return.
type errExit struct{ code int }

func (e errExit) Error() string { return fmt.Sprintf("pg-decider: exit %d", e.code) }

// validTypes are the entity types a decider can be selected for.
var validTypes = map[string]struct{}{"pr": {}, "issue": {}, "thread": {}}

type viewCtxKey struct{}

// viewFromContext returns the view the CLI read for this invocation (nil
// outside a command run). The seams receive it through their context so
// their pinned signatures stay unchanged and the view is read exactly once.
func viewFromContext(ctx context.Context) *view.View {
	v, _ := ctx.Value(viewCtxKey{}).(*view.View)
	return v
}

// readView reads the composite view and, on failure, prints the diagnostic
// (naming pg-desk) to errOut. Nothing is written to stdout.
func readView(ctx context.Context, errOut io.Writer, typ, id string) (context.Context, bool) {
	v, err := view.Read(ctx, typ, id)
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider: cannot read the %s view of %s from pg-desk: %v\n", typ, id, err)
		return ctx, false
	}
	return context.WithValue(ctx, viewCtxKey{}, v), true
}

func checkType(typ string) error {
	if _, ok := validTypes[typ]; !ok {
		return fmt.Errorf("unknown entity type %q (want pr, issue or thread)", typ)
	}
	return nil
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pg-decider",
		Short:         "Decide and apply the work items an entity's composite view calls for",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newPlanCmd(), newApplyCmd())
	return root
}

// run executes the CLI with args, writing to stdout/stderr (threaded
// explicitly so tests can capture them), and returns the process exit code:
// 0 all applied or none needed, 1 usage/other error, 2 some actions failed,
// 3 view unreadable (nothing applied).
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)

	err := root.ExecuteContext(context.Background())
	if err == nil {
		return exitcode.OK
	}
	var ee errExit
	if errors.As(err, &ee) {
		return ee.code
	}
	if !errors.Is(err, errFailed) {
		fmt.Fprintln(stderr, err)
	}
	return exitcode.Failure
}

func main() {
	exitcode.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
