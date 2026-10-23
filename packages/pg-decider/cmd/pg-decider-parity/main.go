// Command pg-decider-parity runs the parity gate by hand: every synthetic
// scenario is loaded into a schema-2 store (pg-desk migrate --cutover, then
// pg-desk pr refresh), the decider plans it (pg-decider plan), and the plan is
// compared with the scenario's frozen golden (testdata/<scenario>/
// expected-plan.json). Every difference is printed: an entry the golden lists
// and the decider does not plan is a stale expected entry. It makes no live
// contact: all state is temporary.
//
// The three built binaries are named by PG_DECIDER_PARITY_PG_DESK_BIN,
// PG_DECIDER_PARITY_PG_CONNECTOR_BIN and PG_DECIDER_PARITY_PG_DECIDER_BIN. The
// nix check checks.<system>.pg-decider-parity-gate builds them and runs the same
// gate as a test (see the package README).
//
// Exit status: 0 when every scenario matches its golden, 1 when any scenario
// differs from it or could not be run, 2 when the environment is not set or the
// arguments are wrong.
//
// freeze (the one-time recording of the golden, run against a pinned
// pre-removal pg-desk before the sync stage is removed):
//
//	pg-decider-parity freeze -testdata DIR -expected FILE -agent-support-commit HASH
//
// runs every scenario through the OLD sync stage and the decider, requires the
// two plans to differ only as FILE explains, and writes
// DIR/<scenario>/expected-plan.json. It refuses to overwrite a golden. The
// old-side runner and this subcommand are deleted with the sync stage.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/pg-decider/internal/parity"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	env, ok := parity.EnvFromProcess()
	if !ok {
		fmt.Fprintln(errOut, "pg-decider-parity: set PG_DECIDER_PARITY_PG_DESK_BIN, PG_DECIDER_PARITY_PG_CONNECTOR_BIN and PG_DECIDER_PARITY_PG_DECIDER_BIN to the built binaries")
		return 2
	}
	if len(args) > 0 && args[0] == "freeze" {
		return freeze(ctx, env, args[1:], errOut)
	}
	if len(args) > 0 {
		fmt.Fprintf(errOut, "pg-decider-parity: unknown argument %q (the only subcommand is freeze)\n", args[0])
		return 2
	}
	outs, err := parity.RunGate(ctx, env)
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider-parity: %v\n", err)
		return 1
	}
	if !parity.Render(out, outs) {
		return 1
	}
	return 0
}

func freeze(ctx context.Context, env parity.Env, args []string, errOut io.Writer) int {
	fs := flag.NewFlagSet("freeze", flag.ContinueOnError)
	fs.SetOutput(errOut)
	testdata := fs.String("testdata", "", "the internal/parity/testdata directory to write the goldens into")
	expected := fs.String("expected", "", "the file of documented old-versus-decider differences (the former testdata/expected-diff.json)")
	commit := fs.String("agent-support-commit", "", "the agent-support main commit the three binaries were built from (recorded in every golden)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *testdata == "" || *expected == "" || *commit == "" {
		fmt.Fprintln(errOut, "pg-decider-parity freeze: -testdata, -expected and -agent-support-commit are all required")
		return 2
	}
	data, err := os.ReadFile(*expected)
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider-parity freeze: %v\n", err)
		return 2
	}
	explained, err := parity.ParseExpected(data)
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider-parity freeze: %v\n", err)
		return 2
	}
	resolved, err := filepath.EvalSymlinks(env.PgDeskBin)
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider-parity freeze: %v\n", err)
		return 2
	}
	from := parity.Frozen{AgentSupportCommit: *commit, PgDesk: filepath.Dir(filepath.Dir(resolved))}
	gs, err := parity.Freeze(ctx, env, explained, from)
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider-parity freeze: %v\n", err)
		return 1
	}
	if err := parity.WriteGoldens(*testdata, gs); err != nil {
		fmt.Fprintf(errOut, "pg-decider-parity freeze: %v\n", err)
		return 1
	}
	return 0
}
