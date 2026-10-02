package classify

import "encoding/json"

// Record is one classified change. Siblings read only Kind.
type Record struct {
	Kind Kind
}

// Snapshot is one observation of an entity, the classifier's input. The write
// entry point builds it from the stored entity (old) and the fresh gather
// result (new); this package only defines the type.
type Snapshot struct {
	Type string
	ID   string
	// Exists is false when there is no previous snapshot (first run, a new
	// watched query, or --reset).
	Exists bool
	// Active is false when the stored row is inactive (removed).
	Active bool
	// Degraded is set when the hydration that produced this snapshot was
	// degraded; the issue and thread payloads do not carry it themselves.
	Degraded bool
	// Payload is the opaque entity.facts JSON (gather.Facts for pr,
	// gather.IssueFacts, gather.ThreadFacts).
	Payload json.RawMessage
	// AsOf, ContentHash and HeadSHA are informational: they are not compared
	// for identity because a pr payload embeds as_of.
	AsOf        string
	ContentHash string
	HeadSHA     string
	// Decorations is the entity's serialized interpretation row; may be empty.
	Decorations json.RawMessage
}

// Classifier is the per-type Strategy. It is only ever called with two
// observed snapshots (both Exists, both Active, old and new not identical);
// the common invariants are handled before it is called.
type Classifier interface {
	Classify(old, new Snapshot) []Record
}
