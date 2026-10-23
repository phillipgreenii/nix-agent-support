package feed

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// conflictReason is why one consumer may not hold both keys of one query; it
// ends the message of a *KeyConflictError.
const conflictReason = "one consumer acting on both feeds would act twice on one CI event"

// DefaultLimit is the number of rows a poll returns at most when the request
// sets none.
const DefaultLimit = shared.DefaultLimit

type (
	// Observer is told about redeliveries. A nil Observer is allowed.
	Observer = shared.Observer
	// PollResult is the answer of a poll.
	PollResult = shared.PollResult
)

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

// Feed serves the change feed: the shared feed bound to the GitHub registration
// (the views pr and ci), with the cursor key spelled (consumer, kind, query).
type Feed struct{ sh *shared.Feed }

// New returns a Feed.
func New(cfg Config) (*Feed, error) {
	var q shared.Queries
	if cfg.Queries != nil {
		q = sharedQueries{cfg.Queries}
	}
	sh, err := shared.New(shared.Config{
		Spec: changes.Spec(), Store: cfg.Store, Queries: q, Clock: cfg.Clock,
		Retention: cfg.Retention, ConsumerStaleAfter: cfg.ConsumerStaleAfter, Observer: cfg.Observer,
		ConflictReason: conflictReason,
	})
	if err != nil {
		return nil, err
	}
	return &Feed{sh: sh}, nil
}

// Shared returns the shared feed the facade wraps, which the shared wire
// handlers (pkg/connectord/feedop) are built on.
func (f *Feed) Shared() *shared.Feed { return f.sh }

// checkKind refuses a kind that is neither of the two views.
func checkKind(kind string) error {
	if kind != KindPR && kind != KindCI {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("feed kind %q is neither %q nor %q", kind, KindPR, KindCI))
	}
	return nil
}

// Poll returns what the key (Consumer, Kind, Query) has not yet acknowledged. It
// never moves the position (Ack does), except Reset, which moves it to the head,
// and the creation of a key, which starts it at the tail. Calls for one key are
// serialised.
func (f *Feed) Poll(ctx context.Context, req PollRequest) (PollResult, error) {
	if err := checkKind(req.Kind); err != nil {
		return PollResult{}, err
	}
	return f.sh.Poll(ctx, shared.PollRequest{
		View: req.Kind, Consumer: req.Consumer, Scope: req.Query,
		Cached: req.Cached, Reset: req.Reset, Limit: req.Limit,
	})
}

// Ack records that the consumer has received everything up to nextSeq (the
// NextSeq of a poll, after the rows were flushed). It only moves the position
// forward, creates a key that does not exist, and refuses a nextSeq above the
// head of the log with ErrAckBeyondHead.
func (f *Feed) Ack(ctx context.Context, kind, consumer, query string, nextSeq int64) error {
	if err := checkKind(kind); err != nil {
		return err
	}
	_, err := f.sh.Ack(ctx, shared.AckRequest{View: kind, Consumer: consumer, Scope: query, NextSeq: nextSeq})
	return err
}

// Prune applies change_log_retention to the log and consumer_stale_after to the
// consumer table, and returns how many rows and keys it deleted.
func (f *Feed) Prune(ctx context.Context) (rows, keys int64, err error) { return f.sh.Prune(ctx) }

// NoteMovedAside records that the daemon started on a fresh store because the
// previous one was moved aside at at.
func (f *Feed) NoteMovedAside(ctx context.Context, at time.Time) error {
	return f.sh.NoteMovedAside(ctx, at)
}
