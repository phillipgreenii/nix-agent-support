// Package groupsvc is the refresh engine of the non-summary entity groups: the
// counterpart of what internal/summary's Service does for the summary group,
// generalized over the groups.Fetcher interface so that each group packet only
// supplies a fetcher.
//
//   - Service.Register installs one scheduler executor per group (executor.go):
//     a batch of due and fill tasks goes through the fetcher, and each result is
//     stored through the generation-checked groups.Commit, so a refresh that
//     began before a write loses.
//   - Service.Ensure is the spec's "Entity read" for any set of groups
//     (ensure.go): it records the access, serves what is fresh from the store,
//     queues the stale groups at interactive priority, fetches them in parallel
//     (single-flight per entity and group), waits up to the caller's deadline
//     without cancelling a fetch that outlives it, and falls back to the stale
//     content inside expiry.
//   - The background cycle books each group's next task at its fresh_until for
//     entities in the keep-alive set (book.go), Rebuild restores the queue after
//     a restart (rebuild.go), and a group fetch that says the PR is gone
//     tombstones the entity.
//   - write.go holds the helpers the write path and the refresh and revalidation
//     packets stand on: SerializeWrite, DrainWrites, InvalidateAndQueue,
//     QueueGroup, RescheduleGroup and Inspect.
//
// The package performs no origin call of its own except the first-sighting
// resolution of an owner/repo#n it has never seen; every group fetch is the
// Fetcher's, and goes through the governor.
package groupsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// ErrNoSuchPR is the sentinel for a pull request GitHub says does not exist (a
// reference it cannot resolve, or an entity whose group fetch found it gone). The
// ops map it to not_found.
var ErrNoSuchPR = errors.New("groupsvc: no such pull request")

// ErrBadRef is the sentinel for an id that is neither a node id nor owner/repo#n.
var ErrBadRef = errors.New("groupsvc: malformed pull request reference")

// ErrNoFetcher is the error of a group nobody registered a fetcher for.
var ErrNoFetcher = errors.New("groupsvc: no fetcher registered for the group")

// Store is the part of the daemon store the service uses; *store.Store
// satisfies it.
type Store interface {
	groups.GenStore
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	PutEntity(ctx context.Context, e store.EntityRow) error
	TouchEntity(ctx context.Context, kind, id string, at time.Time) error
	EnsureEntity(ctx context.Context, kind, id string) error
	ListEntities(ctx context.Context, kind string) ([]store.EntityRow, error)
	ListQueries(ctx context.Context, configuredOnly bool) ([]store.QueryRow, error)
}

var _ Store = (*store.Store)(nil)

// RefResolver resolves owner/repo#n references the alias table has not seen.
// *entity.Resolver satisfies it.
type RefResolver interface {
	Resolve(ctx context.Context, call ratefeed.Meta, refs []entity.Ref) ([]entity.Resolution, int64, error)
}

var _ RefResolver = (*entity.Resolver)(nil)

// AuthMarker is the identity tracker's authentication state. *identity.Tracker
// satisfies it.
type AuthMarker interface {
	MarkUnauthenticated(err error)
	Clear()
}

// RefreshInfo says how a refreshed group was fetched.
type RefreshInfo struct {
	// Started is when the fetch began.
	Started time.Time
	// Class and Purpose are what the call was admitted as.
	Class   governor.Class
	Purpose governor.Purpose
}

// Observer receives the staleness and revalidation facts telemetry reports.
type Observer interface {
	// Served fires for every group an Ensure answers, with how far past
	// fresh_until the served row is (zero when it is fresh).
	Served(group string, staleness time.Duration)
	// Revalidated is called by the revalidation packet with the group and the
	// result word of one revalidation.
	Revalidated(group, result string)
}

