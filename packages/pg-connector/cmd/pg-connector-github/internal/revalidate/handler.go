package revalidate

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Engine is the part of the group refresh engine (*groupsvc.Service) the handler
// uses.
type Engine interface {
	// QueueGroup queues the refresh of one group at the class, due at due (zero is
	// now); it merges with a queued task.
	QueueGroup(ctx context.Context, id, group string, class governor.Class, due time.Time) error
	// RescheduleGroup replaces the queued background task of the pair at due, and
	// never downgrades or postpones a class 1 or 2 task.
	RescheduleGroup(ctx context.Context, id, group string, due time.Time) error
	// Inspect reports the best class among the queued and the running tasks of
	// the pair, and whether one is queued and whether one is running.
	Inspect(ctx context.Context, id, group string) (class governor.Class, queued, running bool, err error)
	// Revalidated forwards a revalidation result to the engine's observer.
	Revalidated(group, result string)
}

// Observer receives the revalidation metric events. groupsvc.Observer
// satisfies it.
type Observer interface {
	Revalidated(group, result string)
}

// Config wires the handler.
type Config struct {
	// Store is the daemon store; *store.Store satisfies it.
	Store groups.GenStore
	// Engine is the group refresh engine (the *groupsvc.Service).
	Engine Engine
	// KeepAlive reports whether an entity is in the keep-alive set: the summary
	// service's InKeepAlive.
	KeepAlive func(ctx context.Context, nodeID string) (bool, error)
	Rules     *groups.Rules
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Observer receives the revalidation events; nil means the engine's observer
	// (Engine.Revalidated).
	Observer Observer
	// OnError, when set, is told about an error the hook could not return (the
	// hook has no error result); the remaining work of the summary still runs.
	OnError func(err error)
}

// Handler applies Decide to every refreshed summary.
type Handler struct {
	cfg   Config
	clock governor.Clock

	// beforeCommit, when set (tests), runs between reading a row for extension
	// and committing the extension.
	beforeCommit func(nodeID, group string)
}

// New returns the handler. It panics when the store, engine, keep-alive probe or
// rules are missing: a wiring mistake.
func New(cfg Config) *Handler {
	if cfg.Store == nil || cfg.Engine == nil || cfg.KeepAlive == nil || cfg.Rules == nil {
		panic("revalidate: New needs a store, engine, keep-alive probe and rules")
	}
	h := &Handler{cfg: cfg, clock: cfg.Clock}
	if h.clock == nil {
		h.clock = governor.SystemClock{}
	}
	return h
}

// OnSummaryRefreshed has the exact signature of summary.ServiceConfig's hook. It
// runs on the summary executor's goroutine, makes no origin call and writes no
// change row: it revalidates, queues dependent groups and populates a first
// sighting through the engine. Install it as the hook, or call it first from a
// handler that chains more behavior after it.
func (h *Handler) OnSummaryRefreshed(prev *summary.Summary, next summary.Stored) {
	if next.Summary.Gone || next.Summary.NodeID == "" {
		return
	}
	ctx := context.Background()
	node := next.Summary.NodeID
	plan := Decide(prev, next)

	if plan.Populate {
		h.populate(ctx, node)
	}
	for _, g := range plan.Queue {
		if err := h.cfg.Engine.QueueGroup(ctx, node, g, governor.ClassInvalidated, h.clock.Now()); err != nil {
			h.fail(err)
			continue
		}
		h.report(g, ResultRepulled)
	}
	for _, g := range plan.Extend {
		if err := h.extend(ctx, node, g); err != nil {
			h.fail(err)
		}
	}
}

// populate books the background groups of a first sighting when the entity is in
// the keep-alive set. A group already fresh is skipped by the engine's executor.
func (h *Handler) populate(ctx context.Context, node string) {
	in, err := h.cfg.KeepAlive(ctx, node)
	if err != nil {
		h.fail(err)
		return
	}
	if !in {
		return
	}
	now := h.clock.Now()
	for _, g := range PopulateGroups() {
		if err := h.cfg.Engine.RescheduleGroup(ctx, node, g, now); err != nil {
			h.fail(err)
			continue
		}
		h.report(g, ResultRepulled)
	}
}

// extend revalidates one stored row whose summary fingerprint is unchanged: its
// fresh_until becomes min(max_age_at, now + ttl) by a generation-checked write
// and its background task moves to it. A row that does not exist, or has a
// refresh queued at class 1 or 2 or running, is left alone; a row at its maximum
// age is not extended and its due re-pull stands.
func (h *Handler) extend(ctx context.Context, node, group string) error {
	row, ok, err := h.cfg.Store.GetGroup(ctx, entity.KindPR, node, group)
	if err != nil || !ok || len(row.Content) == 0 {
		return err
	}
	class, queued, running, err := h.cfg.Engine.Inspect(ctx, node, group)
	if err != nil {
		return err
	}
	if running || (queued && class <= governor.ClassInvalidated) {
		// A dependent refresh is already on its way: it is neither replaced nor
		// postponed, and the row it will replace is not extended.
		return nil
	}
	now := h.clock.Now()
	until, ok := h.cfg.Rules.Extend(row, group, now)
	if !ok {
		if row.MaxAgeAt.IsZero() || now.Before(row.MaxAgeAt) {
			return nil // no maximum recorded: the row's own schedule stands
		}
		h.report(group, ResultMaxAge)
		if !queued {
			// Nothing is booked: book the forced re-pull at the maximum.
			return h.cfg.Engine.RescheduleGroup(ctx, node, group, row.MaxAgeAt)
		}
		return nil
	}
	if h.beforeCommit != nil {
		h.beforeCommit(node, group)
	}
	tok := groups.Token{Kind: entity.KindPR, ID: node, Group: group, Generation: row.Generation}
	row.FreshUntil = until
	stored, err := groups.Commit(ctx, h.cfg.Store, tok, row)
	if err != nil {
		return err
	}
	if !stored {
		return nil // an invalidation landed first and wins
	}
	h.report(group, ResultExtended)
	return h.cfg.Engine.RescheduleGroup(ctx, node, group, until)
}

func (h *Handler) report(group, result string) {
	if h.cfg.Observer != nil {
		h.cfg.Observer.Revalidated(group, result)
		return
	}
	h.cfg.Engine.Revalidated(group, result)
}

func (h *Handler) fail(err error) {
	if err == nil || h.cfg.OnError == nil || errors.Is(err, context.Canceled) {
		return
	}
	h.cfg.OnError(err)
}
