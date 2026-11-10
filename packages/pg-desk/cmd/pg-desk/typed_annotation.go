package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The typed homes of the kept verbs: pg-desk <type> hide|unhide|wip and
// pg-desk pr feedback list|set [design 6.9]. The top-level
// hide/unhide/wip/feedback are the pr forms and share the same code, so every
// verb writes the key/value annotation API (hidden, wip,
// disposition.<comment_id>).

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newTypedHideCmd(t), newTypedUnhideCmd(t), newTypedWIPCmd(t))
	}
	typeGroup(entityTypePR).AddCommand(newTypedFeedbackCmd())
}

// withStore opens the store, refuses one that is not on schema 2 (the store's
// own error, unchanged) and runs fn against it.
func withStore(fn func(st *store.Store) error) error {
	st, err := openDeskStore()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.RequireNewSchema(); err != nil {
		return err
	}
	return fn(st)
}

func newTypedHideCmd(entityType string) *cobra.Command {
	return &cobra.Command{
		Use:   "hide <id> [reason]",
		Short: fmt.Sprintf("Hide a %s from the triage board", entityType),
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			reason := ""
			if len(args) == 2 {
				reason = args[1]
			}
			return runTypedHidden(cmd, "hide", entityType, args[0], true, reason)
		},
	}
}

func newTypedUnhideCmd(entityType string) *cobra.Command {
	return &cobra.Command{
		Use:   "unhide <id>",
		Short: fmt.Sprintf("Unhide a %s on the triage board", entityType),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTypedHidden(cmd, "unhide", entityType, args[0], false, "")
		},
	}
}

// hiddenBeadsNote is printed by a PR hide: hiding a PR stops the lane from
// opening new work for it, but never touches a bead that is already open.
const hiddenBeadsNote = "beads already open for a hidden PR stay open and claimable"

func runTypedHidden(cmd *cobra.Command, cmdName, entityType, ref string, hidden bool, reason string) error {
	if err := runTypedHiddenWrite(cmd, cmdName, entityType, ref, hidden, reason); err != nil {
		return err
	}
	// The annotation is written; tell the operator what makes the lane act on
	// it (the composed command; the verb never calls the decider). Only PRs
	// have a lane, so issue and thread print nothing extra.
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("%s: load config: %w", cmdName, err)
	}
	preface := ""
	if hidden && entityType == entityTypePR {
		preface = hiddenBeadsNote
	}
	return printComposedCommand(cmd.OutOrStdout(), cfg, entityType, ref, preface)
}

func runTypedHiddenWrite(cmd *cobra.Command, cmdName, entityType, ref string, hidden bool, reason string) error {
	return withStore(
		func(st *store.Store) error {
			cfg, err := deskConfigLoad(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: load config: %w", cmdName, err)
			}
			w, err := newKVWrite(cfg, cmdName, entityType, ref, store.AnnotationHidden, hiddenValue(hidden, reason), annotationChangeOrigin, "", false)
			if err != nil {
				return err
			}
			return w.set(st)
		},
	)
}

func newTypedWIPCmd(entityType string) *cobra.Command {
	return &cobra.Command{
		Use:   "wip on|off <id>",
		Short: fmt.Sprintf("Mark a %s work-in-progress on the triage board", entityType),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var wip bool
			switch args[0] {
			case "on":
				wip = true
			case "off":
				wip = false
			default:
				return fmt.Errorf("wip: %q is not \"on\" or \"off\"", args[0])
			}
			return runTypedWIP(cmd, entityType, args[1], wip)
		},
	}
}

// runTypedWIP is wip on|off for entityType: it writes the key/value wip
// annotation. The top-level `wip` verb calls it with the pr type.
func runTypedWIP(cmd *cobra.Command, entityType, ref string, wip bool) error {
	return withStore(
		func(st *store.Store) error {
			cfg, err := deskConfigLoad(cmd.Context())
			if err != nil {
				return fmt.Errorf("wip: load config: %w", err)
			}
			w, err := newKVWrite(cfg, "wip", entityType, ref, store.AnnotationWIP, boolValue(wip), annotationChangeOrigin, "", false)
			if err != nil {
				return err
			}
			return w.set(st)
		},
	)
}

