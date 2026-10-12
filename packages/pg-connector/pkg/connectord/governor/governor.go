// Package governor is the budget governor of a pg-connector daemon: it meters
// every origin call against three buckets on one token (GraphQL points, REST
// core requests, REST search requests), decides for each prospective spend of a
// given priority class whether to admit it, and records what was spent
// (INV-GH-BUDGET-1 to INV-GH-BUDGET-5 of the GitHub behavior docs).
//
// # Rules
//
//   - The cap of each bucket is hard for every class: nothing spends past it.
//   - Nothing spends below the bucket's reserve of the origin's own remaining.
//   - The background classes (3 to 5) stop at the background share of each
//     bucket's cap, REST as well as GraphQL, leaving the rest to classes 1 and 2.
//   - A secondary limit pauses the background classes for the stated time;
//     classes 1 and 2 stay admitted.
//
// Spend is counted in a rolling window per bucket (an hour, or a minute for REST
// search), so no interval of that length ever holds more than the cap. Readings
// the origin reports (the rateLimit selection folded into a GraphQL document, the
// X-RateLimit-* headers of a REST call) enter through Observe and Record; they
// include other processes' spend on the token, so a falling reading tightens
// admission even when this daemon is far under its own cap.
//
// With a Store, every recorded call is appended to the budget table and replayed
// when the governor is created, so a restart does not forget the window already
// spent and a catch-up after a long outage cannot exceed the cap.
//
// # Use
//
// A caller asks for admission, makes the call, then settles the ticket with what
// was spent. Admission reserves the estimated cost so concurrent workers cannot
// jointly overshoot the cap:
//
//	tk, err := g.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Cost: 1})
//	if err != nil { /* *Refusal: serve stale, or answer unavailable naming err */ }
//	... call the origin ...
//	_ = tk.Settle(governor.Spend{Purpose: governor.PurposeQuery, Cost: cost, Reading: &reading, Outcome: "ok"})
//
// A call that failed before reaching the origin releases the ticket instead.
package governor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// DefaultPauseReason names a pause whose caller gave no reason.
const DefaultPauseReason = "secondary rate limit"

// pruneInterval is how often a Record prunes budget rows past retention.
const pruneInterval = time.Hour

// ErrTicketDone is returned when a Ticket is settled twice.
var ErrTicketDone = errors.New("governor: ticket already settled or released")

// Options wire the governor to its surroundings. The zero value is a
// memory-only governor on the system clock.
type Options struct {
	// Clock is the time source; nil means SystemClock.
	Clock Clock
	// Store persists spend; nil keeps it in memory only.
	Store Store
	// Logger receives persistence failures; nil discards them.
	Logger connectord.Logger
	// OnPauseChange fires on every pause start, with the pause's reason and end
	// time, and on every pause end, with that pause's reason and a zero time.
	// The end is noticed lazily (by the next call into the governor, or Tick), so
	// a daemon that wants it logged promptly calls Tick on a short timer. It runs
	// outside the governor's lock and MAY call back into it.
	OnPauseChange func(bucket Bucket, reason string, until time.Time)
	// OnSpend fires for every recorded origin call, outside the governor's lock.
	// The event log and the telemetry subscribe here.
	OnSpend func(SpendEvent)
}

// entry is one spend inside a bucket's window.
type entry struct {
	at      time.Time
	cost    int64
	class   Class // 0: unknown (a replayed row without a class), counted as background
	purpose Purpose
}

func (e entry) background() bool { return e.class == 0 || e.class.Background() }

type bucketState struct {
	name  Bucket
	lim   Limits
	bgCap int64

	ledger []entry // spend inside the window, oldest first
	sum    int64   // total cost of ledger
	bgSum  int64   // background cost of ledger

	inflight, inflightBG int64 // reserved by admitted, unsettled requests

	reading    Reading
	hasReading bool
	since      int64 // spend recorded since reading was taken

	pausedUntil time.Time
	pauseReason string
}

// Governor meters spend. It is safe for concurrent use.
type Governor struct {
	cfg       Config
	clock     Clock
	store     Store
	log       connectord.Logger
	onPause   func(Bucket, string, time.Time)
	onSpend   func(SpendEvent)
	retention time.Duration

	buckets map[Bucket]*bucketState // fixed after New

	mu        sync.Mutex
	pending   []func() // callbacks queued under mu, run after it is released
	lastPrune time.Time
}

