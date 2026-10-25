package summary

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Fetcher is the origin call the service makes: one JQL search with the issues'
// changelog and comments expanded. *origin.Runner satisfies it; the call's
// class, purpose and batch size ride on ctx (origin.WithCall).
type Fetcher interface {
	Search(ctx context.Context, jql string, expand []string) (*internal.PjiraSearchResult, error)
}

// Observer is told what the service does, for the daemon's metrics. A nil
// Observer in the config means none. Implementations MUST NOT block.
type Observer interface {
	// BatchSent is called for every executor batch, once with the ids that were
	// due (role due) and once with those that filled spare slots (role fill),
	// each only when non-empty, before the search is made.
	BatchSent(ids []string, role scheduler.Role)
	// Gone is called after the executor or Ensure tombstoned an issue the origin
	// no longer shows.
	Gone(id string)
}

// ServiceConfig wires the service.
type ServiceConfig struct {
	Store     *store.Store
	Persister *Persister
	Fetcher   Fetcher
	Scheduler *scheduler.Scheduler
	// KeepAlive is the retention rule (KeepAlive in this package satisfies it).
	KeepAlive groupsvc.KeepAlive
	// Bucket is the governor bucket one search spends from; empty means origin.Bucket.
	Bucket governor.Bucket
	// Expiry is expiry: how long a stored summary may be served stale; zero
	// means config.DefaultExpiry.
	Expiry time.Duration
	// BatchSize is batch_size, the most issues one search carries; zero means
	// config.DefaultBatchSize.
	BatchSize int
	// Members answers the ids (groups.FoldRef form) of the issues that are
	// members of a configured query: their summaries are refreshed by the query
	// run, so this service books no task for them. Nil means none.
	Members func(ctx context.Context) (map[string]bool, error)
	// Observer is optional.
	Observer Observer
	// Clock is the time source; nil means the Persister's, then the system's.
	Clock governor.Clock
}

// Service runs the refresh of the primary (summary) group for the issues that
// are not members of a configured query, and the read-through of one issue.
type Service struct {
	cfg   ServiceConfig
	clock governor.Clock
}

// NewService returns the service and registers its executor with the scheduler
// under the kind groups.Summary.
func NewService(cfg ServiceConfig) (*Service, error) {
	switch {
	case cfg.Store == nil, cfg.Persister == nil, cfg.Fetcher == nil, cfg.Scheduler == nil, cfg.KeepAlive == nil:
		return nil, errors.New("summary: service needs a store, persister, fetcher, scheduler and keep-alive")
	}
	if cfg.Bucket == "" {
		cfg.Bucket = origin.Bucket
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = config.DefaultExpiry
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = config.DefaultBatchSize
	}
	s := &Service{cfg: cfg, clock: cfg.Clock}
	if s.clock == nil {
		s.clock = cfg.Persister.Clock
	}
	if s.clock == nil {
		s.clock = governor.SystemClock{}
	}
	cfg.Scheduler.Register(groups.Summary, executor{s})
	return s, nil
}

func (s *Service) now() time.Time { return s.clock.Now() }

// members is the set of query-member ids, never nil.
func (s *Service) members(ctx context.Context) (map[string]bool, error) {
	if s.cfg.Members == nil {
		return map[string]bool{}, nil
	}
	m, err := s.cfg.Members(ctx)
	if m == nil {
		m = map[string]bool{}
	}
	return m, err
}

// task is the summary refresh task of id.
func (s *Service) task(id string, class governor.Class, due, access time.Time) scheduler.Task {
	return scheduler.Task{
		Kind: groups.Summary, Entity: id, Group: groups.Summary,
		Class: class, Due: due, Access: access,
	}
}

// bookNext books the next background refresh of id, due at due, at class 4
// (below the query runs' class 3), when the issue is retained and is not a
// member of a configured query. An issue past the keep-alive cutoff, a
// tombstoned one and one with no entity row get nothing.
func (s *Service) bookNext(ctx context.Context, id string, due time.Time, members map[string]bool) error {
	if members[id] {
		return nil
	}
	row, ok, err := s.cfg.Store.GetEntity(ctx, groups.EntityKind, id)
	if err != nil || !ok || !row.TombstonedAt.IsZero() {
		return err
	}
	keep, err := s.cfg.KeepAlive.Retain(ctx, id)
	if err != nil || !keep {
		return err
	}
	return s.cfg.Scheduler.Submit(s.task(id, governor.ClassKeepAlive, due, row.LastAccess))
}

// BookAfterDeparture books the summary task of an issue that has just left its
// last configured query, due at its stored fresh_until (now when it has none).
// An issue that is still a member of some query, is no longer retained or is
// unknown gets nothing.
func (s *Service) BookAfterDeparture(ctx context.Context, id string) error {
	id = groups.FoldRef(id)
	m, err := s.members(ctx)
	if err != nil {
		return err
	}
	var due time.Time
	g, ok, err := s.cfg.Store.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	if err != nil {
		return err
	}
	if ok {
		due = g.FreshUntil
	}
	return s.bookNext(ctx, id, due, m)
}

// Queue submits one summary task for id at class, due at due (zero is due
// now): the write-notice path re-pulls an issue after a write with it. An id
// that is not a Jira key is an error.
func (s *Service) Queue(ctx context.Context, id string, class governor.Class, due time.Time) error {
	id = groups.FoldRef(id)
	if !internal.IsJiraKey(id) {
		return fmt.Errorf("summary: queue: %q is not a Jira key", id)
	}
	row, _, err := s.cfg.Store.GetEntity(ctx, groups.EntityKind, id)
	if err != nil {
		return err
	}
	return s.cfg.Scheduler.Submit(s.task(id, class, due, row.LastAccess))
}

// Sweep evicts the issues that have been closed for the expiry (see the
// package-level Sweep) and returns how many it evicted. The daemon calls it on
// a timer.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	return Sweep(ctx, s.cfg.Store, s.cfg.Expiry, s.now())
}
