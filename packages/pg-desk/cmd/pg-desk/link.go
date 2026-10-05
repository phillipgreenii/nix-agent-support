package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// linkChangeOrigin labels the change_log rows `link add|remove` appends:
// pg-desk itself originates them [design 6.5].
const linkChangeOrigin = "pg-desk"

// defaultLinkRelation is the relation `link add` records when --relation is
// not given (AddExternalXref requires a non-empty relation).
const defaultLinkRelation = "references"

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newLinkCmd(t))
	}
}

// newLinkCmd builds the `pg-desk <type> link` group with its add and remove
// verbs: the operator-facing external-link verbs [design 6.3]. An external
// link is a claim by the acting actor next to (never instead of) any derived
// claim on the same link.
func newLinkCmd(entityType string) *cobra.Command {
	g := &cobra.Command{
		Use:   "link",
		Short: fmt.Sprintf("Add or remove an external link on a %s", entityType),
	}
	g.AddCommand(newLinkAddCmd(entityType), newLinkRemoveCmd(entityType))
	return g
}

func newLinkAddCmd(entityType string) *cobra.Command {
	var relation, reason, actor string
	c := &cobra.Command{
		Use:   "add <id> <type>:<id>",
		Short: fmt.Sprintf("Record an external link from a %s to another entity", entityType),
		Long: fmt.Sprintf(`Record an external link (origin external:<actor>, with when and an optional
reason) from the %s <id> to the entity <type>:<id>. The link is recorded
even when the same link is already derived, so it survives if the source
entity later changes; it never replaces or removes a derived link. Appends a
link_changed record (work_changed for a PR whose other end is one of its own
work items) for both entities.`, entityType),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLinkAdd(cmd, entityType, args[0], args[1], relation, reason, actor)
		},
	}
	c.Flags().StringVar(&relation, "relation", defaultLinkRelation, "Relation of the link")
	c.Flags().StringVar(&reason, "reason", "", "Why the link exists (recorded with the claim)")
	c.Flags().StringVar(&actor, "actor", "", "Attribute the link to this actor (defaults to the configured actor)")
	return c
}

func newLinkRemoveCmd(entityType string) *cobra.Command {
	var actor string
	c := &cobra.Command{
		Use:   "remove <id> <type>:<id>",
		Short: fmt.Sprintf("Remove the acting actor's external links between a %s and another entity", entityType),
		Long: fmt.Sprintf(`Remove the external links the acting actor added between the %s <id> and the
entity <type>:<id>, in either direction and across all relations. Only that
actor's external claims are removed: a derived link, or another actor's
external link, is never touched. On a link that exists only as derived it
fails: change the source entity instead.`, entityType),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLinkRemove(cmd, entityType, args[0], args[1], actor)
		},
	}
	c.Flags().StringVar(&actor, "actor", "", "Act as this actor (defaults to the configured actor)")
	return c
}

// linkEnds is the resolved pair a link verb operates on.
type linkEnds struct {
	repo           string
	fromType, from string
	toType, to     string
}

// parseTypedRef splits "<type>:<id>" at the FIRST colon (the id may itself
// contain colons), as pg-desk links does.
func parseTypedRef(s string) (entityType, ref string, err error) {
	entityType, ref, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || entityType == "" || ref == "" {
		return "", "", fmt.Errorf("%q is not shaped <type>:<id>", s)
	}
	for _, t := range typedEntityTypes {
		if t == entityType {
			return entityType, ref, nil
		}
	}
	return "", "", fmt.Errorf("unknown entity type %q in %q (want one of %s)", entityType, s, strings.Join(typedEntityTypes, ", "))
}

func resolveLinkEnds(cfg *config.Config, entityType, ref, other string) (linkEnds, error) {
	otherType, otherRef, err := parseTypedRef(other)
	if err != nil {
		return linkEnds{}, err
	}
	repo, id, err := resolveTypedRef(cfg, entityType, ref)
	if err != nil {
		return linkEnds{}, err
	}
	otherRepo, otherID, err := resolveTypedRef(cfg, otherType, otherRef)
	if err != nil {
		return linkEnds{}, err
	}
	if repo != otherRepo {
		return linkEnds{}, fmt.Errorf("%s and %s are in different repositories", ref, other)
	}
	if entityType == otherType && id == otherID {
		return linkEnds{}, fmt.Errorf("cannot link %s %s to itself", entityType, id)
	}
	return linkEnds{repo: repo, fromType: entityType, from: id, toType: otherType, to: otherID}, nil
}

// pairLinks is every stored claim on a link between the two ends, in either
// direction.
func pairLinks(st *store.Store, e linkEnds) ([]store.XrefLink, error) {
	out, err := st.ListXrefLinksFrom(e.repo, e.fromType, e.from)
	if err != nil {
		return nil, err
	}
	in, err := st.ListXrefLinksTo(e.repo, e.fromType, e.from)
	if err != nil {
		return nil, err
	}
	var pair []store.XrefLink
	for _, l := range out {
		if l.ToType == e.toType && l.ToID == e.to {
			pair = append(pair, l)
		}
	}
	for _, l := range in {
		if l.FromType == e.toType && l.FromID == e.to {
			pair = append(pair, l)
		}
	}
	return pair, nil
}