// New returns a governor for cfg. With a store it replays the budget rows still
// inside each window, so spend already recorded counts.
func New(cfg Config, opts Options) (*Governor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	g := &Governor{
		cfg: cfg, clock: opts.Clock, store: opts.Store, log: opts.Logger,
		onPause: opts.OnPauseChange, onSpend: opts.OnSpend,
		buckets: map[Bucket]*bucketState{},
	}
	if g.clock == nil {
		g.clock = SystemClock{}
	}
	if g.log == nil {
		g.log = connectord.NopLogger
	}
	var longest time.Duration
	for _, b := range Buckets() {
		l := cfg.limits(b)
		g.buckets[b] = &bucketState{name: b, lim: l, bgCap: backgroundCap(l.Cap, cfg.BackgroundShare)}
		longest = max(longest, l.Window)
	}
	g.retention = cfg.Retention
	if g.retention <= 0 {
		g.retention = DefaultRetention
	}
	g.retention = max(g.retention, longest)

	now := g.clock.Now()
	g.lastPrune = now
	if g.store != nil {
		for _, b := range Buckets() {
			st := g.buckets[b]
			rows, err := g.store.BudgetSince(context.Background(), string(b), now.Add(-st.lim.Window))
			if err != nil {
				return nil, fmt.Errorf("governor: replay %s budget rows: %w", b, err)
			}
			for _, row := range rows {
				st.replay(row)
			}
			st.expire(now)
		}
		g.prune(now)
	}
	return g, nil
}

// backgroundCap is the cap times the share, rounded down (with a hair of slack
// so 0.9 of 4000 is 3600, not 3599 by floating-point error).
func backgroundCap(cap int64, share float64) int64 {
	return int64(math.Floor(float64(cap)*share + 1e-9))
}

// ---- bucket accounting (callers hold Governor.mu, or own the bucket) --------

// addEntry inserts e keeping the ledger ordered by time.
func (b *bucketState) addEntry(e entry) {
	i := len(b.ledger)
	for i > 0 && b.ledger[i-1].at.After(e.at) {
		i--
	}
	b.ledger = append(b.ledger, entry{})
	copy(b.ledger[i+1:], b.ledger[i:])
	b.ledger[i] = e
	b.sum += e.cost
	if e.background() {
		b.bgSum += e.cost
	}
}

// expire drops the spend that has left the window: an entry counts while
// now < at + window.
func (b *bucketState) expire(now time.Time) {
	cutoff := now.Add(-b.lim.Window)
	n := sort.Search(len(b.ledger), func(i int) bool { return b.ledger[i].at.After(cutoff) })
	if n == 0 {
		return
	}
	for _, e := range b.ledger[:n] {
		b.sum -= e.cost
		if e.background() {
			b.bgSum -= e.cost
		}
	}
	b.ledger = append(b.ledger[:0], b.ledger[n:]...)
}

// accept takes a reading into force. A reading from an earlier window than the
// one in force (a response that arrived late) is ignored; otherwise the latest
// reading wins, since the origin's own count is authoritative as of its time.
func (b *bucketState) accept(r Reading) bool {
	if r.Remaining < 0 {
		return false
	}
	if b.hasReading && !r.ResetAt.IsZero() && !b.reading.ResetAt.IsZero() && r.ResetAt.Before(b.reading.ResetAt) {
		return false
	}
	b.reading, b.hasReading, b.since = r, true, 0
	return true
}

// apply books one spend and the reading it carried; it reports whether the
// reading was taken into force. A call whose reading is absent or stale still
// reduces the estimate of what remains.
func (b *bucketState) apply(e entry, r *Reading) bool {
	if e.cost > 0 {
		b.addEntry(e)
	}
	if r != nil && b.accept(*r) {
		return true
	}
	b.since += e.cost
	return false
}

// replay books a stored row at start-up.
func (b *bucketState) replay(row store.BudgetSample) {
	class, purpose := decodePurpose(row.Purpose)
	var r *Reading
	if row.Remaining >= 0 {
		r = &Reading{Remaining: row.Remaining, ResetAt: row.ResetAt, At: row.At}
	}
	b.apply(entry{at: row.At, cost: row.Spent, class: class, purpose: purpose}, r)
}

// remaining is the best estimate of what the origin has left: its last reading
// less what has been spent since. It is not known (false) before the first
// reading and after the reading's window has reset.
func (b *bucketState) remaining(now time.Time) (int64, bool) {
	if !b.hasReading {
		return 0, false
	}
	expiry := b.reading.ResetAt
	if expiry.IsZero() {
		expiry = b.reading.At.Add(b.lim.Window)
	}
	if !now.Before(expiry) {
		return 0, false
	}
	return max(b.reading.Remaining-b.since, 0), true
}

