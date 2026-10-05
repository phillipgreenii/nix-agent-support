package collect

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/analyze"
	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

// Pass names, also the pass label values.
const (
	PassMain       = "main"
	PassThroughput = "throughput"
)

// baseReasons are the failure reasons any bd-driven pass can produce.
func baseReasons() []failure.Reason {
	return []failure.Reason{
		failure.StaleIssuesJSONL, failure.Timeout, failure.BDError, failure.SchemaSkew, failure.ParseError,
	}
}

// MainPass is the periodic content snapshot: four bd spawns per database in
// steady state (list, ready, blocked, count), plus one extra spawn per
// spawn-only queue.
type MainPass struct {
	queues   []queue.Queue
	labelCap int

	mu       sync.Mutex
	statuses map[string][]string // db -> stored status names
}

// NewMainPass builds the main pass.
func NewMainPass(queues []queue.Queue, labelCap int) *MainPass {
	return &MainPass{queues: queues, labelCap: labelCap, statuses: map[string][]string{}}
}

// Name implements Pass.
func (p *MainPass) Name() string { return PassMain }

// Reasons implements Pass.
func (p *MainPass) Reasons() []failure.Reason { return baseReasons() }

// Init fetches each database's stored-status set once at start-up. Failures
// are ignored here: Collect refetches when the cache is empty, so a database
// that was unreachable at start recovers on its first good cycle.
func (p *MainPass) Init(ctx context.Context, dbs []DB) {
	for _, db := range dbs {
		if names, err := db.Adapter.Statuses(ctx); err == nil {
			p.setStatuses(db.Name, names)
		}
	}
}

func (p *MainPass) getStatuses(db string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.statuses[db]...)
}

func (p *MainPass) setStatuses(db string, names []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses[db] = append([]string{}, names...)
}

// Collect implements Pass.
func (p *MainPass) Collect(ctx context.Context, env PassEnv) (metrics.Emitter, error) {
	ad := env.DB.Adapter
	list, err := ad.List(ctx, bd.ListOpts{})
	if err != nil {
		return nil, err
	}
	ready, err := ad.Ready(ctx, nil)
	if err != nil {
		return nil, err
	}
	blocked, err := ad.Blocked(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := ad.CountByStatus(ctx)
	if err != nil {
		return nil, err
	}

	statuses := p.getStatuses(env.DB.Name)
	if len(statuses) == 0 || len(analyze.UnknownStatuses(list, statuses)) > 0 {
		fetched, err := ad.Statuses(ctx)
		if err != nil {
			return nil, err
		}
		statuses = mergeNames(fetched, analyze.UnknownStatuses(list, fetched))
		p.setStatuses(env.DB.Name, statuses)
	}

	queueBeads := map[string][]bd.Bead{}
	for _, q := range p.queues {
		if q.Class != queue.SpawnOnly {
			continue
		}
		got, err := ad.Ready(ctx, q.Args)
		if err != nil {
			return nil, err
		}
		queueBeads[q.Name] = got
	}

	return analyze.Summarize(analyze.Input{
		Beads:       list,
		Ready:       ready,
		Blocked:     blocked,
		ClosedCount: counts["closed"],
		Statuses:    statuses,
		Queues:      p.queues,
		QueueBeads:  queueBeads,
		LabelCap:    p.labelCap,
		Now:         env.Now,
	}), nil
}

func mergeNames(a, b []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, n := range append(append([]string{}, a...), b...) {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ThroughputPass counts beads created and closed in the trailing 24 hours with
// two bd spawns per database.
type ThroughputPass struct{}

// Name implements Pass.
func (ThroughputPass) Name() string { return PassThroughput }

// Reasons implements Pass.
func (ThroughputPass) Reasons() []failure.Reason { return baseReasons() }

// Window is the throughput look-back.
const Window = 24 * time.Hour

// Collect implements Pass. --all is required: without it the default
// not-closed filter would hide every closed bead.
func (ThroughputPass) Collect(ctx context.Context, env PassEnv) (metrics.Emitter, error) {
	since := env.Now.Add(-Window)
	created, err := env.DB.Adapter.List(ctx, bd.ListOpts{All: true, CreatedAfter: since})
	if err != nil {
		return nil, err
	}
	closed, err := env.DB.Adapter.List(ctx, bd.ListOpts{All: true, ClosedAfter: since})
	if err != nil {
		return nil, err
	}
	return analyze.Throughput{Created: len(created), Closed: len(closed)}, nil
}
