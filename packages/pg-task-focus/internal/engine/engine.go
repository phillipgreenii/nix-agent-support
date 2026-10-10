// Package engine is the single writer of a pg-task-focus data directory. It
// owns the write path: the idempotency lookup, the read-only gate, the
// planning and validation of a request (command.Build, which replays the
// whole candidate log), the append and fsync, the adoption of the validated
// model and the state version. It also keeps the record of request ids and
// the no-op cache, reloads the configuration, reports the store's health and
// tells observers and callbacks what happened. Verify checks a log offline.
//
// Nothing else writes the store or the model: the engine opens the store
// itself, from Options.Dir, and one function both appends and adopts.
package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

// Version is the state version: the number of lines in the log and the
// configuration generation.
type Version = command.Version

// Health is the store's write health; the zero value means healthy.
type Health = store.Health

// Options configures Open.
type Options struct {
	// Dir is the data directory; the engine opens the store in it.
	Dir string
	// Config is the validated configuration; it MUST be set.
	Config *config.Config
	// ConfigGeneration is the generation of Config, the second member of the
	// state version. The caller owns its seed and MUST advance it on every
	// reload, so that a version never recurs.
	ConfigGeneration int64
	// Clock is the time source of every event and of the read-only instant;
	// nil means the system clock.
	Clock clock.Clock
	// NewID draws a fresh event, batch or cycle id; nil means a ULID of the
	// clock's reading and random entropy.
	NewID func() event.ID
	// Observer is told what the engine does; nil means nobody.
	Observer Observer
	// FS is the file system the store reads and writes the log through; nil
	// means the operating system.
	FS store.FS
	// AdoptFault is a test seam: when it returns an error, the validated
	// candidate is not adopted after a durable append, which is the
	// adoption-failure path. nil means adoption always proceeds.
	AdoptFault func() error
}

// Result is the answer to a request. Changed is set when events were
// appended, and EventIDs lists them in log order (a batch's batch.committed
// excluded), with BatchID set for a batch. A no-op is Changed false with a
// Note saying why and no EventIDs. Replayed is set when the answer is the
// result an earlier request with the same id and content received, from the
// record of request ids or from the no-op cache. Version is the state version
// once the request was handled (for a replayed answer, the present one).
// Preview is the dry run's preview.
type Result struct {
	Changed  bool
	Note     string
	EventIDs []event.ID
	BatchID  event.ID
	Replayed bool
	Version  Version
	Preview  *command.Preview
}

// Snapshot is a consistent reading of the engine: the model, the
// configuration and the state version they make together. The model and the
// configuration are never changed in place, so a snapshot stays valid.
type Snapshot struct {
	Model   *projection.Model
	Config  *config.Config
	Version Version
}

// The store_unavailable messages after the READ-ONLY sentence.
const (
	msgRolledBack       = "the outcome is unknown: retry with the same id"
	msgUnknownOutcome   = "The outcome is unknown; if the request carried an id, retry with it after the restart"
	msgStoredNotAdopted = "The change is stored; if the request carried an id, retry with it after the restart to receive the result"
)

// errClosed is what Do and SetConfig return once the engine is closed.
var errClosed = errors.New("engine: the engine is closed")

// ReadOnlyMessage is the sentence every client shows while the store is
// read-only: "READ-ONLY: <reason>. Restart pg-task-focus to recover". It is
// empty for a healthy store.
func ReadOnlyMessage(h Health) string {
	if !h.ReadOnly {
		return ""
	}
	return "READ-ONLY: " + string(h.Reason) + ". Restart pg-task-focus to recover"
}

// Engine is the open data directory. It is safe for concurrent use: requests
// are serialized, and reads never wait for a write in progress.
type Engine struct {
	st         *store.Store
	clock      clock.Clock
	newID      func() event.ID
	obs        Observer
	adoptFault func() error
	callbacks  callbacks

	// mu serializes the write path (Do, SetConfig, Close) and guards what
	// only it touches: the record of request ids, the no-op cache, the last
	// health reported and closed.
	mu         sync.Mutex
	index      *index
	noOps      *noOpCache
	lastHealth Health
	closed     bool

	// state guards what readers see. It is written only while mu is held,
	// so the write path may read these fields under mu alone.
	state   sync.RWMutex
	model   *projection.Model
	cfg     *config.Config
	version Version
}

