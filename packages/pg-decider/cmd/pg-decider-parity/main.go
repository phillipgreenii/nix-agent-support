// Command pg-decider-parity runs the old-versus-new parity gate by hand: every
// synthetic scenario goes through the old sync (pg-desk, sync.mode = plan) and
// the new decider (pg-decider plan), the two plans are normalized into one
// vocabulary and diffed, and every difference the expected-diff list does not
// explain is printed. It makes no live contact: all state is temporary.
//
// The three built binaries are named by PG_DECIDER_PARITY_PG_DESK_BIN,
// PG_DECIDER_PARITY_PG_CONNECTOR_BIN and PG_DECIDER_PARITY_PG_DECIDER_BIN. The
// nix check checks.<system>.pg-decider-parity-gate builds them and runs the same
// gate as a test (see the package README).
//
// Exit status: 0 when every scenario matches or differs only as listed, 1 when
// any scenario has an unexplained difference, a listed exception that did not
// occur, or could not be run, 2 when the environment is not set.
//
// The tool is deleted with internal/sync in the cutover phase; it is not part
// of the package output (default.nix builds cmd/pg-decider only).
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-decider/internal/parity"
)

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr)) }

func run(ctx context.Context, out, errOut io.Writer) int {
	env, ok := parity.EnvFromProcess()
	if !ok {
		fmt.Fprintln(errOut, "pg-decider-parity: set PG_DECIDER_PARITY_PG_DESK_BIN, PG_DECIDER_PARITY_PG_CONNECTOR_BIN and PG_DECIDER_PARITY_PG_DECIDER_BIN to the built binaries")
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
