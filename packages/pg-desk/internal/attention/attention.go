// Package attention is pg-desk's read-time attention evaluator
// (docs/behavior/pg-desk/attention.md, ADR 0081): one pure function,
// Evaluate, that both the dashboard payload and the pg-desk-attention plugin
// call, so a PR that needs the operator is decided in exactly one place.
//
// Evaluate runs four stages in order:
//
//  1. project: one View per entity, from the stored interpretation row plus
//     the narrow facts a rule needs (project.go);
//  2. raise: every enabled registered Rule returns zero or more Candidates
//     (rules.go);
//  3. suppress: a Chain of Responsibility drops a candidate with a recorded
//     reason (suppress.go);
//  4. group and rank: surviving candidates collapse to one Item per entity
//     (this file), which is placed in its work-context group, and groups and
//     items are put in canonical order (group.go).
//
// Purity (INV-ATTNEVAL-1): the package writes nothing, execs nothing, opens
// no network connection and reads time only through the injected Clock. It
// imports neither os/exec nor net; purity_test.go asserts that.
package attention

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// SchemaVersion is the schemaVersion of `pg-desk attention list --json`
// (additive evolution only).
const SchemaVersion = 1

// Clock is the only source of time. interpret.FixedClock satisfies it in
// tests.
type Clock = interpret.Clock

// Severity is an attention severity.
type Severity string

// The closed severity vocabulary, lowest first (mirrors
// config.AttentionSeverities).
const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// rank orders severities; an unknown severity ranks below low.
func (s Severity) rank() int {
	switch s {
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	}
	return 0
}

// Reader is the read-only view of the local store Evaluate needs.
// *store.Store satisfies it; nothing here writes.
type Reader interface {
	RequireNewSchema() error
	ListInterpretations() ([]store.Interpretation, error)
	// ListEntities lets the projector reach an issue or thread entity that
	// has no interpretation row.
	ListEntities() ([]store.Entity, error)
	GetEntity(repo, entityType, entityID string) (store.Entity, bool, error)
	GetPRAnnotation(repo, entityType, entityID string) (store.Annotation, bool, error)
	ListKVAnnotations(repo, entityType, entityID string) ([]store.KVAnnotation, error)
	// LinkedReader is the cross-reference read grouping uses (the same one
	// `pg-desk links` uses, so the two cannot disagree on what is linked).
	links.LinkedReader
}

// Inputs is everything Evaluate reads.
type Inputs struct {
	Store Reader
	// Repo is the configured repository every stored row is keyed by.
	Repo   string
	Config *config.Config
	Clock  Clock
	// Stacks names the PR stack of a PR. Nil switches the stack grouping
	// level off (see group.go).
	Stacks StackSource
}

// Candidate is one reason a rule found that an entity needs the operator.
type Candidate struct {
	Kind     string
	Type     string
	ID       string
	Severity Severity
	Reason   string
	// Since is when the cause began, as far as the stored data says (the
	// interpretation's as-of); the zero time when unknown.
	Since time.Time
}

// Item is the one result row of an entity that has surviving candidates. Its
// Type is the entity type and its ID the entity id, so {Type, ID} is directly
// a valid ref for `pg-desk links`.
type Item struct {
	Type     string   `json:"type"`
	ID       string   `json:"id"`
	Summary  string   `json:"summary"`
	Severity Severity `json:"severity"`
	// Rule is the primary (most severe) rule kind.
	Rule string `json:"rule"`
	// Group is the key of the item's group.
	Group string `json:"group"`
}

// Group is a set of items that belong to one work context, items in
// canonical order.
type Group struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Items []Item `json:"items"`
}

// Dropped is a candidate a suppressor removed, with who removed it
// (INV-ATTNEVAL-4).
type Dropped struct {
	Candidate Candidate
	// By names the first applicable suppressor: "hidden", "wip",
	// "suppress.attention", "suppress.<rule kind>" or a context
	// suppressor's name.
	By string
}

// Trace is everything the evaluator concluded about one entity; it is what
// `pg-desk attention explain` prints.
type Trace struct {
	Type string
	ID   string
	// Survived are the candidates that were not suppressed.
	Survived []Candidate
	// Dropped are the candidates a suppressor removed.
	Dropped []Dropped
	// NotRaised says, per enabled rule that raised nothing, why. Disabled
	// rules are listed with the reason "disabled".
	NotRaised map[string]string
	// Group is the group key of the entity's item; "" when it has none.
	Group string
}

// Result is Evaluate's answer. Items and Groups are in canonical order.
type Result struct {
	// Now is the injected clock's reading.
	Now time.Time
	// Degraded is true when the store is not on the migrated schema, so the
	// suppress.* annotation overrides could not be read (INV-ATTNEVAL-5).
	Degraded bool
	Items    []Item
	Groups   []Group
	// Traces is keyed by "<type>:<id>" and covers every projected entity: one
	// with a stored interpretation row, or a non-pr one with only a stored
	// entity row.
	Traces map[string]Trace
}

