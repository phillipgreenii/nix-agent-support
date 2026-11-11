// Package levels holds the admission decision of pg-router: one level
// snapshot type and one pure function, Decide, that returns the admission
// decision and its explanation together, so they cannot disagree.
//
// The package is a leaf: it defines its own minimal input types and imports
// neither config nor eventqueue (both import it). The clause compiler
// converts compiled requirements into Requirement and Clause.
//
// Exported names: Scale, Registry, Op (OpEq, OpNe, OpLt, OpLe, OpGt, OpGe),
// AllowedLevels, Kind (KindLevel, KindUnknown, KindStale), State (StateHeld,
// StateStale), Level, Snapshot, Clause, Requirement, Entry, Result, Decide.
package levels

import (
	"sort"
	"time"
)

// Scale is the ordered level scale of one resource, best level first, plus
// the level an absent reading is taken to have (the resource's unknown value:
// the best level for every resource except credentials, which read the worst).
type Scale struct {
	Levels  []string
	Unknown string
}

// Registry maps a resource name to its scale. The decision function reads
// scales from it and hard-codes none.
type Registry map[string]Scale

// Op is a clause operator; "greater" means better, by position in the scale.
type Op string

// The six clause operators.
const (
	OpEq Op = "=="
	OpNe Op = "!="
	OpLt Op = "<"
	OpLe Op = "<="
	OpGt Op = ">"
	OpGe Op = ">="
)

// AllowedLevels expands "<resource> op level" over the scale into the set of
// levels that satisfy it, best first. It returns nil when level is not on the
// scale or op is unknown. Position 0 is the best level, so a greater level is
// a lower index.
func AllowedLevels(s Scale, op Op, level string) []string {
	pos := -1
	for i, l := range s.Levels {
		if l == level {
			pos = i
		}
	}
	if pos < 0 {
		return nil
	}
	var out []string
	for i, l := range s.Levels {
		var ok bool
		switch op {
		case OpEq:
			ok = i == pos
		case OpNe:
			ok = i != pos
		case OpLt:
			ok = i > pos
		case OpLe:
			ok = i >= pos
		case OpGt:
			ok = i < pos
		case OpGe:
			ok = i <= pos
		default:
			return nil
		}
		if ok {
			out = append(out, l)
		}
	}
	return out
}

// Kind says why a clause fails.
type Kind string

// Entry kinds.
const (
	// KindLevel: the held level is outside the clause's allowed set.
	KindLevel Kind = "level"
	// KindUnknown: no level was held; the unknown value is outside the set.
	KindUnknown Kind = "unknown"
	// KindStale: the held level is stale (inside its grace period) and is
	// outside the set.
	KindStale Kind = "stale"
)

// State is the projection's verdict on a held level.
type State int

// Level states. The staleness rules are applied by the projection, not here.
const (
	StateHeld State = iota
	StateStale
)

// Level is one resource's current level as the projection decided it.
type Level struct {
	Value    string
	State    State
	Source   string
	Owner    string
	Since    time.Time // when the level was reported or entered
	ExpireAt time.Time // zero means no expiry
}

// Snapshot is the single level snapshot type: the levels per resource and the
// evaluation time. It carries no lock; the owner publishes whole snapshots.
// A resource with no entry reads as absent.
type Snapshot struct {
	Now    time.Time
	Levels map[string]Level
}

// Clause is one ORIGINAL requires clause. Allowed is the set of levels that
// satisfy this clause alone.
type Clause struct {
	Index    int
	Text     string
	Resource string
	Allowed  []string
}

// Requirement is one registration's compiled requirements. Clauses on one
// resource are ANDed (an intersection of allowed sets); different resources
// are ANDed. An empty Requirement always admits.
type Requirement struct {
	Clauses []Clause
}

// Entry explains one failing original clause.
type Entry struct {
	ClauseIndex int
	ClauseText  string
	Resource    string
	Level       string // the level the clause was judged against
	Kind        Kind
	Source      string
	Owner       string
	Age         time.Duration // Now - Since; zero for an absent level
	ExpireAt    time.Time
	Allowed     []string
}

// Result is the decision and its explanation. Admit is true exactly when
// Failing is empty. Reason is a slot the queue site fills (for example the
// start stagger); Decide never sets it.
type Result struct {
	Admit   bool
	Failing []Entry
	Reason  string
}

// Decide is the pure admission function over (snapshot, registry,
// requirement). Both the decision and the explanation come from it.
func Decide(snap Snapshot, reg Registry, req Requirement) Result {
	failing := failingClauses(snap, reg, req)
	return Result{Admit: len(failing) == 0, Failing: failing}
}

// effective returns the level a resource reads at, and the entry kind to use
// if it is outside a clause's set.
func effective(snap Snapshot, reg Registry, resource string) (Level, Kind) {
	if lv, ok := snap.Levels[resource]; ok {
		if lv.State == StateStale {
			return lv, KindStale
		}
		return lv, KindLevel
	}
	return Level{Value: reg[resource].Unknown}, KindUnknown
}

func failingClauses(snap Snapshot, reg Registry, req Requirement) []Entry {
	var out []Entry
	for _, c := range req.Clauses {
		lv, kind := effective(snap, reg, c.Resource)
		if contains(c.Allowed, lv.Value) {
			continue
		}
		e := Entry{
			ClauseIndex: c.Index, ClauseText: c.Text, Resource: c.Resource,
			Level: lv.Value, Kind: kind, Source: lv.Source, Owner: lv.Owner,
			ExpireAt: lv.ExpireAt, Allowed: append([]string(nil), c.Allowed...),
		}
		if kind != KindUnknown && !lv.Since.IsZero() {
			e.Age = snap.Now.Sub(lv.Since)
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ClauseIndex < out[j].ClauseIndex })
	return out
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
