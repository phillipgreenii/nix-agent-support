package main

import (
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The typed homes of the kept verbs: pg-desk <type> hide|unhide|wip and
// pg-desk pr feedback list|set [design 6.9]. Until the cutover is done
// everywhere the top-level hide/unhide/wip/feedback keep working untouched,
// so each typed form is schema-dual: on an old-schema store a pr verb runs
// exactly the top-level verb; on a new-schema store it writes the key/value
// annotation API (hidden, wip, disposition.<comment_id>). issue and thread
// have no old-schema counterpart and refuse an old-schema store.

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newTypedHideCmd(t), newTypedUnhideCmd(t), newTypedWIPCmd(t))
	}
	typeGroup(entityTypePR).AddCommand(newTypedFeedbackCmd())
}

// runSchemaDual opens the store and runs newPath against it when it is on
// the new schema. On an old-schema store it closes the handle and runs
// oldPath (the existing top-level verb, which opens its own); a nil oldPath
// means the verb has no old-schema form, so the store's refusal is returned.
func runSchemaDual(oldPath func() error, newPath func(st *store.Store) error) error {
	st, err := deskStoreOpen()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	rerr := st.RequireNewSchema()
	if rerr == nil {
		defer func() { _ = st.Close() }()
		return newPath(st)
	}
	_ = st.Close()
	if oldPath != nil && errors.Is(rerr, store.ErrOldSchema) {
		return oldPath()
	}
	return rerr
}

// oldPathFor is the old-schema form for entityType: the top-level pr verb
// for pr, none for any other type.
func oldPathFor(entityType string, f func() error) func() error {
	if entityType != entityTypePR {
		return nil
	}
	return f
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

func runTypedHidden(cmd *cobra.Command, cmdName, entityType, ref string, hidden bool, reason string) error {
	return runSchemaDual(
		oldPathFor(entityType, func() error { return setHiddenAnnotation(cmd, cmdName, ref, hidden, reason) }),
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
			ref := args[1]
			return runSchemaDual(
				oldPathFor(entityType, func() error { return setWIPAnnotation(cmd, ref, wip) }),
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
		},
	}
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
			return runSchemaDual(
				func() error { return runFeedbackList(cmd, args[0]) },
				func(st *store.Store) error { return runTypedFeedbackList(cmd, st, args[0]) },
			)
		},
	})
	var disposition, actor string
	set := &cobra.Command{
		Use:   "set <id> <comment-id>",
		Short: "Record a disposition override, attributed to the caller",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validDisposition(disposition) {
				return fmt.Errorf("feedback set: --disposition must be one of open|will-fix|wont-fix|no-action, got %q", disposition)
			}
			return runSchemaDual(
				func() error { return runFeedbackSetWith(cmd, args[0], args[1], disposition, actor) },
				func(st *store.Store) error { return runTypedFeedbackSet(cmd, st, args[0], args[1], disposition, actor) },
			)
		},
	}
	set.Flags().StringVar(&disposition, "disposition", "", "open|will-fix|wont-fix|no-action (required)")
	set.Flags().StringVar(&actor, "actor", "", "Attribute this override to this actor (defaults to the configured actor)")
	g.AddCommand(set)
	return g
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

// runTypedFeedbackList is feedback list on a new-schema store: the stored
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

// runTypedFeedbackSet is feedback set on a new-schema store: it writes
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