// ListDocument is the JSON document of `pg-desk attention list --json`:
// schemaVersion 1, evolving additively only.
type ListDocument struct {
	SchemaVersion int     `json:"schemaVersion"`
	Now           string  `json:"now"`
	Degraded      bool    `json:"degraded,omitempty"`
	Items         []Item  `json:"items"`
	Groups        []Group `json:"groups"`
}

// Document renders the result as the list document. Items and Groups are
// never null.
func (r Result) Document() ListDocument {
	d := ListDocument{
		SchemaVersion: SchemaVersion,
		Now:           r.Now.UTC().Format(time.RFC3339),
		Degraded:      r.Degraded,
		Items:         r.Items,
		Groups:        r.Groups,
	}
	if d.Items == nil {
		d.Items = []Item{}
	}
	if d.Groups == nil {
		d.Groups = []Group{}
	}
	return d
}

// Ref is the "<type>:<id>" form of an entity.
func Ref(entityType, id string) string { return entityType + ":" + id }

// Evaluate is the only entry point. It reads the store, never writes it, and
// reads time only from in.Clock. An unreadable store, or an unknown rule kind
// in the configuration, is an error, never an empty result that reads as
// "all clear" (INV-ATTNEVAL-6).
func Evaluate(in Inputs) (Result, error) {
	if in.Store == nil {
		return Result{}, fmt.Errorf("attention: no store")
	}
	if in.Clock == nil {
		return Result{}, fmt.Errorf("attention: no clock")
	}
	cfg := in.Config
	if cfg == nil {
		cfg = &config.Config{}
	}
	settings, err := Resolve(cfg.Attention)
	if err != nil {
		return Result{}, err
	}
	ann, degraded, err := newAnnotationReader(in.Store)
	if err != nil {
		return Result{}, err
	}

	views, err := Project(in.Store, in.Repo, cfg)
	if err != nil {
		return Result{}, err
	}
	byRef := make(map[string]*View, len(views))
	for _, v := range views {
		byRef[v.Ref()] = v
	}
	chain := suppressorChain()

	res := Result{
		Now:      in.Clock.Now().UTC(),
		Degraded: degraded,
		Items:    []Item{},
		Groups:   []Group{},
		Traces:   make(map[string]Trace, len(views)),
	}
	type survivor struct {
		view  *View
		cands []Candidate
	}
	var survivors []survivor

	for _, v := range views {
		tr := Trace{Type: v.Type, ID: v.ID, NotRaised: map[string]string{}}
		var raised []Candidate
		for _, r := range registeredRules() {
			rs := settings.Rules[r.Kind()]
			if !rs.Enabled {
				tr.NotRaised[r.Kind()] = "disabled"
				continue
			}
			cs, why := r.Raise(v, rs)
			if len(cs) == 0 {
				if why == "" {
					why = "did not apply"
				}
				tr.NotRaised[r.Kind()] = why
				continue
			}
			raised = append(raised, cs...)
		}
		if len(raised) > 0 {
			state, err := ann.read(v.Repo, v.Type, v.ID)
			if err != nil {
				return Result{}, err
			}
			for _, c := range raised {
				if by, dropped := firstSuppressor(chain, c, state, byRef); dropped {
					tr.Dropped = append(tr.Dropped, Dropped{Candidate: c, By: by})
					continue
				}
				tr.Survived = append(tr.Survived, c)
			}
		}
		if len(tr.Survived) > 0 {
			survivors = append(survivors, survivor{view: v, cands: tr.Survived})
		}
		res.Traces[v.Ref()] = tr
	}

	labels := map[string]string{}
	var items []Item
	for _, s := range survivors {
		item := collapse(s.view, s.cands)
		anchor, err := groupOf(in, s.view)
		if err != nil {
			return Result{}, fmt.Errorf("attention: group %s: %w", s.view.Ref(), err)
		}
		item.Group = anchor.key()
		labels[item.Group] = anchor.Label
		items = append(items, item)
	}
	res.Groups = buildGroups(items, labels)
	res.Items = flatten(res.Groups)
	for _, it := range res.Items {
		tr := res.Traces[Ref(it.Type, it.ID)]
		tr.Group = it.Group
		res.Traces[Ref(it.Type, it.ID)] = tr
	}
	return res, nil
}

// collapse folds an entity's surviving candidates into its one item
// (INV-ATTNEVAL-3): the most severe candidate is the primary reason (ties
// broken by rule kind, so the result is deterministic), and the summary says
// how many others the entity raised.
func collapse(v *View, cands []Candidate) Item {
	sorted := append([]Candidate(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Severity.rank() != sorted[j].Severity.rank() {
			return sorted[i].Severity.rank() > sorted[j].Severity.rank()
		}
		return sorted[i].Kind < sorted[j].Kind
	})
	primary := sorted[0]
	summary := primary.Reason
	if n := len(sorted) - 1; n > 0 {
		summary = fmt.Sprintf("%s (+%d more)", summary, n)
	}
	return Item{
		Type:     v.Type,
		ID:       v.ID,
		Summary:  summary,
		Severity: primary.Severity,
		Rule:     primary.Kind,
		// Group is filled in by the grouping stage (group.go).
	}
}

// joinReasons renders a list of causes for a rule's reason text.
func joinReasons(parts []string) string { return strings.Join(parts, "; ") }
