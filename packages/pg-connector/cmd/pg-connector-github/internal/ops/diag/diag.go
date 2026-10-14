// Package diag is the two diagnostic and read-your-own-write ops of the daemon,
// served by both instances: refresh and explain.
//
//   - refresh (pr refresh <id>, pr refresh --query Q, ci refresh <pr-id>) queues
//     the named groups of a pull request at the write-invalidated class (class 2)
//     and RETURNS AT ONCE; it never waits for a fetch. It is the way a caller
//     that changed a pull request out of band (git push, gh pr create) reads its
//     own write back without waiting up to one summary_ttl (INV-GH-DIAG-3).
//   - explain (pr explain <id>, ci explain <pr-id>) answers why an entity is
//     stale from the store alone, with no origin call and no spend
//     (INV-GH-DIAG-2). The document is the Phase 2 explain package's, extended
//     with a per-group verdict; ExplainHandlers builds it.
//
// The pending settle row, the awaited groups and the last change rows of explain
// belong to the change engine and are not carried here.
//
// # Wire shape
//
// refresh answers {"queued": [{"id": "owner/repo#n", "groups": [...]}]}: one
// entry per pull request, id its canonical reference, groups the groups queued
// (in the order of the spec's field-group table). All validation (exactly one of
// id or query, the group names, the query name) happens before anything is
// queued, so a failure leaves nothing half done. A failure is a plain handler
// error: invalid_argument, query_not_recognized, not_found for a pull request
// GitHub says does not exist, unauthenticated or unavailable for a resolver
// failure.
//
// # What refresh queues
//
// pr refresh queues summary, detail, conversation, files, commits and runs by
// default (pending only when named); ci refresh queues summary and runs. Each
// named group is first invalidated (groupsvc.Service.InvalidateAndQueueAs: a
// refresh that began before the call loses, and any reader sees the group stale
// at once) and then queued at class 2, so a repeated call merges into the queued
// task. For a pull request the daemon has never seen the handler resolves the
// reference (the one origin call it can make), records an access (a store write
// that creates the entity row and keeps it alive) and queues only the summary:
// the other groups' tasks would be skipped by the executor until a summary
// exists, and the revalidation of the first summary queues them.
//
// A refresh of the summary that races a background summary fetch already running
// is accepted: the summary executor books its own next cycle with
// Scheduler.Reschedule, which can replace the class 2 summary task queued
// meanwhile, postponing the refresh by up to one summary_ttl. The groups' own
// queued tasks are not affected.
package diag

import (
	"context"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The two ops, as the router keys them.
const (
	OpRefresh = "refresh"
	OpExplain = explain.OpName
)

// Ops is the set of ops this package serves, in a fixed order.
var Ops = []string{OpRefresh, OpExplain}

// Engine is the group refresh engine as the handlers use it.
// *groupsvc.Service satisfies it.
type Engine interface {
	Resolve(ctx context.Context, id string) (string, entity.Ref, error)
	InvalidateAndQueueAs(ctx context.Context, id, group, origin string) error
}

var _ Engine = (*groupsvc.Service)(nil)

// Summaries is the summary service's access entry. *summary.Service satisfies
// it.
type Summaries interface {
	Access(ctx context.Context, id string) error
}

var _ Summaries = (*summary.Service)(nil)

// Queries is the query runner as refresh uses it: the stored row of a query and
// the queued re-run. *query.Runner satisfies it.
type Queries interface {
	Row(ctx context.Context, key query.Key) (store.QueryRow, bool, error)
	QueueRun(key query.Key, class governor.Class) error
}

var _ Queries = (*query.Runner)(nil)

// QueryConfig says which queries the daemon's own config defines.
// *config.Config satisfies it.
type QueryConfig interface {
	HasQuery(name string) bool
}

// Store is the part of the daemon store the handlers read. *store.Store
// satisfies it.
type Store interface {
	explain.Store
	GetGroup(ctx context.Context, kind, id, group string) (store.GroupRow, bool, error)
}

var _ Store = (*store.Store)(nil)

// Deps is what the handlers need.
type Deps struct {
	Engine    Engine
	Summaries Summaries
	Queries   Queries
	Config    QueryConfig
	Store     Store
	// Aliases is the alias table; refresh --query uses it to find the node id of
	// a stored member without an origin call.
	Aliases *entity.Identity

	// Queue, Rules, Jobs and Clock are explain's (see explain.Config): the
	// scheduler's position accessor, the freshness rules, the job table and the
	// time source (nil means the system clock). Jobs may be nil.
	Queue explain.Queue
	Rules *groups.Rules
	Jobs  explain.JobReader
	Clock governor.Clock
}

// wireErr maps the engine's and the resolver's errors onto the closed taxonomy.
func wireErr(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, groupsvc.ErrNoSuchPR):
		return scriptout.WrapError(scriptout.ErrNotFound, err.Error())
	case errors.Is(err, groupsvc.ErrBadRef):
		return scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	case errors.Is(err, vcs.ErrAuthInvalid):
		return scriptout.WrapError(scriptout.ErrUnauthenticated, fmt.Sprintf("%s: %v", what, err))
	}
	// A refusal or a rate-limit answer is unavailable naming the cause; anything
	// else passes through for scriptout's own fallback to unavailable.
	return metered.WireError(fmt.Errorf("%s: %w", what, err))
}