// freeAt is when enough spend has aged out of the window to free need units
// (background spend only when bgOnly); zero when waiting would never free that
// much (the reservation of in-flight requests does not age out).
func (b *bucketState) freeAt(need int64, bgOnly bool) time.Time {
	var freed int64
	for _, e := range b.ledger {
		if bgOnly && !e.background() {
			continue
		}
		freed += e.cost
		if freed >= need {
			return e.at.Add(b.lim.Window)
		}
	}
	return time.Time{}
}

// ---- locking ----------------------------------------------------------------

// enter takes the lock, reads the clock and advances every bucket to it.
func (g *Governor) enter() time.Time {
	g.mu.Lock()
	now := g.clock.Now()
	g.advance(now)
	return now
}

// leave releases the lock, then runs the callbacks queued while it was held.
func (g *Governor) leave() {
	cbs := g.pending
	g.pending = nil
	g.mu.Unlock()
	for _, f := range cbs {
		f()
	}
}

func (g *Governor) queue(f func()) { g.pending = append(g.pending, f) }

// advance ages out spend and ends elapsed pauses.
func (g *Governor) advance(now time.Time) {
	for _, name := range Buckets() {
		b := g.buckets[name]
		b.expire(now)
		if !b.pausedUntil.IsZero() && !now.Before(b.pausedUntil) {
			reason := b.pauseReason
			b.pausedUntil, b.pauseReason = time.Time{}, ""
			if g.onPause != nil {
				g.queue(func() { g.onPause(name, reason, time.Time{}) })
			}
		}
	}
}

// Tick lets a pause that has elapsed end now (firing OnPauseChange) without
// waiting for the next admission. A daemon calls it on a short timer.
func (g *Governor) Tick() {
	g.enter()
	g.leave()
}

// ---- admission ---------------------------------------------------------------

func (g *Governor) request(req Request) (*bucketState, Request, error) {
	b, ok := g.buckets[req.Bucket]
	if !ok {
		return nil, req, fmt.Errorf("governor: unknown bucket %q", req.Bucket)
	}
	if !req.Class.Valid() {
		return nil, req, fmt.Errorf("governor: invalid class %d", int(req.Class))
	}
	if req.Cost < 1 {
		req.Cost = 1
	}
	return b, req, nil
}

// check applies the rules to req. The order is the rules that bind every class
// (the reserve, then the cap) and then the ones that bind the background only
// (a pause, then the background share), so a refusal names the most basic rule
// in the way.
func (g *Governor) check(b *bucketState, req Request, now time.Time) *Refusal {
	name, unit, cost := string(b.name), b.name.unit(), req.Cost
	refuse := func(kind RefusalKind, limit int64, retry time.Time, format string, args ...any) *Refusal {
		return &Refusal{
			Kind: kind, Bucket: b.name, Class: req.Class, Cost: cost, Limit: limit,
			Reason: fmt.Sprintf(format, args...), RetryAt: retry,
		}
	}
	if rem, ok := b.remaining(now); ok && rem-b.inflight-cost < b.lim.Reserve {
		return refuse(RefusedReserve, b.lim.Reserve, b.reading.ResetAt,
			"%s below reserve: %d %s remaining at the origin, reserve %d, this spend %d",
			name, rem-b.inflight, unit, b.lim.Reserve, cost)
	}
	if b.sum+b.inflight+cost > b.lim.Cap {
		return refuse(RefusedCap, b.lim.Cap, b.freeAt(b.sum+b.inflight+cost-b.lim.Cap, false),
			"%s cap reached: %d of %d %s per %s spent, this spend %d",
			name, b.sum+b.inflight, b.lim.Cap, unit, windowName(b.lim.Window), cost)
	}
	if req.Class.Background() {
		if now.Before(b.pausedUntil) {
			return refuse(RefusedPaused, 0, b.pausedUntil,
				"%s background work paused until %s: %s", name, b.pausedUntil.UTC().Format(time.RFC3339), b.pauseReason)
		}
		if b.bgSum+b.inflightBG+cost > b.bgCap {
			return refuse(RefusedBackgroundShare, b.bgCap, b.freeAt(b.bgSum+b.inflightBG+cost-b.bgCap, true),
				"%s background share reached: %d of %d %s per %s spent (background share of a cap of %d), this spend %d",
				name, b.bgSum+b.inflightBG, b.bgCap, unit, windowName(b.lim.Window), b.lim.Cap, cost)
		}
	}
	return nil
}

