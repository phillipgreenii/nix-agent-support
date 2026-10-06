package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// heartbeatNow is the clock; overridable in tests.
var heartbeatNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

// heartbeatLedgerShowTimeout bounds the local `pg-connector ledger show`
// read, so a wedged connector can never hold the heartbeat (and the
// liveness stamp's caller) open.
const heartbeatLedgerShowTimeout = 30 * time.Second

// heartbeatRunLedgerShow execs `pg-connector ledger show --output json` and
// returns its stdout: the D10-permitted literal ("pg-connector" is the one
// binary this module may exec by name). `ledger show` is a local read with
// no network call, which is what lets the heartbeat read source ages at zero
// origin cost. A package-level var so tests can stub it without a real
// pg-connector on $PATH.
var heartbeatRunLedgerShow = func(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, heartbeatLedgerShowTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "pg-connector", "ledger", "show", "--output", "json").Output()
}

// heartbeatCmd implements `pg-desk heartbeat`: stamps meta.last_heartbeat
// and records each connector source's last successful fetch time under the
// meta source_fetch.* keys [docs/behavior/pg-desk/operator-commands.md,
// docs/behavior/pg-desk/freshness.md]. This is the command the
// `desk-heartbeat` ccpool role runs (design section 6.1's
// `command.argv = ["pg-desk", "heartbeat"]`) each time the `desk-heartbeat`
// query (heartbeat_item.go) emits a fresh item — it consumes
// meta.last_heartbeat's own staleness bound (packet 7's serve).
//
// The liveness stamp is written first and never depends on the connector:
// a pg-connector that is absent, slow or answering garbage costs only the
// source-age refresh (reported on stderr), never the heartbeat.
var heartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Stamp the store's last-heartbeat time and record source fetch times",
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := deskStoreOpen()
		if err != nil {
			return fmt.Errorf("heartbeat: open store: %w", err)
		}
		defer func() { _ = st.Close() }()

		if err := st.SetMeta(store.MetaKeyLastHeartbeat, heartbeatNow()); err != nil {
			return fmt.Errorf("heartbeat: %w", err)
		}
		return recordSourceFetches(cmd, st)
	},
}

// recordSourceFetches copies the connector ledger's freshness stamps into
// the store. A failure to READ the ledger is reported on stderr and is not
// an error (the previous source_fetch.* values simply stay, and age); only a
// failure to WRITE the store fails the command.
func recordSourceFetches(cmd *cobra.Command, st *store.Store) error {
	out, err := heartbeatRunLedgerShow(cmd.Context())
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "heartbeat: source ages not refreshed: pg-connector ledger show: %v\n", err)
		return nil
	}
	rows, err := freshness.ParseLedgerShow(out)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "heartbeat: source ages not refreshed: %v\n", err)
		return nil
	}
	if err := freshness.Update(st, rows); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(heartbeatCmd)
}
