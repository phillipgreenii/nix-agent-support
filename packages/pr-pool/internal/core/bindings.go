package core

// Bindings is the core's view of the CONFIGURED bindings, reduced to the one
// question ingest asks of them: does ANY configured binding declare this event
// `type`? That is the first decision node of invariants.md's dispatch flowchart,
// and answering `no` is what INV-DISP-3 requires the core to REJECT.
//
// It holds every type a configured binding declares, INCLUDING the bindings a
// run-scoped selector disabled for this run — validity is judged against the
// CONFIGURATION, never against the run's active subset (INV-DISP-3,
// INV-WORKFLOW-1). Whether a binding is ACTIVE this run is a different question
// with a different answer channel: a listener registered on the queue. An event
// of a DECLARED but inactive type is accepted and enqueued, waits, is offered to
// nobody, and is dropped unconsumed-expired (INV-EVT-1, INV-EVT-4) — expected,
// and neither an error nor a warning. Collapsing the two questions into one would
// reject exactly the events the invariant requires the core to keep.
type Bindings map[string]bool

// NewBindings collects the event types the configured bindings declare. It is a
// SET, so order and repetition carry no meaning — the caller passes each
// binding's declared type and nothing else about the binding, because nothing
// else about it is ingest's business.
func NewBindings(declaredTypes ...string) Bindings {
	b := make(Bindings, len(declaredTypes))
	for _, t := range declaredTypes {
		b[t] = true
	}
	return b
}

// Declares reports whether any configured binding declares this event type.
//
// A nil or empty Bindings declares NOTHING, so every type is unknown to it. That
// is the strict reading of INV-DISP-3 — no configured binding declares the type —
// and it is the loud one: such a core rejects every event it is handed rather
// than quietly accepting anything. Listen refuses to start a core without a
// binding set for that reason, so the nil case is reachable only from a Service
// assembled directly.
func (b Bindings) Declares(eventType string) bool { return b[eventType] }
