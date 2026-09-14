// Package report holds the structured, high-level summary of what one dispatch
// did (closed bead X, created beads Y/Z, ...). It supersedes the ad-hoc slog
// "created" markers. It is a pure-value leaf: it imports nothing in-repo, so any
// package (roles, orchestrator, complete, eventlog) can share these types without
// an import cycle. The vocabulary is closed and code-produced (not operator input).
//
// Duplicated verbatim from packages/pg-router/internal/report (still needed
// there by its own orchestrator, which stays in-core): a zero-dependency
// leaf, so a byte-for-byte copy costs nothing (docket pg2-oju6w Task 5.2/5.3,
// folded per the operator's 2026-09-11 decision; docs/adr/0065's Addendum).
// Result.Fields() is what this module's dispatch handler JSON-encodes into
// the wire dispatch-reply's opaque `outcome` STRING (handler.dispatch-reply
// schema — docket pg2-oju6w's Task 5.4 retypes that property from object to
// string, matching the core's own wireclient.Reply.Outcome Go field) — ADR
// 0065's "work-outcome... is an opaque string the core stores" framing,
// realized here as a JSON shape (stringified) the core never interprets.
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
