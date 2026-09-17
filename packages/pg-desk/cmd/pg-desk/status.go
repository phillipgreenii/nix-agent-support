package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// statusRunPgConnectorLedgerShow execs `pg-connector ledger show` and
// returns its combined output — the D10-permitted literal ("pg-connector"
// is the one binary this module may exec by name; see
// composition_test.go). A package-level var so tests can stub it without a
// real pg-connector on $PATH. A failure here (pg-connector absent, or a
// non-zero exit) is reported inline in status's own output rather than
// failing the whole command: `status`'s own exit-code contract
// [docs/behavior/pg-desk/operator-commands.md] is "1 when the store cannot
// be opened" only — a pg-connector-side problem is doctor's job to fail on.
var statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "pg-connector", "ledger", "show").CombinedOutput()
	return string(out), err
}

// statusCmd implements `pg-desk status`
// [docs/behavior/pg-desk/operator-commands.md]. sync.mode's planned-rows
// section has nothing to show this phase (sync.mode is not yet a
// configuration key at all — see that doc's "Out of scope") so it is never
// rendered.
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print the store path, schema version, and health summary",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(cmd)
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command) error {
	st, err := deskStoreOpen()
	if err != nil {
		return fmt.Errorf("status: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	w := cmd.OutOrStdout()

	schemaVersion, _, err := st.GetMeta(store.MetaKeySchemaVersion)
	if err != nil {
		return fmt.Errorf("status: read meta.schema_version: %w", err)
	}
	entityCount, err := st.CountEntities()
	if err != nil {
		return fmt.Errorf("status: count entities: %w", err)
	}
	interps, err := st.ListInterpretations()
	if err != nil {
		return fmt.Errorf("status: list interpretations: %w", err)
	}
	degraded, syncErrors := 0, 0
	for _, i := range interps {
		if i.Degraded {
			degraded++
		}
		if i.SyncError != "" {
			syncErrors++
		}
	}
	lastHeartbeat, _, err := st.GetMeta(store.MetaKeyLastHeartbeat)
	if err != nil {
		return fmt.Errorf("status: read meta.last_heartbeat: %w", err)
	}
	lastRun, _, err := st.GetMeta(store.MetaKeyLastRun)
	if err != nil {
		return fmt.Errorf("status: read meta.last_run: %w", err)
	}
	lastSweep, _, err := st.GetMeta(store.MetaKeyLastSweep)
	if err != nil {
		return fmt.Errorf("status: read meta.last_sweep: %w", err)
	}

	fmt.Fprintf(w, "store: %s\n", store.DefaultPath())
	fmt.Fprintf(w, "schema_version: %s\n", orDash(schemaVersion))
	fmt.Fprintf(w, "entities: %d\n", entityCount)
	fmt.Fprintf(w, "interpretations: %d\n", len(interps))
	fmt.Fprintf(w, "degraded: %d\n", degraded)
	fmt.Fprintf(w, "sync_errors: %d\n", syncErrors)
	fmt.Fprintf(w, "last_heartbeat: %s\n", orDash(lastHeartbeat))
	fmt.Fprintf(w, "last_run: %s\n", orDash(lastRun))
	fmt.Fprintf(w, "last_sweep: %s\n", orDash(lastSweep))

	ledgerOut, ledgerErr := statusRunPgConnectorLedgerShow(cmd.Context())
	if ledgerErr != nil {
		fmt.Fprintf(w, "pg-connector ledger show: unavailable (%v)\n", ledgerErr)
	} else {
		io.WriteString(w, "pg-connector ledger show:\n")
		io.WriteString(w, ledgerOut)
	}
	return nil
}
