package feed

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	// DefaultLimit is the number of rows a poll returns at most when the request
	// sets none.
	DefaultLimit = 500
	// scanBatch is how many log rows one read of a scan fetches; a poll scans
	// past the rows it filters out until it has Limit rows or reaches the head.
	scanBatch = 1000
)

// Observer is told about redeliveries. A nil Observer is allowed.
type Observer interface {
	// Redelivered reports that a poll of (consumer, scope) handed out rows the
	// same cursor was already given and has not acknowledged.
	Redelivered(consumer, scope string, rows int)
}

// Config wires a Feed.
type Config struct {
	// Spec is the registered connector's vocabulary; it MUST be valid.
	Spec    registry.Spec
	Store   *store.Store
	Queries Queries
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Retention is change_log_retention and ConsumerStaleAfter is
	// consumer_stale_after; both are required and positive.
	Retention, ConsumerStaleAfter time.Duration
	// Observer is optional.
	Observer Observer
	// ConflictReason ends the message of a *KeyConflictError: why one consumer
	// may not hold the cursors of two views for one scope. Empty means a generic
	// reason.
	ConflictReason string
}

// PollRequest is one poll. View names one of Spec.Views (the empty name is
// Spec.Views[0]) and Scope the configured query; Limit <= 0 means DefaultLimit.
type PollRequest struct {
	View, Consumer, Scope string
	Cached, Reset         bool
	Limit                 int
}

// PollResult is the answer of a poll. Rows are the deliverable rows after the
// cursor's position in ascending Seq. NextSeq is the log position scanned up to:
// it can be past the last returned row because rows of non-members are skipped,
// and equals the log head when the scan reached it; the consumer acknowledges it
// with Ack once it has flushed the rows. Position is the cursor's acknowledged
// sequence number (0 for a cursor that does not exist). Replay is set only by
// Reset: the current members' references to replay as added.
type PollResult struct {
	Rows              []store.ChangeRow
	NextSeq, Position int64
	Replay            []string
}

// AckRequest is one acknowledgement of the cursor (View, Consumer, Scope).
type AckRequest struct {
	View, Consumer, Scope string
	NextSeq               int64
}

// AckResult is the answer of an Ack: the cursor's stored acknowledged sequence
// number after the acknowledgement.
type AckResult struct{ AckedSeq int64 }

// handed is what a cursor was given and has not acknowledged: the position it
// was given them at and the highest sequence number among them.
type handed struct{ pos, max int64 }

// Feed serves the change feed.
type Feed struct {
	cfg   Config
	locks keyLocks

	mu     sync.Mutex
	handed map[string]handed // by consumer
}

// New returns a Feed.
func New(cfg Config) (*Feed, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("feed: a store is required")
	case cfg.Queries == nil:
		return nil, errors.New("feed: a Queries is required")
	case cfg.Retention <= 0 || cfg.ConsumerStaleAfter <= 0:
		return nil, errors.New("feed: retention and consumer_stale_after must be positive")
	}
	if err := cfg.Spec.Validate(); err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	if cfg.ConflictReason == "" {
		cfg.ConflictReason = defaultConflictReason()
	}
	return &Feed{cfg: cfg, handed: map[string]handed{}}, nil
}

// Spec returns the registration the feed was built with.
func (f *Feed) Spec() registry.Spec { return f.cfg.Spec }

// KeyLocksHeld is the number of cursor locks held or awaited right now; it is
// zero when no call is in flight. It exists for tests of the lock bookkeeping.
func (f *Feed) KeyLocksHeld() int { return f.locks.size() }

// HandedCursors is the number of cursors the redelivery bookkeeping tracks. It
// exists for tests of that bookkeeping.
func (f *Feed) HandedCursors() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.handed)
}

