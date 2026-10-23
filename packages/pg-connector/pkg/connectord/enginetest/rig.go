package enginetest

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Start is the manual clock's first reading.
var Start = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

const (
	// Day is one day, for retention and staleness settings.
	Day = 24 * time.Hour
	// bucket is the governor bucket of the runner's query task; the rig has no
	// governor, so the scheduler admits every batch.
	bucket governor.Bucket = "invented_bucket"
	// settleTimeout and bodyCap configure the engine.
	settleTimeout = 30 * time.Second
	bodyCap       = 256
)

// RigOption configures NewRig.
type RigOption func(*rigConfig)

type rigConfig struct {
	reg       []Option
	retention time.Duration
	stale     time.Duration
}

// WithRegistration passes options to NewRegistration.
func WithRegistration(opts ...Option) RigOption {
	return func(c *rigConfig) { c.reg = append(c.reg, opts...) }
}

// WithFeedTimes sets the feed's retention and the age after which an idle
// consumer is stale (default 14 and 7 days).
func WithFeedTimes(retention, consumerStaleAfter time.Duration) RigOption {
	return func(c *rigConfig) { c.retention, c.stale = retention, consumerStaleAfter }
}

// Rig is the shared engine, query runner and feed over one registration of
// invented names, one real store, a manual clock and the scripted fakes. The
// scheduler loop runs for the life of the test, so Runner.Fetch completes.
type Rig struct {
	T   *testing.T
	Reg Registration

	Store     *store.Store
	Clock     *governor.ManualClock
	Scheduler *scheduler.Scheduler
	Engine    *engine.Engine
	Runner    *query.Runner
	Feed      *feed.Feed
	// Queries is the feed's view of the runner and the store.
	Queries *Queries

	Classifier *Classifier
	Lister     *Lister
	Aliases    *Aliases
	Settings   *Settings
	Tracker    *Tracker
	Confirmer  *Confirmer
	Auth       *Auth
}

