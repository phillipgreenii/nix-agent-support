package feedop

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// EntityMetaOf is the metadata a read answer carries beside one entity, built
// from the entity's stored row: the one place the stored watched flag becomes
// the wire's entity_meta.watched, so every daemon states it the same way.
//
// found is the second result of Store.GetEntity. An entity with no stored row is
// stated as not watched (the daemon holds nothing for it, so nothing watches
// it); a caller that could not consult the store at all MUST NOT call this and
// leaves the entity's EntityMeta nil, which a reader takes as "not stated".
//
// It only READS the row. Exposing the flag never sets or clears it, never
// touches the entity's version and never appends a change row (INV-ICD-5 to -7,
// INV-ICD-15): the flag is set by a watched query or a watching read, and
// cleared only when the entity row is deleted at eviction (INV-ICD-13).
func EntityMetaOf(ent store.EntityRow, found bool) *schema.EntityMeta {
	return schema.WatchedMeta(found && !ent.WatchedAt.IsZero())
}
