package changes

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// storedSummary reads the entity's stored summary inside the transaction; ok is
// false when it has none (so the entity has no baseline).
func storedSummary(ctx context.Context, tx *store.Tx, node string) (s summary.Summary, ok bool, err error) {
	row, found, err := tx.GetGroup(ctx, LogKindPR, node, groups.Summary)
	if err != nil {
		return summary.Summary{}, false, err
	}
	if !found || len(row.Content) == 0 {
		return summary.Summary{}, false, nil
	}
	if err := json.Unmarshal(row.Content, &s); err != nil {
		return summary.Summary{}, false, fmt.Errorf("changes: decode stored summary of %s: %w", node, err)
	}
	return s, true, nil
}

// originTime is the summary's updated_at when it parses, else zero.
func originTime(s summary.Summary) time.Time {
	t, err := time.Parse(time.RFC3339, s.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// pendingResult is the merged result a pending row holds and the groups it
// still awaits.
func pendingResult(p store.PendingChange) (classify.Result, []Awaiting, error) {
	fs, err := classify.DecodeFields(p.Fields)
	if err != nil {
		return classify.Result{}, nil, fmt.Errorf("changes: pending %s/%s: %w", p.Kind, p.ID, err)
	}
	aw, err := DecodeAwaited(p.Awaited)
	if err != nil {
		return classify.Result{}, nil, fmt.Errorf("changes: pending %s/%s: %w", p.Kind, p.ID, err)
	}
	return classify.Result{Kinds: p.Kinds, Fields: fs}, aw, nil
}

// savePending writes p with res and aw as its content.
func savePending(ctx context.Context, tx *store.Tx, p store.PendingChange, res classify.Result, aw []Awaiting) error {
	fields, err := classify.EncodeFields(res.Fields)
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
func (e *Engine) putPending(ctx context.Context, tx *store.Tx, p store.PendingChange, res classify.Result, aw []Awaiting) error {
	if err := savePending(ctx, tx, p, res, aw); err != nil {
		return err
	}
	if len(aw) == 0 {
		return e.Complete(ctx, tx, p, ResultSettled)
	}
	return nil
}

// appendRow appends one change row for the entity node: it bumps the entity's
// version once, appends the row and tells the observer. It appends nothing, and
// reports false, when the result is empty or the entity row is gone.
func (e *Engine) appendRow(ctx context.Context, tx *store.Tx, node, ref, head string, res classify.Result, origin string, originAt time.Time) (bool, error) {
	if res.Empty() || ref == "" {
		return false, nil
	}
	if _, ok, err := tx.GetEntity(ctx, LogKindPR, node); err != nil || !ok {
		return false, err
	}
	fields, err := classify.EncodeFields(res.Fields)
	if err != nil {
		return false, err
	}
	version, err := tx.BumpEntityVersion(ctx, LogKindPR, node)
	if err != nil {
		return false, err
	}
	at := e.now()
	if _, err := tx.AppendChange(ctx, store.ChangeRow{
		Kind: LogKindPR, ID: ref, Version: version, HeadSHA: head,
		Kinds: res.Kinds, Fields: fields, Origin: origin, OriginUpdatedAt: originAt, At: at,
	}); err != nil {
		return false, err
	}
	if o := e.observer(); o != nil && !originAt.IsZero() {
		for _, k := range res.Kinds {
			o.Detected(k, origin, at.Sub(originAt))
		}
	}
	return true, nil
}

// appendNow appends a result for node with the stored summary supplying the
// reference, head and origin time (the group-refresh path). An entity with no
// stored summary has no baseline and gets nothing.
func (e *Engine) appendNow(ctx context.Context, tx *store.Tx, node string, res classify.Result, origin string) error {
	if res.Empty() {
		return nil
	}
	sum, ok, err := storedSummary(ctx, tx, node)
	if err != nil || !ok {
		return err
	}
	_, err = e.appendRow(ctx, tx, node, sum.Ref(), sum.HeadRefOid, res, origin, originTime(sum))
	return err
}

// Complete ends the pending row p now. The row is appended as one change row
// (or dropped when its merged result is empty), the pending row is deleted and
// result is reported to the observer. A pending row whose entity row or stored
// summary is gone is deleted without a row and without a report; a pending row
// that is no longer stored is a no-op. The row that is ended is the one stored
// in tx, which is p as of the last detection.
func (e *Engine) Complete(ctx context.Context, tx *store.Tx, p store.PendingChange, result string) error {
	cur, ok, err := tx.GetPending(ctx, p.Kind, p.ID)
	if err != nil || !ok {
		return err
	}
	if err := tx.DeletePending(ctx, cur.Kind, cur.ID); err != nil {
		return err
	}
	if cur.Kind != LogKindPR {
		return nil
	}
	sum, ok, err := storedSummary(ctx, tx, cur.ID)
	if err != nil || !ok {
		return err
	}
	if _, ok, err := tx.GetEntity(ctx, LogKindPR, cur.ID); err != nil || !ok {
		return err
	}
	res, _, err := pendingResult(cur)
	if err != nil {
		return err
	}
	if _, err := e.appendRow(ctx, tx, cur.ID, sum.Ref(), cur.HeadSHA, res, cur.Origin, cur.OriginUpdatedAt); err != nil {
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
func (e *Engine) expire(ctx context.Context, tx *store.Tx, node string, queue []string, now time.Time) error {
	for _, g := range queue {
		if g == groups.Summary || !slices.Contains(groups.All(), g) {
			continue
		}
		row, ok, err := tx.GetGroup(ctx, LogKindPR, node, g)
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