// NewRig builds the rig; the store and the scheduler loop are closed by the
// test's cleanup.
func NewRig(t *testing.T, opts ...RigOption) *Rig {
	t.Helper()
	cfg := rigConfig{retention: 14 * Day, stale: 7 * Day}
	for _, o := range opts {
		o(&cfg)
	}
	reg := NewRegistration(cfg.reg...)

	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(Start)

	r := &Rig{
		T: t, Reg: reg, Store: st, Clock: clk,
		Classifier: NewClassifier(reg), Lister: NewLister(), Aliases: NewAliases(reg.Spec.FoldRef),
		Settings: NewSettings(), Tracker: &Tracker{}, Confirmer: NewConfirmer(), Auth: &Auth{},
	}
	r.Scheduler = scheduler.New(scheduler.Options{Clock: clk, Workers: 2, PollInterval: 5 * time.Millisecond, RetryDelay: time.Minute})
	if r.Engine, err = engine.New(engine.Config{Spec: reg.Spec, Classifier: r.Classifier, Clock: clk, SettleTimeout: settleTimeout, BodyCap: bodyCap}); err != nil {
		t.Fatal(err)
	}
	if r.Runner, err = query.New(query.Config{
		Spec: reg.Spec, StateFieldPrefix: "state_of:", Bucket: bucket,
		Settings: r.Settings, Store: st, Aliases: r.Aliases, Tracker: r.Tracker, Lister: r.Lister,
		Scheduler: r.Scheduler, Auth: r.Auth, Clock: clk, Changes: r.Engine, Confirmer: r.Confirmer,
	}); err != nil {
		t.Fatal(err)
	}
	r.Queries = &Queries{settings: r.Settings, runner: r.Runner, st: st, clk: clk}
	if r.Feed, err = feed.New(feed.Config{Spec: reg.Spec, Store: st, Queries: r.Queries, Clock: clk, Retention: cfg.retention, ConsumerStaleAfter: cfg.stale}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Scheduler.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// Queries is the feed.Queries over the rig's settings, runner and store: a query
// is known when the settings define it, its members are the references of its
// stored row, and Run re-runs it only when its stored result is stale. The
// watched references are the members of the queries the settings mark watched.
type Queries struct {
	settings *Settings
	runner   *query.Runner
	st       *store.Store
	clk      governor.Clock
}

var _ feed.Queries = (*Queries)(nil)

// Known implements feed.Queries.
func (q *Queries) Known(name string) bool { _, ok := q.settings.Expr(name); return ok }

// Names implements feed.Queries.
func (q *Queries) Names() []string { return q.settings.Names() }

// Watched implements feed.Queries.
func (q *Queries) Watched(name string) bool { return q.settings.Watched(name) }

// Members implements feed.Queries.
func (q *Queries) Members(ctx context.Context, name string) (feed.Members, error) {
	row, ok, err := q.st.GetQuery(ctx, query.Key{Name: name}.String())
	if err != nil || !ok {
		return feed.Members{}, err
	}
	return feed.Members{Refs: row.IDs, Baseline: !row.BaselineAt.IsZero()}, nil
}

// Run implements feed.Queries.
func (q *Queries) Run(ctx context.Context, name string) error {
	key := query.Key{Name: name}
	row, ok, err := q.runner.Row(ctx, key)
	if err != nil {
		return err
	}
	if ok && !row.FetchedAt.IsZero() && row.FreshUntil.After(q.clk.Now()) {
		return nil
	}
	_, err = q.runner.Fetch(ctx, key)
	return err
}

// WatchedRefs implements feed.Queries.
func (q *Queries) WatchedRefs(ctx context.Context) ([]string, error) {
	var out []string
	for _, name := range q.settings.Names() {
		if !q.settings.Watched(name) {
			continue
		}
		m, err := q.Members(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, ref := range m.Refs {
			if !slices.Contains(out, ref) {
				out = append(out, ref)
			}
		}
	}
	return out, nil
}

func (r *Rig) must(err error) {
	r.T.Helper()
	if err != nil {
		r.T.Fatal(err)
	}
}

// InTx runs fn in one store transaction.
func (r *Rig) InTx(fn func(ctx context.Context, tx *store.Tx) error) error {
	return r.Store.InTx(context.Background(), func(tx *store.Tx) error { return fn(context.Background(), tx) })
}

// Seed stores the entity (version 1), its primary content c and the alias of its
// reference, with no change recorded: the first sighting is a baseline.
func (r *Rig) Seed(id string, c Content) {
	r.T.Helper()
	r.must(r.InTx(func(ctx context.Context, tx *store.Tx) error {
		if err := tx.PutEntity(ctx, store.EntityRow{Kind: r.Reg.Entity, ID: id, Version: 1}); err != nil {
			return err
		}
		row := store.GroupRow{
			Kind: r.Reg.Entity, ID: id, Group: r.Reg.Primary, Content: c.Encode(), Generation: 1,
			FetchedAt: Start, FreshUntil: Start.Add(time.Hour),
		}
		row.SetStamps(c.Stamp, "")
		return tx.PutGroup(ctx, row)
	}))
	r.must(r.Aliases.Put(context.Background(), id, c.Ref))
}

// SeedGroup stores a non-primary group as a previous fetch left it.
func (r *Rig) SeedGroup(id, group, content string) {
	r.T.Helper()
	r.must(r.InTx(func(ctx context.Context, tx *store.Tx) error {
		return tx.PutGroup(ctx, store.GroupRow{
			Kind: r.Reg.Entity, ID: id, Group: group, Content: []byte(content), Fingerprint: "seed", Generation: 1,
			FetchedAt: Start, FreshUntil: Start.Add(time.Hour),
		})
	}))
}

// ChangePrimary stores the entity's primary content after mutate and runs the
// engine's primary detection in the same transaction, the way the persister's
// seam does. purpose says what the refresh was admitted as.
func (r *Rig) ChangePrimary(id string, purpose governor.Purpose, mutate func(*Content)) error {
	r.T.Helper()
	return r.InTx(func(ctx context.Context, tx *store.Tx) error {
		row, ok, err := tx.GetGroup(ctx, r.Reg.Entity, id, r.Reg.Primary)
		if err != nil || !ok {
			r.T.Fatalf("stored primary of %s: ok=%v err=%v", id, ok, err)
		}
		c, err := decode(row.Content)
		if err != nil {
			return err
		}
		prev := row.Content
		mutate(&c)
		row.Content, row.Generation = c.Encode(), row.Generation+1
		row.SetStamps(c.Stamp, "")
		if err := tx.PutGroup(ctx, row); err != nil {
			return err
		}
		return r.Engine.DetectPrimary(ctx, tx, engine.PrimaryChange{ID: id, Prev: prev, Next: row.Content, Info: engine.RefreshInfo{Purpose: purpose}})
	})
}

// RefreshGroup stores a refreshed non-primary group and runs the engine's group
// detection in the same transaction; started is when the fetch began.
func (r *Rig) RefreshGroup(id, group, content string, started time.Time) error {
	r.T.Helper()
	return r.InTx(func(ctx context.Context, tx *store.Tx) error {
		old, had, err := tx.GetGroup(ctx, r.Reg.Entity, id, group)
		if err != nil {
			return err
		}
		var prev *store.GroupRow
		if had {
			prev = &old
		}
		now := r.Clock.Now()
		next := store.GroupRow{
			Kind: r.Reg.Entity, ID: id, Group: group, Content: []byte(content), Fingerprint: "fp",
			FetchedAt: now, FreshUntil: now.Add(time.Hour), Generation: old.Generation + 1,
		}
		if err := tx.PutGroup(ctx, next); err != nil {
			return err
		}
		return r.Engine.DetectGroup(ctx, tx, prev, next, engine.RefreshInfo{Started: started, Purpose: governor.PurposeRefresh})
	})
}

// RunQuery runs the configured query now (an interactive run, whatever its
// freshness) and returns its stored row.
func (r *Rig) RunQuery(name string) store.QueryRow {
	r.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, err := r.Runner.Fetch(ctx, query.Key{Name: name})
	r.must(err)
	return row
}

// Rows returns every change row in the log in ascending Seq.
func (r *Rig) Rows() []store.ChangeRow {
	r.T.Helper()
	rows, err := r.Store.ChangesAfter(context.Background(), 0, 0)
	r.must(err)
	return rows
}

// Poll polls the cursor (consumer, view, scope); an empty view is the default.
func (r *Rig) Poll(view, consumer, scope string) feed.PollResult {
	r.T.Helper()
	res, err := r.Feed.Poll(context.Background(), feed.PollRequest{View: view, Consumer: consumer, Scope: scope})
	r.must(err)
	return res
}

// Ack acknowledges the cursor up to and including nextSeq of a poll.
func (r *Rig) Ack(view, consumer, scope string, nextSeq int64) {
	r.T.Helper()
	_, err := r.Feed.Ack(context.Background(), feed.AckRequest{View: view, Consumer: consumer, Scope: scope, NextSeq: nextSeq})
	r.must(err)
}
