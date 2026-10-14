package feed

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
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
	// Redelivered reports that a poll of (consumer, query) handed out rows
	// the same key was already given and has not acknowledged.
	Redelivered(consumer, query string, rows int)
}

// Config wires a Feed.
type Config struct {
	Store   *store.Store
	Queries Queries
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Retention is change_log_retention and ConsumerStaleAfter is
	// consumer_stale_after; both are required and positive.
	Retention, ConsumerStaleAfter time.Duration
	// Observer is optional.
	Observer Observer
}

// PollRequest is one poll. Kind is "pr" or "ci"; Limit <= 0 means DefaultLimit.
type PollRequest struct {
	Kind, Consumer, Query string
	Cached, Reset         bool
	Limit                 int
}

// PollResult is the answer of a poll. Rows are the deliverable rows after the
// key's position in ascending Seq. NextSeq is the log position scanned up to: it
// can be past the last returned row because rows of non-members are skipped, and
// equals the log head when the scan reached it; the consumer acknowledges it with
// Ack once it has flushed the rows. Position is the key's acknowledged sequence
// number (0 for a key that does not exist). Replay is set only by Reset: the
// current members' references to replay as added.
type PollResult struct {
	Rows              []store.ChangeRow
	NextSeq, Position int64
	Replay            []string
}

// handed is what a key was given and has not acknowledged: the position it was
// given them at and the highest sequence number among them.
type handed struct{ pos, max int64 }

// Feed serves the change feed.
type Feed struct {
	cfg   Config
	locks keyLocks

	mu     sync.Mutex
	handed map[keyID]handed
}

// New returns a Feed.
func New(cfg Config) (*Feed, error) {
	switch {
	case cfg.Store == nil:
		return nil, errNoStore
	case cfg.Queries == nil:
		return nil, errors.New("feed: a Queries is required")
	case cfg.Retention <= 0 || cfg.ConsumerStaleAfter <= 0:
		return nil, errors.New("feed: retention and consumer_stale_after must be positive")
	}
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	return &Feed{cfg: cfg, handed: map[keyID]handed{}}, nil
}

// Poll returns what the key (Consumer, Kind, Query) has not yet acknowledged. It
// never moves the position (Ack does), except Reset, which moves it to the head,
// and the creation of a key, which starts it at the tail. Calls for one key are
// serialised.
func (f *Feed) Poll(ctx context.Context, req PollRequest) (PollResult, error) {
	if err := validate(req.Kind, req.Consumer, req.Query); err != nil {
		return PollResult{}, err
	}
	if err := f.checkKnown(req.Query); err != nil {
		return PollResult{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	k := keyID{req.Consumer, req.Kind, req.Query}
	defer f.locks.lock(k)()

	now := f.cfg.Clock.Now()
	row, exists, err := f.cfg.Store.GetConsumer(ctx, k.consumer, k.kind, k.query)
	if err != nil {
		return PollResult{}, err
	}
	if !exists {
		if err := f.checkConflict(ctx, k); err != nil {
			return PollResult{}, err
		}
	}
	if err := f.checkMovedAside(ctx, k, now, exists, row.SeenAt, req.Reset); err != nil {
		return PollResult{}, err
	}
	if !req.Cached {
		if err := f.cfg.Queries.Run(ctx, k.query); err != nil {
			return PollResult{}, err
		}
	}
	members, err := f.cfg.Queries.Members(ctx, k.query)
	if err != nil {
		return PollResult{}, err
	}
	head, err := f.cfg.Store.ChangeHead(ctx)
	if err != nil {
		return PollResult{}, err
	}

	switch {
	case req.Reset:
		if err := f.cfg.Store.PutConsumer(ctx, store.ConsumerRow{Consumer: k.consumer, Kind: k.kind, Query: k.query, AckedSeq: head, SeenAt: now}); err != nil {
			return PollResult{}, err
		}
		f.forget(k)
		replay := slices.Clone(members.Refs)
		if replay == nil {
			replay = []string{}
		}
		return PollResult{NextSeq: head, Position: head, Replay: replay}, nil
	case !exists && !members.Baseline:
		// The tail is taken after the baseline exists; until then the key is not
		// created, and the consumer's Ack of NextSeq creates it.
		return PollResult{NextSeq: head}, nil
	case !exists:
		if err := f.cfg.Store.PutConsumer(ctx, store.ConsumerRow{Consumer: k.consumer, Kind: k.kind, Query: k.query, AckedSeq: head, SeenAt: now}); err != nil {
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
	rows, next, err := f.scan(ctx, k.kind, k.query, row.AckedSeq, head, limit, newMemberSet(members.Refs))
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

// scan reads the log after pos up to head and returns the rows the key
// delivers, at most limit of them (limit <= 0 means no limit), and the position
// scanned up to.
func (f *Feed) scan(ctx context.Context, kind, query string, pos, head int64, limit int, members memberSet) ([]store.ChangeRow, int64, error) {
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
			if r.Kind != changes.LogKindPR {
				continue
			}
			if ok, _ := DeliveryForKey(kind, r, query, members.has(r.ID)); !ok {
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

// noteHanded tracks the rows a key was given, to report a redelivery: rows the
// key was already handed at the same position and has not acknowledged.
func (f *Feed) noteHanded(k keyID, pos int64, rows []store.ChangeRow) {
	f.mu.Lock()
	h, had := f.handed[k]
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
		f.handed[k] = h
	}
	f.mu.Unlock()
	if redelivered > 0 && f.cfg.Observer != nil {
		f.cfg.Observer.Redelivered(k.consumer, k.query, redelivered)
	}
}

func (f *Feed) forget(k keyID) {
	f.mu.Lock()
	delete(f.handed, k)
	f.mu.Unlock()
}

// Ack records that the consumer has received everything up to nextSeq (the
// NextSeq of a poll, after the rows were flushed). It only moves the position
// forward: a nextSeq at or below the position is a no-op. It creates a key that
// does not exist (checking the conflict and moved-aside rules), and refuses a
// nextSeq above the head of the log with ErrAckBeyondHead.
func (f *Feed) Ack(ctx context.Context, kind, consumer, query string, nextSeq int64) error {
	if err := validate(kind, consumer, query); err != nil {
		return err
	}
	if nextSeq < 0 {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("next_seq %d is negative", nextSeq))
	}
	if err := f.checkKnown(query); err != nil {
		return err
	}
	k := keyID{consumer, kind, query}
	defer f.locks.lock(k)()

	head, err := f.cfg.Store.ChangeHead(ctx)
	if err != nil {
		return err
	}
	if nextSeq > head {
		return fmt.Errorf("%w: next_seq %d, head %d", ErrAckBeyondHead, nextSeq, head)
	}
	now := f.cfg.Clock.Now()
	row, exists, err := f.cfg.Store.GetConsumer(ctx, consumer, kind, query)
	if err != nil {
		return err
	}
	if !exists {
		if err := f.checkConflict(ctx, k); err != nil {
			return err
		}
		if err := f.checkMovedAside(ctx, k, now, false, time.Time{}, false); err != nil {
			return err
		}
		return f.cfg.Store.PutConsumer(ctx, store.ConsumerRow{Consumer: consumer, Kind: kind, Query: query, AckedSeq: nextSeq, SeenAt: now})
	}
	if nextSeq <= row.AckedSeq {
		return nil
	}
	row.AckedSeq, row.SeenAt = nextSeq, now
	if err := f.cfg.Store.PutConsumer(ctx, row); err != nil {
		return err
	}
	f.forget(k)
	return nil
}
