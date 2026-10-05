package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func init() {
	typeGroup(entityTypePR).AddCommand(newForceReviewCmd())
}

// newForceReviewCmd builds `pg-desk pr force-review <id>`: it only sets the
// force_review annotation to the head SHA the PR is at now. Honoring and
// clearing the flag once is the deciders' [design 6.9, 9.6].
func newForceReviewCmd() *cobra.Command {
	var actor string
	c := &cobra.Command{
		Use:   "force-review <id>",
		Short: "Request a review of a PR regardless of the deciders' verdict",
		Long: `Set the force_review annotation on the PR to the head SHA it is at now, and
append an annotation_changed record. This only sets the flag; a decider
consumes it once.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForceReview(cmd, args[0], actor)
		},
	}
	c.Flags().StringVar(&actor, "actor", "", "Attribute this to this actor (defaults to the configured actor)")
	return c
}

func runForceReview(cmd *cobra.Command, ref, actorFlag string) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("force-review: load config: %w", err)
	}
	st, err := openNewSchemaStore("force-review")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	w, err := newKVWrite(cfg, "force-review", entityTypePR, ref, store.AnnotationForceReview, "", annotationChangeOrigin, actorFlag, true)
	if err != nil {
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