// openNewSchemaStore opens the store for a link or consumer verb and refuses an
// old-schema one.
func openNewSchemaStore(cmdName string) (*store.Store, error) {
	st, err := deskStoreOpen()
	if err != nil {
		return nil, fmt.Errorf("%s: open store: %w", cmdName, err)
	}
	if err := requireNewSchemaForTypedVerb(st); err != nil {
		_ = st.Close()
		return nil, err
	}
	return st, nil
}

func runLinkAdd(cmd *cobra.Command, entityType, ref, other, relation, reason, actorFlag string) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("link add: load config: %w", err)
	}
	ends, err := resolveLinkEnds(cfg, entityType, ref, other)
	if err != nil {
		return fmt.Errorf("link add: %w", err)
	}
	actor := actorFor(cfg, actorFlag)
	if actor == "" {
		return fmt.Errorf("link add: no actor available (pass --actor or set config's actor)")
	}
	if strings.TrimSpace(relation) == "" {
		return fmt.Errorf("link add: --relation must not be empty")
	}
	st, err := openNewSchemaStore("link add")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	before, err := pairLinks(st, ends)
	if err != nil {
		return fmt.Errorf("link add: %w", err)
	}
	now := annotationNow()
	if err := st.AddExternalXref(store.XrefLink{
		Repo: ends.repo, FromType: ends.fromType, FromID: ends.from,
		ToType: ends.toType, ToID: ends.to,
		Relation: relation, Actor: actor, ActedAt: now, Reason: reason,
		FirstSeen: now, LastConfirmed: now,
	}); err != nil {
		return fmt.Errorf("link add: %w", err)
	}
	after, err := pairLinks(st, ends)
	if err != nil {
		return fmt.Errorf("link add: %w", err)
	}
	if err := appendLinkChanges(st, ends.repo, before, after, now); err != nil {
		return fmt.Errorf("link add: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "linked %s %s -> %s %s (relation %s, origin external:%s)\n",
		ends.fromType, ends.from, ends.toType, ends.to, relation, actor)
	return err
}

func runLinkRemove(cmd *cobra.Command, entityType, ref, other, actorFlag string) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("link remove: load config: %w", err)
	}
	ends, err := resolveLinkEnds(cfg, entityType, ref, other)
	if err != nil {
		return fmt.Errorf("link remove: %w", err)
	}
	actor := actorFor(cfg, actorFlag)
	if actor == "" {
		return fmt.Errorf("link remove: no actor available (pass --actor or set config's actor)")
	}
	st, err := openNewSchemaStore("link remove")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	before, err := pairLinks(st, ends)
	if err != nil {
		return fmt.Errorf("link remove: %w", err)
	}
	mine := "external:" + actor
	removed := 0
	for _, l := range before {
		if l.Origin != mine {
			continue
		}
		ok, err := st.RemoveExternalXref(l.Repo, l.FromType, l.FromID, l.ToType, l.ToID, l.Relation, actor)
		if err != nil {
			return fmt.Errorf("link remove: %w", err)
		}
		if ok {
			removed++
		}
	}
	if removed == 0 {
		return noExternalLinkError(before, ends, actor)
	}
	after, err := pairLinks(st, ends)
	if err != nil {
		return fmt.Errorf("link remove: %w", err)
	}
	if err := appendLinkChanges(st, ends.repo, before, after, annotationNow()); err != nil {
		return fmt.Errorf("link remove: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %d external link(s) between %s %s and %s %s (origin %s)\n",
		removed, ends.fromType, ends.from, ends.toType, ends.to, mine)
	return err
}

// noExternalLinkError explains why link remove found nothing of the actor's
// to remove: a link that exists only as derived is changed at its source, not
// here [design 6.3].
func noExternalLinkError(pair []store.XrefLink, ends linkEnds, actor string) error {
	var extractors []string
	seen := map[string]bool{}
	for _, l := range pair {
		if x, ok := strings.CutPrefix(l.Origin, "derived:"); ok && !seen[x] {
			seen[x] = true
			extractors = append(extractors, x)
		}
	}
	sort.Strings(extractors)
	if len(extractors) > 0 {
		return fmt.Errorf("link remove: derived from %s; change the source entity", strings.Join(extractors, ", "))
	}
	return fmt.Errorf("link remove: no external link by %s between %s %s and %s %s",
		actor, ends.fromType, ends.from, ends.toType, ends.to)
}

// appendLinkChanges appends the local-source change records for the link
// changes between two observations of the pair's claims, one per entity end
// per kind (link_changed, or work_changed for a PR whose other end is its own
// work item), with origin pg-desk. An end with no stored entity row is
// skipped; the other end still gets its record [design 6.5].
func appendLinkChanges(st *store.Store, repo string, before, after []store.XrefLink, at string) error {
	changes := classify.DiffLinks(before, after)
	for _, t := range classify.LinkRecords(changes, classify.NewStoreWorkLookup(st, repo)) {
		if _, err := st.AppendEntityChange(repo, t.EntityType, t.EntityID, []string{string(t.Kind)}, linkChangeOrigin, at); err != nil && !errors.Is(err, store.ErrNoEntity) {
			return fmt.Errorf("append %s for %s %s: %w", t.Kind, t.EntityType, t.EntityID, err)
		}
	}
	return nil
}
