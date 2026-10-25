package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Store is the part of the daemon store Persister uses; *store.Store satisfies
// it. A test wraps the store to interleave a write with a refresh.
type Store interface {
	groupkit.ThenStore
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	SetClosedAt(ctx context.Context, kind, id string, at time.Time) error
	InTx(ctx context.Context, fn func(tx *store.Tx) error) error
}

var _ Store = (*store.Store)(nil)

// StoreHook runs inside the store transaction that writes a refreshed summary,
// after the entity and group rows are written, with the summary stored before
// (nil for a first sighting), the one stored now (groups.Stored form), whether
// this is the first sighting, and what the refresh was admitted as. An error
// rolls the transaction back, so the summary is not stored, and is returned by
// Persist. The hook MUST use only tx: the store's single connection is occupied
// by it. changes.Engine.DetectSummary satisfies it.
type StoreHook func(ctx context.Context, tx *store.Tx, prev *groups.SummaryContent, next groups.SummaryContent, first bool, info engine.RefreshInfo) error

// GoneHook runs inside the store transaction that sets tombstoned_at, and only
// when the entity was visible before. An error rolls the transaction back, so
// the entity stays visible. The hook MUST use only tx.
type GoneHook func(ctx context.Context, tx *store.Tx, id string, purpose governor.Purpose) error

// Stored is what Persist did with one summary. The zero value means nothing was
// stored (the refresh lost the generation race).
type Stored struct {
	// First is true when the entity had no stored summary before (Prev is nil).
	First bool
	// Prev is the summary stored before this one, nil for a first sighting.
	Prev *groups.SummaryContent
	// FreshUntil is when the stored summary stops being fresh.
	FreshUntil time.Time
}

// Persister stores fetched summaries.
type Persister struct {
	Store Store
	// Rules is the freshness table (groups.Rules); its TerminalTTL is the
	// terminal_ttl.
	Rules groupkit.Table
	// Hook, when set, runs in the same transaction as the content write.
	Hook StoreHook
	// OnGone, when set, runs in the transaction MarkGone tombstones in.
	OnGone GoneHook
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// AfterCommit, when set, runs on the caller's goroutine after the
	// transaction committed, and only when a row was stored. prev is nil for a
	// first sighting; next is the stored (groups.Stored) form. It MUST NOT block.
	AfterCommit func(ctx context.Context, prev *groups.SummaryContent, next groups.SummaryContent, st Stored)
}

// Begin reads the summary group's current generation. EVERY caller takes the
// token BEFORE its origin call, so a write between the fetch and the commit
// makes the commit lose.
func (p *Persister) Begin(ctx context.Context, id string) (groupkit.Token, error) {
	return groupkit.Begin(ctx, p.Store, groups.EntityKind, groups.FoldRef(id), groups.Summary)
}

