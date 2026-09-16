package main

import "github.com/spf13/cobra"

// heartbeatItemCmd is a stub: printing the single desk-heartbeat pr-pool
// item lands in a later packet of this docket. See
// docs/behavior/pg-desk/operator-commands.md.
var heartbeatItemCmd = &cobra.Command{
	Use:   "heartbeat-item",
	Short: "Print the desk-heartbeat pr-pool item (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("heartbeat-item")
	},
}

func init() {
	rootCmd.AddCommand(heartbeatItemCmd)
}
