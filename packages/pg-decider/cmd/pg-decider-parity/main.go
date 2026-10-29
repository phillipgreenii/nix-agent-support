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
// The one-time recording of the golden (freeze) and the old-side runner it used
// were deleted with the sync stage (PR lane refactor, packet P9a-2); the goldens
// are frozen.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-decider/internal/parity"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	env, ok := parity.EnvFromProcess()
	if !ok {
		fmt.Fprintln(errOut, "pg-decider-parity: set PG_DECIDER_PARITY_PG_DESK_BIN, PG_DECIDER_PARITY_PG_CONNECTOR_BIN and PG_DECIDER_PARITY_PG_DECIDER_BIN to the built binaries")
		return 2
	}
	if len(args) > 0 {
		fmt.Fprintf(errOut, "pg-decider-parity: unknown argument %q (it takes no arguments)\n", args[0])
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
