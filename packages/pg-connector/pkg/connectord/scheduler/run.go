package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// batch is one batch the batcher assembled and removed from the queue.
type batch struct {
	exec    Executor
	items   []Item
	flights []*Flight
	state   *BatchState
}

// held reports whether the governor's last refusal keeps e back at now.
func (s *Scheduler) held(e *entry, exec Executor, now time.Time) bool {
	if exec == nil {
		return false
	}
	b := exec.Spend(e.Class, 1).Bucket
	if e.Class == governor.ClassInteractive || b == "" {
		return false
	}
	h := s.holdBG
	if e.Class == governor.ClassInvalidated {
		h = s.holdFG
	}
	return now.Before(h[b])
}

// nextBatch assembles the next batch at now, or returns nil when nothing can go.
// A batch the Gate refuses is not returned: it sets the hold and the search goes
// on. The caller holds s.mu.
func (s *Scheduler) nextBatch(now time.Time) *batch {
	for {
		var due []*entry
		for k, e := range s.queue {
			exec := s.execs[e.Kind]
			if exec == nil || s.running[k] != nil || !due2(e, now) || s.held(e, exec, now) {
				continue
			}
			due = append(due, e)
		}
		if len(due) == 0 {
			return nil
		}
		s.sortEntries(due, now)
		lead := due[0]
		exec := s.execs[lead.Kind]
		limit := max(exec.MaxBatch(), 1)

		picked := []*entry{lead}
		roles := []Role{RoleDue}
		for _, e := range due[1:] {
			if len(picked) >= limit {
				break
			}
			if e.Kind == lead.Kind {
				picked = append(picked, e)
				roles = append(roles, RoleDue)
			}
		}
		if len(picked) < limit && s.opt.FillHorizon > 0 {
			var fill []*entry
			for k, e := range s.queue {
				if e.Kind != lead.Kind || s.running[k] != nil || due2(e, now) ||
					e.Due.After(now.Add(s.opt.FillHorizon)) {
					continue
				}
				fill = append(fill, e)
			}
			slices.SortFunc(fill, func(a, b *entry) int {
				switch {
				case a.Due.Before(b.Due):
					return -1
				case b.Due.Before(a.Due):
					return 1
				case a.seq < b.seq:
					return -1
				case a.seq > b.seq:
					return 1
				}
				return 0
			})
			for _, e := range fill {
				if len(picked) >= limit {
					break
				}
				picked = append(picked, e)
				roles = append(roles, RoleFill)
			}
		}

		leadClass := s.effClass(lead, now)
		if lead.Class == governor.ClassInteractive || lead.Class == governor.ClassInvalidated {
			leadClass = lead.Class
		}
		req := exec.Spend(leadClass, len(picked))
		if s.opt.Gate != nil && req.Bucket != "" {
			if err := s.opt.Gate.Check(req); err != nil {
				s.refused(lead, req, err, now)
				continue
			}
		}

		b := &batch{exec: exec}
		bs := &BatchState{Kind: lead.Kind, At: now, Running: true}
		for i, e := range picked {
			delete(s.queue, e.key())
			f := e.flight
			if f == nil {
				f = newFlight()
			}
			f.start(now)
			s.running[e.key()] = f
			b.items = append(b.items, Item{Task: e.Task, Role: roles[i]})
			b.flights = append(b.flights, f)
			bs.Items = append(bs.Items, BatchItem{Entity: e.Entity, Group: e.Group, Class: e.Class, Role: roles[i]})
		}
		b.state = bs
		s.history = append(s.history, bs)
		return b
	}
}

// due2 is due for an entry (a separate name keeps the method set small).
func due2(e *entry, now time.Time) bool { return due(e.Task, now) }

// refused handles a Gate refusal of a batch led by lead: interactive tasks are
// completed with it (their callers serve stale), and the refused bucket's class
// group is held until the refusal's RetryAt.
func (s *Scheduler) refused(lead *entry, req governor.Request, err error, now time.Time) {
	retry := s.retryAt(err, now)
	switch lead.Class {
	case governor.ClassInteractive, governor.ClassInvalidated:
		s.holdFG[req.Bucket] = retry
		s.holdBG[req.Bucket] = retry
		if lead.Class == governor.ClassInteractive {
			// Every waiting caller is refused, not just the lead's.
			// The refreshes themselves stay queued as background work.
			for _, e := range s.queue {
				if e.Class == governor.ClassInteractive && s.execs[e.Kind] != nil &&
					s.execs[e.Kind].Spend(e.Class, 1).Bucket == req.Bucket {
					if e.flight != nil {
						e.flight.complete(Result{Err: err})
					}
					e.Class, e.flight, e.Due = governor.ClassKeepAlive, nil, now
				}
			}
		}
	default:
		s.holdBG[req.Bucket] = retry
	}
}

