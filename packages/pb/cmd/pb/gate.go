package main

import "github.com/spf13/cobra"

func newGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Manage pn:applied gates",
	}
	cmd.AddCommand(newGateCreateCmd())
	cmd.AddCommand(newGateCheckCmd())
	cmd.AddCommand(newGateAttachVerifiedChildCmd())
	return cmd
}
