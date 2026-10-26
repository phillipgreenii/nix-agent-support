package schema

// EntityMeta is the connector's own metadata about one entity of a show or list
// answer: facts the daemon keeps beside the entity, NOT part of its normalised
// content. It travels under the entity's "entity_meta" key. A change to it
// MUST NOT bump the entity's version, append a change row or produce an event,
// and a consumer that compares an entity's content for change MUST drop the
// whole key (the way it drops as_of, stale, served_from and age_seconds).
//
// Every field is a pointer so that "false" and "not stated" differ: an answer
// from a daemon that predates the field, or a read that could not consult the
// store, omits it, and a reader MUST then treat the fact as unknown rather than
// as false. Added by bead pg2-0o693; the shape is additive.
type EntityMeta struct {
	// Watched is the entity's stored watched flag: true when the entity is a
	// member of a watched query or a read of that one entity carried the watch
	// option, sticky after the entity leaves every query and cleared only when
	// the daemon evicts the entity. false is a statement (the daemon holds the
	// entity or does not, and it is not watched); nil is no statement.
	Watched *bool `json:"watched,omitempty"`
}

// WatchedMeta returns an EntityMeta stating the watched flag.
func WatchedMeta(watched bool) *EntityMeta { return &EntityMeta{Watched: &watched} }

// WatchedState reads the watched flag of a possibly nil EntityMeta: stated is
// false when the answer says nothing about it, in which case watched is false
// too but MUST NOT be read as "not watched".
func (m *EntityMeta) WatchedState() (watched, stated bool) {
	if m == nil || m.Watched == nil {
		return false, false
	}
	return *m.Watched, true
}
