package groupsvc

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Target is one entity a Fetcher is asked to fetch a group for.
type Target struct {
	ID       string // the store entity id
	Ref      string // the wire reference as of the primary content
	Stamp    string // opaque: the primary content's first stamp
	Stamp2   string // opaque: its second stamp
	Terminal bool
	// Attrs are the opaque connector attributes of engine.Primary.
	Attrs map[string]string
}

// Outcome is the result of fetching a group for one Target.
type Outcome struct {
	Content     []byte
	Fingerprint string
	// Stamp and Stamp2 are the stamps the content describes.
	Stamp, Stamp2 string
	// FreshUntil, when non-zero, replaces the rules table's fresh_until.
	FreshUntil time.Time
	// LastError is a non-fatal problem stored together with the new content.
	LastError string
	// Cost is what the fetch spent, in the governor's unit for its bucket.
	Cost int64
	// Err, when non-nil, means nothing is stored; ErrGone marks a deleted entity.
	Err error
}

// sentinel is the type of the errors this package declares: their text is a
// method result, so the declarations carry no string literal.
type sentinel int

const (
	sentinelGone sentinel = iota + 1
	sentinelNoFetcher
	sentinelUnknownGroup
	sentinelNoReference
	sentinelNotRefreshed
)

func (e sentinel) Error() string {
	switch e {
	case sentinelGone:
		return "groupsvc: entity is gone"
	case sentinelNoFetcher:
		return "groupsvc: no fetcher registered for the group"
	case sentinelUnknownGroup:
		return "groupsvc: unknown group"
	case sentinelNoReference:
		return "the stored primary content names no entity reference"
	case sentinelNotRefreshed:
		return "groupsvc: the refresh finished without storing a fresh row"
	default:
		return "groupsvc: unknown error"
	}
}

// ErrGone is the Fetch error for an entity that is deleted or no longer
// accessible; the executor turns it into a tombstone.
var ErrGone error = sentinelGone

// ErrNoFetcher is the error of a group nobody registered a fetcher for.
var ErrNoFetcher error = sentinelNoFetcher

// ErrUnknownGroup is the error of a group name outside Config.Spec.Groups. It is
// returned wrapped with the group name: `groupsvc: unknown group "name"`.
var ErrUnknownGroup error = sentinelUnknownGroup

// errNoReference is the per-target error of an entity whose stored primary
// content has no reference to fetch with (it is gone, or names none); the target
// is failed with it and never fetched.
var errNoReference error = sentinelNoReference

// CallMeta is what one group fetch is admitted as.
type CallMeta struct {
	Class     governor.Class
	Purpose   governor.Purpose
	Group     string
	BatchSize int
}

// Fetcher fetches one group for batches of entities. The connector implements
// it, the service runs it.
type Fetcher interface {
	// Group is the group name this fetcher produces.
	Group() string
	// MaxBatch is the most targets one Fetch call takes.
	MaxBatch() int
	// Spend is the governor request for fetching n targets as the class.
	Spend(class governor.Class, n int) governor.Request
	// Fetch fetches the group for every target and returns one Outcome per
	// Target, in order.
	Fetch(ctx context.Context, call CallMeta, targets []Target) []Outcome
}

// KeepAlive is the connector's retention rule for entities that are neither
// tombstoned nor members of a configured query.
type KeepAlive interface {
	// Retain reports whether the entity is kept alive.
	Retain(ctx context.Context, id string) (bool, error)
	// Access records that the entity was read.
	Access(ctx context.Context, id string) error
}

// ErrorPolicy is the connector's reading of an origin failure.
type ErrorPolicy interface {
	// Classify turns an origin failure into the error the caller sees, pausing the
	// governor where the failure asks for it.
	Classify(g *governor.Governor, fallbackPause time.Duration, err error) error
	// IsRateLimit reports whether err is the origin's rate-limit answer.
	IsRateLimit(err error) bool
	// IsUnauthenticated reports whether err is the origin refusing the credentials.
	IsUnauthenticated(err error) bool
}

// Describer reads the stored primary content. engine.Classifier satisfies it;
// the service needs nothing else of it.
type Describer interface {
	Describe(content []byte) (engine.Primary, error)
}

var _ Describer = engine.Classifier(nil)

