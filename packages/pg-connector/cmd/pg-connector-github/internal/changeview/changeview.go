// Package changeview adapts the change engine to the two operator-facing ops:
// it satisfies status.ChangeSource (the daemon's report) and explain.ChangeReader
// (the pending settle row and last change rows of one pull request) from the
// store, the settle lifecycle and the feed. The ops packages define their own
// interfaces and import neither the engine nor the feed; this package is the
// seam, so the dependency points from here to them.
//
// Everything here reads: it makes no origin call and writes nothing. It uses
// *store.Store methods, never a transaction, so it is called with no store
// transaction open.
//
// # Conventions
//
//   - Times are UTC at whole-second precision (the ops' document form).
//   - Ages are computed with the Clock the adapter was built with (never
//     time.Now), so a test with a manual clock is deterministic.
//   - A pending row is listed by the pull request's wire reference
//     owner/repo#n when the alias table knows its node id and by the node id
//     otherwise.
//   - The settle counts are the lifecycle's (in memory, last hour); the consumer
//     positions are the feed's own (Feed.Positions), not the raw consumer rows.
package changeview

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Store is the part of the store the adapters read. *store.Store satisfies it.
type Store interface {
	ChangeHead(ctx context.Context) (int64, error)
	ChangeOldest(ctx context.Context) (seq int64, ok bool, err error)
	ListPending(ctx context.Context) ([]store.PendingChange, error)
	LastChanges(ctx context.Context, kind, id string, n int) ([]store.ChangeRow, error)
}

// Settle is the settle lifecycle as the adapter reads it. *changes.Lifecycle
// satisfies it.
type Settle interface {
	Stats(ctx context.Context) (changes.Stats, error)
}

// Positions is the feed as the adapter reads it. *feed.Feed satisfies it.
type Positions interface {
	Positions(ctx context.Context) ([]feed.Position, error)
}

// Refs maps a node id back to its wire reference. *entity.Identity satisfies it.
type Refs interface {
	Ref(ctx context.Context, nodeID string) (entity.Ref, bool, error)
}

var (
	_ Store     = (*store.Store)(nil)
	_ Settle    = (*changes.Lifecycle)(nil)
	_ Positions = (*feed.Feed)(nil)
	_ Refs      = (*entity.Identity)(nil)
)

// Config builds the adapters. Store is required by both; Settle, Positions and
// Refs by NewStatusSource only. Clock nil means the system clock.
type Config struct {
	Store     Store
	Settle    Settle
	Positions Positions
	Refs      Refs
	Clock     governor.Clock
}

func (c Config) clock() governor.Clock {
	if c.Clock == nil {
		return governor.SystemClock{}
	}
	return c.Clock
}

func utc(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(time.Second)
}

// ---- status.ChangeSource ---------------------------------------------------

// StatusSource satisfies status.ChangeSource.
type StatusSource struct{ cfg Config }

var _ status.ChangeSource = (*StatusSource)(nil)

// NewStatusSource returns the adapter behind the status report's changes
// section; it needs the store, the lifecycle, the feed and the alias table.
func NewStatusSource(cfg Config) (*StatusSource, error) {
	if cfg.Store == nil || cfg.Settle == nil || cfg.Positions == nil || cfg.Refs == nil {
		return nil, errors.New("changeview: the status source needs the store, the settle lifecycle, the feed and the alias table")
	}
	return &StatusSource{cfg: cfg}, nil
}

// ChangeLog is the head and the oldest retained sequence number (0 when empty).
func (s *StatusSource) ChangeLog(ctx context.Context) (head, oldest int64, err error) {
	head, err = s.cfg.Store.ChangeHead(ctx)
	if err != nil {
		return 0, 0, err
	}
	oldest, ok, err := s.cfg.Store.ChangeOldest(ctx)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		oldest = 0
	}
	return head, oldest, nil
}

