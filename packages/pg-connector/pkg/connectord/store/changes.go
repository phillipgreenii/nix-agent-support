package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// This file holds the accessors of the change engine's tables (schema version
// 3): change_log, pending_change, consumer and store_meta. The store keeps opaque
// kinds, ids and JSON; it knows nothing of GitHub, of change kinds or of the
// settle rule. Every Put is an insert-or-replace, a Delete of a row that does
// not exist is a no-op, and a missing row for a Get is reported through the
// boolean, never as an error.

// rawJSON turns an opaque JSON document into its column text ("" for none).
func rawJSON(m json.RawMessage) string { return string(m) }

// jsonOf reads an opaque JSON column back: the empty text is no document (nil).
func jsonOf(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func encodeKinds(kinds []string) (string, error) {
	if kinds == nil {
		kinds = []string{}
	}
	raw, err := json.Marshal(kinds)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeKinds(s string) ([]string, error) {
	var kinds []string
	if err := json.Unmarshal([]byte(s), &kinds); err != nil {
		return nil, err
	}
	return kinds, nil
}

// ---- change_log ---------------------------------------------------------

// ChangeRow is one row of the change log. Kind is the log kind (pr or ci), ID the
// wire reference, Fields an opaque JSON document the classifier owns, HeadSHA the
// head commit recorded with the row (the feed delivers the entity with the row's
// own version and head).
type ChangeRow struct {
	Seq             int64
	Kind, ID        string
	Version         int64
	HeadSHA         string
	Kinds           []string
	Fields          json.RawMessage
	Origin          string
	OriginUpdatedAt time.Time
	At              time.Time
}

const changeCols = `seq, kind, id, version, head_sha, kinds, fields, origin, origin_updated_at, at`

func scanChange(sc interface{ Scan(...any) error }) (ChangeRow, error) {
	var (
		c            ChangeRow
		kinds, flds  string
		originUpdate sql.NullInt64
		at           int64
	)
	if err := sc.Scan(&c.Seq, &c.Kind, &c.ID, &c.Version, &c.HeadSHA, &kinds, &flds, &c.Origin, &originUpdate, &at); err != nil {
		return ChangeRow{}, err
	}
	var err error
	if c.Kinds, err = decodeKinds(kinds); err != nil {
		return ChangeRow{}, fmt.Errorf("decode kinds of change %d: %w", c.Seq, err)
	}
	c.Fields = jsonOf(flds)
	c.OriginUpdatedAt = timeOf(originUpdate)
	c.At = time.UnixMilli(at).UTC()
	return c, nil
}

// AppendChange appends c to the change log and returns its sequence number:
// strictly increasing, dense, and never reused (not even after the newest rows
// were pruned; an append that is rolled back gives its number back). It ignores
// c.Seq.
func (t *Tx) AppendChange(ctx context.Context, c ChangeRow) (seq int64, err error) {
	kinds, err := encodeKinds(c.Kinds)
	if err != nil {
		return 0, fmt.Errorf("store: encode kinds of change %s/%s: %w", c.Kind, c.ID, err)
	}
	res, err := t.tx.ExecContext(ctx, `
INSERT INTO change_log (kind, id, version, head_sha, kinds, fields, origin, origin_updated_at, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Kind, c.ID, c.Version, c.HeadSHA, kinds, rawJSON(c.Fields), c.Origin,
		msOrNull(c.OriginUpdatedAt), c.At.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: append change %s/%s: %w", c.Kind, c.ID, err)
	}
	if seq, err = res.LastInsertId(); err != nil {
		return 0, fmt.Errorf("store: append change %s/%s: %w", c.Kind, c.ID, err)
	}
	return seq, nil
}

func (s *Store) queryChanges(ctx context.Context, what, query string, args ...any) ([]ChangeRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []ChangeRow
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, fmt.Errorf("store: %s: %w", what, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}

// ChangesAfter returns the rows with Seq greater than afterSeq in ascending Seq
// order; limit <= 0 means no limit.
func (s *Store) ChangesAfter(ctx context.Context, afterSeq int64, limit int) ([]ChangeRow, error) {
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	return s.queryChanges(ctx, "changes after",
		`SELECT `+changeCols+` FROM change_log WHERE seq > ? ORDER BY seq LIMIT ?`, afterSeq, limit)
}

// ChangeHead returns the highest sequence number ever assigned (0 for none). It
// is unaffected by pruning.
func (s *Store) ChangeHead(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'change_log'`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: change head: %w", err)
	}
	return seq, nil
}

// ChangeOldest returns the lowest retained sequence number; ok is false when the
// log is empty.
func (s *Store) ChangeOldest(ctx context.Context) (seq int64, ok bool, err error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT min(seq) FROM change_log`).Scan(&v); err != nil {
		return 0, false, fmt.Errorf("store: change oldest: %w", err)
	}
	return v.Int64, v.Valid, nil
}

// LastChanges returns the newest n rows of one entity (kind, id), newest first.
func (s *Store) LastChanges(ctx context.Context, kind, id string, n int) ([]ChangeRow, error) {
	if n <= 0 {
		return nil, nil
	}
	return s.queryChanges(ctx, "last changes",
		`SELECT `+changeCols+` FROM change_log WHERE kind = ? AND id = ? ORDER BY seq DESC LIMIT ?`, kind, id, n)
}

// PruneChangesBefore deletes the rows whose At is before t and returns how many.
// It never lowers ChangeHead: sequence numbers are not reused.
func (s *Store) PruneChangesBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM change_log WHERE at < ?`, t.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: prune changes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune changes: %w", err)
	}
	return n, nil
}

// ---- pending_change -----------------------------------------------------

// PendingChange is a detected change waiting for its settle barrier. Fields and
// Awaited are opaque JSON owned by the engine; ID is whatever entity key the
// engine chooses.
type PendingChange struct {
	Kind, ID        string
	Kinds           []string
	Fields, Awaited json.RawMessage
	HeadSHA         string
	BaseSHA         string
	Origin          string
	OriginUpdatedAt time.Time
	DetectedAt      time.Time
	Deadline        time.Time
}

const pendingCols = `kind, id, kinds, fields, awaited, head_sha, base_sha, origin, origin_updated_at, detected_at, deadline`

func scanPending(sc interface{ Scan(...any) error }) (PendingChange, error) {
	var (
		p                         PendingChange
		kinds, flds, awaited      string
		originUpdate, found, dead sql.NullInt64
	)
	if err := sc.Scan(&p.Kind, &p.ID, &kinds, &flds, &awaited, &p.HeadSHA, &p.BaseSHA, &p.Origin,
		&originUpdate, &found, &dead); err != nil {
		return PendingChange{}, err
	}
	var err error
	if p.Kinds, err = decodeKinds(kinds); err != nil {
		return PendingChange{}, fmt.Errorf("decode kinds of pending %s/%s: %w", p.Kind, p.ID, err)
	}
	p.Fields, p.Awaited = jsonOf(flds), jsonOf(awaited)
	p.OriginUpdatedAt, p.DetectedAt, p.Deadline = timeOf(originUpdate), timeOf(found), timeOf(dead)
	return p, nil
}

// GetPending returns the pending row of (kind, id); ok is false when there is
// none.
func (t *Tx) GetPending(ctx context.Context, kind, id string) (p PendingChange, ok bool, err error) {
	p, err = scanPending(t.tx.QueryRowContext(ctx,
		`SELECT `+pendingCols+` FROM pending_change WHERE kind = ? AND id = ?`, kind, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PendingChange{}, false, nil
	}
	if err != nil {
		return PendingChange{}, false, fmt.Errorf("store: get pending %s/%s: %w", kind, id, err)
	}
	return p, true, nil
}

// PutPending inserts or replaces the pending row of (p.Kind, p.ID).
func (t *Tx) PutPending(ctx context.Context, p PendingChange) error {
	kinds, err := encodeKinds(p.Kinds)
	if err != nil {
		return fmt.Errorf("store: encode kinds of pending %s/%s: %w", p.Kind, p.ID, err)
	}
	_, err = t.tx.ExecContext(ctx, `
INSERT INTO pending_change (`+pendingCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (kind, id) DO UPDATE SET
  kinds = excluded.kinds, fields = excluded.fields, awaited = excluded.awaited,
  head_sha = excluded.head_sha, base_sha = excluded.base_sha, origin = excluded.origin,
  origin_updated_at = excluded.origin_updated_at, detected_at = excluded.detected_at,
  deadline = excluded.deadline`,
		p.Kind, p.ID, kinds, rawJSON(p.Fields), rawJSON(p.Awaited), p.HeadSHA, p.BaseSHA, p.Origin,
		msOrNull(p.OriginUpdatedAt), msOrNull(p.DetectedAt), msOrNull(p.Deadline))
	if err != nil {
		return fmt.Errorf("store: put pending %s/%s: %w", p.Kind, p.ID, err)
	}
	return nil
}

// DeletePending removes the pending row of (kind, id) (no error when absent).
func (t *Tx) DeletePending(ctx context.Context, kind, id string) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM pending_change WHERE kind = ? AND id = ?`, kind, id); err != nil {
		return fmt.Errorf("store: delete pending %s/%s: %w", kind, id, err)
	}
	return nil
}

// ListPending returns every pending row by ascending Deadline (a row with no
// deadline first; ties by kind then id).
func (s *Store) ListPending(ctx context.Context) ([]PendingChange, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+pendingCols+` FROM pending_change ORDER BY deadline, kind, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list pending: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PendingChange
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list pending: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list pending: %w", err)
	}
	return out, nil
}

// CountPending returns the number of pending rows.
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM pending_change`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count pending: %w", err)
	}
	return n, nil
}

// ---- consumer -----------------------------------------------------------

// ConsumerRow is one feed cursor, keyed (Consumer, Kind, Query).
type ConsumerRow struct {
	Consumer, Kind, Query string
	AckedSeq              int64
	SeenAt                time.Time
}

const consumerCols = `consumer, kind, query, acked_seq, seen_at`

func scanConsumer(sc interface{ Scan(...any) error }) (ConsumerRow, error) {
	var (
		c    ConsumerRow
		seen int64
	)
	if err := sc.Scan(&c.Consumer, &c.Kind, &c.Query, &c.AckedSeq, &seen); err != nil {
		return ConsumerRow{}, err
	}
	c.SeenAt = time.UnixMilli(seen).UTC()
	return c, nil
}

func getConsumer(ctx context.Context, db dbtx, consumer, kind, query string) (c ConsumerRow, ok bool, err error) {
	c, err = scanConsumer(db.QueryRowContext(ctx,
		`SELECT `+consumerCols+` FROM consumer WHERE consumer = ? AND kind = ? AND query = ?`, consumer, kind, query))
	if errors.Is(err, sql.ErrNoRows) {
		return ConsumerRow{}, false, nil
	}
	if err != nil {
		return ConsumerRow{}, false, fmt.Errorf("store: get consumer %q/%s/%q: %w", consumer, kind, query, err)
	}
	return c, true, nil
}

func putConsumer(ctx context.Context, db dbtx, c ConsumerRow) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO consumer (`+consumerCols+`) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (consumer, kind, query) DO UPDATE SET
  acked_seq = excluded.acked_seq, seen_at = excluded.seen_at`,
		c.Consumer, c.Kind, c.Query, c.AckedSeq, c.SeenAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put consumer %q/%s/%q: %w", c.Consumer, c.Kind, c.Query, err)
	}
	return nil
}

func deleteConsumer(ctx context.Context, db dbtx, consumer, kind, query string) error {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM consumer WHERE consumer = ? AND kind = ? AND query = ?`, consumer, kind, query); err != nil {
		return fmt.Errorf("store: delete consumer %q/%s/%q: %w", consumer, kind, query, err)
	}
	return nil
}

