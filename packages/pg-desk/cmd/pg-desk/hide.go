package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// annotationNow is the clock for every annotation-table write this file and
// wip.go make (hide/unhide/wip all stamp SetAt the same way); overridable
// in tests.
var annotationNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

// hideCmd implements `pg-desk hide <pr> [reason]`: writes the annotation
// table only, never any pipeline table [docs/behavior/pg-desk/hide-unhide-wip.md].
var hideCmd = &cobra.Command{
	Use:   "hide <pr> [reason]",
	Short: "Hide a PR from the triage board",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		reason := ""
		if len(args) == 2 {
			reason = args[1]
		}
		return setHiddenAnnotation(cmd, "hide", args[0], true, reason)
	},
}

func init() {
	rootCmd.AddCommand(hideCmd)
}

// setHiddenAnnotation resolves ref to (repo, entityID), confirms the entity
// is known to the store [hide-unhide-wip.md's exit-1 case: "<pr> does not
// resolve to exactly one stored entity"], and upserts the annotation row's
// hidden/hidden_reason fields — preserving any existing wip flag, since a
// hide/unhide write must not clobber a WIP annotation set independently
// [store-schema.md's annotation table: "hidden state and reason, WIP
// state ... written only by the CLI"]. cmdName ("hide" or "unhide") only
// prefixes error text, so a caller's error always names the command it
// actually invoked.
func setHiddenAnnotation(cmd *cobra.Command, cmdName, ref string, hidden bool, reason string) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("%s: load config: %w", cmdName, err)
	}
	st, err := deskStoreOpen()
	if err != nil {
		return fmt.Errorf("%s: open store: %w", cmdName, err)
	}
	defer func() { _ = st.Close() }()

	repo, id, err := resolvePRRef(cfg, ref)
	if err != nil {
		return fmt.Errorf("%s: %w", cmdName, err)
	}
	if _, found, gerr := st.GetEntity(repo, entityTypePR, id); gerr != nil {
		return fmt.Errorf("%s: %w", cmdName, gerr)
	} else if !found {
		return fmt.Errorf("%s: %s does not resolve to a stored entity", cmdName, ref)
	}

	existing, _, gerr := st.GetPRAnnotation(repo, entityTypePR, id)
	if gerr != nil {
		return fmt.Errorf("%s: read existing annotation: %w", cmdName, gerr)
	}

	h := hidden
	a := store.Annotation{
		Repo:         repo,
		EntityType:   entityTypePR,
		EntityID:     id,
		Hidden:       &h,
		HiddenReason: reason,
		WIP:          existing.WIP,
		SetBy:        actorFor(cfg, ""),
		SetAt:        annotationNow(),
	}
	if err := st.UpsertAnnotation(a); err != nil {
		return fmt.Errorf("%s: %w", cmdName, err)
	}
	return nil
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