// Config wires the service.
type Config struct {
	Store     Store
	Scheduler *scheduler.Scheduler
	// Summary is the summary group's service: its keep-alive rule and access
	// bookkeeping are the engine's.
	Summary *summary.Service
	Rules   *groups.Rules
	// Aliases is the alias table (node id to owner/repo#n).
	Aliases *entity.Identity
	// Resolver is the first-sighting resolver, used only for a reference the
	// alias table does not know; nil makes such a reference an error.
	Resolver RefResolver
	// Identity receives the authentication markers; nil means none.
	Identity AuthMarker
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Governor is paused when a fetch answers a rate limit; nil pauses nothing.
	Governor *governor.Governor
	// Expiry is expiry: how long a stored group may be served stale; zero means
	// the config default.
	Expiry time.Duration
	// FallbackPause is how long a rate-limit answer without a Retry-After pauses
	// the background classes; zero means metered.DefaultFallbackPause.
	FallbackPause time.Duration

	// OnGroupRefreshed fires after a refreshed group is stored and only then (a
	// superseded or failed refresh fires nothing), with the row stored before it
	// (nil for a first sighting), the row now stored and how it was fetched. It
	// runs on the executor's goroutine and MUST NOT block. The change engine
	// attaches here.
	OnGroupRefreshed func(prev *store.GroupRow, next store.GroupRow, info RefreshInfo)
}

// Service runs the refresh of the non-summary groups and the read-through.
type Service struct {
	cfg   Config
	clock governor.Clock

	mu       sync.Mutex
	fetchers map[string]groups.Fetcher
	observer Observer
	// commits counts the refreshes stored per (entity, group) since start; an
	// Ensure compares it before and after its fetch to say FromOrigin.
	commits map[pairKey]uint64

	writes writeLocks
}

type pairKey struct{ node, group string }

// New returns the service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil, cfg.Scheduler == nil, cfg.Summary == nil, cfg.Rules == nil, cfg.Aliases == nil:
		return nil, errors.New("groupsvc: service needs a store, scheduler, summary service, rules and alias table")
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = config.DefaultExpiry
	}
	s := &Service{cfg: cfg, clock: cfg.Clock, fetchers: map[string]groups.Fetcher{}, commits: map[pairKey]uint64{}}
	if s.clock == nil {
		s.clock = governor.SystemClock{}
	}
	s.writes.init()
	return s, nil
}

func (s *Service) now() time.Time { return s.clock.Now() }

// Register installs the executor of the fetcher's group under the scheduler kind
// f.Group(). Registering a group again replaces the fetcher. It panics for the
// summary group (which summary.Service owns) and for a name outside the closed
// set of groups: both are wiring mistakes.
func (s *Service) Register(f groups.Fetcher) {
	g := f.Group()
	if g == summary.GroupName || !isGroup(g) {
		panic(fmt.Sprintf("groupsvc: cannot register a fetcher for group %q", g))
	}
	s.mu.Lock()
	s.fetchers[g] = f
	s.mu.Unlock()
	s.cfg.Scheduler.Register(g, &executor{s: s, f: f})
}

func isGroup(name string) bool {
	for _, g := range groups.All() {
		if g == name {
			return true
		}
	}
	return false
}

func (s *Service) fetcher(group string) (groups.Fetcher, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.fetchers[group]
	return f, ok
}

// registered returns the registered groups in the order of groups.All.
func (s *Service) registered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, g := range groups.All() {
		if _, ok := s.fetchers[g]; ok {
			out = append(out, g)
		}
	}
	return out
}

// SetObserver installs the observer; nil means none.
func (s *Service) SetObserver(o Observer) {
	s.mu.Lock()
	s.observer = o
	s.mu.Unlock()
}

func (s *Service) obs() Observer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observer
}

// Revalidated forwards one revalidation result to the observer, for the
// revalidation packet.
func (s *Service) Revalidated(group, result string) {
	if o := s.obs(); o != nil {
		o.Revalidated(group, result)
	}
}

func (s *Service) commitCount(node, group string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits[pairKey{node, group}]
}

func (s *Service) countCommit(node, group string) {
	s.mu.Lock()
	s.commits[pairKey{node, group}]++
	s.mu.Unlock()
}

// ---- identity ----------------------------------------------------------------

// canonical maps an id (a node id or owner/repo#n) to the entity key with no
// origin call. A reference the alias table does not know is returned in its
// lower-cased form with known false.
func (s *Service) canonical(ctx context.Context, id string) (key string, known bool, err error) {
	if !strings.Contains(id, "#") {
		return id, true, nil
	}
	ref, perr := entity.ParseRef(id)
	if perr != nil {
		return strings.ToLower(id), false, nil
	}
	node, _, ok, err := s.cfg.Aliases.NodeID(ctx, ref)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return strings.ToLower(id), false, nil
	}
	return node, true, nil
}

