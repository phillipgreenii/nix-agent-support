package main

import "github.com/spf13/cobra"

// serveCmd is a stub: the soak-port payload/metrics server lands in a later
// packet of this docket. See docs/behavior/pg-desk/serve.md.
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve the triage payload and /metrics (soak port only; stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("serve")
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
