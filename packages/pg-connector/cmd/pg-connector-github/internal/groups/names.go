// Package groups holds the record rules every non-summary entity field group
// stands on: the closed set of group names and the table that assigns every
// field of schema.PR and schema.CIRun to one group, the freshness rules (TTLs,
// maximum ages, terminal_ttl, head and base stamps), the Fetcher interface the
// group packets implement, and the generation-checked write path (Begin,
// Commit, Invalidate) that keeps a refresh begun before a write from
// overwriting content stored after it.
package groups

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
)

// The closed set of group names, the `group` of a row in the store's
// entity_group table.
const (
	Summary      = summary.GroupName
	Detail       = "detail"
	Conversation = "conversation"
	Files        = "files"
	Commits      = "commits"
	Pending      = "pending"
	Runs         = "runs"
)

// All returns every group name in the order the spec's "Field groups" table
// lists them. The caller owns the slice.
func All() []string {
	return []string{Summary, Detail, Conversation, Files, Commits, Pending, Runs}
}
