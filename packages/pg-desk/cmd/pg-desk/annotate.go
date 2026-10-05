package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// annotationChangeOrigin is the default origin of an annotation written
// through a pg-desk verb (the annotation row's origin and the change_log
// record's origin) [design 9.6].
const annotationChangeOrigin = "pg-desk"

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newAnnotateCmd(t))
	}
}

// newAnnotateCmd builds `pg-desk <type> annotate <id> --key K --value V
// [--origin O]`: the general key/value annotation write [design 6.9, 9.6].
func newAnnotateCmd(entityType string) *cobra.Command {
	var key, value, origin, actor string
	c := &cobra.Command{
		Use:   "annotate <id>",
		Short: fmt.Sprintf("Set a key/value annotation on a %s", entityType),
		Long: fmt.Sprintf(`Set the annotation --key to --value on the %s <id>, replacing any earlier value
of that key, and append an annotation_changed record in the same transaction.
Reserved keys (hidden, wip, disposition.<comment_id>, suppress.<kind>,
force_review, ready_to_land) MUST carry the value shape documented for them;
any other key is stored as given. --origin names the writer (default %q).`, entityType, annotationChangeOrigin),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnnotate(cmd, entityType, args[0], key, value, origin, actor)
		},
	}
	c.Flags().StringVar(&key, "key", "", "Annotation key (required)")
	c.Flags().StringVar(&value, "value", "", "Annotation value (required)")
	c.Flags().StringVar(&origin, "origin", annotationChangeOrigin, "Who is writing the annotation")
	c.Flags().StringVar(&actor, "actor", "", "Attribute the annotation to this actor (defaults to the configured actor)")
	_ = c.MarkFlagRequired("key")
	_ = c.MarkFlagRequired("value")
	return c
}

func runAnnotate(cmd *cobra.Command, entityType, ref, key, value, origin, actorFlag string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("annotate: --key must not be empty")
	}
	if strings.TrimSpace(origin) == "" {
		return fmt.Errorf("annotate: --origin must not be empty")
	}
	if err := validateAnnotationValue(key, value); err != nil {
		return fmt.Errorf("annotate: %w", err)
	}
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("annotate: load config: %w", err)
	}
	st, err := openNewSchemaStore("annotate")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	w, err := newKVWrite(cfg, "annotate", entityType, ref, key, value, origin, actorFlag, true)
	if err != nil {
		return err
	}
	if err := w.set(st); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "annotated %s %s: %s\n", entityType, w.id, key)
	return err
}

// kvWrite is one resolved key/value annotation write.
type kvWrite struct {
	cmdName, entityType, ref string
	repo, id                 string
	key, value, origin, by   string
}

// newKVWrite resolves ref and the acting actor. requireActor makes a missing
// actor an error (the hide/unhide/wip/feedback verbs keep their old rule of
// accepting an empty one).
func newKVWrite(cfg *config.Config, cmdName, entityType, ref, key, value, origin, actorFlag string, requireActor bool) (*kvWrite, error) {
	repo, id, err := resolveTypedRef(cfg, entityType, ref)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cmdName, err)
	}
	actor := actorFor(cfg, actorFlag)
	if requireActor && actor == "" {
		return nil, fmt.Errorf("%s: no actor available (pass --actor or set config's actor)", cmdName)
	}
	return &kvWrite{
		cmdName: cmdName, entityType: entityType, ref: ref, repo: repo, id: id,
		key: key, value: value, origin: origin, by: actor,
	}, nil
}

// set writes the annotation and its annotation_changed record (one
// transaction, in the store).
func (w *kvWrite) set(st *store.Store) error {
	err := st.SetAnnotation(store.KVAnnotation{
		Repo: w.repo, EntityType: w.entityType, EntityID: w.id,
		Key: w.key, Value: w.value, Origin: w.origin, SetBy: w.by, SetAt: annotationNow(),
	})
	return w.wrap(err)
}

// remove deletes the annotation (appending annotation_changed when a row was
// removed) and reports whether one existed.
func (w *kvWrite) remove(st *store.Store) (bool, error) {
	removed, err := st.DeleteAnnotation(w.repo, w.entityType, w.id, w.key, w.origin, annotationNow())
	return removed, w.wrap(err)
}

func (w *kvWrite) wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNoEntity) {
		return fmt.Errorf("%s: %s does not resolve to a stored entity", w.cmdName, w.ref)
	}
	return fmt.Errorf("%s: %w", w.cmdName, err)
}

// hiddenValue is the JSON value of the reserved hidden key
// {"value": true|false, "reason": string|null} [design 9.6]. An empty reason
// is null.
func hiddenValue(hidden bool, reason string) string {
	var r *string
	if reason != "" {
		r = &reason
	}
	b, _ := json.Marshal(struct {
		Value  bool    `json:"value"`
		Reason *string `json:"reason"`
	}{hidden, r})
	return string(b)
}

// boolValue is the "true"/"false" value of the wip and ready_to_land keys.
func boolValue(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// validateAnnotationValue enforces the reserved-key value shapes of
// design 9.6, which the precedence-order readers rely on. A key that is not
// reserved (including decider.<name>.<k>, a free string) is accepted as
// given.
func validateAnnotationValue(key, value string) error {
	bad := func(want string) error { return fmt.Errorf("key %q takes %s, got %q", key, want, value) }
	switch {
	case key == store.AnnotationHidden:
		var h struct {
			Value  *bool   `json:"value"`
			Reason *string `json:"reason"`
		}
		dec := json.NewDecoder(strings.NewReader(value))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&h); err != nil || h.Value == nil || dec.More() {
			return bad(`JSON {"value": true|false, "reason": string|null}`)
		}
	case key == store.AnnotationWIP, key == store.AnnotationReadyToLand:
		if value != "true" && value != "false" {
			return bad("true or false")
		}
	case key == store.AnnotationForceReview:
		if strings.TrimSpace(value) == "" {
			return bad("the head SHA it was requested at")
		}
	case strings.HasPrefix(key, "suppress."):
		if strings.TrimPrefix(key, "suppress.") == "" {
			return fmt.Errorf("key %q needs a kind: suppress.<kind>", key)
		}
		if value != "true" {
			return bad("true")
		}
	case strings.HasPrefix(key, "disposition."):
		if strings.TrimPrefix(key, "disposition.") == "" {
			return fmt.Errorf("key %q needs a comment id: disposition.<comment_id>", key)
		}
		switch value {
		case store.DispositionWillFix, store.DispositionWontFix, store.DispositionNoAction:
		default:
			return bad("will-fix, wont-fix or no-action")
		}
	}
	return nil
}
