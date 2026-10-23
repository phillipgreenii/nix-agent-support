package feed

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// conflictReason is why one consumer may not move between the pr and ci views;
// it ends the message of a *KeyConflictError.
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

type (
	// PollRequest is one poll: View is "pr" or "ci" (the empty view is the PR feed)
	// and Scope the configured query; Limit <= 0 means DefaultLimit.
	PollRequest = shared.PollRequest
	// AckRequest is one acknowledgement of the consumer's cursor.
	AckRequest = shared.AckRequest
)

// Feed serves the change feed: the shared feed bound to the GitHub registration
// (the views pr and ci). A consumer holds one cursor.
type Feed struct{ sh *shared.Feed }

// New returns a Feed.
func New(cfg Config) (*Feed, error) {
	sh, err := shared.New(shared.Config{
		Spec: changes.Spec(), Store: cfg.Store, Queries: cfg.Queries, Clock: cfg.Clock,
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

// Poll returns what the consumer's cursor has not yet acknowledged. It never
// moves the position (Ack does), except Reset, which moves it to the head (and
// rebinds the cursor to a new scope), and the creation of a cursor, which starts
// it at the tail. Calls for one consumer are serialised.
func (f *Feed) Poll(ctx context.Context, req PollRequest) (PollResult, error) {
	return f.sh.Poll(ctx, req)
}

// Ack records that the consumer has received everything up to req.NextSeq (the
// NextSeq of a poll, after the rows were flushed). It only moves the position
// forward, creates a cursor that does not exist, and refuses a NextSeq above the
// head of the log with ErrAckBeyondHead.
func (f *Feed) Ack(ctx context.Context, req AckRequest) error {
	_, err := f.sh.Ack(ctx, req)
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