func windowName(d time.Duration) string {
	switch d {
	case time.Hour:
		return "hour"
	case time.Minute:
		return "minute"
	default:
		return d.String()
	}
}

// Check reports whether req would be admitted now, without reserving anything.
// The error is a *Refusal when the governor declines. It is the check a retry
// guard runs before another attempt.
func (g *Governor) Check(req Request) error {
	b, req, err := g.request(req)
	if err != nil {
		return err
	}
	now := g.enter()
	defer g.leave()
	if r := g.check(b, req, now); r != nil {
		return r
	}
	return nil
}

// Admit decides whether req may spend. On admission it reserves req.Cost until
// the returned Ticket is settled or released, so concurrent requests cannot
// jointly pass the cap. When it declines, the error is a *Refusal.
func (g *Governor) Admit(req Request) (*Ticket, error) {
	b, req, err := g.request(req)
	if err != nil {
		return nil, err
	}
	now := g.enter()
	defer g.leave()
	if r := g.check(b, req, now); r != nil {
		return nil, r
	}
	b.inflight += req.Cost
	if req.Class.Background() {
		b.inflightBG += req.Cost
	}
	return &Ticket{g: g, req: req}, nil
}

// Ticket is an admitted, reserved spend.
type Ticket struct {
	g    *Governor
	req  Request
	done bool // guarded by g.mu
}

// Request returns the admitted request (its cost normalized to at least 1).
func (t *Ticket) Request() Request { return t.req }

// Settle releases the reservation and records what the call actually spent. A
// zero Bucket or Class in s defaults to the ticket's. The in-memory accounting
// is updated even when persisting fails; the returned error is that failure.
func (t *Ticket) Settle(s Spend) error {
	t.g.mu.Lock()
	done := t.done
	t.done = true
	t.g.mu.Unlock()
	if done {
		return ErrTicketDone
	}
	if s.Bucket == "" {
		s.Bucket = t.req.Bucket
	}
	if s.Class == 0 {
		s.Class = t.req.Class
	}
	return t.g.record(s, &t.req)
}

// Release gives the reservation back without recording a spend: the call never
// reached the origin.
func (t *Ticket) Release() {
	g := t.g
	g.enter()
	if !t.done {
		t.done = true
		g.release(t.req)
	}
	g.leave()
}

// release returns a reservation (caller holds g.mu).
func (g *Governor) release(req Request) {
	b := g.buckets[req.Bucket]
	if b == nil {
		return
	}
	b.inflight = max(b.inflight-req.Cost, 0)
	if req.Class.Background() {
		b.inflightBG = max(b.inflightBG-req.Cost, 0)
	}
}

// ---- recording ---------------------------------------------------------------

// Record books an origin call that was not admitted through a Ticket (a call
// that has already happened cannot be refused, so it is always recorded, even
// past the cap). It appends the call to the budget table, fires the spend
// observer, and returns the persistence error if there was one.
func (g *Governor) Record(s Spend) error { return g.record(s, nil) }

func (g *Governor) record(s Spend, reserved *Request) error {
	now := g.enter()
	if reserved != nil {
		g.release(*reserved)
	}
	b := g.buckets[s.Bucket]
	if b == nil {
		g.leave()
		return fmt.Errorf("governor: unknown bucket %q", s.Bucket)
	}
	at := s.At
	if at.IsZero() {
		at = now
	}
	cost := max(s.Cost, 0)
	var rd *Reading
	if s.Reading != nil {
		r := *s.Reading
		if r.At.IsZero() {
			r.At = at
		}
		r.Bucket = s.Bucket
		rd = &r
	}
	accepted := b.apply(entry{at: at, cost: cost, class: s.Class, purpose: s.Purpose}, rd)
	remaining := int64(unknownRemaining)
	if v, ok := b.remaining(now); ok {
		remaining = v
	}
	row := store.BudgetSample{
		Bucket: string(s.Bucket), At: at, Remaining: unknownRemaining,
		Spent: cost, Purpose: encodePurpose(s.Class, s.Purpose),
	}
	if accepted {
		row.Remaining, row.ResetAt = rd.Remaining, rd.ResetAt
	}
	ev := SpendEvent{
		At: at, Bucket: s.Bucket, Class: s.Class, Purpose: s.Purpose, Group: s.Group,
		BatchSize: s.BatchSize, Cost: cost, Remaining: remaining, Outcome: s.Outcome,
	}
	g.leave()

	var err error
	if g.store != nil {
		err = g.persist(row, now)
	}
	if g.onSpend != nil {
		g.onSpend(ev)
	}
	return err
}

