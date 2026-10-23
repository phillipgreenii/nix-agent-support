// Package query is the shared query runner: it re-runs a connector's configured
// queries on their own cadence and answers an interactive re-run, storing each
// result in the store's query table.
//
// The runner knows nothing of any origin. The connector hands it five small
// interfaces (Lister, Aliases, Settings, Tracker, Confirmer) plus the registry
// Spec whose Roles name the membership row kinds; see interfaces.go.
//
// # What a query is
//
// A configured query is a NAME the connector's own settings define, whose value
// is one or more expressions in the origin's native syntax. The request's own
// config block is never consulted: a name the settings do not know is unknown. A
// Key is the name plus the arguments that change what the origin is asked (see
// Key); the key text is the key of the stored row.
//
// # Membership-only form
//
// A run reads only WHO is in the result (the Lister answers the members, never
// their fields). The row keeps the ordered display references, a complete flag
// (false for a listing the origin cut off), fetched_at, fresh_until,
// last_success_at and last_error. Each run also records the members' aliases (id
// to reference), so a group service can fetch them by id.
//
// # Scheduling
//
// The runner registers an executor of kind Kind with the scheduler. A configured
// query is a class 3 task due at its row's fresh_until (Schedule queues the first
// ones; each run books the next); after every run of a configured query the
// executor calls Track with the ordered references, so a member that no list ever
// reads still gets an entity row and a refresh. A row stored as configured makes
// its members keep-alive. An AD-HOC key (arguments) is cached under the ad-hoc
// ttl and never re-run in the background: it runs only for a caller that is
// waiting (class 1, Fetch).
//
// # Membership changes
//
// A configured query keeps a membership baseline (the row's BaselineAt): the
// first COMPLETE run sets it and appends no arrival row, so a cold store never
// logs every member as new; a truncated run sets nothing and a baseline is never
// cleared. With Config.Changes set, a later run appends an arrival row per new
// member (only when the previous listing was complete) and, for a departure from
// a COMPLETE listing, makes one confirming read (Config.Confirmer) and appends a
// departure row carrying the read's state, unless the read found the entity gone
// (its tombstone path logs the removal). The listings are compared by stable id,
// so a rename is neither. The run's row and these rows commit in ONE
// transaction; a departure that cannot be confirmed fails the run and is
// recomputed by the next one, never dropped. An ad-hoc key has no baseline and no
// rows.
//
// # Failure
//
// A failed run keeps the previous ids and times and records last_error (a
// governor refusal is the budget speaking, not the origin failing, and records
// nothing). The reader serves the stored list when it is inside expiry.
package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const (
	// Kind is the scheduler executor kind of a query task, and Group its group.
	Kind  = "query"
	Group = "query"
	// entityPrefix starts the scheduler entity of a query task, so a query can
	// never collide with an entity's own id.
	entityPrefix = "query:"
)

// ErrPending is returned by Fetch when ctx ended while the run was still going:
// the run is not cancelled, it completes in the background and is stored.
var ErrPending error = pendingError{}

// pendingError is the type of ErrPending: a sentinel the guard's no-literal
// rule for declarations allows, because its text is a method result.
type pendingError struct{}

func (pendingError) Error() string { return "query: run still in progress at the deadline" }

