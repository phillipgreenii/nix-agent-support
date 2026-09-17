package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
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
		return setWIPAnnotation(cmd, args[1], wip)
	},
}

func init() {
	rootCmd.AddCommand(wipCmd)
}

// setWIPAnnotation mirrors setHiddenAnnotation (hide.go) but writes the
// wip field instead, preserving any existing hidden/hidden_reason.
func setWIPAnnotation(cmd *cobra.Command, ref string, wip bool) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("wip: load config: %w", err)
	}
	st, err := deskStoreOpen()
	if err != nil {
		return fmt.Errorf("wip: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	repo, id, err := resolvePRRef(cfg, ref)
	if err != nil {
		return fmt.Errorf("wip: %w", err)
	}
	if _, found, gerr := st.GetEntity(repo, entityTypePR, id); gerr != nil {
		return fmt.Errorf("wip: %w", gerr)
	} else if !found {
		return fmt.Errorf("wip: %s does not resolve to a stored entity", ref)
	}

	existing, _, gerr := st.GetPRAnnotation(repo, entityTypePR, id)
	if gerr != nil {
		return fmt.Errorf("wip: read existing annotation: %w", gerr)
	}

	w := wip
	a := store.Annotation{
		Repo:         repo,
		EntityType:   entityTypePR,
		EntityID:     id,
		Hidden:       existing.Hidden,
		HiddenReason: existing.HiddenReason,
		WIP:          &w,
		SetBy:        actorFor(cfg, ""),
		SetAt:        annotationNow(),
	}
	if err := st.UpsertAnnotation(a); err != nil {
		return fmt.Errorf("wip: %w", err)
	}
	return nil
}
