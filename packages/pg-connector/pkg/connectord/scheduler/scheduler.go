// Package scheduler is the generic refresh engine of a pg-connector daemon: one
// priority queue of (entity, group) tasks, a worker pool, single-flight per
// (entity, group), aging so a low class is not starved by a higher one, and a
// batcher that packs due tasks of one kind into one origin call, filling spare
// slots with the tasks closest to going stale.
//
// It knows nothing about an upstream. A connector registers one Executor per
// task kind; the Executor says how many tasks one call carries (MaxBatch), what
// the call costs the governor (Spend) and runs it (Execute). Submit adds work,
// SubmitInteractive adds work a caller waits on, Step (or Run) dispatches it.
//
// # Classes
//
// A task carries one of the governor's five priority classes (governor.Class):
// 1 interactive, 2 write-invalidated and refresh-requested, 3 due re-runs of
// configured queries and their overdue members, 4 overdue keep-alive entities
// (most recently accessed first), 5 fill. A task is DUE when its Due time has
// come (a zero Due is always due). Due tasks run in class order; a not-yet-due
// task is never a lead. ClassFill is a task that is only ever a rider: before
// its Due it fills a spare slot of a batch already going out, once due it is a
// class 4 task.
//
// # Batches
//
// A batch is the highest-priority due task (the lead), then the other due
// tasks of the same kind in priority order, then fill: tasks of that kind not
// yet due but due within FillHorizon, nearest first. Fill never displaces a due
// task and a batch never exceeds the executor's MaxBatch. A batch goes out as
// soon as it has a lead: nothing waits for a batch to fill.
//
// # Governor
//
// The scheduler asks the Gate before every batch (Check, no reservation: the
// executor's own call admits and settles). A refused batch stays queued and
// holds the refused bucket's class group until the refusal's RetryAt; a refused
// interactive task is completed with the refusal so its caller can serve stale.
// An executor whose own admission refuses reports the *governor.Refusal as the
// task's error and is treated the same way.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Clock is the scheduler's time source (governor.Clock: SystemClock in
// production, governor.ManualClock in tests).
type Clock = governor.Clock

// Gate is the governor as the scheduler uses it. *governor.Governor satisfies
// it.
type Gate interface {
	Check(governor.Request) error
}

// Role is a task's part in a batch.
type Role string

const (
	// RoleDue: the task was due when the batch went out.
	RoleDue Role = "due"
	// RoleFill: the task was not yet due; it filled a spare slot.
	RoleFill Role = "fill"
)

// ErrCanceled completes the flight of a task that was canceled.
var ErrCanceled = errors.New("scheduler: task canceled")

// Task is one refresh of one (entity, group).
type Task struct {
	// Kind selects the Executor; empty means Group.
	Kind string
	// Entity and Group identify the task: at most one is queued and at most one
	// runs per pair (single-flight).
	Entity string
	Group  string
	Class  governor.Class
	// Due is when the task becomes due (the group's fresh_until); zero is due
	// now.
	Due time.Time
	// Access is when the entity was last accessed; class 4 runs the most
	// recently accessed first.
	Access time.Time
	// Payload is opaque to the scheduler and handed to the Executor.
	Payload any
}

type key struct{ entity, group string }

func (t Task) key() key { return key{t.Entity, t.Group} }

func (t Task) validate() error {
	if t.Entity == "" {
		return errors.New("scheduler: task has no entity")
	}
	if t.Kind == "" && t.Group == "" {
		return fmt.Errorf("scheduler: task for %q has neither kind nor group", t.Entity)
	}
	if !t.Class.Valid() {
		return fmt.Errorf("scheduler: task %s/%s has invalid class %d", t.Entity, t.Group, int(t.Class))
	}
	return nil
}

// Item is one task of a batch with its role.
type Item struct {
	Task Task
	Role Role
}

