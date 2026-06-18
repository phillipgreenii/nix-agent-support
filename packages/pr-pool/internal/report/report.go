// Package report holds the structured, high-level summary of what one dispatch
// did (closed bead X, created beads Y/Z, ...). It supersedes the ad-hoc slog
// "created" markers. It is a pure-value leaf: it imports nothing in-repo, so any
// package (roles, orchestrator, complete, eventlog) can share these types without
// an import cycle. The vocabulary is closed and code-produced (not operator input).
package report

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