// Config wires a Runner.
type Config struct {
	// Spec is the connector's registration: its Roles give the membership row
	// kinds and its FoldRef (nil means the identity) the form in which two
	// references of one entity compare equal.
	Spec registry.Spec
	// StateFieldPrefix starts the name of the extra field a departure row
	// carries: the field StateFieldPrefix+query whose After is the confirming
	// read's State (a JSON string).
	StateFieldPrefix string
	// UnauthError builds the error an interactive or background run answers while
	// the connector is unauthenticated; cause is the tracker's own error (nil when
	// it has none). Nil means a plain error carrying the cause text.
	UnauthError func(cause error) error
	// Bucket is the governor bucket of the one-unit spend a query task declares
	// (the Lister admits each origin call itself).
	Bucket governor.Bucket

	Settings Settings
	Store    Store
	// Aliases records the members' aliases and resolves a previous listing.
	Aliases   Aliases
	Tracker   Tracker
	Lister    Lister
	Scheduler *scheduler.Scheduler
	// Auth is optional: nil means always authenticated.
	Auth Auth
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Changes records membership changes; nil means none are recorded (the
	// baseline still is) and Confirmer is never called.
	Changes Changes
	// Confirmer makes the read that confirms a departure; it is used only when
	// Changes is set. With Changes set and Confirmer nil a departure cannot be
	// confirmed, so the run does not store its result (the departure is retried
	// by the next run, never dropped).
	Confirmer Confirmer
	// Log reports an event the run survives (a previous id that cannot be a
	// departure candidate); nil discards.
	Log func(event string, kv ...any)
}

// Runner runs queries.
type Runner struct{ cfg Config }

// New returns a Runner and registers its executor with the scheduler under Kind.
func New(cfg Config) (*Runner, error) {
	switch {
	case cfg.Settings == nil, cfg.Store == nil, cfg.Aliases == nil, cfg.Tracker == nil,
		cfg.Lister == nil, cfg.Scheduler == nil, cfg.Bucket == "":
		return nil, errors.New("query: runner needs settings, a store, aliases, a tracker, a lister, a scheduler and a bucket")
	}
	if cfg.Changes != nil && (cfg.Spec.Roles.EnteredQuery == "" || cfg.Spec.Roles.LeftQuery == "") {
		return nil, errors.New("query: recording membership changes needs the arrival and departure roles of the spec")
	}
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	if cfg.UnauthError == nil {
		cfg.UnauthError = func(cause error) error {
			if cause == nil {
				return errors.New("query: unauthenticated")
			}
			return fmt.Errorf("query: unauthenticated: %w", cause)
		}
	}
	r := &Runner{cfg: cfg}
	cfg.Scheduler.Register(Kind, executor{r})
	return r, nil
}

func (r *Runner) now() time.Time { return r.cfg.Clock.Now() }

// fold is the spec's reference fold (the identity when the spec has none).
func (r *Runner) fold(ref string) string {
	if r.cfg.Spec.FoldRef == nil {
		return ref
	}
	return r.cfg.Spec.FoldRef(ref)
}

// Expr returns the configured expressions of a query name, and false when the
// connector's own settings do not define it.
func (r *Runner) Expr(name string) ([]string, bool) { return r.cfg.Settings.Expr(name) }

// TTL is how long a stored result of key stays fresh.
func (r *Runner) TTL(key Key) time.Duration { return r.cfg.Settings.TTL(key) }

// Row returns the stored row of key.
func (r *Runner) Row(ctx context.Context, key Key) (store.QueryRow, bool, error) {
	return r.cfg.Store.GetQuery(ctx, key.String())
}

func (r *Runner) unauth() error {
	if r.cfg.Auth == nil {
		return nil
	}
	if bad, err := r.cfg.Auth.Unauthenticated(); bad {
		return r.cfg.UnauthError(err)
	}
	return nil
}

// log reports an event the run survives.
func (r *Runner) log(event string, kv ...any) {
	if r.cfg.Log != nil {
		r.cfg.Log(event, kv...)
	}
}

func entityOf(k Key) string { return entityPrefix + k.String() }

func taskOf(k Key, class governor.Class, due time.Time) scheduler.Task {
	return scheduler.Task{Kind: Kind, Entity: entityOf(k), Group: Group, Class: class, Due: due, Payload: k}
}

// keyOf is the Key a query task runs: its payload, else the configured name its
// entity carries.
func keyOf(t scheduler.Task) Key {
	if k, ok := t.Payload.(Key); ok {
		return k
	}
	return Key{Name: strings.TrimPrefix(t.Entity, entityPrefix)}
}

// Schedule queues the first run of every configured query, due when its stored
// result goes stale (now when it has none). It also demotes a stored row of a
// query the settings no longer define: its members stop being keep-alive. Call it
// at start; a run books the next one.
func (r *Runner) Schedule(ctx context.Context) error {
	rows, err := r.cfg.Store.ListQueries(ctx, true)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, ok := r.Expr(row.Key); !ok {
			row.Configured = false
			if err := r.cfg.Store.PutQuery(ctx, row); err != nil {
				return err
			}
		}
	}
	for _, name := range r.cfg.Settings.Names() {
		key := Key{Name: name}
		var due time.Time
		if row, ok, err := r.Row(ctx, key); err != nil {
			return err
		} else if ok {
			due = row.FreshUntil
		}
		if err := r.cfg.Scheduler.Submit(taskOf(key, governor.ClassQuery, due)); err != nil {
			return err
		}
	}
	return nil
}