// Result is what an Executor reports for one Item.
type Result struct {
	// Err is the failure. A *governor.Refusal (the executor's own admission
	// declined) holds the class group back until RetryAt; any other error
	// requeues the task after RetryDelay.
	Err error
	// Skipped says the task needed no work (the entity left the keep-alive
	// set, or the group was already fresh); it is dropped, not retried.
	Skipped bool
}

// Executor runs the tasks of one kind.
type Executor interface {
	// MaxBatch is the most tasks one Execute call carries; below 1 counts as 1.
	MaxBatch() int
	// Spend is the governor request one batch of n tasks led by class makes. An
	// empty Bucket means the batch is not metered.
	Spend(class governor.Class, n int) governor.Request
	// Execute runs the batch and returns one Result per Item, in order. ctx is
	// the scheduler's, not a caller's: a caller's deadline never cancels it.
	Execute(ctx context.Context, items []Item) []Result
}

// Options configure a Scheduler.
type Options struct {
	// Clock is the time source; nil means the system clock.
	Clock Clock
	// Gate is asked before every batch; nil admits everything.
	Gate Gate
	// Workers is the most batches in flight; below 1 means DefaultWorkers.
	Workers int
	// FillHorizon is how far past now a not-yet-due task may be and still fill a
	// spare slot; zero means no fill.
	FillHorizon time.Duration
	// AgeAfter is how long a class 4 task may be overdue before it competes as
	// class 3; zero means DefaultAgeAfter.
	AgeAfter time.Duration
	// RetryDelay is how long a failed task waits before it is retried; zero
	// means DefaultRetryDelay.
	RetryDelay time.Duration
	// PollInterval bounds how long Run sleeps between passes; zero means
	// DefaultPollInterval.
	PollInterval time.Duration
	// History is how many finished batches State keeps; zero means 32.
	History int
}

// Defaults of Options.
const (
	DefaultWorkers      = 4
	DefaultAgeAfter     = 5 * time.Minute
	DefaultRetryDelay   = 30 * time.Second
	DefaultPollInterval = time.Second
	defaultHistory      = 32
)

type entry struct {
	Task
	seq    uint64
	flight *Flight
}

// Scheduler is the queue, the worker pool and the batcher. It is safe for
// concurrent use.
type Scheduler struct {
	opt   Options
	clock Clock

	mu      sync.Mutex
	execs   map[string]Executor
	queue   map[key]*entry
	running map[key]*Flight
	seq     uint64
	active  int
	// hold maps a bucket and a foreground flag to the time before which the
	// class group makes no further attempt, set by a governor refusal.
	holdFG, holdBG map[governor.Bucket]time.Time
	history        []*BatchState

	wake chan struct{}
	wg   sync.WaitGroup
}

// New returns a Scheduler.
func New(opt Options) *Scheduler {
	if opt.Clock == nil {
		opt.Clock = governor.SystemClock{}
	}
	if opt.Workers < 1 {
		opt.Workers = DefaultWorkers
	}
	if opt.AgeAfter <= 0 {
		opt.AgeAfter = DefaultAgeAfter
	}
	if opt.RetryDelay <= 0 {
		opt.RetryDelay = DefaultRetryDelay
	}
	if opt.PollInterval <= 0 {
		opt.PollInterval = DefaultPollInterval
	}
	if opt.History <= 0 {
		opt.History = defaultHistory
	}
	return &Scheduler{
		opt: opt, clock: opt.Clock,
		execs: map[string]Executor{}, queue: map[key]*entry{}, running: map[key]*Flight{},
		holdFG: map[governor.Bucket]time.Time{}, holdBG: map[governor.Bucket]time.Time{},
		wake: make(chan struct{}, 1),
	}
}

// Register installs the executor of a kind (replacing an earlier one).
func (s *Scheduler) Register(kind string, e Executor) {
	s.mu.Lock()
	s.execs[kind] = e
	s.mu.Unlock()
	s.kick()
}

func (s *Scheduler) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (t *Task) normalize() {
	if t.Kind == "" {
		t.Kind = t.Group
	}
}

