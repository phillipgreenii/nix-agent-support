// Package query is the daemon's query runner: it re-runs the configured queries
// on their own cadence and answers an interactive re-run, storing each result in
// the store's query table.
//
// # What a query is
//
// A configured query is a NAME the daemon's own config defines (pr.queries),
// whose value is one or more GitHub search expressions in GitHub's native
// syntax. The request's config block is never consulted: a name the daemon does
// not know is unknown, whatever the request carries. A Key is the name plus the
// arguments that change what GitHub is asked (see Key); the key text is the key
// of the stored row.
//
// # Membership-only form
//
// A run reads only WHO is in the result: one GraphQL search per expression in
// the membership-only form (a node id, the number and the repository per
// member, 1 point per page of PageSize), never the members' fields. The row
// keeps the ordered ids in the wire form owner/repo#n, a complete flag (false
// for a listing past GitHub's 1,000-result search limit), fetched_at,
// fresh_until, last_success_at and last_error. Each run also records the
// members' aliases (node id to reference), so the summary group can fetch them
// by node id.
//
// # Scheduling
//
// The runner registers an executor of kind Kind with the scheduler. A
// configured query is a class 3 task due at its row's fresh_until (Schedule
// queues the first ones; each run books the next); after every run of a
// configured query the executor calls Track with the ordered ids, so a member
// that no list ever reads still gets an entity row and a class 3 summary
// refresh. A row stored as configured makes its members keep-alive (the summary
// service reads them). An AD-HOC key (arguments) is cached under
// adhoc_query_ttl and never re-run in the background: it runs only for a caller
// that is waiting (class 1, Fetch).
//
// # Membership changes
//
// A configured query keeps a membership baseline (the row's BaselineAt): the
// first COMPLETE run sets it and appends no entered_query row, so a cold store
// never logs every open PR as new; a truncated run sets nothing and a baseline
// is never cleared. With Config.Changes set, a later run appends an
// entered_query row per new member (only when the previous listing was
// complete) and, for a departure from a COMPLETE listing, makes one confirming
// entity read (Config.Confirm) and appends left_query carrying the read's state,
// unless the read found the PR gone (its tombstone path logs removed). The
// listings are compared by node id, so a repository rename is neither. The run's
// row and these rows commit in ONE transaction; a departure that cannot be
// confirmed fails the run and is recomputed by the next one, never dropped. An
// ad-hoc key has no baseline and no rows.
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
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

const (
	// Kind is the scheduler executor kind of a query task, and Group its group.
	Kind  = "query"
	Group = "query"
	// entityPrefix starts the scheduler entity of a query task, so a query can
	// never collide with a PR's node id.
	entityPrefix = "query:"
)

// ErrPending is returned by Fetch when ctx ended while the run was still going:
// the run is not cancelled, it completes in the background and is stored.
var ErrPending = errors.New("query: run still in progress at the deadline")

// Store is the part of the daemon store the runner uses; *store.Store satisfies
// it.
type Store interface {
	GetQuery(ctx context.Context, key string) (store.QueryRow, bool, error)
	PutQuery(ctx context.Context, q store.QueryRow) error
	ListQueries(ctx context.Context, configuredOnly bool) ([]store.QueryRow, error)
	DeleteQuery(ctx context.Context, key string) error
	// InTx runs fn in one store transaction: the run's result and its
	// membership rows commit together.
	InTx(ctx context.Context, fn func(tx *store.Tx) error) error
}

var _ Store = (*store.Store)(nil)

// Tracker is the summary service's Track: it resolves the wire references to
// node ids, gives each entity without a row one and queues a class 3 summary
// task for it. *summary.Service satisfies it.
type Tracker interface {
	Track(ctx context.Context, refs []string) (unresolved []string, err error)
}

// Auth is the typed unauthenticated state. *identity.Tracker satisfies it.
type Auth interface {
	// Observe books the outcome of an origin call: nil clears the state, an
	// authentication failure sets it, any other error leaves it.
	Observe(err error) bool
	Unauthenticated() (bool, error)
}

// Config wires a Runner.
type Config struct {
	// Settings is the daemon's own config: the configured queries (pr.queries)
	// and the TTLs (query_ttl, adhoc_query_ttl).
	Settings *config.Config
	Store    Store
	// Identity records the members' aliases.
	Identity *entity.Identity
	Tracker  Tracker
	// GH runs `gh <args...>`; *github.CLI satisfies it. Any retry of a transient
	// failure belongs to it.
	GH ratefeed.Runner
	// Governor admits each page; Feed books its spend and reading.
	Governor  *governor.Governor
	Feed      *ratefeed.Feed
	Scheduler *scheduler.Scheduler
	// Auth is optional: nil means always authenticated.
	Auth Auth
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Changes records membership changes (entered_query, left_query); nil means
	// none are recorded (the baseline still is) and Confirm is never called.
	Changes Changes
	// Confirm makes the entity read that confirms a departure; it is used only
	// when Changes is set. With Changes set and Confirm nil a departure cannot be
	// confirmed, so the run does not store its result (the departure is retried
	// by the next run, never dropped).
	Confirm Confirmer
	// Log reports an event the run survives (a previous id that cannot be a
	// departure candidate); nil discards.
	Log func(event string, kv ...any)
}

