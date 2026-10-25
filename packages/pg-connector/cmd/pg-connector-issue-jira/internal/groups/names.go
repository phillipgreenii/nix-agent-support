package groups

// The closed set of group names stored for an issue, the group of a row in the
// store's entity_group table.
const (
	Summary = "summary"
	Detail  = "detail"
)

// EntityKind is the kind of the entity every group here describes.
const EntityKind = "issue"

// All returns every stored group name in canonical order (summary, then
// detail). The caller owns the slice. The children group is never stored and is
// not listed.
func All() []string { return []string{Summary, Detail} }