// Observe feeds a reading that arrived without a spend of its own (for example
// the remaining of a probe). It is the governor's input feed for readings: the
// reading takes effect at once and tightens admission as it falls.
func (g *Governor) Observe(r Reading) error {
	b := g.buckets[r.Bucket]
	if b == nil {
		return fmt.Errorf("governor: unknown bucket %q", r.Bucket)
	}
	now := g.enter()
	if r.At.IsZero() {
		r.At = now
	}
	accepted := b.accept(r)
	g.leave()
	if !accepted || g.store == nil {
		return nil
	}
	return g.persist(store.BudgetSample{
		Bucket: string(r.Bucket), At: r.At, Remaining: r.Remaining, ResetAt: r.ResetAt,
	}, now)
}

func (g *Governor) persist(row store.BudgetSample, now time.Time) error {
	if err := g.store.AppendBudget(context.Background(), row); err != nil {
		g.log.Log("budget_persist_failed", "bucket", row.Bucket, "error", err.Error())
		return fmt.Errorf("governor: record %s spend: %w", row.Bucket, err)
	}
	g.mu.Lock()
	due := now.Sub(g.lastPrune) >= pruneInterval
	if due {
		g.lastPrune = now
	}
	g.mu.Unlock()
	if due {
		g.prune(now)
	}
	return nil
}

// prune deletes budget rows older than the retention.
func (g *Governor) prune(now time.Time) {
	if _, err := g.store.PruneBudgetBefore(context.Background(), now.Add(-g.retention)); err != nil {
		g.log.Log("budget_prune_failed", "error", err.Error())
	}
}

// ---- pauses -------------------------------------------------------------------

// Pause holds the background classes of one bucket for d (INV-GH-BUDGET-5: a
// secondary limit pauses the background for the stated time). Classes 1 and 2
// stay admitted. A pause never shortens an existing one; a longer one extends
// it. OnPauseChange fires when a pause starts or is extended.
func (g *Governor) Pause(bucket Bucket, d time.Duration, reason string) error {
	b := g.buckets[bucket]
	if b == nil {
		return fmt.Errorf("governor: unknown bucket %q", bucket)
	}
	now := g.enter()
	defer g.leave()
	g.pauseLocked(b, now.Add(d), reason, now)
	return nil
}

// PauseAll holds the background classes of every bucket for d. A secondary
// limit is the token's, not one bucket's, so a response that carries one pauses
// them all.
func (g *Governor) PauseAll(d time.Duration, reason string) {
	now := g.enter()
	defer g.leave()
	for _, name := range Buckets() {
		g.pauseLocked(g.buckets[name], now.Add(d), reason, now)
	}
}

func (g *Governor) pauseLocked(b *bucketState, until time.Time, reason string, now time.Time) {
	if !until.After(now) {
		return
	}
	if reason == "" {
		reason = DefaultPauseReason
	}
	if b.pausedUntil.After(now) && !until.After(b.pausedUntil) {
		return
	}
	b.pausedUntil, b.pauseReason = until, reason
	if g.onPause != nil {
		name := b.name
		g.queue(func() { g.onPause(name, reason, until) })
	}
}

// ---- state ---------------------------------------------------------------------

// State returns a snapshot of every bucket.
func (g *Governor) State() State {
	now := g.enter()
	defer g.leave()
	st := State{At: now}
	for _, name := range Buckets() {
		b := g.buckets[name]
		bs := BucketState{
			Bucket: name, Cap: b.lim.Cap, BackgroundCap: b.bgCap, Reserve: b.lim.Reserve, Window: b.lim.Window,
			Spent: b.sum, BackgroundSpent: b.bgSum, InFlight: b.inflight,
			SpentByPurpose: map[Purpose]int64{}, Remaining: unknownRemaining,
		}
		for _, e := range b.ledger {
			bs.SpentByPurpose[e.purpose] += e.cost
		}
		if rem, ok := b.remaining(now); ok {
			bs.Remaining, bs.Limit, bs.ResetAt = rem, b.reading.Limit, b.reading.ResetAt
		}
		if now.Before(b.pausedUntil) {
			bs.Paused, bs.PauseReason, bs.PausedUntil = true, b.pauseReason, b.pausedUntil
		}
		st.Buckets = append(st.Buckets, bs)
	}
	return st
}
