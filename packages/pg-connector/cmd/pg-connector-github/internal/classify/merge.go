package classify

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

// roles maps the membership and removed kinds of this connector onto the
// roles the shared engine builds results for.
var roles = registry.Roles{
	Removed:      KindRemoved,
	EnteredQuery: KindEnteredQuery,
	LeftQuery:    KindLeftQuery,
}

// Merge combines two results of the same entity, earlier then later, the way the
// settle barrier does: kinds are unioned (in canonical order) and, per field
// name, the earliest before and the latest after are kept. A field whose after
// equals its before again is dropped: a value changed and changed back is no
// change. Two values are equal when their compacted JSON is.
//
// Merge is associative over a chain of results (each later before is the
// earlier after, which every classifier output of one entity satisfies).
func Merge(earlier, later Result) Result { return engine.Merge(Kinds(), earlier, later) }

// Membership is the Result of an entered_query or left_query row for the named
// query: the kind and one field query:<name>, whose After is the query name (a
// JSON string) for entered_query and whose Before is for left_query. Any other
// kind yields the zero Result.
func Membership(kind, query string) Result { return engine.MembershipResult(roles, kind, query) }

// Removed is the Result of a tombstone: the removed kind and one field
// query:<name> per query that listed the entity, with Before the query name.
// Duplicate names collapse to one field.
func Removed(queries []string) Result { return engine.RemovedResult(KindRemoved, queries) }