// Persist stores one refreshed summary: the entity row, the summary group row
// (the groups.Stored content, its fingerprint, fresh_until and max_age_at from
// Rules.Schedule) and Hook, all in one transaction through groupkit.CommitThen
// with tok. When the group moved since tok was taken the commit loses: nothing
// is stored, no hook runs, AfterCommit is not called, and Persist returns the
// zero Stored and a nil error.
//
// The entity key is groups.FoldRef(c.Issue.ID). A tombstoned entity is revived
// (the issue reappeared). The entity's last_access and keep_alive are kept.
//
// After the commit, when the issue is closed and the entity's closed_at is still
// zero, closed_at becomes now (an existing stamp never moves); when the issue is
// open and closed_at is set, it is cleared (a reopen). The group's schedule uses
// the stored closed_at, or now for the Persist that sees the issue closed, so an
// issue closed for less than the terminal TTL keeps its normal schedule and is
// refreshed again.
func (p *Persister) Persist(ctx context.Context, tok groupkit.Token, c groups.SummaryContent, info engine.RefreshInfo) (Stored, error) {
	id := groups.FoldRef(c.Issue.ID)
	if id == "" {
		return Stored{}, errors.New("summary: persist: issue has no id")
	}
	if tok.Kind != groups.EntityKind || tok.ID != id || tok.Group != groups.Summary {
		return Stored{}, fmt.Errorf("summary: persist %s: token is for %s/%s/%s", id, tok.Kind, tok.ID, tok.Group)
	}
	now := p.now()
	next := groups.Stored(c)
	content, err := json.Marshal(next)
	if err != nil {
		return Stored{}, fmt.Errorf("summary: persist %s: encode: %w", id, err)
	}

	// prev is read after the token was taken and before the commit: when the
	// commit wins, the group did not move in between, so this is what the
	// commit replaces.
	prev, err := p.readPrev(ctx, id)
	if err != nil {
		return Stored{}, err
	}
	closed := groups.IsClosed(c.Issue)
	row := store.GroupRow{
		Kind: groups.EntityKind, ID: id, Group: groups.Summary,
		Content: content, Fingerprint: groups.Fingerprint(c),
		FetchedAt: now,
	}
	// The schedule needs closed_at, which lives in the entity row: first from a
	// read before the commit (the row must carry a schedule), then confirmed
	// from the entity row read inside the transaction.
	pre, _, err := p.Store.GetEntity(ctx, groups.EntityKind, id)
	if err != nil {
		return Stored{}, err
	}
	row.FreshUntil, row.MaxAgeAt = p.schedule(pre.ClosedAt, closed, now)

	var storedClosedAt time.Time
	st := Stored{First: prev == nil, Prev: prev}
	won, err := groupkit.CommitThen(ctx, p.Store, tok, row, func(tx *store.Tx) error {
		ent, ok, err := tx.GetEntity(ctx, groups.EntityKind, id)
		if err != nil {
			return err
		}
		if !ok {
			ent = store.EntityRow{Kind: groups.EntityKind, ID: id}
		}
		storedClosedAt = ent.ClosedAt
		ent.TombstonedAt = time.Time{} // the issue is visible again
		if err := tx.PutEntity(ctx, ent); err != nil {
			return err
		}
		fresh, maxAge := p.schedule(ent.ClosedAt, closed, now)
		if !fresh.Equal(row.FreshUntil) || !maxAge.Equal(row.MaxAgeAt) {
			g, ok, err := tx.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
			if err != nil || !ok {
				return errors.Join(err, errors.New("summary: group row vanished in its own transaction"))
			}
			g.FreshUntil, g.MaxAgeAt = fresh, maxAge
			if err := tx.PutGroup(ctx, g); err != nil {
				return err
			}
		}
		st.FreshUntil = fresh
		if p.Hook != nil {
			return p.Hook(ctx, tx, prev, next, prev == nil, info)
		}
		return nil
	})
	if err != nil {
		return Stored{}, err
	}
	if !won {
		return Stored{}, nil
	}

	switch {
	case closed && storedClosedAt.IsZero():
		err = p.Store.SetClosedAt(ctx, groups.EntityKind, id, now)
	case !closed && !storedClosedAt.IsZero():
		err = p.Store.SetClosedAt(ctx, groups.EntityKind, id, time.Time{})
	}
	if err != nil {
		return Stored{}, err
	}
	if p.AfterCommit != nil {
		p.AfterCommit(ctx, prev, next, st)
	}
	return st, nil
}

// schedule is the group's fresh_until and max_age_at for a summary fetched at
// now: terminal when the issue is closed and has been for the terminal TTL. An
// issue seen closed by this very fetch counts as closed now; an open one has
// no closed-at whatever the entity row says (a reopen).
func (p *Persister) schedule(stored time.Time, closed bool, now time.Time) (freshUntil, maxAgeAt time.Time) {
	var closedAt time.Time
	if closed {
		closedAt = stored
		if closedAt.IsZero() {
			closedAt = now
		}
	}
	return p.Rules.Schedule(groups.Summary, groups.Terminal(closedAt, now, p.Rules.TerminalTTL), now)
}

// readPrev decodes the stored summary; nil when there is none or its content
// does not decode.
func (p *Persister) readPrev(ctx context.Context, id string) (*groups.SummaryContent, error) {
	g, ok, err := p.Store.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	if err != nil || !ok || len(g.Content) == 0 {
		return nil, err
	}
	var prev groups.SummaryContent
	if json.Unmarshal(g.Content, &prev) != nil {
		return nil, nil
	}
	return &prev, nil
}

// MarkGone tombstones the issue's entity in one transaction and calls OnGone
// only when the entity was visible before. An entity the store has no row for
// is left absent.
func (p *Persister) MarkGone(ctx context.Context, id string, purpose governor.Purpose) error {
	key := groups.FoldRef(id)
	now := p.now()
	return p.Store.InTx(ctx, func(tx *store.Tx) error {
		row, ok, err := tx.GetEntity(ctx, groups.EntityKind, key)
		if err != nil || !ok {
			return err
		}
		visible := row.TombstonedAt.IsZero()
		row.TombstonedAt = now
		if err := tx.PutEntity(ctx, row); err != nil {
			return err
		}
		if visible && p.OnGone != nil {
			return p.OnGone(ctx, tx, key, purpose)
		}
		return nil
	})
}

func (p *Persister) now() time.Time {
	if p.Clock == nil {
		return time.Now()
	}
	return p.Clock.Now()
}
