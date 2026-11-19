package wire

import "sort"

// OpDescriptor is one operation of the daemon's protocol. Exactly one of
// CancelBody and CancelExempt is set: CancelBody is a valid request body for an
// operation whose reads run under the request context (the cancel audit posts
// it); CancelExempt is the reason an operation does not.
type OpDescriptor struct {
	Name         string
	CancelBody   string
	CancelExempt string
}

// Registry is the one list of operations. The capability list hello reports
// (OpNames) and the cancel table of the cancel audit (CancelBodies,
// CancelExempt) are derived from it, so adding an operation is an edit here
// only. Order is not significant: OpNames sorts.
var Registry = []OpDescriptor{
	{Name: "hello", CancelExempt: "answers from the daemon's own state, no store read"},
	{Name: "status", CancelExempt: "answers from the snapshot the status loop keeps, issues no query"},
	{Name: "coverage", CancelBody: `{}`},
	{Name: "query", CancelBody: `{}`},
	{Name: "files.inventory", CancelBody: `{}`},
	{Name: "search", CancelBody: `{"q":"line"}`},
	{Name: "feed", CancelBody: `{}`},
	{Name: "snapshot", CancelBody: `{}`},
	{Name: "raw", CancelBody: `{"file":"f_1","length":1}`},
	{Name: "conversation.show", CancelBody: `{"session_id":"example-session"}`},
	{Name: "conversation.list", CancelBody: `{}`},
	{Name: "process.show", CancelBody: `{"pid":1}`},
	{Name: "process.list", CancelBody: `{}`},
	{Name: "sessions.facts", CancelBody: `{"session_ids":["example-session"],"fields":["awaiting_input"]}`},
	{Name: "held", CancelBody: `{}`},
	{Name: "where", CancelBody: `{"session":"example-session"}`},
	{Name: "remove", CancelExempt: "mutates the archive and the index; cancelling half way would leave partial state"},
	{Name: "extract", CancelExempt: "copies archive files and commits stage rows; cancelling half way would leave partial state"},
	{Name: "sweep", CancelExempt: "starts a background sweep and answers from daemon state, no store read"},
	{Name: "doctor", CancelExempt: "verifies files on disk and writes its report; a long file walk, not a pool read"},
}

// OpNames is the capability list: every operation name, sorted.
func OpNames() []string {
	out := make([]string, 0, len(Registry))
	for _, o := range Registry {
		out = append(out, o.Name)
	}
	sort.Strings(out)
	return out
}

// CancelBodies is the cancel table's cancellable half: operation name to a
// valid body.
func CancelBodies() map[string]string {
	out := map[string]string{}
	for _, o := range Registry {
		if o.CancelBody != "" {
			out[o.Name] = o.CancelBody
		}
	}
	return out
}

// CancelExempt is the cancel table's exempt half: operation name to the reason.
func CancelExempt() map[string]string {
	out := map[string]string{}
	for _, o := range Registry {
		if o.CancelExempt != "" {
			out[o.Name] = o.CancelExempt
		}
	}
	return out
}
