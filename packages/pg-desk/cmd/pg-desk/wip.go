package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// wipCmd implements `pg-desk wip on|off <pr>`: writes the annotation
// table's wip field only. `wip on` does NOT convert a ready PR to draft
// upstream — that upstream conversion is an accepted, recorded loss for
// this whole window (D15); this command only ever records the annotation
// [docs/behavior/pg-desk/hide-unhide-wip.md].
var wipCmd = &cobra.Command{
	Use:   "wip on|off <pr>",
	Short: "Mark a PR work-in-progress on the triage board",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		state := args[0]
		var wip bool
		switch state {
		case "on":
			wip = true
		case "off":
			wip = false
		default:
			return fmt.Errorf("wip: %q is not \"on\" or \"off\"", state)
		}
		return runTypedWIP(cmd, entityTypePR, args[1], wip)
	},
}

func init() {
	rootCmd.AddCommand(wipCmd)
}