func (s *Scheduler) retryAt(err error, now time.Time) time.Time {
	var r *governor.Refusal
	if errors.As(err, &r) && r.RetryAt.After(now) {
		return r.RetryAt
	}
	return now.Add(s.opt.RetryDelay)
}

// Step dispatches every batch that can go now, up to the free workers, and
// returns how many it started. ctx is the context the executors run under.
func (s *Scheduler) Step(ctx context.Context) int {
	started := 0
	for {
		s.mu.Lock()
		if s.active >= s.opt.Workers {
			s.mu.Unlock()
			return started
		}
		b := s.nextBatch(s.clock.Now())
		if b == nil {
			s.mu.Unlock()
			return started
		}
		s.active++
		s.wg.Add(1)
		s.mu.Unlock()
		started++
		go s.run(ctx, b)
	}
}

func (s *Scheduler) run(ctx context.Context, b *batch) {
	defer s.wg.Done()
	res := s.execute(ctx, b)
	s.finish(b, res)
}

func (s *Scheduler) execute(ctx context.Context, b *batch) (res []Result) {
	defer func() {
		if r := recover(); r != nil {
			res = make([]Result, len(b.items))
			for i := range res {
				res[i] = Result{Err: fmt.Errorf("scheduler: executor panicked: %v", r)}
			}
		}
	}()
	res = b.exec.Execute(ctx, b.items)
	if len(res) != len(b.items) {
		err := fmt.Errorf("scheduler: executor returned %d results for %d items", len(res), len(b.items))
		res = make([]Result, len(b.items))
		for i := range res {
			res[i] = Result{Err: err}
		}
	}
	return res
}

// finish books a finished batch: it frees the keys, requeues what failed and
// completes the flights.
func (s *Scheduler) finish(b *batch, res []Result) {
	s.mu.Lock()
	now := s.clock.Now()
	s.active--
	b.state.Running = false
	if over := len(s.history) - s.opt.History; over > 0 {
		// Drop the oldest finished batches; a running one is never dropped.
		kept := s.history[:0]
		dropped := 0
		for _, h := range s.history {
			if dropped < over && !h.Running {
				dropped++
				continue
			}
			kept = append(kept, h)
		}
		s.history = kept
	}
	for i, it := range b.items {
		k := it.Task.key()
		delete(s.running, k)
		r := res[i]
		var refusal *governor.Refusal
		switch {
		case r.Err != nil && errors.As(r.Err, &refusal):
			bucket := b.exec.Spend(it.Task.Class, 1).Bucket
			retry := s.retryAt(r.Err, now)
			if bucket != "" {
				if it.Task.Class <= governor.ClassInvalidated {
					s.holdFG[bucket] = retry
				}
				s.holdBG[bucket] = retry
			}
			s.requeue(it.Task, now)
		case r.Err != nil:
			s.requeue(it.Task, now.Add(s.opt.RetryDelay))
		}
		b.flights[i].complete(r)
	}
	s.mu.Unlock()
	s.kick()
}

// requeue puts a task that did not finish back, at the background class when it
// was a waiting caller's. A newer queued task for the same pair wins.
func (s *Scheduler) requeue(t Task, due time.Time) {
	if _, ok := s.queue[t.key()]; ok {
		return
	}
	if t.Class == governor.ClassInteractive {
		t.Class = governor.ClassKeepAlive
	}
	t.Due = due
	s.seq++
	s.queue[t.key()] = &entry{Task: t, seq: s.seq}
}

// Wait blocks until no batch is in flight.
func (s *Scheduler) Wait() { s.wg.Wait() }

// Drain runs Step and Wait until a Step starts nothing: every task that can go
// at the clock's current time has run (and everything it submitted meanwhile).
// It is for tests with a fake clock and for a shutdown flush.
func (s *Scheduler) Drain(ctx context.Context) {
	for {
		n := s.Step(ctx)
		s.Wait()
		if n == 0 {
			return
		}
	}
}

// nextWake is how long Run may sleep: until the next not-yet-due task comes due,
// the next hold ends, or PollInterval.
func (s *Scheduler) nextWake(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	wait := s.opt.PollInterval
	for _, e := range s.queue {
		if !e.Due.IsZero() && e.Due.After(now) {
			wait = min(wait, e.Due.Sub(now))
		}
	}
	for _, h := range []map[governor.Bucket]time.Time{s.holdFG, s.holdBG} {
		for _, t := range h {
			if t.After(now) {
				wait = min(wait, t.Sub(now))
			}
		}
	}
	return max(wait, time.Millisecond)
}

// Run dispatches until ctx is done, then waits for the batches in flight. It is
// the daemon's loop; the executors run under ctx.
func (s *Scheduler) Run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		s.Step(ctx)
		timer.Reset(s.nextWake(s.clock.Now()))
		select {
		case <-ctx.Done():
			s.Wait()
			return
		case <-s.wake:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}
