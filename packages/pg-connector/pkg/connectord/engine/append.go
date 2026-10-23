package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// storedPrimary reads the entity's stored primary content inside the
// transaction and describes it; ok is false when it has none (so the entity has
// no baseline).
func (e *Engine) storedPrimary(ctx context.Context, tx *store.Tx, id string) (p Primary, ok bool, err error) {
	row, found, err := tx.GetGroup(ctx, e.spec.EntityKind, id, e.spec.PrimaryGroup)
	if err != nil {
		return Primary{}, false, err
	}
	if !found || len(row.Content) == 0 {
		return Primary{}, false, nil
	}
	p, err = e.cls.Describe(row.Content)
	if err != nil {
		return Primary{}, false, fmt.Errorf("engine: decode stored primary of %s: %w", id, err)
	}
	return p, true, nil
}

// pendingResult is the merged result a pending row holds and the groups it
// still awaits.
func pendingResult(p store.PendingChange) (Result, []Awaiting, error) {
	fs, err := DecodeFields(p.Fields)
	if err != nil {
		return Result{}, nil, fmt.Errorf("engine: pending %s/%s: %w", p.Kind, p.ID, err)
	}
	aw, err := DecodeAwaited(p.Awaited)
	if err != nil {
		return Result{}, nil, fmt.Errorf("engine: pending %s/%s: %w", p.Kind, p.ID, err)
	}
	return Result{Kinds: p.Kinds, Fields: fs}, aw, nil
}

// savePending writes p with res and aw as its content.
func savePending(ctx context.Context, tx *store.Tx, p store.PendingChange, res Result, aw []Awaiting) error {
	fields, err := EncodeFields(res.Fields)
	if err != nil {
		return err
	}
	awaited, err := encodeAwaited(aw)
	if err != nil {
		return err
	}
	p.Kinds, p.Fields, p.Awaited = res.Kinds, fields, awaited
	return tx.PutPending(ctx, p)
}

// putPending is savePending, and a row left with nothing awaited is ended at
// once as settled.
func (e *Engine) putPending(ctx context.Context, tx *store.Tx, p store.PendingChange, res Result, aw []Awaiting) error {
	if err := savePending(ctx, tx, p, res, aw); err != nil {
		return err
	}
	if len(aw) == 0 {
		return e.Complete(ctx, tx, p, ResultSettled)
	}
	return nil
}

// appendRow appends one change row for the entity id: it bumps the entity's
// version once, appends the row and tells the observer. It appends nothing, and
// reports false, when the result is empty or the entity row is gone.
func (e *Engine) appendRow(ctx context.Context, tx *store.Tx, id, ref, stamp string, res Result, origin string, originAt time.Time) (bool, error) {
	if res.Empty() || ref == "" {
		return false, nil
	}
	if _, ok, err := tx.GetEntity(ctx, e.spec.EntityKind, id); err != nil || !ok {
		return false, err
	}
	fields, err := EncodeFields(res.Fields)
	if err != nil {
		return false, err
	}
	version, err := tx.BumpEntityVersion(ctx, e.spec.EntityKind, id)
	if err != nil {
		return false, err
	}
	at := e.now()
	row := store.ChangeRow{
		Kind: e.spec.EntityKind, ID: ref, Version: version,
		Kinds: res.Kinds, Fields: fields, Origin: origin, OriginUpdatedAt: originAt, At: at,
	}
	row.SetStamp(stamp)
	if _, err := tx.AppendChange(ctx, row); err != nil {
		return false, err
	}
	if o := e.observer(); o != nil && !originAt.IsZero() {
		for _, k := range res.Kinds {
			o.Detected(k, origin, at.Sub(originAt))
		}
	}
	return true, nil
}

// appendNow appends a result for id with the stored primary content supplying
// the reference, stamp and origin time (the group-refresh path). An entity with
// no stored primary content has no baseline and gets nothing.
func (e *Engine) appendNow(ctx context.Context, tx *store.Tx, id string, res Result, origin string) error {
	if res.Empty() {
		return nil
	}
	prim, ok, err := e.storedPrimary(ctx, tx, id)
	if err != nil || !ok {
		return err
	}
	_, err = e.appendRow(ctx, tx, id, prim.Ref, prim.Stamp, res, origin, prim.OriginAt)
	return err
}

// Complete ends the pending row p now. The row is appended as one change row
// (or dropped when its merged result is empty), the pending row is deleted and
// result is reported to the observer. A pending row whose entity row or stored
// primary content is gone is deleted without a row and without a report; a
// pending row that is no longer stored is a no-op. The row that is ended is the
// one stored in tx, which is p as of the last detection.
func (e *Engine) Complete(ctx context.Context, tx *store.Tx, p store.PendingChange, result string) error {
	cur, ok, err := tx.GetPending(ctx, p.Kind, p.ID)
	if err != nil || !ok {
		return err
	}
	if err := tx.DeletePending(ctx, cur.Kind, cur.ID); err != nil {
		return err
	}
	if cur.Kind != e.spec.EntityKind {
		return nil
	}
	prim, ok, err := e.storedPrimary(ctx, tx, cur.ID)
	if err != nil || !ok {
		return err
	}
	if _, ok, err := tx.GetEntity(ctx, e.spec.EntityKind, cur.ID); err != nil || !ok {
		return err
	}
	res, _, err := pendingResult(cur)
	if err != nil {
		return err
	}
	stamp, _ := cur.Stamps()
	if _, err := e.appendRow(ctx, tx, cur.ID, prim.Ref, stamp, res, cur.Origin, cur.OriginUpdatedAt); err != nil {
		return err
	}
	if o := e.observer(); o != nil {
		o.Settled(result)
	}
	return nil
}

// expire sets fresh_until to now on the stored rows of the newly awaited
// groups, content and generation kept, so a read after a timeout goes to
// origin. A group with no stored content, or already stale, is left alone.
func (e *Engine) expire(ctx context.Context, tx *store.Tx, id string, queue []string, now time.Time) error {
	for _, g := range queue {
		if g == e.spec.PrimaryGroup || !slices.Contains(e.spec.Groups, g) {
			continue
		}
		row, ok, err := tx.GetGroup(ctx, e.spec.EntityKind, id, g)
		if err != nil {
			return err
		}
		if !ok || len(row.Content) == 0 || !row.FreshUntil.After(now) {
			continue
		}
		row.FreshUntil = now
		if err := tx.PutGroup(ctx, row); err != nil {
			return err
		}
	}
	return nil
}
