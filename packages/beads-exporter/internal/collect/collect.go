// Package collect runs collection passes against every configured database and
// publishes the results as immutable snapshots.
//
// A Pass is the unit of collection. Each pass keeps its own last-success
// timestamp, its own error counters and its own domain series, and a failure
// of one pass for one database never touches another pass or another database.
// A new pass (for example stranded-claim detection) plugs in by implementing
// Pass and registering its metric families; nothing here needs restructuring.
package collect

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/analyze"
	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

// Clock supplies the current time.
type Clock interface{ Now() time.Time }

// DB is one configured database and its adapter.
type DB struct {
	Name    string
	Adapter bd.Adapter
}

// PassEnv is the input of one pass run for one database.
type PassEnv struct {
	DB  DB
	Now time.Time
}

// Pass is one collection pass.
type Pass interface {
	// Name is the pass label value: main, throughput or stranded.
	Name() string
	// Reasons lists the failure reasons this pass can produce; the error
	// counter is zero-filled over exactly these.
	Reasons() []failure.Reason
	// Collect gathers the pass's domain data. On error the pass's series for
	// the database are dropped.
	Collect(ctx context.Context, env PassEnv) (metrics.Emitter, error)
}

// Meta is the bookkeeping of one pass for one database.
type Meta struct {
	Attempted   bool
	OK          bool
	LastSuccess time.Time
	Duration    time.Duration
	Errors      map[failure.Reason]uint64
	Reasons     []failure.Reason
}

// PassSnapshot is a pass's published state for one database.
type PassSnapshot struct {
	Meta Meta
	// Data is nil when the last attempt failed: the domain series are
	// dropped, never re-served stale.
	Data metrics.Emitter
}

// DBSnapshot is one database's published state.
type DBSnapshot struct {
	Name   string
	Passes map[string]PassSnapshot
}

// Snapshot is an immutable view of every database. It is never mutated after
// publication.
type Snapshot struct {
	DBs []DBSnapshot
}

// Samples flattens the snapshot into samples: each pass's domain series plus
// the exporter health series.
func (s *Snapshot) Samples() []metrics.Sample {
	if s == nil {
		return nil
	}
	var out []metrics.Sample
	for _, d := range s.DBs {
		names := make([]string, 0, len(d.Passes))
		for n := range d.Passes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			ps := d.Passes[n]
			if ps.Data != nil {
				out = append(out, ps.Data.Samples(d.Name)...)
			}
			out = append(out, metaSamples(d.Name, n, ps.Meta)...)
		}
	}
	return out
}

func metaSamples(db, pass string, m Meta) []metrics.Sample {
	if !m.Attempted {
		return nil
	}
	lab := func(kv ...string) []metrics.Label {
		return append([]metrics.Label{{Name: "db", Value: db}}, metrics.SeriesLabel(kv...)...)
	}
	var out []metrics.Sample
	if pass == "main" {
		up := 0.0
		if m.OK {
			up = 1
		}
		out = append(out, metrics.Sample{Family: metrics.FamExporterUp, Labels: lab(), Value: up})
	}
	if !m.LastSuccess.IsZero() {
		out = append(out, metrics.Sample{Family: metrics.FamPassLastSuccess, Labels: lab("pass", pass), Value: analyze.Seconds(m.LastSuccess)})
	}
	out = append(out, metrics.Sample{Family: metrics.FamCollectDuration, Labels: lab("pass", pass), Value: m.Duration.Seconds()})
	for _, r := range m.Reasons {
		out = append(out, metrics.Sample{Family: metrics.FamCollectErrors, Labels: lab("pass", pass, "reason", string(r)), Value: float64(m.Errors[r])})
	}
	return out
}

type passState struct {
	meta Meta
	data metrics.Emitter
}

