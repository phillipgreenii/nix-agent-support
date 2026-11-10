package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// annotationNow is the clock for every annotation-table write this file and
// wip.go make (hide/unhide/wip all stamp SetAt the same way); overridable
// in tests.
var annotationNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

// hideCmd implements `pg-desk hide <pr> [reason]`: writes the annotation
// table only, never any pipeline table [docs/behavior/pg-desk/hide-unhide-wip.md].
// It is the pr form of `pg-desk pr hide` and writes the key/value hidden
// annotation (runTypedHidden).
var hideCmd = &cobra.Command{
	Use:   "hide <pr> [reason]",
	Short: "Hide a PR from the triage board",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		reason := ""
		if len(args) == 2 {
			reason = args[1]
		}
		return runTypedHidden(cmd, "hide", entityTypePR, args[0], true, reason)
	},
}

func init() {
	rootCmd.AddCommand(hideCmd)
}

// actorFor resolves the actor attribution for an annotation write: the
// explicit flag value when non-empty, else the configured actor
// [docs/behavior/pg-desk/feedback.md: "attributed to the caller (--actor,
// default the configured actor)"]. hide/unhide/wip carry no --actor flag of
// their own (only feedback set does — this packet's Binding decisions), so
// they always pass "" and get the configured actor.
func actorFor(cfg *config.Config, flagActor string) string {
	if flagActor != "" {
		return flagActor
	}
	if cfg != nil {
		return cfg.Actor
	}
	return ""
}