// Poll returns what the consumer's cursor has not yet acknowledged. A consumer
// holds ONE cursor, bound to the View and Scope of the call that created it. Poll
// never moves the position (Ack does), except Reset, which moves it to the head
// (and rebinds the cursor to a new Scope), and the creation of a cursor, which
// starts it at the tail. Calls for one consumer are serialised.
func (f *Feed) Poll(ctx context.Context, req PollRequest) (PollResult, error) {
	view, err := f.validate(req.View, req.Consumer, req.Scope)
	if err != nil {
		return PollResult{}, err
	}
	if err := f.checkKnown(req.Scope); err != nil {
		return PollResult{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	k := keyID{req.Consumer, view.Name, req.Scope}
	defer f.locks.lock(k.consumer)()

	now := f.cfg.Clock.Now()
	row, exists, err := f.cfg.Store.GetConsumer(ctx, k.consumer)
	if err != nil {
		return PollResult{}, err
	}
	if exists {
		if err := f.checkBinding(row, k, req.Reset); err != nil {
			return PollResult{}, err
		}
	}
	if err := f.checkMovedAside(ctx, k, now, exists, row.SeenAt, req.Reset); err != nil {
		return PollResult{}, err
	}
	if !req.Cached {
		if err := f.runQueries(ctx, k.scope); err != nil {
			return PollResult{}, err
		}
	}
	var members Members
	if k.scope != "" {
		if members, err = f.cfg.Queries.Members(ctx, k.scope); err != nil {
			return PollResult{}, err
		}
	}
	head, err := f.cfg.Store.ChangeHead(ctx)
	if err != nil {
		return PollResult{}, err
	}

	if req.Reset {
		replay, err := f.replay(ctx, k.scope, members)
		if err != nil {
			return PollResult{}, err
		}
		if err := f.cfg.Store.PutConsumer(ctx, store.ConsumerRow{Consumer: k.consumer, View: k.view, Scope: k.scope, AckedSeq: head, SeenAt: now}); err != nil {
			return PollResult{}, err
		}
		f.forget(k.consumer)
		return PollResult{NextSeq: head, Position: head, Replay: replay}, nil
	}
	if !exists {
		ready, err := f.baselined(ctx, k.scope, members)
		if err != nil {
			return PollResult{}, err
		}
		if !ready {
			// The tail is taken after the baseline exists; until then the cursor is
			// not created, and the consumer's Ack of NextSeq creates it.
			return PollResult{NextSeq: head}, nil
		}
		if err := f.cfg.Store.PutConsumer(ctx, store.ConsumerRow{Consumer: k.consumer, View: k.view, Scope: k.scope, AckedSeq: head, SeenAt: now}); err != nil {
			return PollResult{}, err
		}
		return PollResult{NextSeq: head, Position: head}, nil
	}

	gone, err := f.expired(ctx, row.AckedSeq, head)
	if err != nil {
		return PollResult{}, err
	}
	if gone {
		return PollResult{}, f.expiredError(k, row.AckedSeq)
	}
	rows, next, err := f.scan(ctx, view, k.scope, row.AckedSeq, head, limit, newMemberSet(members.Refs, f.cfg.Spec.FoldRef))
	if err != nil {
		return PollResult{}, err
	}
	row.SeenAt = now
	if err := f.cfg.Store.PutConsumer(ctx, row); err != nil {
		return PollResult{}, err
	}
	f.noteHanded(k, row.AckedSeq, rows)
	return PollResult{Rows: rows, NextSeq: next, Position: row.AckedSeq}, nil
}

// runQueries makes the stored results a poll reads fresh: a scoped poll runs its
// one query, an unscoped poll runs every configured watched query. Each run
// honours its own freshness, so a fresh stored result is a no-op.
func (f *Feed) runQueries(ctx context.Context, scope string) error {
	if scope != "" {
		return f.cfg.Queries.Run(ctx, scope)
	}
	for _, name := range f.cfg.Queries.Names() {
		if !f.cfg.Queries.Watched(name) {
			continue
		}
		if err := f.cfg.Queries.Run(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// baselined reports whether a new cursor of scope may start at the tail: a scoped
// cursor needs its query's membership baseline, an unscoped one needs the
// baseline of every configured watched query (members is the scope's stored
// result, unused when the scope is empty).
func (f *Feed) baselined(ctx context.Context, scope string, members Members) (bool, error) {
	if scope != "" {
		return members.Baseline, nil
	}
	for _, name := range f.cfg.Queries.Names() {
		if !f.cfg.Queries.Watched(name) {
			continue
		}
		m, err := f.cfg.Queries.Members(ctx, name)
		if err != nil {
			return false, err
		}
		if !m.Baseline {
			return false, nil
		}
	}
	return true, nil
}

// replay is the reference list a Reset hands back to be replayed as added. With
// DeliverByMembership it is the scope's members. With DeliverByWatched it is the
// watched members of the scope, or every watched reference for an unscoped
// consumer.
func (f *Feed) replay(ctx context.Context, scope string, members Members) ([]string, error) {
	refs := members.Refs
	if f.cfg.Spec.Delivery == registry.DeliverByWatched {
		watched, err := f.cfg.Queries.WatchedRefs(ctx)
		if err != nil {
			return nil, err
		}
		if scope == "" {
			refs = watched
		} else {
			set := newMemberSet(watched, f.cfg.Spec.FoldRef)
			refs = nil
			for _, r := range members.Refs {
				if set.has(r) {
					refs = append(refs, r)
				}
			}
		}
	}
	replay := slices.Clone(refs)
	if replay == nil {
		replay = []string{}
	}
	return replay, nil
}

// scan reads the log after pos up to head and returns the rows the cursor
// delivers, at most limit of them (limit <= 0 means no limit), and the position
// scanned up to.
func (f *Feed) scan(ctx context.Context, view registry.View, scope string, pos, head int64, limit int, members memberSet) ([]store.ChangeRow, int64, error) {
	var out []store.ChangeRow
	next := pos
	for next < head {
		batch, err := f.cfg.Store.ChangesAfter(ctx, next, scanBatch)
		if err != nil {
			return nil, 0, err
		}
		for _, r := range batch {
			if r.Seq > head {
				return out, max(head, pos), nil
			}
			next = r.Seq
			if r.Kind != f.cfg.Spec.EntityKind {
				continue
			}
			if ok, _ := Deliver(f.cfg.Spec, view, r, scope, members.has(r.ID)); !ok {
				continue
			}
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				return out, next, nil
			}
		}
		if len(batch) < scanBatch {
			break
		}
	}
	return out, max(head, pos), nil
}

// noteHanded tracks the rows a cursor was given, to report a redelivery: rows
// the cursor was already handed at the same position and has not acknowledged.
func (f *Feed) noteHanded(k keyID, pos int64, rows []store.ChangeRow) {
	f.mu.Lock()
	h, had := f.handed[k.consumer]
	redelivered := 0
	if had && h.pos == pos {
		for _, r := range rows {
			if r.Seq <= h.max {
				redelivered++
			}
		}
	} else {
		h = handed{pos: pos}
	}
	for _, r := range rows {
		h.max = max(h.max, r.Seq)
	}
	if len(rows) > 0 || had && h.pos == pos {
		f.handed[k.consumer] = h
	}
	f.mu.Unlock()
	if redelivered > 0 && f.cfg.Observer != nil {
		f.cfg.Observer.Redelivered(k.consumer, k.scope, redelivered)
	}
}

func (f *Feed) forget(consumer string) {
	f.mu.Lock()
	delete(f.handed, consumer)
	f.mu.Unlock()
}

// Ack records that the consumer has received everything up to NextSeq (the
// NextSeq of a poll, after the rows were flushed). It only moves the position
// forward: a NextSeq at or below the position is a no-op. It creates a cursor
// that does not exist (checking the moved-aside rule), refuses a view or scope
// other than the cursor's own, and refuses
// a NextSeq above the head of the log with ErrAckBeyondHead. The result is the
// cursor's stored position after the acknowledgement.
func (f *Feed) Ack(ctx context.Context, req AckRequest) (AckResult, error) {
	view, err := f.validate(req.View, req.Consumer, req.Scope)
	if err != nil {
		return AckResult{}, err
	}
	if req.NextSeq < 0 {
		return AckResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("next_seq %d is negative", req.NextSeq))
	}
	if err := f.checkKnown(req.Scope); err != nil {
		return AckResult{}, err
	}
	k := keyID{req.Consumer, view.Name, req.Scope}
	defer f.locks.lock(k.consumer)()

	head, err := f.cfg.Store.ChangeHead(ctx)
	if err != nil {
		return AckResult{}, err
	}
	if req.NextSeq > head {
		return AckResult{}, fmt.Errorf("%w: next_seq %d, head %d", ErrAckBeyondHead, req.NextSeq, head)
	}
	now := f.cfg.Clock.Now()
	row, exists, err := f.cfg.Store.GetConsumer(ctx, k.consumer)
	if err != nil {
		return AckResult{}, err
	}
	if exists {
		if err := f.checkBinding(row, k, false); err != nil {
			return AckResult{}, err
		}
	}
	if !exists {
		if err := f.checkMovedAside(ctx, k, now, false, time.Time{}, false); err != nil {
			return AckResult{}, err
		}
		err := f.cfg.Store.PutConsumer(ctx, store.ConsumerRow{Consumer: k.consumer, View: k.view, Scope: k.scope, AckedSeq: req.NextSeq, SeenAt: now})
		if err != nil {
			return AckResult{}, err
		}
		return AckResult{AckedSeq: req.NextSeq}, nil
	}
	if req.NextSeq <= row.AckedSeq {
		return AckResult{AckedSeq: row.AckedSeq}, nil
	}
	row.AckedSeq, row.SeenAt = req.NextSeq, now
	if err := f.cfg.Store.PutConsumer(ctx, row); err != nil {
		return AckResult{}, err
	}
	f.forget(k.consumer)
	return AckResult{AckedSeq: row.AckedSeq}, nil
}
