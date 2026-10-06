package attention

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/dependency"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Suppression is a Chain of Responsibility: a candidate is dropped, with its
// reason recorded, by the first applicable of
//
//  1. the entity is hidden (an unmigrated store reads the old annotation
//     column), or is a wip own PR and the candidate is a pr.own-* rule;
//  2. the entity carries suppress.attention, or suppress.<rule kind> for the
//     candidate's own kind;
//  3. a registered context Suppressor claims it.
//
// Steps 1 and 2 are the first two steps of every PR rule's precedence order
// in docs/behavior/pg-desk/annotate.md.

// SuppressEnv is what a context suppressor may read about an entity's
// neighbours. It is read-only and offline, like the rest of the evaluator.
type SuppressEnv struct {
	// Views is every projected entity, keyed by "<type>:<id>".
	Views map[string]*View
	// Dependencies answers PR-to-PR dependency questions over the same store
	// read as the rest of the evaluation.
	Dependencies *dependency.Resolver
}

// Suppressor is a context suppressor: step 3 of the chain, which MAY look at
// the entity's neighbours through env. The first member is
// blockedByOpenDependency (dependency_suppressor.go).
type Suppressor interface {
	// Name is recorded as the suppressing reason.
	Name() string
	// Suppress reports whether it claims the candidate. An error is an
	// unreadable store, which fails the evaluation: it is never an all-clear
	// (INV-ATTNEVAL-6).
	Suppress(c Candidate, env *SuppressEnv) (bool, error)
}

var (
	suppressorsMu sync.RWMutex
	suppressors   []Suppressor
)

// RegisterSuppressor appends a context suppressor to the chain. It panics on
// a duplicate name. Registration order is evaluation order.
func RegisterSuppressor(s Suppressor) {
	suppressorsMu.Lock()
	defer suppressorsMu.Unlock()
	for _, have := range suppressors {
		if have.Name() == s.Name() {
			panic(fmt.Sprintf("attention: RegisterSuppressor called twice for %q", s.Name()))
		}
	}
	suppressors = append(suppressors, s)
}

func suppressorChain() []Suppressor {
	suppressorsMu.RLock()
	defer suppressorsMu.RUnlock()
	return append([]Suppressor(nil), suppressors...)
}

// annotationState is the suppression-relevant annotation state of one entity.
type annotationState struct {
	hidden bool
	wip    bool
	// suppressed holds the suppress.<x> keys set to true, by x ("attention" or
	// a rule kind). Always empty on an unmigrated store.
	suppressed map[string]bool
}

// annotationReader reads annotation state through the schema the store is on.
type annotationReader struct {
	r        Reader
	migrated bool
}

// newAnnotationReader probes the schema once. degraded is true on an
// unmigrated store, where suppress.* overrides do not exist
// (INV-ATTNEVAL-5). Any other error is returned: an unreadable store is never
// an all-clear.
func newAnnotationReader(r Reader) (a *annotationReader, degraded bool, err error) {
	switch e := r.RequireNewSchema(); {
	case e == nil:
		return &annotationReader{r: r, migrated: true}, false, nil
	case errors.Is(e, store.ErrOldSchema):
		return &annotationReader{r: r}, true, nil
	default:
		return nil, false, fmt.Errorf("attention: read schema version: %w", e)
	}
}

func (a *annotationReader) read(repo, entityType, id string) (annotationState, error) {
	st := annotationState{suppressed: map[string]bool{}}
	if !a.migrated {
		ann, found, err := a.r.GetPRAnnotation(repo, entityType, id)
		if err != nil {
			return st, fmt.Errorf("attention: read annotation of %s:%s: %w", entityType, id, err)
		}
		if found {
			st.hidden = ann.Hidden != nil && *ann.Hidden
			st.wip = ann.WIP != nil && *ann.WIP
		}
		return st, nil
	}
	anns, err := a.r.ListKVAnnotations(repo, entityType, id)
	if err != nil {
		return st, fmt.Errorf("attention: read annotations of %s:%s: %w", entityType, id, err)
	}
	for _, an := range anns {
		switch {
		case an.Key == store.AnnotationHidden:
			var h struct {
				Value bool `json:"value"`
			}
			if jerr := json.Unmarshal([]byte(an.Value), &h); jerr != nil {
				return st, fmt.Errorf("attention: decode hidden annotation of %s:%s: %w", entityType, id, jerr)
			}
			st.hidden = h.Value
		case an.Key == store.AnnotationWIP:
			st.wip = an.Value == "true"
		case strings.HasPrefix(an.Key, store.KeySuppress("")):
			if an.Value == "true" {
				st.suppressed[strings.TrimPrefix(an.Key, store.KeySuppress(""))] = true
			}
		}
	}
	return st, nil
}

// firstSuppressor walks the chain and returns the first applicable
// suppressor's name.
func firstSuppressor(chain []Suppressor, c Candidate, st annotationState, env *SuppressEnv) (by string, dropped bool, err error) {
	switch {
	case st.hidden:
		return "hidden", true, nil
	case st.wip && strings.HasPrefix(c.Kind, "pr.own-"):
		return "wip", true, nil
	case st.suppressed["attention"]:
		return store.KeySuppress("attention"), true, nil
	case st.suppressed[c.Kind]:
		return store.KeySuppress(c.Kind), true, nil
	}
	for _, s := range chain {
		claimed, serr := s.Suppress(c, env)
		if serr != nil {
			return "", false, fmt.Errorf("attention: suppressor %s on %s: %w", s.Name(), Ref(c.Type, c.ID), serr)
		}
		if claimed {
			return s.Name(), true, nil
		}
	}
	return "", false, nil
}
