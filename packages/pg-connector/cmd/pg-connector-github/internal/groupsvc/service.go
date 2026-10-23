// Package groupsvc is the GitHub face of the shared group refresh service
// (pkg/connectord/groupsvc): the refresh engine of the non-summary entity
// groups, counterpart of what internal/summary's Service does for the summary
// group.
//
// The engine itself lives in the shared library: the generation-checked group
// executor, Ensure (the spec's "Entity read" for any set of groups), the
// background cycle, Rebuild and the write helpers (SerializeWrite, DrainWrites,
// InvalidateAndQueue, QueueGroup, RescheduleGroup, Inspect). This package binds
// it to GitHub as a thin adapter and keeps the exported API its callers use:
//
//   - New builds the shared Config: the pull request Spec, the summary
//     describer and alias table of the leaf packages (groups, entity), the
//     summary service as the KeepAlive rule, ErrorPolicy as the reading of an
//     origin failure, and the GitHub sentinels (ErrNoSuchPR, ErrBadRef,
//     summary.ErrUnknownEntity).
//   - Register takes the unchanged GitHub fetchers (groups.Fetcher) through the
//     Register-boundary adapter (fetcheradapter.go).
//   - Ensure and Resolve wrap the shared methods and convert the wire reference
//     and the stored primary content back to entity.Ref and summary.Summary.
//
// This package MUST NOT import the GitHub changes or query packages: the
// unedited changes tests import it while query imports changes.
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
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	sharedsvc "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// ErrNoSuchPR is the sentinel for a pull request GitHub says does not exist (a
// reference it cannot resolve, or an entity whose group fetch found it gone). The
// ops map it to not_found.
var ErrNoSuchPR = errors.New("groupsvc: no such pull request")

// ErrBadRef is the sentinel for an id that is neither a node id nor owner/repo#n.
var ErrBadRef = errors.New("groupsvc: malformed pull request reference")

// ErrNoFetcher is the error of a group nobody registered a fetcher for (the
// shared sentinel).
var ErrNoFetcher = sharedsvc.ErrNoFetcher

// Store is the part of the daemon store the service uses; *store.Store
// satisfies it.
type Store = sharedsvc.Store

// RefResolver resolves owner/repo#n references the alias table has not seen.
// *entity.Resolver satisfies it.
type RefResolver interface {
	Resolve(ctx context.Context, call ratefeed.Meta, refs []entity.Ref) ([]entity.Resolution, int64, error)
}

var _ RefResolver = (*entity.Resolver)(nil)

// AuthMarker is the identity tracker's authentication state. *identity.Tracker
// satisfies it.
type AuthMarker = sharedsvc.AuthMarker

// RefreshInfo says how a refreshed group was fetched (the shared engine's type).
type RefreshInfo = engine.RefreshInfo

// The origins a refresh request can note, as the change engine reads them when
// it classifies the refresh that follows (the shared engine's words).
const (
	OriginQuery   = engine.OriginQuery
	OriginRefresh = engine.OriginRefresh
	OriginWrite   = engine.OriginWrite
	OriginRead    = engine.OriginRead
)

// OriginHints receives the origin of a refresh the service is about to queue
// because of a write or a refresh request.
type OriginHints = sharedsvc.OriginHints

// Observer receives the staleness and revalidation facts telemetry reports.
type Observer = sharedsvc.Observer

// Status says how a group of an Ensure answer may be used.
type Status = sharedsvc.Status

const (
	// Fresh: the row is inside fresh_until and its stamps match the current
	// summary, or this very call fetched it.
	Fresh = sharedsvc.Fresh
	// Stale: the row is served past its freshness but inside expiry.
	Stale = sharedsvc.Stale
	// Unavailable: there is no row inside expiry; Err says why.
	Unavailable = sharedsvc.Unavailable
)

// EnsureOptions are the options of Ensure.
type EnsureOptions = sharedsvc.EnsureOptions

// GroupResult is the answer for one wanted group.
type GroupResult = sharedsvc.GroupResult

// EnsureResult is the answer of Ensure.
type EnsureResult struct {
	// Summary is the current summary the groups were judged against; zero when no
	// summary could be had (every group is then Unavailable).
	Summary summary.Summary
	// Groups has one entry per wanted group, in the order asked, summary
	// included.
	Groups []GroupResult
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
	// OnGroupStoring runs INSIDE the store transaction that writes a refreshed
	// group, after the row is written, and only when the row is stored (a refresh
	// superseded by a newer generation runs nothing, which is how a refresh that
	// started before a write logs no change). prev is the row read before the
	// fetch (nil when it had no content) and next the row now stored. An error
	// rolls the group write back and fails the task. It MUST use only tx.
	OnGroupStoring func(ctx context.Context, tx *store.Tx, prev *store.GroupRow, next store.GroupRow, info RefreshInfo) error
	// OnGone runs inside the store transaction that tombstones an entity a group
	// fetch found gone, and only when the entity was visible before, with the
	// purpose the fetch was admitted under. An error rolls the tombstone back
	// and fails the task. It MUST use only tx.
	OnGone func(ctx context.Context, tx *store.Tx, node string, purpose governor.Purpose) error
}

