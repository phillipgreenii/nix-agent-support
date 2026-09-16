package main

import "github.com/spf13/cobra"

// heartbeatCmd is a stub: stamping meta.last_heartbeat lands in a later
// packet of this docket. See docs/behavior/pg-desk/operator-commands.md.
var heartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Stamp the store's last-heartbeat time (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("heartbeat")
	},
}

func init() {
	rootCmd.AddCommand(heartbeatCmd)
}