// Runner runs queries.
type Runner struct{ cfg Config }

// New returns a Runner and registers its executor with the scheduler under Kind.
func New(cfg Config) (*Runner, error) {
	switch {
	case cfg.Settings == nil, cfg.Store == nil, cfg.Identity == nil, cfg.Tracker == nil,
		cfg.GH == nil, cfg.Governor == nil, cfg.Feed == nil, cfg.Scheduler == nil:
		return nil, errors.New("query: runner needs settings, a store, identity, tracker, gh, governor, feed and scheduler")
	}
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	r := &Runner{cfg: cfg}
	cfg.Scheduler.Register(Kind, executor{r})
	return r, nil
}

func (r *Runner) now() time.Time { return r.cfg.Clock.Now() }

// Expr returns the configured expressions of a query name, and false when the
// daemon's own config does not define it.
func (r *Runner) Expr(name string) (schema.QueryExpr, bool) {
	e, ok := r.cfg.Settings.PR.Queries[name]
	return e, ok
}

// TTL is how long a stored result of key stays fresh: query_ttl for the
// configured query, adhoc_query_ttl for an ad-hoc key.
func (r *Runner) TTL(key Key) time.Duration {
	if key.Adhoc() {
		return r.cfg.Settings.AdhocQueryTTL.Std()
	}
	return r.cfg.Settings.QueryTTLFor(key.Name).Std()
}

// Row returns the stored row of key.
func (r *Runner) Row(ctx context.Context, key Key) (store.QueryRow, bool, error) {
	return r.cfg.Store.GetQuery(ctx, key.String())
}

// UnauthenticatedError is the error a fetch answers while the daemon is
// unauthenticated; it wraps vcs.ErrAuthInvalid.
func UnauthenticatedError(cause error) error {
	if cause == nil {
		return vcs.ErrAuthInvalid
	}
	return fmt.Errorf("%w: %v", vcs.ErrAuthInvalid, cause)
}

func (r *Runner) unauth() error {
	if r.cfg.Auth == nil {
		return nil
	}
	if bad, err := r.cfg.Auth.Unauthenticated(); bad {
		return UnauthenticatedError(err)
	}
	return nil
}

func (r *Runner) observe(err error) {
	if r.cfg.Auth != nil {
		r.cfg.Auth.Observe(err)
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
// query the config no longer defines: its members stop being keep-alive. Call
// it at start; a run books the next one.
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
	names := make([]string, 0, len(r.cfg.Settings.PR.Queries))
	for name := range r.cfg.Settings.PR.Queries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
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
// still fresh (the background classes skip a fresh one). A name the config does
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
	l, err := r.list(ctx, key, expr, ratefeed.Meta{Class: class, Purpose: purpose})
	if err != nil {
		return store.QueryRow{}, err
	}
	now := r.now()
	// A configured key has a baseline and membership rows; an ad-hoc key neither.
	var prev store.QueryRow
	var plan membershipPlan
	var left []changes.Membership
	if !key.Adhoc() {
		var err error
		if prev, _, err = r.Row(ctx, key); err != nil {
			return store.QueryRow{}, err
		}
		if r.cfg.Changes != nil {
			// Before the alias writes below (they move a renamed PR's alias) and
			// before the transaction (the lookups read the store).
			if plan, err = r.planMembership(ctx, key, prev, l); err != nil {
				return store.QueryRow{}, err
			}
			if left, err = r.confirmDepartures(ctx, key, plan.departures); err != nil {
				return store.QueryRow{}, err
			}
		}
	}
	ids := make([]string, len(l.members))
	for i, m := range l.members {
		if _, err := r.cfg.Identity.Put(ctx, m.nodeID, m.ref); err != nil {
			return store.QueryRow{}, err
		}
		ids[i] = m.ref.String()
	}
	row := store.QueryRow{
		Key: key.String(), Configured: !key.Adhoc(), IDs: ids, Complete: l.complete,
		FetchedAt: now, FreshUntil: now.Add(r.TTL(key)), LastSuccessAt: now,
	}
	if !key.Adhoc() {
		row.BaselineAt = baselineFor(prev, l.complete, now)
	}
	rows := membershipRows(key, plan.arrivals, left)
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

// Spend is one point, the first page of a run (the run admits each page itself).
func (executor) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: class, Cost: 1}
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
		return scheduler.Result{Skipped: true} // the config no longer defines it
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
