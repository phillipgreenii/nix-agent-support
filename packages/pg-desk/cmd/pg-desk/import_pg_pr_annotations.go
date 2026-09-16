package main

import "github.com/spf13/cobra"

// importPgPrAnnotationsCmd is a stub: the one-shot cutover tool copying
// pg-pr's pull_request.user_hidden/user_hidden_reason/wip columns into
// pg-desk's annotation table lands in a later packet of this docket. See
// docs/behavior/pg-desk/import-pg-pr-annotations.md.
var importPgPrAnnotationsCmd = &cobra.Command{
	Use:   "import-pg-pr-annotations",
	Short: "One-shot cutover: copy pg-pr's hidden/wip annotations into the pg-desk store (stub)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return notImplemented("import-pg-pr-annotations")
	},
}

func init() {
	rootCmd.AddCommand(importPgPrAnnotationsCmd)
}
