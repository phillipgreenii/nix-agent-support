package feed

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Config wires the Jira feed.
type Config struct {
	// Spec is the registration; the zero value means changes.Spec().
	Spec  registry.Spec
	Store *store.Store
	// Queries is the adapter from NewQueries.
	Queries libfeed.Queries
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Retention is change_log_retention and ConsumerStaleAfter is
	// consumer_stale_after; both are required and positive.
	Retention, ConsumerStaleAfter time.Duration
	// Observer is told about redeliveries; nil is allowed.
	Observer libfeed.Observer
}

// New returns the shared feed bound to the Jira registration. A consumer holds
// one cursor, so no conflict reason is needed: the registration has one view.
func New(cfg Config) (*libfeed.Feed, error) {
	spec := cfg.Spec
	if spec.EntityKind == "" {
		spec = changes.Spec()
	}
	return libfeed.New(libfeed.Config{
		Spec: spec, Store: cfg.Store, Queries: cfg.Queries, Clock: cfg.Clock,
		Retention: cfg.Retention, ConsumerStaleAfter: cfg.ConsumerStaleAfter, Observer: cfg.Observer,
	})
}