// Open takes the data directory, recovers the end of its log, replays it and
// rebuilds the record of request ids from the log's req_hash fields. A log
// the store refuses (corrupt, an unknown version, locked by another process)
// or whose timeline is impossible is an error, and the directory is released.
//
// Open does not check the log's active profile against Options.Config: a log
// whose profile the configuration no longer defines opens, and a period
// change that names no profile is then refused with unknown_profile until a
// request names a defined one. Whether a service refuses to start on such a
// log is the caller's decision (SetConfig, by contrast, refuses a reload that
// drops the active profile).
func Open(opts Options) (*Engine, error) {
	if opts.Config == nil {
		return nil, errors.New("engine: Options needs a Config")
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	newID := opts.NewID
	if newID == nil {
		newID = func() event.ID { return event.NewID(clk.Now(), rand.Reader) }
	}
	var obs Observer = nopObserver{}
	if opts.Observer != nil {
		obs = opts.Observer
	}
	st, events, rec, err := store.Open(store.Options{Dir: opts.Dir, FS: opts.FS, Now: clk.Now})
	if err != nil {
		return nil, err
	}
	// Until the engine is built, the store is released on every way out, a
	// panic in the observer included, so the directory is never left claimed
	// by a store nobody can close. Store.Close is safe to call twice.
	opened := false
	defer func() {
		if !opened {
			_ = st.Close()
		}
	}()
	obs.Recovered(rec)
	began := time.Now()
	m, err := projection.Replay(events)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("replaying the event log: %w", err), st.Close())
	}
	obs.Replayed(len(events), time.Since(began))
	opened = true
	return &Engine{
		st: st, clock: clk, newID: newID, obs: obs, adoptFault: opts.AdoptFault,
		index: newIndex(events), noOps: newNoOpCache(noOpCacheSize),
		model: m, cfg: opts.Config,
		version: Version{LogLines: len(events), ConfigGeneration: opts.ConfigGeneration},
	}, nil
}

// Do handles one request. In order: (a) a dry run is planned and its preview
// returned, without the record of request ids, the no-op cache or the
// read-only gate, and nothing is appended; (b) a request with an id is looked
// up in the record of request ids and then in the no-op cache, comparing only
// the request hash: the same hash returns the original result, marked
// Replayed, and other content is id_conflict; (c) a store that is read-only
// refuses it (store_unavailable with the READ-ONLY sentence); (d) the request
// is planned and validated by command.Build; a no-op is answered, and
// remembered under its id, without appending; otherwise the events are
// appended and made durable, the request is recorded under its id, the
// validated model is adopted, the version advances, the observer is told and
// OnCommit runs.
//
// A panic in user code during a commit is re-raised. Raised after the model
// is adopted (an Observer method, an OnCommit callback), it leaves the engine
// consistent and writable; raised before (the AdoptFault seam), it first
// makes the store read-only with ReasonAdopt, as an adoption failure does. In
// both cases the request is recorded, so a retry with its id replays it.
//
// Every refusal is a *command.Rejection: one from Build, id_conflict, or
// store_unavailable (the store was read-only; an append failed and was rolled
// back; the request's own append made the store read-only; or the change is
// stored but could not be adopted). Any other error is not a refusal: a
// cancelled ctx before the request is handled, or a closed engine.
func (e *Engine) Do(ctx context.Context, c command.Command) (Result, error) {
	if c == nil {
		return Result{}, errors.New("engine: no command")
	}
	// A request whose context is done is not handled: checked before waiting
	// for the write path, and again once it is taken.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if e.closed {
		return Result{}, errClosed
	}
	if c.IsDryRun() {
		return e.dryRun(c)
	}
	id := c.ClientID()
	var hash string
	if id != "" {
		if h, err := c.ReqHash(); err == nil { // a hash that fails is Build's invalid_request
			hash = h
			if r, done, err := e.lookup(id, hash); done {
				return r, err
			}
		}
	}
	if h := e.st.Health(); h.ReadOnly {
		return Result{}, e.reportRejection(storeUnavailable(ReadOnlyMessage(h)))
	}
	plan, err := command.Build(e.env(), c)
	if err != nil {
		return Result{}, e.reportBuildErr(err)
	}
	if plan.NoOp {
		r := Result{Note: plan.Note, Version: e.version}
		if hash != "" {
			e.noOps.put(&noOp{id: id, hash: hash, result: r})
		}
		return r, nil
	}
	return e.commit(plan)
}

// lookup is step (b) of Do: done is set when the request is answered.
func (e *Engine) lookup(id event.ID, hash string) (Result, bool, error) {
	r, found, conflict := e.index.lookup(id, hash)
	if conflict != nil {
		return Result{}, true, e.reportRejection(conflict)
	}
	if found {
		return Result{
			Changed: true, EventIDs: append([]event.ID(nil), r.events...), BatchID: r.batch,
			Replayed: true, Version: e.version,
		}, true, nil
	}
	n, found, conflict := e.noOps.lookup(id, hash)
	if conflict != nil {
		return Result{}, true, e.reportRejection(conflict)
	}
	if found {
		out := n.result
		out.Replayed, out.Version = true, e.version
		return out, true, nil
	}
	return Result{}, false, nil
}

