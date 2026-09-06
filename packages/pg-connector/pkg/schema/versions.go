// versions.go: the umbrella-side lookup this build uses to detect
// per-capability schema-version skew against a Tier-2 backend's own
// self-declared capabilities response.
//
// A backend's capabilities op reports
// {"schemaVersions": {"<capability>": N, ...}} — one entry per
// schema-bearing capability it implements, keyed by the same capability
// name used in scriptout.CapabilitiesResponse.SchemaVersions
// [design: §4.3]. Because the umbrella (cmd/pg-connector) and each Tier-2
// backend are separate nix derivations, versioned and deployed
// independently, the version this build currently expects for a given
// capability can differ from what a backend built at another commit
// reports for that same capability — CurrentSchemaVersions is this build's
// side of that comparison, checked in cmd/pg-connector/config_validate.go.
package schema

// CurrentSchemaVersions maps every schema-bearing capability's name to its
// current schema version, as known by whatever binary this source built.
// It exists solely so a caller (cmd/pg-connector's "config validate") can
// compare a backend's self-declared
// scriptout.CapabilitiesResponse.SchemaVersions map against this build's
// own expectations without needing a bespoke per-capability comparison
// wired in four separate places — one generic map, one generic loop.
//
// A capability key a backend declares that isn't present here (e.g. a
// future attention/search-only plugin this build doesn't yet know about)
// is not a mismatch by omission — the caller skips keys it has no opinion
// on rather than treating "unknown to me" as "wrong."
var CurrentSchemaVersions = map[string]int{
	"pr":    SchemaVersion,
	"ci":    CISchemaVersion,
	"scm":   ScmSchemaVersion,
	"issue": IssueSchemaVersion,
}
