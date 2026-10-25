package client

import (
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// RouterInstance is the router instance the daemon registers every op of the
// Jira backend under (serve.Instance; a test holds the two equal). It is NOT
// the config's instance, which is the registry name the umbrella knows the
// backend by.
const RouterInstance = "issue"

// The op names the client routes itself.
const (
	OpCreate     = "create"
	OpComment    = "comment"
	OpTransition = "transition"
	OpUpdate     = "update"
	OpClose      = "close"
)

// WriteOps are the ops the client performs directly against the origin tool.
var WriteOps = []string{OpCreate, OpComment, OpTransition, OpUpdate, OpClose}

// ForwardedOps are the ops the daemon registers and the client LISTS in its
// capabilities. Listing is not a gate: the client forwards every op that is
// not a write, listed or not. The write notices (write_begin, invalidate) are
// the client's own conversation with the daemon and are deliberately not
// listed. A test in this package holds the union of this list, WriteOps and the
// notice ops equal to the daemon's registered ops.
var ForwardedOps = []string{
	scriptout.OpAuthStatus, "changes", "changes_ack", "children", "deps", "explain",
	scriptout.OpList, scriptout.OpListActivity, "search", scriptout.OpShow, "status",
}

// IsWrite reports whether op is one of the five writes.
func IsWrite(op string) bool {
	for _, w := range WriteOps {
		if w == op {
			return true
		}
	}
	return false
}

// OpNames is the union of the forwarded and the local op names (the five
// writes and capabilities), sorted: what capabilities reports as ops.
func OpNames() []string {
	names := make([]string, 0, len(ForwardedOps)+len(WriteOps)+1)
	names = append(names, ForwardedOps...)
	names = append(names, WriteOps...)
	names = append(names, scriptout.OpCapabilities)
	sort.Strings(names)
	return names
}

// SchemaVersionFor is the schema version that stamps an answer the client
// composes itself for op (an unavailable for a forwarded read): the search and
// activity ops carry their own capability's version, every other op the issue
// version.
func SchemaVersionFor(op string) int {
	switch op {
	case "search":
		return schema.SearchSchemaVersion
	case scriptout.OpListActivity:
		return schema.ActivitySchemaVersion
	}
	return schema.IssueSchemaVersion
}