// Expire deletes the stored results of ad-hoc keys last fetched longer ago than
// expiry (and those that never succeeded). A configured query's row is never
// deleted. It returns how many it deleted; the daemon calls it on a timer.
func (r *Runner) Expire(ctx context.Context, expiry time.Duration) (int, error) {
	rows, err := r.cfg.Store.ListQueries(ctx, false)
	if err != nil {
		return 0, err
	}
	now := r.now()
	n := 0
	for _, row := range rows {
		if row.Configured || (!row.FetchedAt.IsZero() && now.Sub(row.FetchedAt) <= expiry) {
			continue
		}
		if err := r.cfg.Store.DeleteQuery(ctx, row.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Fetch re-runs key now for a caller that is waiting: a class 1 task, which
// shares the run of a caller (or the scheduler) already running it. It returns
// when the run has finished (the stored row, or the run's error: a
// *governor.Refusal when the budget declined it) or ctx is done (ErrPending; the
// run is not cancelled and is stored when it completes).
func (r *Runner) Fetch(ctx context.Context, key Key) (store.QueryRow, error) {
	if _, ok := r.Expr(key.Name); !ok {
		return store.QueryRow{}, fmt.Errorf("query: %q is not a configured query", key.Name)
	}
	f, err := r.cfg.Scheduler.SubmitInteractive(taskOf(key, governor.ClassInteractive, time.Time{}))
	if err != nil {
		return store.QueryRow{}, err
	}
	select {
	case <-f.Done():
	case <-ctx.Done():
		return store.QueryRow{}, ErrPending
	}
	if err := f.Result().Err; err != nil {
		return store.QueryRow{}, err
	}
	row, ok, err := r.Row(ctx, key)
	if err != nil {
		return store.QueryRow{}, err
	}
	if !ok {
		return store.QueryRow{}, fmt.Errorf("query: %q has no stored result after its run", key)
	}
	return row, nil
}

// QueueRun queues a re-run of the configured query key at class (class 2, the
// write-invalidated class, for a refresh) and returns at once: it neither runs
// nor waits for the run. A task already queued for the key is merged the way
// scheduler.Submit merges (the better class and the earlier due time win), and a
// run queued at class 1 or 2 re-runs the query even while its stored result is
// still fresh (the background classes skip a fresh one). A name the settings do
// not define, or an ad-hoc key (never re-run), is an error.
func (r *Runner) QueueRun(key Key, class governor.Class) error {
	if _, ok := r.Expr(key.Name); !ok {
		return fmt.Errorf("query: %q is not a configured query", key.Name)
	}
	if key.Adhoc() {
		return fmt.Errorf("query: %q is an ad-hoc key and is never re-run", key)
	}
	if !class.Valid() {
		return fmt.Errorf("query: invalid class %d", int(class))
	}
	return r.cfg.Scheduler.Submit(taskOf(key, class, time.Time{}))
}

// run lists key's members from the origin and stores the result.
func (r *Runner) run(ctx context.Context, key Key, class governor.Class) (store.QueryRow, error) {
	expr, ok := r.Expr(key.Name)
	if !ok {
		return store.QueryRow{}, fmt.Errorf("query: %q is not a configured query", key.Name)
	}
	purpose := governor.PurposeQuery
	if class == governor.ClassInteractive {
		purpose = governor.PurposeInteractive
	}
	l, err := r.cfg.Lister.List(ctx, key, expr, CallMeta{Class: class, Purpose: purpose})
	if err != nil {
		return store.QueryRow{}, err
	}
	now := r.now()
	// A configured key has a baseline and membership rows; an ad-hoc key neither.
	var prev store.QueryRow
	var plan membershipPlan
	var left []membershipRow
	if !key.Adhoc() {
		var err error
		if prev, _, err = r.Row(ctx, key); err != nil {
			return store.QueryRow{}, err
		}
		if r.cfg.Changes != nil {
			// Before the alias writes below (they move a renamed member's alias)
			// and before the transaction (the lookups read the store).
			if plan, err = r.planMembership(ctx, key, prev, l); err != nil {
				return store.QueryRow{}, err
			}
			if left, err = r.confirmDepartures(ctx, key, plan.departures); err != nil {
				return store.QueryRow{}, err
			}
		}
	}
	ids := make([]string, len(l.Members))
	for i, m := range l.Members {
		if err := r.cfg.Aliases.Put(ctx, m.ID, m.Ref); err != nil {
			return store.QueryRow{}, err
		}
		ids[i] = m.Ref
	}
	row := store.QueryRow{
		Key: key.String(), Configured: !key.Adhoc(), IDs: ids, Complete: l.Complete,
		FetchedAt: now, FreshUntil: now.Add(r.TTL(key)), LastSuccessAt: now,
	}
	if !key.Adhoc() {
		row.BaselineAt = baselineFor(prev, l.Complete, now)
	}
	rows := r.membershipRows(key, plan.arrivals, left)
	if len(rows) == 0 {
		if err := r.cfg.Store.PutQuery(ctx, row); err != nil {
			return store.QueryRow{}, err
		}
		return row, nil
	}
	err = r.cfg.Store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.PutQuery(ctx, row); err != nil {
			return err
		}
		for _, m := range rows {
			if err := r.cfg.Changes.AppendMembership(ctx, tx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return store.QueryRow{}, err
	}
	return row, nil
}

// recordError notes a failed run on the stored row (a row is created when there
// is none), keeping the ids and times of the last success.
func (r *Runner) recordError(ctx context.Context, key Key, err error) {
	var refusal *governor.Refusal
	if errors.As(err, &refusal) {
		return // the budget speaking, not the origin failing
	}
	row, ok, gerr := r.Row(ctx, key)
	if gerr != nil {
		return
	}
	if !ok {
		row = store.QueryRow{Key: key.String(), Configured: !key.Adhoc()}
	}
	row.LastError = err.Error()
	_ = r.cfg.Store.PutQuery(ctx, row)
}

// executor adapts the runner to scheduler.Executor.
type executor struct{ r *Runner }

// MaxBatch is 1: a query run is its own origin call.
func (executor) MaxBatch() int { return 1 }

// Spend is one unit, the first page of a run (the Lister admits each page itself).
func (e executor) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Bucket: e.r.cfg.Bucket, Class: class, Cost: 1}
}

// Execute runs the one query of the batch.
func (e executor) Execute(ctx context.Context, items []scheduler.Item) []scheduler.Result {
	res := make([]scheduler.Result, len(items))
	for i, it := range items {
		res[i] = e.r.execute(ctx, it)
	}
	return res
}

func (r *Runner) execute(ctx context.Context, it scheduler.Item) scheduler.Result {
	key := keyOf(it.Task)
	class := it.Task.Class
	interactive := class == governor.ClassInteractive
	if key.Adhoc() && !interactive {
		return scheduler.Result{Skipped: true} // an ad-hoc key is never re-run
	}
	if _, ok := r.Expr(key.Name); !ok {
		return scheduler.Result{Skipped: true} // the settings no longer define it
	}
	if class > governor.ClassInvalidated {
		// Another path may have re-run it since this task was queued: book the
		// next cycle instead. A task of class 1 or 2 (a caller waiting, or a
		// refresh the caller asked for) re-runs the query whatever its age.
		row, ok, err := r.Row(ctx, key)
		if err != nil {
			return scheduler.Result{Err: err}
		}
		if ok && row.FreshUntil.After(r.now()) {
			_ = r.cfg.Scheduler.Reschedule(taskOf(key, governor.ClassQuery, row.FreshUntil))
			return scheduler.Result{Skipped: true}
		}
	}
	if err := r.unauth(); err != nil {
		return scheduler.Result{Err: err} // retried after the scheduler's delay
	}
	if class == governor.ClassFill {
		class = governor.ClassKeepAlive
	}
	row, err := r.run(ctx, key, class)
	if err != nil {
		r.recordError(ctx, key, err)
		return scheduler.Result{Err: err}
	}
	if key.Adhoc() {
		return scheduler.Result{}
	}
	if _, err := r.cfg.Tracker.Track(ctx, row.IDs); err != nil {
		return scheduler.Result{Err: err}
	}
	if err := r.cfg.Scheduler.Reschedule(taskOf(key, governor.ClassQuery, row.FreshUntil)); err != nil {
		return scheduler.Result{Err: err}
	}
	return scheduler.Result{}
}