// PendingRows are the pending settle rows, soonest deadline first (the store's
// order).
func (s *StatusSource) PendingRows(ctx context.Context) ([]status.PendingRow, error) {
	rows, err := s.cfg.Store.ListPending(ctx)
	if err != nil {
		return nil, err
	}
	var out []status.PendingRow
	for _, p := range rows {
		aw, err := changes.DecodeAwaited(p.Awaited)
		if err != nil {
			return nil, fmt.Errorf("pending row %s/%s: %w", p.Kind, p.ID, err)
		}
		id := p.ID
		ref, ok, err := s.cfg.Refs.Ref(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			id = ref.String()
		}
		row := status.PendingRow{ID: id, DetectedAt: utc(p.DetectedAt), Deadline: utc(p.Deadline)}
		for _, a := range aw {
			row.Awaited = append(row.Awaited, a.Group)
		}
		out = append(out, row)
	}
	return out, nil
}

// SettleStats are the lifecycle's last-hour timeout and failure counts.
func (s *StatusSource) SettleStats(ctx context.Context) (status.SettleStats, error) {
	st, err := s.cfg.Settle.Stats(ctx)
	if err != nil {
		return status.SettleStats{}, err
	}
	return status.SettleStats{Timeouts: st.Timeouts, Failures: st.Failures}, nil
}

// Consumers are the feed's positions, one per stored cursor key.
func (s *StatusSource) Consumers(ctx context.Context) ([]status.Consumer, error) {
	ps, err := s.cfg.Positions.Positions(ctx)
	if err != nil {
		return nil, err
	}
	now := s.cfg.clock().Now()
	var out []status.Consumer
	for _, p := range ps {
		c := status.Consumer{
			Consumer: p.Consumer, Kind: p.Kind, Query: p.Query,
			AckedSeq: p.AckedSeq, LagRows: p.LagRows, SeenAt: utc(p.SeenAt),
		}
		if !p.OldestUnacked.IsZero() {
			c.OldestUnackedAgeSeconds = max(int64(now.Sub(p.OldestUnacked).Seconds()), 0)
		}
		out = append(out, c)
	}
	return out, nil
}

// ---- explain.ChangeReader --------------------------------------------------

// Reader satisfies explain.ChangeReader.
type Reader struct{ cfg Config }

var _ explain.ChangeReader = (*Reader)(nil)

// NewReader returns the adapter behind explain's pending_settle and
// last_changes; it needs only the store.
func NewReader(cfg Config) (*Reader, error) {
	if cfg.Store == nil {
		return nil, errors.New("changeview: the explain reader needs the store")
	}
	return &Reader{cfg: cfg}, nil
}

// PendingSettle is the pending row of the pull request's node id.
func (r *Reader) PendingSettle(ctx context.Context, nodeID string) (explain.PendingSettle, bool, error) {
	rows, err := r.cfg.Store.ListPending(ctx)
	if err != nil {
		return explain.PendingSettle{}, false, err
	}
	for _, p := range rows {
		if p.Kind != changes.LogKindPR || p.ID != nodeID {
			continue
		}
		aw, err := changes.DecodeAwaited(p.Awaited)
		if err != nil {
			return explain.PendingSettle{}, false, err
		}
		out := explain.PendingSettle{
			Awaited:    make([]explain.AwaitedGroup, 0, len(aw)),
			DetectedAt: utc(p.DetectedAt), Deadline: utc(p.Deadline), Kinds: p.Kinds,
		}
		for _, a := range aw {
			out.Awaited = append(out.Awaited, explain.AwaitedGroup{Group: a.Group, Since: utc(a.Since)})
		}
		return out, true, nil
	}
	return explain.PendingSettle{}, false, nil
}

// LastChanges are the newest n change rows of ref, newest first.
func (r *Reader) LastChanges(ctx context.Context, ref string, n int) ([]explain.ChangeRef, error) {
	rows, err := r.cfg.Store.LastChanges(ctx, changes.LogKindPR, ref, n)
	if err != nil {
		return nil, err
	}
	var out []explain.ChangeRef
	for _, c := range rows {
		out = append(out, explain.ChangeRef{Seq: c.Seq, Version: c.Version, Kinds: c.Kinds, Origin: c.Origin, At: utc(c.At)})
	}
	return out, nil
}
