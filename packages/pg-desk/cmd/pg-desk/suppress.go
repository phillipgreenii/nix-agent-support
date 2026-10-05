package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newSuppressCmd(t, true), newSuppressCmd(t, false))
	}
}

// newSuppressCmd builds `pg-desk <type> suppress <id> --kind K` (suppress
// true) or `unsuppress <id> --kind K`: the suppress.<kind> annotation, read by
// every PR rule as step 2 of the precedence order [design 6.9, 9.6].
func newSuppressCmd(entityType string, suppress bool) *cobra.Command {
	var kind, actor string
	verb, short := "suppress", "Suppress a kind of signal on"
	if !suppress {
		verb, short = "unsuppress", "Stop suppressing a kind of signal on"
	}
	c := &cobra.Command{
		Use:   verb + " <id>",
		Short: fmt.Sprintf("%s a %s", short, entityType),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSuppress(cmd, verb, entityType, args[0], kind, actor, suppress)
		},
	}
	c.Flags().StringVar(&kind, "kind", "", "The kind to (un)suppress (required)")
	c.Flags().StringVar(&actor, "actor", "", "Attribute this to this actor (defaults to the configured actor)")
	_ = c.MarkFlagRequired("kind")
	return c
}

func runSuppress(cmd *cobra.Command, verb, entityType, ref, kind, actorFlag string, suppress bool) error {
	if strings.TrimSpace(kind) == "" {
		return fmt.Errorf("%s: --kind must not be empty", verb)
	}
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("%s: load config: %w", verb, err)
	}
	st, err := openNewSchemaStore(verb)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	value := ""
	if suppress {
		value = "true"
	}
	w, err := newKVWrite(cfg, verb, entityType, ref, store.KeySuppress(kind), value, annotationChangeOrigin, actorFlag, true)
	if err != nil {
		return err
	}
	if suppress {
		if err := w.set(st); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "suppressed %s on %s %s\n", kind, entityType, w.id)
		return err
	}
	removed, err := w.remove(st)
	if err != nil {
		return err
	}
	if !removed {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s was not suppressed on %s %s; nothing to do\n", kind, entityType, w.id)
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "unsuppressed %s on %s %s\n", kind, entityType, w.id)
	return err
}
