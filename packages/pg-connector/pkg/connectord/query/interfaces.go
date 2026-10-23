package query

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Member is one listed entity: a stable id (the store entity id) and its display
// reference (the wire form, which can change on a rename).
type Member struct{ ID, Ref string }

// Listing is what running a query returned: the members in the origin's order and
// whether the list is the whole result.
type Listing struct {
	Members  []Member
	Complete bool
}

// CallMeta is what the origin call is admitted as.
type CallMeta struct {
	Class   governor.Class
	Purpose governor.Purpose
}

// Lister runs every expression of a configured query against the origin and unions
// the members. The shared runner never builds an origin request itself.
type Lister interface {
	List(ctx context.Context, key Key, exprs []string, call CallMeta) (Listing, error)
}

// Aliases resolves the display references of a previous listing to stable ids, so a
// rename is neither an arrival nor a departure (the runner's planMembership).
type Aliases interface {
	Put(ctx context.Context, id, ref string) error
	Resolve(ctx context.Context, refs []string) (map[string]Alias, error) // keyed by the ref given
}

// Alias is what a display reference resolves to: the stable id and the reference
// as recorded now. An Alias whose ID is empty names a reference the
// implementation has already reported as unusable: the runner skips it without a
// report of its own. A reference that is left out of the map is reported by the
// runner as having no alias.
type Alias struct{ ID, CanonicalRef string }

// Settings is the connector's own query configuration.
type Settings interface {
	// Expr returns the expressions of a configured query, and false for a name
	// the configuration does not define.
	Expr(name string) (exprs []string, ok bool)
	Watched(name string) bool
	Names() []string           // configured query names, sorted
	TTL(key Key) time.Duration // query_ttl for a configured key, the ad-hoc ttl otherwise
}

// Tracker gives each listed entity without a row one and queues its first refresh.
type Tracker interface {
	Track(ctx context.Context, refs []string) (unresolved []string, err error)
}

// Confirmer makes the one read that tells an entity that closed apart from one that
// was merely filtered out of a query; State is opaque to the shared code. Any
// failure, a governor refusal included, is an error: the departure is then retried
// by the next run.
type Confirmer interface {
	Confirm(ctx context.Context, id, ref string) (Confirmation, error)
}

// Confirmation is what one confirming read found: the entity's stored state, or
// Gone when the read found the entity gone (it is tombstoned).
type Confirmation struct {
	State string
	Gone  bool
}

// Changes is the part of the change engine the runner uses to record a membership
// change; *engine.Engine satisfies it. The call runs inside the transaction that
// stores the run's result, so the row and the result are visible together or not
// at all.
type Changes interface {
	AppendMembership(ctx context.Context, tx *store.Tx, m engine.Membership) error
}

var _ Changes = (*engine.Engine)(nil)

// Store is the part of the daemon store the runner uses; *store.Store satisfies
// it.
type Store interface {
	GetQuery(ctx context.Context, key string) (store.QueryRow, bool, error)
	PutQuery(ctx context.Context, q store.QueryRow) error
	ListQueries(ctx context.Context, configuredOnly bool) ([]store.QueryRow, error)
	DeleteQuery(ctx context.Context, key string) error
	// InTx runs fn in one store transaction: the run's result and its
	// membership rows commit together.
	InTx(ctx context.Context, fn func(tx *store.Tx) error) error
}

var _ Store = (*store.Store)(nil)

// Auth is the typed unauthenticated state: the runner asks it before every
// run. Booking the outcome of an origin call is the Lister's business.
type Auth interface {
	Unauthenticated() (bool, error)
}
