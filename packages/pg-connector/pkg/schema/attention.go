// attention.go: the attention entity/capability's shared JSON wire shape,
// built by the "generic attention entity/capability" packet on top of the
// Tier-1 core's pkg/schema placeholder (see doc.go).
//
// Attention is one of the two generic, cross-cutting Tier-1 capabilities
// (the other is search) — not tied to any one entity type, and
// deliberately stateless: it MUST NOT carry any acknowledge/hide/unhide
// field or method anywhere in this file or in pkg/provider/attention.
// Resolving the signal means doing the underlying thing; how a source
// clears itself is source-specific and happens out-of-band.
package schema

// AttentionSchemaVersion is the attention capability's own schema version,
// populated into the wire envelope's schemaVersion field by each of the
// attention capability's dispatch-table entries
// (pkg/provider/attention.NewDispatchTable) — independent of both
// pkg/scriptout.ProtocolVersion and every other capability's own
// <Entity>SchemaVersion, matching the PRSchemaVersion/CISchemaVersion/
// ScmSchemaVersion/IssueSchemaVersion convention already established
// (INV-VER-1). It is also registered into CurrentSchemaVersions in
// versions.go, so a future attention-capability schema-version mismatch is
// detectable rather than silently skipped by
// cmd/pg-connector/config_validate.go's checkSchemaVersions (an
// unknown-to-this-build capability key is otherwise treated as "no
// opinion," never a mismatch).
const AttentionSchemaVersion = 1

// AttentionItem is the attention capability's shared JSON wire shape: a
// single source's own list_attention response item is exactly this shape —
// {type, id, summary} plus optional severity — carried by
// pkg/provider/attention.Provider.ListAttention. Tier 1's aggregated
// pg-connector attention list output (built by the sibling aggregation
// packet) is a strict superset that adds via/truncated/total_before_cap at
// the merge/envelope layer; it is never a violation of this per-source
// shape, and this type MUST NOT itself gain those fields.
type AttentionItem struct {
	// Type identifies the kind of thing this item is about (e.g. a PR, a
	// CI run, an issue) — a source-defined, generic string, not a closed
	// enum owned by this package.
	Type string `json:"type"`
	// ID is this item's identity, source-defined.
	ID string `json:"id"`
	// Summary is a short, human-readable description of why this item
	// currently qualifies for attention.
	Summary string `json:"summary"`

	// Severity is this item's severity, drawn from ValidSeverities. A
	// source with no opinion on an item's severity omits the field
	// entirely (omitempty) — it MUST NOT be defaulted to any value by any
	// caller of this package.
	Severity Severity `json:"severity,omitempty"`
}

// Severity is the attention capability's closed string enum, with a
// canonical rank ordering (low < medium < high < critical) defined exactly
// once here so a consuming aggregation algorithm (the sibling "attention
// aggregation + CLI verb" packet's merge/sort) never re-derives it. It
// follows the same closed-enum + "ValidX"/"IsValid" convention Disposition
// already establishes in pr.go.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// ValidSeverities is the closed set above, in ascending rank order — used
// for validation, CLI help/error text, and as Rank's own lookup table.
var ValidSeverities = []Severity{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}

// IsValid reports whether s is one of ValidSeverities.
func (s Severity) IsValid() bool {
	for _, v := range ValidSeverities {
		if s == v {
			return true
		}
	}
	return false
}

// severityRank maps each valid Severity to its canonical rank — the sole
// place this ordering is defined (INV-VER-1-style "defined once, never
// re-derived" convention, applied here to severity rather than schema
// version).
var severityRank = map[Severity]int{
	SeverityLow:      0,
	SeverityMedium:   1,
	SeverityHigh:     2,
	SeverityCritical: 3,
}

// Rank returns s's canonical rank (low < medium < high < critical), or -1
// for an invalid/empty Severity — a caller comparing ranks MUST check
// IsValid first if it needs to distinguish "no opinion" from "lowest
// severity."
func (s Severity) Rank() int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return -1
}