// dryRun plans a dry run and returns its preview; nothing is recorded.
func (e *Engine) dryRun(c command.Command) (Result, error) {
	plan, err := command.Build(e.env(), c)
	if err != nil {
		return Result{}, e.reportBuildErr(err)
	}
	return Result{Note: plan.Note, Version: e.version, Preview: plan.Preview}, nil
}

// env is what Build reads. The caller holds mu.
func (e *Engine) env() command.Env {
	return command.Env{Model: e.model, Config: e.cfg, Now: e.clock.Now(), Version: e.version, NewID: e.newID}
}

// commit is the one place the store is written and the model adopted: it
// appends the plan's events and makes them durable, records the request in
// the record of request ids before any user code runs, adopts the plan's
// candidate model, advances the version, and only then tells the observer
// (Appended, Corrected) and runs OnCommit. The caller holds mu.
//
// Once the append is durable, a panic in any later step (the AdoptFault seam,
// an Observer method, an OnCommit callback) is re-raised, never swallowed.
// One raised before the candidate is adopted would leave a writable store
// behind its model, so it first takes the adoption-failure path: the store
// becomes read-only with ReasonAdopt. One raised after adoption leaves the
// engine consistent (the request recorded, the model and the version matching
// the file) and the store writable. The guard stands from the line after the
// append, so a panic in recording the request counts as one before adoption.
// A panic in the observer or OnHealthChange while the store is made read-only
// is dropped: the panic that got there is the one re-raised.
func (e *Engine) commit(plan command.Plan) (Result, error) {
	stats, err := e.st.Append(plan.Events)
	if err != nil {
		return Result{}, e.appendFailed(err)
	}

	// settled is set once a panic can no longer leave a writable store behind
	// its model: the candidate is adopted, or the store is already read-only.
	// The guard stands from the first line after the append, so a defect panic
	// in the request record below takes the same path as one in user code.
	settled := false
	defer func() {
		if p := recover(); p != nil {
			if !settled {
				e.adoptFailedKeeping()
			}
			panic(p)
		}
	}()

	result := Result{Changed: true, BatchID: plan.BatchID}
	for _, ev := range plan.Events {
		e.index.add(ev)
		if ev.Payload.EventType() != event.TypeBatchCommitted {
			result.EventIDs = append(result.EventIDs, ev.ID)
		}
	}

	adoptErr := errors.New("the plan has no candidate model")
	if plan.Candidate != nil {
		adoptErr = nil
		if e.adoptFault != nil {
			adoptErr = e.adoptFault()
		}
	}
	if adoptErr != nil {
		settled = true // adoptFailed marks the store read-only before it runs user code
		h := e.adoptFailed()
		return Result{}, e.reportRejection(storeUnavailable(ReadOnlyMessage(h) + ". " + msgStoredNotAdopted))
	}

	e.state.Lock()
	e.model = plan.Candidate
	e.version.LogLines = plan.Candidate.Lines()
	v := e.version
	e.state.Unlock()
	settled = true
	result.Version = v
	e.observeAppend(plan.Events, stats)
	e.observeCorrections(plan.Events)
	e.callbacks.commit(v)
	return result, nil
}

// adoptFailed is the adoption-failure path: the change is durable and
// recorded, but the model stays behind the log until the restart, so nothing
// more may be written. It returns the store's health. The caller holds mu.
func (e *Engine) adoptFailed() Health {
	e.st.MarkReadOnly(store.ReasonAdopt)
	e.obs.AppendFailed(stageProject)
	return e.healthChanged()
}

// adoptFailedKeeping is adoptFailed for the recover branch of commit, which
// re-raises the panic that brought it there: the store is marked read-only
// first, and a panic in the observer or OnHealthChange it runs after that is
// recovered, so the original panic value is the one that propagates.
func (e *Engine) adoptFailedKeeping() {
	defer func() { _ = recover() }()
	e.adoptFailed()
}

// observeAppend reports each appended event, splitting the stats of the
// append across them as Observer.Appended says.
func (e *Engine) observeAppend(events []event.Event, stats store.AppendStats) {
	for i, ev := range events {
		share := store.AppendStats{Events: 1}
		// Build and Append have each encoded this event already, and
		// encoding is deterministic, so this cannot fail; were it to, the
		// event is reported with Bytes zero rather than not at all.
		if line, err := event.Encode(ev); err == nil {
			share.Bytes = int64(len(line)) + 1
		}
		if i == 0 {
			share.Duration = stats.Duration
		}
		e.obs.Appended(ev.Payload.EventType(), share)
	}
}

// observeCorrections reports a committed correction or retraction.
func (e *Engine) observeCorrections(events []event.Event) {
	for _, ev := range events {
		switch ev.Payload.EventType() {
		case event.TypeEventCorrected:
			e.obs.Corrected(kindCorrect)
		case event.TypeEventRetracted:
			e.obs.Corrected(kindRetract)
		}
	}
}