// Resolution is the answer for one requested reference.
type Resolution struct {
	// Requested is the reference as the caller wrote it.
	Requested string
	// NodeID is the entity's store id; empty when Found is false.
	NodeID string
	// Ref is the reference in the origin's own spelling (the alias table's form).
	Ref string
	// Found is false when the origin answered that no such entity exists (or it
	// is not visible to the credentials).
	Found bool
	// FromStore is true when the alias table already knew the reference, so no
	// origin call was made for it.
	FromStore bool
	// Renamed is true when the origin returned an id the table already had under a
	// different reference; OldRef is that earlier reference.
	Renamed bool
	OldRef  string
}

// RefResolver resolves wire references the alias table has not seen (first
// sighting). It returns one Resolution per reference, in order, and the cost
// the origin calls spent.
type RefResolver interface {
	Resolve(ctx context.Context, call CallMeta, refs []string) ([]Resolution, int64, error)
}

// Store is the part of the daemon store the service uses; *store.Store
// satisfies it.
type Store interface {
	groupkit.ThenStore
	// InTx runs fn in one store transaction (see store.Store.InTx).
	InTx(ctx context.Context, fn func(tx *store.Tx) error) error
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	PutEntity(ctx context.Context, e store.EntityRow) error
	TouchEntity(ctx context.Context, kind, id string, at time.Time) error
	EnsureEntity(ctx context.Context, kind, id string) error
	ListEntities(ctx context.Context, kind string) ([]store.EntityRow, error)
	ListQueries(ctx context.Context, configuredOnly bool) ([]store.QueryRow, error)
}

var _ Store = (*store.Store)(nil)

// AuthMarker is the identity tracker's authentication state.
type AuthMarker interface {
	MarkUnauthenticated(err error)
	Clear()
}

// RefreshInfo says how a refreshed group was fetched (the shared engine's type).
type RefreshInfo = engine.RefreshInfo

// OriginHints receives the origin of a refresh the service is about to queue
// because of a write or a refresh request: class 2 serves both, and the group
// engine's Purpose does not tell them apart from a background refresh.
type OriginHints interface {
	NoteOrigin(id, group, origin string)
}

// Observer receives the staleness and revalidation facts telemetry reports.
type Observer interface {
	// Served fires for every group an Ensure answers, with how far past
	// fresh_until the served row is (zero when it is fresh).
	Served(group string, staleness time.Duration)
	// Revalidated is called with the group and the result word of one
	// revalidation.
	Revalidated(group, result string)
}

// Config wires the service.
type Config struct {
	// Spec is the connector's registration. The service reads EntityKind,
	// PrimaryGroup, Groups and FoldRef of it.
	Spec registry.Spec
	// Describer reads the stored primary content.
	Describer Describer
	Store     Store
	Scheduler *scheduler.Scheduler
	// Rules is the freshness table of the groups.
	Rules groupkit.Table
	// Aliases is the alias table (store id to wire reference).
	Aliases query.Aliases
	// Resolver is the first-sighting resolver, used only for a reference the alias
	// table does not know; nil makes such a reference an error.
	Resolver RefResolver
	// KeepAlive is the connector's retention rule.
	KeepAlive KeepAlive
	// Errors is the connector's reading of origin failures.
	Errors ErrorPolicy
	// Identity receives the authentication markers; nil means none.
	Identity AuthMarker
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Governor is paused when a fetch answers a rate limit; nil pauses nothing.
	Governor *governor.Governor
	// Expiry is how long a stored group may be served stale; required, positive:
	// the shared code has no default.
	Expiry time.Duration
	// FallbackPause is how long a rate-limit answer without a retry hint pauses
	// the background classes.
	FallbackPause time.Duration

	// NoSuchEntity and BadRef are the sentinels Ensure returns for an
	// unresolvable and for a malformed reference.
	NoSuchEntity, BadRef error
	// UnknownEntity is the sentinel an unknown reference is wrapped with.
	UnknownEntity error
	// ParseID is the connector's reference rule: it tells a wire reference from a
	// store id and reports a malformed one. The shared code wraps err with
	// BadRef; an unknown reference is keyed by Spec.FoldRef.
	ParseID func(id string) (wire string, isRef bool, err error)

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
	OnGone func(ctx context.Context, tx *store.Tx, id string, purpose governor.Purpose) error
}