// Submit queues t. When a task for the same (entity, group) is already queued
// the two are merged: the better class, the earlier Due and the later Access
// win, so Submit can only make a task more urgent. A task for a pair that is
// running is queued for after the run.
func (s *Scheduler) Submit(t Task) error {
	t.normalize()
	if err := t.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.submitLocked(t, false)
	s.mu.Unlock()
	s.kick()
	return nil
}

// Reschedule queues t, replacing the queued task for its (entity, group) in
// full (class, Due, Access and payload): it is how an executor books the next
// cycle after a refresh. A queued task a caller waits on keeps its urgency.
func (s *Scheduler) Reschedule(t Task) error {
	t.normalize()
	if err := t.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.submitLocked(t, true)
	s.mu.Unlock()
	s.kick()
	return nil
}

func (s *Scheduler) submitLocked(t Task, replace bool) *entry {
	e, ok := s.queue[t.key()]
	if !ok {
		s.seq++
		e = &entry{Task: t, seq: s.seq}
		s.queue[t.key()] = e
		return e
	}
	if replace && e.flight == nil {
		e.Class, e.Due, e.Access, e.Payload = t.Class, t.Due, t.Access, t.Payload
		return e
	}
	if t.Class < e.Class {
		e.Class = t.Class
	}
	if dueBefore(t.Due, e.Due) {
		e.Due = t.Due
	}
	if t.Access.After(e.Access) {
		e.Access = t.Access
	}
	if t.Payload != nil {
		e.Payload = t.Payload
	}
	return e
}

// dueBefore reports whether a is due earlier than b (a zero time is due first).
func dueBefore(a, b time.Time) bool {
	switch {
	case a.IsZero():
		return !b.IsZero()
	case b.IsZero():
		return false
	}
	return a.Before(b)
}

// Cancel removes the queued task of (entity, group) and completes the flight
// waiting on it with ErrCanceled. A running task is not interrupted.
func (s *Scheduler) Cancel(entity, group string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelLocked(key{entity, group})
}

// CancelEntity cancels every queued task of an entity.
func (s *Scheduler) CancelEntity(entity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.queue {
		if k.entity == entity {
			s.cancelLocked(k)
		}
	}
}

func (s *Scheduler) cancelLocked(k key) {
	e, ok := s.queue[k]
	if !ok {
		return
	}
	delete(s.queue, k)
	if e.flight != nil {
		e.flight.complete(Result{Err: ErrCanceled})
	}
}

// Flight is one running or queued interactive fetch of an (entity, group):
// every caller waiting on the same pair shares it.
type Flight struct {
	done chan struct{}
	res  Result
	once sync.Once

	mu      sync.Mutex
	started time.Time
}

func newFlight() *Flight { return &Flight{done: make(chan struct{})} }

// Done is closed when the task has finished.
func (f *Flight) Done() <-chan struct{} { return f.done }

// Result is the task's outcome; it is valid once Done is closed.
func (f *Flight) Result() Result { return f.res }

// Started is when the task began to run; zero while it is still queued.
func (f *Flight) Started() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *Flight) start(t time.Time) {
	f.mu.Lock()
	f.started = t
	f.mu.Unlock()
}

func (f *Flight) complete(r Result) {
	f.once.Do(func() {
		f.res = r
		close(f.done)
	})
}

// SubmitInteractive queues t as class 1 and due now and returns the flight a
// caller waits on. A concurrent request for the same (entity, group) shares
// one flight: it joins the running one, or the queued one, so the executor
// sees one fetch. The flight outlives the caller's patience: nothing the
// caller does cancels it.
func (s *Scheduler) SubmitInteractive(t Task) (*Flight, error) {
	t.normalize()
	t.Class, t.Due = governor.ClassInteractive, time.Time{}
	if err := t.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if f, ok := s.running[t.key()]; ok {
		s.mu.Unlock()
		return f, nil
	}
	e := s.submitLocked(t, false)
	e.Class, e.Due = governor.ClassInteractive, time.Time{}
	if e.flight == nil {
		e.flight = newFlight()
	}
	f := e.flight
	s.mu.Unlock()
	s.kick()
	return f, nil
}

