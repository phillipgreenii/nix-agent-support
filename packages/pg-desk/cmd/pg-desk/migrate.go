package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// migrateCmd implements `pg-desk migrate --cutover`
// [docs/behavior/pg-desk/store-schema.md, "Schema versions and the
// cutover"]: the ONE explicit step that moves the store from schema
// version 1 to version 2. Nothing else runs it — store.Open never does —
// so it happens exactly when the operator runs it, in the maintenance
// window in which sync is stopped.
//
// The whole change is one transaction: it either lands completely (and the
// store reports version 2) or leaves the store untouched. Running it again
// on a store that is already at version 2 is a no-op that exits 0.
var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply the store schema cutover (schema version 1 -> 2) in one transaction",
	Long: `Apply the store schema cutover: move the store from schema version 1 to
schema version 2 in a single transaction. Run it only in the maintenance
window in which sync is stopped. A failure leaves the store untouched, and
running it again on a store that is already at version 2 does nothing.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrate(cmd)
	},
}

func init() {
	migrateCmd.Flags().Bool("cutover", false, "apply the schema cutover (required; the only migration this command performs)")
	rootCmd.AddCommand(migrateCmd)
}

func runMigrate(cmd *cobra.Command) error {
	cutover, err := cmd.Flags().GetBool("cutover")
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if !cutover {
		return errors.New("migrate: nothing to do without --cutover (the schema cutover is the only migration, and it is never applied implicitly)")
	}

	// Raw open: the point is to change a store that store.Open would not
	// migrate, and to succeed on one it has already migrated.
	st, err := deskStoreOpenRaw()
	if err != nil {
		return fmt.Errorf("migrate --cutover: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	w := cmd.OutOrStdout()
	before, err := st.SchemaVersion()
	if err != nil {
		return fmt.Errorf("migrate --cutover: %w", err)
	}
	if before == store.NewSchemaVersion {
		fmt.Fprintf(w, "store %s is already at schema version %d; nothing to do\n", store.DefaultPath(), before)
		return nil
	}
	if err := st.Cutover(); err != nil {
		return fmt.Errorf("migrate --cutover: %w", err)
	}
	fmt.Fprintf(w, "store %s migrated from schema version %d to schema version %d\n", store.DefaultPath(), before, store.NewSchemaVersion)
	return nil
}
