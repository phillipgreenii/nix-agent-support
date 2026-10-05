package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func init() {
	typeGroup(entityTypePR).AddCommand(newForceReviewCmd())
}

// newForceReviewCmd builds `pg-desk pr force-review <id> [--clear]`: without
// --clear it only sets the force_review annotation to the head SHA the PR is
// at now; with --clear it removes the annotation. Deciding when to honor and
// clear the flag is the deciders' [design 6.9, 9.6]; this is the mechanism.
func newForceReviewCmd() *cobra.Command {
	var actor, origin string
	var clear bool
	c := &cobra.Command{
		Use:   "force-review <id>",
		Short: "Request a review of a PR regardless of the deciders' verdict",
		Long: `Set the force_review annotation on the PR to the head SHA it is at now, and
append an annotation_changed record. Setting never consumes the flag.

With --clear, remove the force_review annotation instead and append one
annotation_changed record (from --origin, default "pg-desk"); a decider uses this to
consume the flag once. Clearing a flag that is not set exits 0 and appends
nothing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !clear && cmd.Flags().Changed("origin") {
				return fmt.Errorf("force-review: --origin applies only with --clear")
			}
			return runForceReview(cmd, args[0], actor, origin, clear)
		},
	}
	c.Flags().StringVar(&actor, "actor", "", "Attribute this to this actor (defaults to the configured actor)")
	c.Flags().BoolVar(&clear, "clear", false, "Remove the force_review annotation instead of setting it")
	c.Flags().StringVar(&origin, "origin", annotationChangeOrigin, "Who is clearing the flag (only with --clear)")
	return c
}

func runForceReview(cmd *cobra.Command, ref, actorFlag, origin string, clear bool) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("force-review: load config: %w", err)
	}
	st, err := openNewSchemaStore("force-review")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if clear && strings.TrimSpace(origin) == "" {
		return fmt.Errorf("force-review: --origin must not be empty")
	}
	if !clear {
		origin = annotationChangeOrigin
	}
	w, err := newKVWrite(cfg, "force-review", entityTypePR, ref, store.AnnotationForceReview, "", origin, actorFlag, true)
	if err != nil {
		return err
	}
	if clear {
		removed, err := w.remove(st)
		if err != nil {
			return err
		}
		if !removed {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "force-review was not requested on pr %s; nothing to do\n", w.id)
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "force-review cleared on pr %s\n", w.id)
		return err
	}
	e, found, err := st.GetEntity(w.repo, entityTypePR, w.id)
	if err != nil {
		return fmt.Errorf("force-review: %w", err)
	}
	if !found {
		return fmt.Errorf("force-review: %s does not resolve to a stored entity", ref)
	}
	if e.HeadSHA == "" {
		return fmt.Errorf("force-review: %s has no recorded head commit", ref)
	}
	w.value = e.HeadSHA
	if err := w.set(st); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "force-review requested on pr %s at %s\n", w.id, e.HeadSHA)
	return err
}
