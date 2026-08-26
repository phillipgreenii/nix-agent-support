package main

import "github.com/spf13/cobra"

func newDrainCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "drain", Short: "Helpers for the /drain-beads work loop"}
	cmd.AddCommand(newDrainIsolateCmd())
	return cmd
}
