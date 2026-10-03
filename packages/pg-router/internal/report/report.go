// Package report holds the structured, high-level summary of what one dispatch
// did (closed bead X, created beads Y/Z, ...). It supersedes the ad-hoc slog
// "created" markers. It is a pure-value leaf: it imports nothing in-repo, so any
// package (roles, orchestrator, complete, eventlog) can share these types without
// an import cycle. The vocabulary is closed and code-produced (not operator input).
package report

import "encoding/json"

type Verb string

const (
	Created       Verb = "created"
	Closed        Verb = "closed"
	HandedBack    Verb = "handed-back"
	Unclaimed     Verb = "unclaimed"
	Escalated     Verb = "escalated"     // add-human
	Indeterminate Verb = "indeterminate" // preserves today's created="unknown" (snapshot read failed)
)

type Ref struct {
	Type string // "bead" today; expandable
	ID   string
}

type Action struct {
	Verb Verb
	Refs []Ref
}

type Result struct {
	Actions []Action
}

// Fields renders the Result for eventlog.Emit's flat fields map: a slice of
// {verb, refs:[{type,id}]} objects under the "actions" key.
func (r Result) Fields() map[string]any {
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

// FromOutcome builds the Result recorded for one dispatch from a handler's
// opaque wire `outcome` string (bead pg2-tq9q5). Two shapes are understood:
//
//   - A JSON object with an "actions" array — exactly what
//     packages/pg-router-ccpool-handler JSON-encodes from its own
//     Result.Fields(): {"actions":[{"verb":"closed","refs":[{"type":"bead",
//     "id":"X"}]}]}. Each entry becomes an Action with the handler's verb and
//     refs (an entry carrying no refs falls back to defaultRefs), and an
//     empty array yields a Result with NO actions. The core only RELAYS these
//     for the event log — it still never branches on a verb's value.
//   - Any other string (a plain token such as "delivered"/"closed") is stored
//     verbatim as the single verb, with defaultRefs — the pre-pg2-tq9q5
//     behavior for handlers that do not return the structured shape.
//
// Before this existed the raw outcome was always wrapped as one verb, so a
// ccpool dispatch logged verb `{"actions":[]}` and a real verb was never
// visible in events.jsonl.
func FromOutcome(outcome string, defaultRefs []Ref) Result {
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
		return Result{Actions: []Action{{Verb: Verb(outcome), Refs: defaultRefs}}}
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
	return Result{Actions: acts}
}
