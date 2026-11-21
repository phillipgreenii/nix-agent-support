package participant

import (
	"encoding/json"
	"time"
)

// Verb is one action a dispatch took. The vocabulary is closed and
// code-produced (not operator input); the core stores it without branching on a
// value.
type Verb string

const (
	Created       Verb = "created"
	Closed        Verb = "closed"
	HandedBack    Verb = "handed-back"
	Unclaimed     Verb = "unclaimed"
	Escalated     Verb = "escalated"     // add-human
	Indeterminate Verb = "indeterminate" // a snapshot read failed
	// Skipped is a moot precheck outcome accepted and completed without work.
	Skipped Verb = "skipped"
)

// Ref names one thing an action touched.
type Ref struct {
	Type string // "bead" today; expandable
	ID   string
}

// Action is one verb with the refs it applied to.
type Action struct {
	Verb Verb
	Refs []Ref
}

// Report is the structured, high-level summary of what one dispatch did (closed
// bead X, created beads Y and Z, ...), the participant's reply to the core. Its
// Actions travel in the dispatch reply's opaque outcome string, encoded by
// Fields and decoded by FromOutcome.
type Report struct {
	Actions []Action
	// StartedAt is when the work this report covers began, set ONLY by a dispatch
	// that ADOPTED a session an earlier offer launched: the adopted session's
	// launch time. Zero otherwise. It is written as the reply's optional
	// startedAt, not into the outcome string.
	StartedAt time.Time
	// Session and Supervisors are set ONLY by a dispatch that completed a lease
	// takeover of an existing session: the session's external id and the
	// supervisor count the takeover returned (at least 1). They are written as
	// the reply's optional session and supervisors, not into the outcome string.
	Session     string
	Supervisors int
}

// Fields renders the report's actions for an event-log record's flat fields
// map: a slice of {verb, refs:[{type,id}]} objects under the "actions" key.
func (r Report) Fields() map[string]any {
	acts := make([]map[string]any, 0, len(r.Actions))
	for _, a := range r.Actions {
		refs := make([]map[string]any, 0, len(a.Refs))
		for _, ref := range a.Refs {
			refs = append(refs, map[string]any{"type": ref.Type, "id": ref.ID})
		}
		acts = append(acts, map[string]any{"verb": string(a.Verb), "refs": refs})
	}
	return map[string]any{"actions": acts}
}

// EncodeOutcome renders the report's actions as the opaque outcome string of a
// dispatch reply.
func EncodeOutcome(r Report) string {
	b, _ := json.Marshal(r.Fields())
	return string(b)
}

// FromOutcome builds the Report recorded for one dispatch from a handler's
// opaque outcome string. Two shapes are understood:
//
//   - A JSON object with an "actions" array, exactly what EncodeOutcome writes.
//     Each entry becomes an Action with the handler's verb and refs (an entry
//     carrying no refs falls back to defaultRefs), and an empty array yields a
//     Report with NO actions.
//   - Any other string (a plain token such as "delivered" or "closed") is stored
//     verbatim as the single verb, with defaultRefs.
func FromOutcome(outcome string, defaultRefs []Ref) Report {
	var wire struct {
		Actions *[]struct {
			Verb string `json:"verb"`
			Refs []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"refs"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(outcome), &wire); err != nil || wire.Actions == nil {
		return Report{Actions: []Action{{Verb: Verb(outcome), Refs: defaultRefs}}}
	}
	acts := make([]Action, 0, len(*wire.Actions))
	for _, a := range *wire.Actions {
		refs := defaultRefs
		if len(a.Refs) > 0 {
			refs = make([]Ref, 0, len(a.Refs))
			for _, r := range a.Refs {
				refs = append(refs, Ref{Type: r.Type, ID: r.ID})
			}
		}
		acts = append(acts, Action{Verb: Verb(a.Verb), Refs: refs})
	}
	return Report{Actions: acts}
}