// appendFailed is the refusal of a request whose append failed, which tells
// whether the outcome is unknown and whether the store is now read-only. The
// caller holds mu.
func (e *Engine) appendFailed(err error) error {
	var ae *store.AppendError
	switch {
	case errors.As(err, &ae):
		stage := stageWrite
		if ae.Stage == store.StageSync {
			stage = stageFsync
		}
		e.obs.AppendFailed(stage)
	case errors.Is(err, store.ErrStoreUnavailable):
		// Nothing was attempted: the store was already read-only or closed.
		if h := e.st.Health(); h.ReadOnly {
			return e.reportRejection(storeUnavailable(ReadOnlyMessage(h)))
		}
		return errClosed
	default:
		// An event that cannot be encoded never reaches the file; Build has
		// already encoded each one, so this is a defect, not a refusal.
		return fmt.Errorf("engine: appending: %w", err)
	}
	if h := e.healthChanged(); h.ReadOnly {
		return e.reportRejection(storeUnavailable(ReadOnlyMessage(h) + ". " + msgUnknownOutcome))
	}
	return e.reportRejection(storeUnavailable(msgRolledBack))
}

// healthChanged reads the store's health and, when it differs from the last
// one reported and the store is read-only, runs OnHealthChange. The caller
// holds mu.
func (e *Engine) healthChanged() Health {
	h := e.st.Health()
	if h != e.lastHealth {
		e.lastHealth = h
		if h.ReadOnly {
			e.callbacks.health(h)
		}
	}
	return h
}

// storeUnavailable is the store_unavailable refusal with a message.
func storeUnavailable(msg string) *command.Rejection {
	return &command.Rejection{Reason: command.ReasonStoreUnavailable, Message: msg}
}

// reportRejection tells the observer of a refusal and returns it.
func (e *Engine) reportRejection(r *command.Rejection) error {
	e.obs.Rejected(r.Reason)
	return r
}

// reportBuildErr is reportRejection for an error from Build, which is a
// refusal when it is a *command.Rejection and a defect otherwise; it returns
// err as it is.
func (e *Engine) reportBuildErr(err error) error {
	var r *command.Rejection
	if errors.As(err, &r) {
		e.obs.Rejected(r.Reason)
	}
	return err
}

// Snapshot returns the model, the configuration and the version, read
// together.
func (e *Engine) Snapshot() Snapshot {
	e.state.RLock()
	defer e.state.RUnlock()
	return Snapshot{Model: e.model, Config: e.cfg, Version: e.version}
}

// State is what a client shows at now, with the store's health in State.Store.
// It is two reads, the snapshot and then the health, not one atomic reading:
// a failed write between them may pair a read-only health with the model from
// just before it. That is harmless, because health only ever moves from
// healthy to read-only (only reopening clears it), so State never shows a
// read-only store as healthy.
func (e *Engine) State(now time.Time) view.State {
	snap := e.Snapshot()
	st := view.Build(snap.Model, snap.Config, now)
	h := e.st.Health()
	st.Store = view.StoreHealth{ReadOnly: h.ReadOnly, Reason: string(h.Reason), Since: h.Since}
	return st
}

// Health reports whether the store is read-only, why and since when; the
// zero value means healthy. Only reopening clears it.
func (e *Engine) Health() Health { return e.st.Health() }

// Probe checks that the data directory can still take a write, as
// store.Store.Probe does: nil when it can, the failure otherwise. It writes no
// byte of the log, does not consult or change the read-only state, and never
// waits for a write in progress. It backs the store_writable gauge.
func (e *Engine) Probe() error { return e.st.Probe() }

// StoreSize is the length in bytes of the committed log. It never waits for a
// write in progress. It backs the store_size_bytes gauge.
func (e *Engine) StoreSize() int64 { return e.st.Size() }

// Version is the present state version.
func (e *Engine) Version() Version {
	e.state.RLock()
	defer e.state.RUnlock()
	return e.version
}

// SetConfig replaces the configuration with next at generation, which the
// caller MUST advance on every reload. A reload that no longer defines the
// active profile is refused (config.CheckReload) and the previous
// configuration stays; an accepted one changes the version and runs OnCommit.
// Replay never reads the configuration, so the model is unchanged.
func (e *Engine) SetConfig(next *config.Config, generation int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errClosed
	}
	if err := config.CheckReload(e.cfg, next, e.model.Profile()); err != nil {
		return err
	}
	e.state.Lock()
	e.cfg = next
	e.version.ConfigGeneration = generation
	v := e.version
	e.state.Unlock()
	e.callbacks.commit(v)
	return nil
}

// Close releases the data directory. It is safe to call twice; reads keep
// answering from the last state, and Do and SetConfig are refused.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	return e.st.Close()
}
