package main

import "github.com/spf13/cobra"

// ledgerCmd is a stub. The ledger table (entity-to-bead-id mapping) exists
// in the store's schema ladder but receives no writes until sync ships
// (Phase 10) — see docs/behavior/pg-desk/store-schema.md. This subcommand's
// own real behavior lands in a later packet of this docket.
var ledgerCmd = &cobra.Command{
	Use:   "ledger",
	Short: "Inspect the entity-to-bead-id ledger (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("ledger")
	},
}

func init() {
	rootCmd.AddCommand(ledgerCmd)
}