// due reports whether the task has come due at now.
func due(t Task, now time.Time) bool { return t.Due.IsZero() || !t.Due.After(now) }

// Position is the 1-based rank of the queued task of (entity, group) by due
// time among the queued tasks of its class (1 is next in line), and false when
// the task is not queued.
func (s *Scheduler) Position(entity, group string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.queue[key{entity, group}]
	if !ok {
		return 0, false
	}
	rank := 1
	for _, o := range s.queue {
		if o == e || o.Class != e.Class {
			continue
		}
		if dueBefore(o.Due, e.Due) || (o.Due.Equal(e.Due) && o.seq < e.seq) {
			rank++
		}
	}
	return rank, true
}

// Queued reports whether a task for (entity, group) is queued.
func (s *Scheduler) Queued(entity, group string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.queue[key{entity, group}]
	return ok
}

// ClassState is one class's queue, for telemetry.
type ClassState struct {
	Class governor.Class
	// Depth is the number of queued tasks of the class.
	Depth int
	// OldestOverdue is how far past its Due the most overdue task is; zero when
	// none is overdue.
	OldestOverdue time.Duration
}

// BatchItem is one task of a batch.
type BatchItem struct {
	Entity string
	Group  string
	Class  governor.Class
	Role   Role
}

// BatchState is one batch, running or finished.
type BatchState struct {
	Kind    string
	At      time.Time
	Items   []BatchItem
	Running bool
}

// IDs returns the entities of the items with role r.
func (b BatchState) IDs(r Role) []string {
	var out []string
	for _, it := range b.Items {
		if it.Role == r {
			out = append(out, it.Entity)
		}
	}
	return out
}

// State is a snapshot of the scheduler.
type State struct {
	At time.Time
	// Classes has one entry per class, 1 to 5, in order.
	Classes []ClassState
	// Running is the number of batches in flight.
	Running int
	// Batches are the batches in flight and the most recent finished ones,
	// oldest first.
	Batches []BatchState
}

// State returns a snapshot.
func (s *Scheduler) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	st := State{At: now, Running: s.active}
	for c := governor.ClassInteractive; c <= governor.ClassFill; c++ {
		st.Classes = append(st.Classes, ClassState{Class: c})
	}
	for _, e := range s.queue {
		cs := &st.Classes[int(e.Class)-1]
		cs.Depth++
		if !e.Due.IsZero() && e.Due.Before(now) {
			cs.OldestOverdue = max(cs.OldestOverdue, now.Sub(e.Due))
		}
	}
	for _, b := range s.history {
		c := *b
		c.Items = append([]BatchItem(nil), b.Items...)
		st.Batches = append(st.Batches, c)
	}
	return st
}

// sortEntries orders es by priority at now.
func (s *Scheduler) sortEntries(es []*entry, now time.Time) {
	sort.SliceStable(es, func(i, j int) bool { return s.less(es[i], es[j], now) })
}

// effClass is the class a task competes as: a fill task that is due is class 4,
// and a class 4 task overdue for AgeAfter competes as class 3 (aging).
func (s *Scheduler) effClass(e *entry, now time.Time) governor.Class {
	c := e.Class
	if c == governor.ClassFill {
		c = governor.ClassKeepAlive
	}
	if c == governor.ClassKeepAlive && !e.Due.IsZero() && now.Sub(e.Due) >= s.opt.AgeAfter {
		c = governor.ClassQuery
	}
	return c
}

func (s *Scheduler) less(a, b *entry, now time.Time) bool {
	ca, cb := s.effClass(a, now), s.effClass(b, now)
	if ca != cb {
		return ca < cb
	}
	if ca == governor.ClassKeepAlive && !a.Access.Equal(b.Access) {
		return a.Access.After(b.Access)
	}
	if !a.Due.Equal(b.Due) {
		return dueBefore(a.Due, b.Due)
	}
	return a.seq < b.seq
}