// Resolve returns the node id and the canonical reference of a pull request
// named by a node id or by owner/repo#n. A reference the alias table does not
// know is resolved with the resolver (one repository { pullRequest(number:) }
// document, admitted as interactive); a PR GitHub says does not exist is
// ErrNoSuchPR and a malformed id is ErrBadRef. A node id whose alias is unknown
// comes back with a zero reference.
func (s *Service) Resolve(ctx context.Context, id string) (string, entity.Ref, error) {
	if !strings.Contains(id, "#") {
		if id == "" || strings.ContainsAny(id, " \t\r\n/") {
			return "", entity.Ref{}, fmt.Errorf("%w: %q", ErrBadRef, id)
		}
		ref, _, err := s.cfg.Aliases.Ref(ctx, id)
		if err != nil {
			return "", entity.Ref{}, err
		}
		return id, ref, nil
	}
	ref, err := entity.ParseRef(id)
	if err != nil {
		return "", entity.Ref{}, fmt.Errorf("%w: %v", ErrBadRef, err)
	}
	node, canon, ok, err := s.cfg.Aliases.NodeID(ctx, ref)
	if err != nil {
		return "", entity.Ref{}, err
	}
	if ok {
		return node, canon, nil
	}
	if s.cfg.Resolver == nil {
		return "", entity.Ref{}, fmt.Errorf("groupsvc: %s is not known and no resolver is configured", id)
	}
	res, _, err := s.cfg.Resolver.Resolve(ctx,
		ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive}, []entity.Ref{ref})
	if err != nil {
		return "", entity.Ref{}, err
	}
	if len(res) == 0 || !res[0].Found {
		return "", entity.Ref{}, fmt.Errorf("%w: %s", ErrNoSuchPR, id)
	}
	return res[0].NodeID, res[0].Ref, nil
}

// ---- keep-alive set ----------------------------------------------------------

// members returns the node ids that are a member of a configured query (the
// rule of summary.Service: a query row's ids may be node ids or references).
func (s *Service) members(ctx context.Context) (map[string]bool, error) {
	qs, err := s.cfg.Store.ListQueries(ctx, true)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, q := range qs {
		for _, id := range q.IDs {
			key, known, err := s.canonical(ctx, id)
			if err != nil {
				return nil, err
			}
			if known {
				set[key] = true
			}
		}
	}
	return set, nil
}

// inKeepAlive is summary.Service.InKeepAlive with the member set already in
// hand: a tombstoned entity is out; a member of a configured query is in;
// everyone else is the summary service's call (accessed within the window).
func (s *Service) inKeepAlive(ctx context.Context, row store.EntityRow, members map[string]bool) (bool, error) {
	if !row.TombstonedAt.IsZero() {
		return false, nil
	}
	if members[row.ID] {
		return true, nil
	}
	return s.cfg.Summary.InKeepAlive(ctx, row.ID)
}

// classFor is the class of a background task: 3 for a member of a configured
// query, 4 for an entity kept alive by access alone.
func classFor(member bool) governor.Class {
	if member {
		return governor.ClassQuery
	}
	return governor.ClassKeepAlive
}

func purposeOf(class governor.Class) governor.Purpose {
	switch class {
	case governor.ClassInteractive:
		return governor.PurposeInteractive
	case governor.ClassFill:
		return governor.PurposeFill
	default:
		return governor.PurposeRefresh
	}
}

// servable reports whether a stored row may be served stale: it has content and
// was fetched within expiry.
func (s *Service) servable(row store.GroupRow, ok bool, now time.Time) bool {
	return ok && len(row.Content) > 0 && !row.FetchedAt.IsZero() && now.Sub(row.FetchedAt) <= s.cfg.Expiry
}

// loadSummary reads the entity's stored summary.
func (s *Service) loadSummary(ctx context.Context, node string) (summary.Summary, store.GroupRow, bool, error) {
	row, ok, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, node, summary.GroupName)
	if err != nil || !ok || len(row.Content) == 0 {
		return summary.Summary{}, row, false, err
	}
	var sum summary.Summary
	if err := json.Unmarshal(row.Content, &sum); err != nil {
		return summary.Summary{}, row, false, fmt.Errorf("groupsvc: decode summary of %s: %w", node, err)
	}
	return sum, row, true, nil
}
