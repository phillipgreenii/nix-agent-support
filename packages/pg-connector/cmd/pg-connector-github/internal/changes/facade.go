package changes

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The shared engine's words, re-exported under their GitHub-tree names.
const (
	// ResultSettled: every awaited group was refreshed.
	ResultSettled = engine.ResultSettled
	// ResultFailed: the barrier ended because a refresh failed.
	ResultFailed = engine.ResultFailed
	// ResultTimeout: the barrier ended at its deadline.
	ResultTimeout = engine.ResultTimeout
)

type (
	// Observer receives the facts the change metrics report.
	Observer = engine.Observer
	// Awaiting is one group a pending row waits for.
	Awaiting = engine.Awaiting
	// Stats are the settle lifecycle's counts.
	Stats = engine.Stats
	// Queuer queues a group's refresh.
	Queuer = engine.Queuer
)

// DecodeAwaited reads the Awaited column of a pending row; an empty or null
// document is no awaited groups.
func DecodeAwaited(raw json.RawMessage) ([]Awaiting, error) { return engine.DecodeAwaited(raw) }

// Config configures an Engine.
type Config struct {
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// SettleTimeout is how long a pending row waits for its dependents; zero
	// means config.DefaultSettleTimeout.
	SettleTimeout time.Duration
	// BodyCap is the byte cap of a comment or review body recorded in a field;
	// zero means config.DefaultChangeBodyCap.
	BodyCap int
}

// Engine is the GitHub binding of the shared change engine: the shared engine
// (whose methods it exposes) over the GitHub registration and classifier, plus
// the two methods whose arguments are GitHub types. It is safe for concurrent
// use.
type Engine struct {
	*engine.Engine
}

// New builds an Engine from cfg, applying the defaults.
func New(cfg Config) (*Engine, error) {
	if cfg.SettleTimeout < 0 {
		return nil, fmt.Errorf("changes: settle timeout %s is negative", cfg.SettleTimeout)
	}
	if cfg.BodyCap < 0 {
		return nil, fmt.Errorf("changes: body cap %d is negative", cfg.BodyCap)
	}
	if cfg.SettleTimeout == 0 {
		cfg.SettleTimeout = config.DefaultSettleTimeout
	}
	if cfg.BodyCap == 0 {
		cfg.BodyCap = config.DefaultChangeBodyCap
	}
	e, err := engine.New(engine.Config{
		Spec: Spec(), Classifier: classifier{}, Clock: cfg.Clock,
		SettleTimeout: cfg.SettleTimeout, BodyCap: cfg.BodyCap,
	})
	if err != nil {
		return nil, err
	}
	return &Engine{Engine: e}, nil
}

// DetectSummary is the summary seam's body (summary.StoreHook): it runs inside
// the transaction that stored st, after the rows are written. A nil prev, a
// first sighting, a gone summary and a summary without a node id are the
// baseline and add nothing.
func (e *Engine) DetectSummary(ctx context.Context, tx *store.Tx, prev *summary.Summary, st summary.Stored, info summary.FetchInfo) error {
	c := engine.PrimaryChange{
		ID:   st.Summary.NodeID,
		Info: engine.RefreshInfo{Class: info.Class, Purpose: info.Purpose},
	}
	if prev != nil && !st.First && !st.Summary.Gone {
		var err error
		if c.Prev, err = json.Marshal(prev); err != nil {
			return fmt.Errorf("changes: encode previous summary of %s: %w", c.ID, err)
		}
		if c.Next, err = json.Marshal(st.Summary); err != nil {
			return fmt.Errorf("changes: encode summary of %s: %w", c.ID, err)
		}
	}
	return e.Engine.DetectPrimary(ctx, tx, c)
}

// Membership is an entered_query or left_query event the query packet hands the
// engine.
type Membership struct {
	// Kind is classify.KindEnteredQuery or classify.KindLeftQuery.
	Kind string
	// Query is the query's name; Node the entity key; Ref the wire reference.
	Query, Node, Ref string
	// Extra is appended to the row's fields.
	Extra []classify.Field
}

// AppendMembership records a membership change (see engine.Engine.AppendMembership).
func (e *Engine) AppendMembership(ctx context.Context, tx *store.Tx, m Membership) error {
	return e.Engine.AppendMembership(ctx, tx, engine.Membership{
		Kind: m.Kind, Query: m.Query, ID: m.Node, Ref: m.Ref, Extra: m.Extra,
	})
}

// Lifecycle is the settle lifecycle of an Engine (see engine.Lifecycle): it ends
// the pending rows whose deadline has passed (Run, Sweep), ends every one at
// SIGTERM (FlushAll) and picks the rows up again on start (Recover).
//
// Only the Engine and Store fields are required. A zero Clock is the system
// clock, a zero Interval is one second and a nil Log discards. The fields are
// read once, at the first method call. Its methods are safe for concurrent use.
type Lifecycle struct {
	Engine   *Engine
	Store    *store.Store
	Queue    Queuer
	Clock    governor.Clock
	Interval time.Duration
	Log      func(event string, kv ...any)

	once  sync.Once
	inner *engine.Lifecycle
}

func (l *Lifecycle) life() *engine.Lifecycle {
	l.once.Do(func() {
		var e *engine.Engine
		if l.Engine != nil {
			e = l.Engine.Engine
		}
		l.inner = &engine.Lifecycle{
			Engine: e, Store: l.Store, Queue: l.Queue, Clock: l.Clock, Interval: l.Interval, Log: l.Log,
		}
	})
	return l.inner
}

// Run blocks until ctx ends and calls Sweep every Interval.
func (l *Lifecycle) Run(ctx context.Context) { l.life().Run(ctx) }

// Sweep ends every pending row whose deadline is not after the clock's now.
func (l *Lifecycle) Sweep(ctx context.Context) (completed int, err error) {
	return l.life().Sweep(ctx)
}

// FlushAll ends EVERY pending row now, deadline or not (the SIGTERM drain).
func (l *Lifecycle) FlushAll(ctx context.Context) (completed int, err error) {
	return l.life().FlushAll(ctx)
}

// Recover is the start-up step: it ends the rows whose deadline has passed and
// re-queues the awaited groups of the others.
func (l *Lifecycle) Recover(ctx context.Context) (queued, completed int, err error) {
	return l.life().Recover(ctx)
}

// Stats reports the stored pending count and the last hour's timeouts and
// failures.
func (l *Lifecycle) Stats(ctx context.Context) (Stats, error) { return l.life().Stats(ctx) }
