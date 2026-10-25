package query

import (
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// StateFieldPrefix starts the name of the extra field a departure row carries
// (the confirming read's state). The Jira registration records no membership
// rows of its own, so the field only rides the engine's membership call.
const StateFieldPrefix = "query_state:"

// RunnerConfig wires the Jira query runner.
type RunnerConfig struct {
	// Cfg is the daemon's config: the queries and the TTLs. Required.
	Cfg *config.Config
	// Store is the daemon store. Required.
	Store *store.Store
	// Persister stores each listed issue. Required.
	Persister Persister
	// Fetcher runs the searches (*origin.Runner). Required.
	Fetcher Searcher
	// Scheduler runs the query tasks. Required.
	Scheduler *scheduler.Scheduler
	// Chg is the Jira change engine; it marks the watched members and appends
	// the rows. Required: the daemon's watched-entity model needs it.
	Chg *changes.Engine
	// Summary makes the confirming read of a departure. Required.
	Summary Summaries
	// Auth is the typed unauthenticated state; nil means always authenticated.
	Auth lq.Auth
	// Log reports an event the run survives; nil discards.
	Log func(event string, kv ...any)
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// NewRunner builds the shared query runner over the Jira lister, settings,
// tracker, confirmer and aliases, and registers its executor with the
// scheduler. A run answers unauthenticated (scriptout.ErrUnauthenticated) while
// stored content is still served, and AdHocWatch is false: a query run with the
// watch option is refused (invalid_argument).
func NewRunner(cfg RunnerConfig) (*lq.Runner, error) {
	switch {
	case cfg.Cfg == nil, cfg.Store == nil, cfg.Persister == nil, cfg.Fetcher == nil,
		cfg.Scheduler == nil, cfg.Chg == nil, cfg.Summary == nil:
		return nil, errors.New("query: runner needs config, store, persister, fetcher, scheduler, change engine and summary service")
	}
	spec := changes.Spec()
	rc := lq.Config{
		Spec:             spec,
		StateFieldPrefix: StateFieldPrefix,
		UnauthError:      unauthenticatedError,
		Bucket:           origin.Bucket,
		Settings:         NewSettings(cfg.Cfg),
		Store:            cfg.Store,
		Aliases:          summary.NewAliases(cfg.Store),
		Tracker:          NewTracker(),
		Lister: NewLister(ListerConfig{
			Fetcher: cfg.Fetcher, Persister: cfg.Persister, Store: cfg.Store, Clock: cfg.Clock,
		}),
		Scheduler:  cfg.Scheduler,
		Clock:      cfg.Clock,
		AdHocWatch: false,
		Changes:    cfg.Chg,
		Confirmer:  NewConfirmer(cfg.Summary),
		Log:        cfg.Log,
	}
	if cfg.Auth != nil {
		rc.Auth = cfg.Auth
	}
	return lq.New(rc)
}

// unauthenticatedError is the error a run answers while the daemon is
// unauthenticated; it wraps scriptout.ErrUnauthenticated.
func unauthenticatedError(cause error) error {
	if cause == nil {
		return scriptout.WrapError(scriptout.ErrUnauthenticated, "the daemon is not authenticated")
	}
	return scriptout.WrapError(scriptout.ErrUnauthenticated, cause.Error())
}