// newTypedFeedbackCmd builds `pg-desk pr feedback list|set`; feedback is
// built for pr only [design 6.9].
func newTypedFeedbackCmd() *cobra.Command {
	g := &cobra.Command{
		Use:   "feedback",
		Short: "List or set a PR's feedback dispositions",
	}
	g.AddCommand(&cobra.Command{
		Use:   "list <id>",
		Short: "Print the PR's comments and threads with their current dispositions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeedbackList(cmd, args[0])
		},
	})
	var disposition, actor string
	set := &cobra.Command{
		Use:   "set <id> <comment-id>",
		Short: "Record a disposition override, attributed to the caller",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeedbackSet(cmd, args[0], args[1], disposition, actor)
		},
	}
	set.Flags().StringVar(&disposition, "disposition", "", "open|will-fix|wont-fix|no-action (required)")
	set.Flags().StringVar(&actor, "actor", "", "Attribute this override to this actor (defaults to the configured actor)")
	g.AddCommand(set)
	return g
}

// runFeedbackList is feedback list for pr: the stored verdicts with the
// disposition.<comment_id> overlay. The top-level `feedback list` and
// `pr feedback list` both call it.
func runFeedbackList(cmd *cobra.Command, ref string) error {
	return withStore(func(st *store.Store) error { return runTypedFeedbackList(cmd, st, ref) })
}

// runFeedbackSet is feedback set for pr: it writes disposition.<comment_id>.
// The top-level `feedback set` and `pr feedback set` both call it. The
// disposition is validated before the store is opened.
func runFeedbackSet(cmd *cobra.Command, ref, commentID, disposition, actor string) error {
	if !validDisposition(disposition) {
		return fmt.Errorf("feedback set: --disposition must be one of open|will-fix|wont-fix|no-action, got %q", disposition)
	}
	return withStore(func(st *store.Store) error { return runTypedFeedbackSet(cmd, st, ref, commentID, disposition, actor) })
}

// feedbackBase resolves ref and reads the PR's stored base dispositions.
func feedbackBase(cmd *cobra.Command, st *store.Store, cmdName, ref string) (cfg *config.Config, repo, id string, base []feedbackDisposition, err error) {
	cfg, err = deskConfigLoad(cmd.Context())
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("%s: load config: %w", cmdName, err)
	}
	repo, id, err = resolvePRRef(cfg, ref)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("%s: %w", cmdName, err)
	}
	interp, found, err := st.GetInterpretation(repo, entityTypePR, id)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("%s: %w", cmdName, err)
	}
	if !found {
		return nil, "", "", nil, fmt.Errorf("%s: %s does not resolve", cmdName, ref)
	}
	return cfg, repo, id, dispositionsFromInterpretation(interp.Dispositions), nil
}

// runTypedFeedbackList is feedback list: the stored
// verdicts with the disposition.<comment_id> annotations laid over them.
func runTypedFeedbackList(cmd *cobra.Command, st *store.Store, ref string) error {
	_, repo, id, base, err := feedbackBase(cmd, st, "feedback list", ref)
	if err != nil {
		return err
	}
	overrides := map[string]string{}
	for _, d := range base {
		a, found, aerr := st.GetKVAnnotation(repo, entityTypePR, id, store.KeyDisposition(d.CommentID))
		if aerr != nil {
			return fmt.Errorf("feedback list: read annotation for comment %s: %w", d.CommentID, aerr)
		}
		if found && a.Value != "" {
			overrides[d.CommentID] = a.Value
		}
	}
	merged := applyFeedbackOverrides(base, overrides)
	sort.Slice(merged, func(i, j int) bool { return merged[i].CommentID < merged[j].CommentID })
	return renderFeedbackList(cmd.OutOrStdout(), merged)
}

// runTypedFeedbackSet is feedback set: it writes
// disposition.<comment_id>. The value is the disposition as given, including
// open (an explicit override back to open), so what the user asked for is
// what is stored.
func runTypedFeedbackSet(cmd *cobra.Command, st *store.Store, ref, commentID, disposition, actorFlag string) error {
	cfg, _, _, base, err := feedbackBase(cmd, st, "feedback set", ref)
	if err != nil {
		return err
	}
	known := false
	for _, d := range base {
		if d.CommentID == commentID {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("feedback set: comment %q does not resolve on %s", commentID, ref)
	}
	w, err := newKVWrite(cfg, "feedback set", entityTypePR, ref, store.KeyDisposition(commentID), disposition, annotationChangeOrigin, actorFlag, true)
	if err != nil {
		return err
	}
	return w.set(st)
}
