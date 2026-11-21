package settle

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultHoldLimit is the hold limit used when a query's own is unset.
const DefaultHoldLimit = 24 * time.Hour

// Runner runs one query's settle invocation for a lane and token. A non-nil
// error is a failed report (non-zero exit or timeout).
type Runner func(ctx context.Context, query, lane, token string) error

// Settler evaluates lane frontiers and runs the settle invocation. At most one
// invocation runs per lane at a time; frontier advances during a run are
// coalesced into one follow-up report carrying the newest frontier (the loop
// recomputes the frontier after each run). A failed report is counted, logged,
// never touches an event, and is retried at the next evaluation.
type Settler struct {
	ctx    context.Context
	ledger *Ledger
	run    Runner
	onFail func(query string)

	// holdLimit gives a query's ack_hold_limit (nil: the default), now the clock
	// (nil: time.Now) and onAbandon is told each abandoned event (may be nil).
	holdLimit func(query string) time.Duration
	now       func() time.Time
	onAbandon func(query, eventType string)

	mu    sync.Mutex
	lanes map[string]*laneRun
	wg    sync.WaitGroup
}

type laneRun struct {
	running  bool
	reported string // last token reported successfully by this process
}

// NewSettler builds a Settler over ledger. onFail (may be nil) is called once
// per failed settle invocation with the query name.
func NewSettler(ctx context.Context, ledger *Ledger, run Runner, onFail func(query string)) *Settler {
	return &Settler{ctx: ctx, ledger: ledger, run: run, onFail: onFail, lanes: map[string]*laneRun{}}
}

// ConfigureHold sets how the hold limit is applied: limit gives a query's
// ack_hold_limit (nil or non-positive: DefaultHoldLimit), now the clock (nil:
// time.Now) and onAbandon is called once per abandoned event (may be nil). Call
// it before the Settler is shared.
func (s *Settler) ConfigureHold(limit func(query string) time.Duration, now func() time.Time, onAbandon func(query, eventType string)) {
	s.holdLimit, s.now, s.onAbandon = limit, now, onAbandon
}

func (s *Settler) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Settler) limitFor(query string) time.Duration {
	if s.holdLimit != nil {
		if d := s.holdLimit(query); d > 0 {
			return d
		}
	}
	return DefaultHoldLimit
}

// FrontierAges reports each lane's age of its oldest unsettled event (the
// frontier-age gauge), computed at scrape time.
func (s *Settler) FrontierAges() []LaneAge { return s.ledger.Ages(s.clock()) }

// Ledger returns the ledger the Settler evaluates.
func (s *Settler) Ledger() *Ledger { return s.ledger }

// Evaluate re-evaluates every lane of query and starts a report for each lane
// whose frontier is ahead of what this process last reported. It never blocks
// on the settle invocation.
func (s *Settler) Evaluate(query string) {
	for _, l := range s.ledger.Lanes(query) {
		s.evaluateLane(query, l)
	}
}

// EvaluateAll evaluates every lane of every query: the restart path, which
// rebuilds each frontier from the durable ledger and queue and re-sends the
// report (it is idempotent).
func (s *Settler) EvaluateAll() {
	for _, q := range s.ledger.Queries() {
		s.Evaluate(q)
	}
}

// OnEventSettled records that event id is settled and re-evaluates the lanes:
// the queue's accept hook. A re-evaluation is only needed when a lane listed id.
func (s *Settler) OnEventSettled(id string) {
	changed, err := s.ledger.MarkSettled(id)
	if err != nil {
		slog.Error("settle: persisting the ledger failed", "eventId", id, "err", err)
	}
	if changed {
		s.EvaluateAll()
	}
}

// Restart is the startup path: it rebuilds settlement from the durable queue
// (isSettled reports whether the queue shows an id as settled, which covers an
// accept the queue recorded but the ledger did not, the crash between
// acceptance and the ledger write) and then re-evaluates every lane, re-sending
// each frontier. It MUST run after every listener is registered.
func (s *Settler) Restart(isSettled func(id string) bool) {
	for _, id := range s.ledger.UnsettledIDs() {
		if isSettled(id) {
			if _, err := s.ledger.MarkSettled(id); err != nil {
				slog.Error("settle: persisting the ledger failed", "eventId", id, "err", err)
			}
		}
	}
	s.EvaluateAll()
}

// Wait blocks until every running settle invocation has finished (tests, and an
// orderly shutdown).
func (s *Settler) Wait() { s.wg.Wait() }

// abandonLane applies the hold limit to one lane: each unsettled event first seen
// longer ago than the limit is abandoned (counted, its id logged) and treated as
// settled for the frontier, so the frontier moves to the next unsettled event.
func (s *Settler) abandonLane(query, lane string) {
	gone, err := s.ledger.Abandon(query, lane, s.limitFor(query), s.clock())
	if err != nil {
		slog.Error("settle: persisting the ledger failed", "query", query, "lane", lane, "err", err)
	}
	for _, a := range gone {
		slog.Warn("settle: abandoned an event past the hold limit; its lane frontier moves past it",
			"query", query, "lane", lane, "eventId", a.ID, "type", a.Type, "holdLimit", s.limitFor(query).String())
		if s.onAbandon != nil {
			s.onAbandon(query, a.Type)
		}
	}
}

func (s *Settler) evaluateLane(query, lane string) {
	s.abandonLane(query, lane)
	k := laneKey(query, lane)
	s.mu.Lock()
	lr := s.lanes[k]
	if lr == nil {
		lr = &laneRun{}
		s.lanes[k] = lr
	}
	if lr.running {
		s.mu.Unlock()
		return // the running loop recomputes the frontier when it finishes
	}
	token, ok := s.ledger.Frontier(query, lane)
	if !ok || token == lr.reported {
		s.mu.Unlock()
		return
	}
	lr.running = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.loop(query, lane, lr, token)
}

func (s *Settler) loop(query, lane string, lr *laneRun, token string) {
	defer s.wg.Done()
	for {
		err := s.run(s.ctx, query, lane, token)
		s.mu.Lock()
		if err != nil {
			lr.running = false
			s.mu.Unlock()
			slog.Warn("settle: report failed; retrying at the next frontier evaluation",
				"query", query, "lane", lane, "through", token, "err", err)
			if s.onFail != nil {
				s.onFail(query)
			}
			return
		}
		lr.reported = token
		next, ok := s.ledger.Frontier(query, lane)
		if !ok || next == lr.reported || s.ctx.Err() != nil {
			lr.running = false
			s.mu.Unlock()
			return
		}
		token = next
		s.mu.Unlock()
	}
}
