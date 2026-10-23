// Package query is the GitHub daemon's query adapter over the shared query
// runner (pkg/connectord/query): it supplies the GitHub implementations of the
// runner's small interfaces and keeps the daemon's own wiring surface.
//
// # What a query is
//
// A configured query is a NAME the daemon's own config defines (pr.queries),
// whose value is one or more GitHub search expressions in GitHub's native
// syntax. The request's config block is never consulted: a name the daemon does
// not know is unknown, whatever the request carries. A Key is the name plus the
// arguments that change what GitHub is asked (see Key); the key text is the key
// of the stored row.
//
// # Membership-only form
//
// A run reads only WHO is in the result: one GraphQL search per expression in
// the membership-only form (a node id, the number and the repository per
// member, 1 point per page of PageSize), never the members' fields (see fetch.go,
// the GitHub Lister). The row keeps the ordered ids in the wire form
// owner/repo#n, a complete flag (false for a listing past GitHub's 1,000-result
// search limit), fetched_at, fresh_until, last_success_at and last_error. Each
// run also records the members' aliases (node id to reference, aliases.go), so
// the summary group can fetch them by node id.
//
// # What is shared and what stays here
//
// The scheduling, the membership baseline and the arrival and departure rows,
// the failure handling and the stored row are the shared runner's (see its
// package comment). This package holds the GitHub search document and paging
// (the Lister), the alias adapter over entity.Identity (IdentityAliases), the
// config-backed Settings, the confirming read (NewConfirmer) and the typed
// unauthenticated error.
package query

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

const (
	// Kind is the scheduler executor kind of a query task, and Group its group.
	Kind  = shared.Kind
	Group = shared.Group
)

// ErrPending is returned by Fetch when ctx ended while the run was still going:
// the run is not cancelled, it completes in the background and is stored.
var ErrPending = shared.ErrPending

type (
	// Store is the part of the daemon store the runner uses; *store.Store
	// satisfies it.
	Store = shared.Store
	// Tracker is the summary service's Track: it resolves the wire references to
	// node ids, gives each entity without a row one and queues a class 3 summary
	// task for it. *summary.Service satisfies it.
	Tracker = shared.Tracker
)

// Auth is the typed unauthenticated state. *identity.Tracker satisfies it.
type Auth interface {
	// Observe books the outcome of an origin call: nil clears the state, an
	// authentication failure sets it, any other error leaves it.
	Observe(err error) bool
	Unauthenticated() (bool, error)
}

// Config wires a Runner.
type Config struct {
	// Settings is the daemon's own config: the configured queries (pr.queries)
	// and the TTLs (query_ttl, adhoc_query_ttl).
	Settings *config.Config
	Store    Store
	// Identity records the members' aliases.
	Identity *entity.Identity
	Tracker  Tracker
	// GH runs `gh <args...>`; *github.CLI satisfies it. Any retry of a transient
	// failure belongs to it.
	GH ratefeed.Runner
	// Governor admits each page; Feed books its spend and reading.
	Governor  *governor.Governor
	Feed      *ratefeed.Feed
	Scheduler *scheduler.Scheduler
	// Auth is optional: nil means always authenticated.
	Auth Auth
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Changes records membership changes (entered_query, left_query); nil means
	// none are recorded (the baseline still is) and Confirm is never called.
	Changes Changes
	// Confirm makes the entity read that confirms a departure; it is used only
	// when Changes is set. With Changes set and Confirm nil a departure cannot be
	// confirmed, so the run does not store its result (the departure is retried
	// by the next run, never dropped).
	Confirm Confirmer
	// Log reports an event the run survives (a previous id that cannot be a
	// departure candidate); nil discards.
	Log func(event string, kv ...any)
}

// Runner runs queries: the shared runner, whose public methods (TTL, Row,
// Schedule, Expire, Fetch, QueueRun) it exposes, with Expr in the shape the
// daemon's callers read.
type Runner struct{ *shared.Runner }

// New returns a Runner over the shared runner built from cfg, and registers its
// executor with the scheduler under Kind.
func New(cfg Config) (*Runner, error) {
	switch {
	case cfg.Settings == nil, cfg.Store == nil, cfg.Identity == nil, cfg.Tracker == nil,
		cfg.GH == nil, cfg.Governor == nil, cfg.Feed == nil, cfg.Scheduler == nil:
		return nil, errors.New("query: runner needs settings, a store, identity, tracker, gh, governor, feed and scheduler")
	}
	sc := shared.Config{
		Spec:             changes.Spec(),
		StateFieldPrefix: StateFieldPrefix,
		UnauthError:      UnauthenticatedError,
		Bucket:           governor.BucketGraphQL,
		Settings:         configSettings{cfg.Settings},
		Store:            cfg.Store,
		Aliases:          NewAliases(cfg.Identity, cfg.Log),
		Tracker:          cfg.Tracker,
		Lister:           &lister{gh: cfg.GH, governor: cfg.Governor, feed: cfg.Feed, auth: cfg.Auth},
		Scheduler:        cfg.Scheduler,
		Clock:            cfg.Clock,
		Confirmer:        cfg.Confirm,
		Log:              cfg.Log,
	}
	if cfg.Auth != nil {
		sc.Auth = cfg.Auth
	}
	if cfg.Changes != nil {
		sc.Changes = changesAdapter{cfg.Changes}
	}
	r, err := shared.New(sc)
	if err != nil {
		return nil, err
	}
	return &Runner{Runner: r}, nil
}

// Expr returns the configured expressions of a query name, and false when the
// daemon's own config does not define it.
func (r *Runner) Expr(name string) (schema.QueryExpr, bool) {
	e, ok := r.Runner.Expr(name)
	if !ok {
		return nil, false
	}
	return schema.QueryExpr(e), true
}

// UnauthenticatedError is the error a fetch answers while the daemon is
// unauthenticated; it wraps vcs.ErrAuthInvalid.
func UnauthenticatedError(cause error) error {
	if cause == nil {
		return vcs.ErrAuthInvalid
	}
	return fmt.Errorf("%w: %v", vcs.ErrAuthInvalid, cause)
}
