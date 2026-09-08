// search.go: the search entity/capability's shared JSON wire shape, built by
// the "generic search entity/capability" packet on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go).
//
// Search is one of the two generic, cross-cutting Tier-1 capabilities (the
// other is attention) — not tied to any one entity type. Unlike attention,
// search has no severity/state concept; its own shape carries a small core
// set plus an extensible, backend/type-declared Attributes map (see
// SearchResult's own doc comment).
package schema

// SearchSchemaVersion is the search capability's own schema version,
// populated into the wire envelope's schemaVersion field by each of the
// search capability's dispatch-table entries
// (pkg/provider/search.NewDispatchTable) — independent of both
// pkg/scriptout.ProtocolVersion and every other capability's own
// <Entity>SchemaVersion, matching the PRSchemaVersion/CISchemaVersion/
// ScmSchemaVersion/IssueSchemaVersion/AttentionSchemaVersion convention
// already established (INV-VER-1). It is also registered into
// CurrentSchemaVersions in versions.go, so a future search-capability
// schema-version mismatch is detectable rather than silently skipped by
// cmd/pg-connector/config_validate.go's checkSchemaVersions (an
// unknown-to-this-build capability key is otherwise treated as "no
// opinion," never a mismatch).
const SearchSchemaVersion = 1

// SearchResult is the search capability's shared JSON wire shape: a single
// source's own search response item is exactly this shape — the core set
// {type, id, title, url, source} plus an Attributes map carrying whatever
// type-declared or backend-declared extension attributes a query requested
// and the returning backend chose to populate.
//
// Each entity type MAY declare its own additional attributes as a fixed
// part of that type's own schema in this package; each backend
// implementation MAY further declare its own additional attributes beyond
// its type's set, dynamically, via its own capabilities response's
// Vocabulary map (the same mechanism a backend's own per-backend vocabulary
// already uses to declare capabilities dynamically —
// pkg/scriptout.CapabilitiesResponse.Vocabulary; pkg/provider/search adds
// no new wire concept of its own for this). Either kind of extension
// attribute, when populated for a given result, lives in Attributes — never
// as a new top-level field added later without a schema-version bump.
//
// SearchResult carries NO score field, under any name (rank, relevance,
// …) — most backends expose an ordering, not a comparable magnitude, and
// forcing one would produce numbers that look comparable across backends
// while meaning nothing of the kind.
type SearchResult struct {
	// Type identifies the kind of thing this result is about (e.g. a PR, a
	// CI run, an issue) — a source-defined, generic string, not a closed
	// enum owned by this package.
	Type string `json:"type"`
	// ID is this result's identity, source-defined.
	ID string `json:"id"`
	// Title is a short, human-readable label for this result.
	Title string `json:"title"`
	// URL links to this result's own canonical location.
	URL string `json:"url"`
	// Source identifies which registered search.sources entry returned this
	// result.
	Source string `json:"source"`

	// Attributes carries whatever type-declared or backend-declared
	// extension attributes a query's requested fields list asked for and
	// this backend chose to populate. Omitted entirely (omitempty) when
	// empty — a result with no extension attributes populated carries only
	// the core set above. Attribute values are deliberately left generic
	// (any) — a freedom boundary this package does not narrow further.
	Attributes map[string]any `json:"attributes,omitempty"`
}