func listConsumers(ctx context.Context, db dbtx) ([]ConsumerRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+consumerCols+` FROM consumer ORDER BY consumer, kind, query`)
	if err != nil {
		return nil, fmt.Errorf("store: list consumers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ConsumerRow
	for rows.Next() {
		c, err := scanConsumer(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list consumers: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list consumers: %w", err)
	}
	return out, nil
}

func deleteConsumersSeenBefore(ctx context.Context, db dbtx, t time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM consumer WHERE seen_at < ?`, t.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: delete stale consumers: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete stale consumers: %w", err)
	}
	return n, nil
}

// GetConsumer returns the cursor of (consumer, kind, query); ok is false when
// there is none.
func (s *Store) GetConsumer(ctx context.Context, consumer, kind, query string) (ConsumerRow, bool, error) {
	return getConsumer(ctx, s.db, consumer, kind, query)
}

// PutConsumer inserts or replaces the cursor of (c.Consumer, c.Kind, c.Query).
func (s *Store) PutConsumer(ctx context.Context, c ConsumerRow) error {
	return putConsumer(ctx, s.db, c)
}

// DeleteConsumer removes one cursor (no error when absent).
func (s *Store) DeleteConsumer(ctx context.Context, consumer, kind, query string) error {
	return deleteConsumer(ctx, s.db, consumer, kind, query)
}

// ListConsumers returns every cursor ordered by consumer, kind, query.
func (s *Store) ListConsumers(ctx context.Context) ([]ConsumerRow, error) {
	return listConsumers(ctx, s.db)
}

// DeleteConsumersSeenBefore removes the cursors last seen before t and returns
// how many.
func (s *Store) DeleteConsumersSeenBefore(ctx context.Context, t time.Time) (int64, error) {
	return deleteConsumersSeenBefore(ctx, s.db, t)
}

// GetConsumer is Store.GetConsumer inside the transaction.
func (t *Tx) GetConsumer(ctx context.Context, consumer, kind, query string) (ConsumerRow, bool, error) {
	return getConsumer(ctx, t.tx, consumer, kind, query)
}

// PutConsumer is Store.PutConsumer inside the transaction.
func (t *Tx) PutConsumer(ctx context.Context, c ConsumerRow) error { return putConsumer(ctx, t.tx, c) }

// DeleteConsumer is Store.DeleteConsumer inside the transaction.
func (t *Tx) DeleteConsumer(ctx context.Context, consumer, kind, query string) error {
	return deleteConsumer(ctx, t.tx, consumer, kind, query)
}

// ListConsumers is Store.ListConsumers inside the transaction.
func (t *Tx) ListConsumers(ctx context.Context) ([]ConsumerRow, error) {
	return listConsumers(ctx, t.tx)
}

// DeleteConsumersSeenBefore is Store.DeleteConsumersSeenBefore inside the
// transaction.
func (t *Tx) DeleteConsumersSeenBefore(ctx context.Context, at time.Time) (int64, error) {
	return deleteConsumersSeenBefore(ctx, t.tx, at)
}

// ---- store_meta ---------------------------------------------------------

func getMeta(ctx context.Context, db dbtx, key string) (value string, ok bool, err error) {
	err = db.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get meta %q: %w", key, err)
	}
	return value, true, nil
}

func putMeta(ctx context.Context, db dbtx, key, value string) error {
	if _, err := db.ExecContext(ctx, `
INSERT INTO store_meta (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("store: put meta %q: %w", key, err)
	}
	return nil
}

// GetMeta returns the value stored under key; a missing key returns
// ("", false, nil).
func (s *Store) GetMeta(ctx context.Context, key string) (value string, ok bool, err error) {
	return getMeta(ctx, s.db, key)
}

// PutMeta stores value under key (insert or replace).
func (s *Store) PutMeta(ctx context.Context, key, value string) error {
	return putMeta(ctx, s.db, key, value)
}

// GetMeta is Store.GetMeta inside the transaction.
func (t *Tx) GetMeta(ctx context.Context, key string) (value string, ok bool, err error) {
	return getMeta(ctx, t.tx, key)
}

// PutMeta is Store.PutMeta inside the transaction.
func (t *Tx) PutMeta(ctx context.Context, key, value string) error {
	return putMeta(ctx, t.tx, key, value)
}