// Service runs the refresh of the non-summary groups and the read-through. The
// methods the facade does not wrap (Rebuild, SerializeWrite, DrainWrites,
// InvalidateAndQueue(As), QueueGroup, RescheduleGroup, Inspect, SetObserver,
// SetOriginHints, Revalidated) are the shared service's own.
type Service struct {
	*sharedsvc.Service
}

// The spec of a pull request entity as the shared service reads it: only the
// fields it uses (it does not call the full registry validation).
func prSpec() registry.Spec {
	return registry.Spec{
		EntityKind:   entity.KindPR,
		PrimaryGroup: groups.Summary,
		Groups:       groups.All(),
		FoldRef:      groups.FoldRef,
	}
}

// New returns the service. It applies the defaults the engine has always applied
// (the config expiry, the rate-limit fallback pause) before building the shared
// service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil, cfg.Scheduler == nil, cfg.Summary == nil, cfg.Rules == nil, cfg.Aliases == nil:
		return nil, errors.New("groupsvc: service needs a store, scheduler, summary service, rules and alias table")
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = config.DefaultExpiry
	}
	if cfg.FallbackPause <= 0 {
		cfg.FallbackPause = metered.DefaultFallbackPause
	}
	sc := sharedsvc.Config{
		Spec:          prSpec(),
		Describer:     groups.Describer{},
		Store:         cfg.Store,
		Scheduler:     cfg.Scheduler,
		Rules:         cfg.Rules.Table(),
		Aliases:       entity.NewAliases(cfg.Aliases, nil),
		KeepAlive:     keepAlive{s: cfg.Summary},
		Errors:        ErrorPolicy{},
		Identity:      cfg.Identity,
		Clock:         cfg.Clock,
		Governor:      cfg.Governor,
		Expiry:        cfg.Expiry,
		FallbackPause: cfg.FallbackPause,
		NoSuchEntity:  ErrNoSuchPR,
		BadRef:        ErrBadRef,
		UnknownEntity: summary.ErrUnknownEntity,
		ParseID:       parseID,

		OnGroupRefreshed: cfg.OnGroupRefreshed,
		OnGroupStoring:   cfg.OnGroupStoring,
		OnGone:           cfg.OnGone,
	}
	if cfg.Resolver != nil {
		sc.Resolver = refResolver{r: cfg.Resolver}
	}
	shared, err := sharedsvc.New(sc)
	if err != nil {
		return nil, err
	}
	return &Service{Service: shared}, nil
}

// Register installs the executor of the fetcher's group. It panics for the
// summary group (which summary.Service owns) and for a name outside the closed
// set of groups: both are wiring mistakes.
func (s *Service) Register(f groups.Fetcher) {
	s.Service.Register(fetcherAdapter{f: f})
}

// Resolve returns the node id and the canonical reference of a pull request
// named by a node id or by owner/repo#n. A reference the alias table does not
// know is resolved with the resolver (one repository { pullRequest(number:) }
// document, admitted as interactive); a PR GitHub says does not exist is
// ErrNoSuchPR and a malformed id is ErrBadRef. A node id whose alias is unknown
// comes back with a zero reference.
func (s *Service) Resolve(ctx context.Context, id string) (string, entity.Ref, error) {
	node, ref, err := s.Service.Resolve(ctx, id)
	if err != nil {
		return "", entity.Ref{}, err
	}
	if ref == "" {
		return node, entity.Ref{}, nil
	}
	r, err := entity.ParseRef(ref)
	if err != nil {
		return "", entity.Ref{}, err
	}
	return node, r, nil
}

// Ensure is the spec's "Entity read" for any set of groups of one PR (see the
// shared Ensure). id is a node id or owner/repo#n. A PR GitHub says does not
// exist (an unknown reference, or an entity already tombstoned) is ErrNoSuchPR
// and a malformed id ErrBadRef. Failures to fetch are NOT errors of Ensure: they
// are the Err of the group's result.
func (s *Service) Ensure(ctx context.Context, id string, want []string, opt EnsureOptions) (EnsureResult, error) {
	res, err := s.Service.Ensure(ctx, id, want, opt)
	if err != nil {
		return EnsureResult{}, err
	}
	out := EnsureResult{Groups: res.Groups}
	if len(res.Content) > 0 {
		if err := json.Unmarshal(res.Content, &out.Summary); err != nil {
			return EnsureResult{}, fmt.Errorf("groupsvc: decode summary of %s: %w", id, err)
		}
	}
	return out, nil
}

// parseID is the pull request reference rule: an id holding a # is a wire
// reference (malformed when entity.ParseRef refuses it); anything else is a node
// id, malformed when empty or when it holds a blank or a slash.
func parseID(id string) (wire string, isRef bool, err error) {
	if strings.Contains(id, "#") {
		if _, err := entity.ParseRef(id); err != nil {
			return id, true, err
		}
		return id, true, nil
	}
	if id == "" || strings.ContainsAny(id, " \t\r\n/") {
		return id, false, errors.New("groupsvc: malformed node id")
	}
	return id, false, nil
}