// Collector owns the pass state and the published snapshot.
type Collector struct {
	clock  Clock
	dbs    []DB
	passes map[string]Pass
	log    *slog.Logger

	// runMu serialises pass runs so two passes never drive the same embedded
	// store at once.
	runMu sync.Mutex

	mu    sync.Mutex
	state map[string]map[string]*passState // db -> pass -> state

	snap atomic.Pointer[Snapshot]
}

// New builds a Collector over dbs and passes. Databases are collected in the
// order given.
func New(clock Clock, dbs []DB, passes []Pass, log *slog.Logger) *Collector {
	c := &Collector{
		clock:  clock,
		dbs:    dbs,
		passes: map[string]Pass{},
		log:    log,
		state:  map[string]map[string]*passState{},
	}
	for _, p := range passes {
		c.passes[p.Name()] = p
	}
	for _, d := range dbs {
		c.state[d.Name] = map[string]*passState{}
	}
	c.snap.Store(&Snapshot{})
	return c
}

// Snapshot returns the last published snapshot. It never blocks on a running
// collection cycle.
func (c *Collector) Snapshot() *Snapshot { return c.snap.Load() }

// RunPass runs the named pass against every database in order, publishing each
// database's result as soon as it finishes so a slow or failing database never
// delays another database's data beyond its own run.
func (c *Collector) RunPass(ctx context.Context, name string) {
	p, ok := c.passes[name]
	if !ok {
		c.log.Error("unknown pass", "pass", name)
		return
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	for _, db := range c.dbs {
		if ctx.Err() != nil {
			return
		}
		start := c.clock.Now()
		data, err := p.Collect(ctx, PassEnv{DB: db, Now: start})
		if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
			return // shutting down: record nothing for an interrupted run
		}
		end := c.clock.Now()
		c.record(p, db.Name, data, err, end.Sub(start), end)
	}
}

func (c *Collector) record(p Pass, db string, data metrics.Emitter, err error, dur time.Duration, end time.Time) {
	c.mu.Lock()
	ps := c.state[db][p.Name()]
	if ps == nil {
		ps = &passState{meta: Meta{Errors: map[failure.Reason]uint64{}, Reasons: p.Reasons()}}
		c.state[db][p.Name()] = ps
	}
	ps.meta.Attempted = true
	ps.meta.Duration = dur
	if err != nil {
		reason := failure.ReasonOf(err)
		ps.meta.OK = false
		ps.meta.Errors[reason]++
		ps.data = nil
		c.log.Warn("collection failed", "db", db, "pass", p.Name(), "reason", string(reason), "error", err.Error())
	} else {
		ps.meta.OK = true
		ps.meta.LastSuccess = end
		ps.data = data
	}
	c.publishLocked(db)
	c.mu.Unlock()
}

// publishLocked rebuilds the snapshot with db's current state swapped in. The
// caller holds c.mu.
func (c *Collector) publishLocked(db string) {
	old := c.snap.Load()
	next := &Snapshot{}
	replaced := false
	for _, d := range old.DBs {
		if d.Name == db {
			next.DBs = append(next.DBs, c.freezeLocked(db))
			replaced = true
		} else {
			next.DBs = append(next.DBs, d)
		}
	}
	if !replaced {
		next.DBs = append(next.DBs, c.freezeLocked(db))
	}
	sort.Slice(next.DBs, func(i, j int) bool { return next.DBs[i].Name < next.DBs[j].Name })
	c.snap.Store(next)
}

func (c *Collector) freezeLocked(db string) DBSnapshot {
	d := DBSnapshot{Name: db, Passes: map[string]PassSnapshot{}}
	for name, ps := range c.state[db] {
		m := ps.meta
		m.Errors = make(map[failure.Reason]uint64, len(ps.meta.Errors))
		for r, n := range ps.meta.Errors {
			m.Errors[r] = n
		}
		m.Reasons = append([]failure.Reason{}, ps.meta.Reasons...)
		d.Passes[name] = PassSnapshot{Meta: m, Data: ps.data}
	}
	return d
}
