package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file holds the typed accessors for the version 1 tables (the change engine's
// tables are in changes.go). They
// store and fetch rows; what a row MEANS (alias resolution, group content,
// budget accounting) belongs to the packages that use them. A zero
// time.Time is stored as NULL and read back as the zero time.

func msOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func timeOf(ms sql.NullInt64) time.Time {
	if !ms.Valid {
		return time.Time{}
	}
	return time.UnixMilli(ms.Int64).UTC()
}

// dbtx is what the shared statement helpers need: both *sql.DB (a statement of
// its own) and *sql.Tx (a statement inside a store transaction, see Tx)
// satisfy it, so a Store method and its Tx namesake run the same SQL.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- query -----------------------------------------------------------

// QueryRow is one cached query: its canonical key and the ordered entity
// ids it returned.
type QueryRow struct {
	Key           string
	Configured    bool
	IDs           []string
	Complete      bool
	FetchedAt     time.Time
	FreshUntil    time.Time
	LastSuccessAt time.Time
	LastError     string
	// BaselineAt is when the first complete run of a configured query
	// established its membership baseline; the zero time (NULL) means no
	// baseline yet.
	BaselineAt time.Time
}

// PutQuery inserts or replaces the row for q.Key. A zero q.BaselineAt keeps the
// stored baseline (it never clears one); a non-zero one replaces it.
func (s *Store) PutQuery(ctx context.Context, q QueryRow) error {
	return putQuery(ctx, s.db, q)
}

func putQuery(ctx context.Context, db dbtx, q QueryRow) error {
	ids := q.IDs
	if ids == nil {
		ids = []string{}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("store: encode query ids: %w", err)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO query (key, configured, ids, complete, fetched_at, fresh_until, last_success_at, last_error, baseline_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET
  configured = excluded.configured, ids = excluded.ids, complete = excluded.complete,
  fetched_at = excluded.fetched_at, fresh_until = excluded.fresh_until,
  last_success_at = excluded.last_success_at, last_error = excluded.last_error,
  baseline_at = COALESCE(excluded.baseline_at, query.baseline_at)`,
		q.Key, boolInt(q.Configured), string(raw), boolInt(q.Complete),
		msOrNull(q.FetchedAt), msOrNull(q.FreshUntil), msOrNull(q.LastSuccessAt), q.LastError,
		msOrNull(q.BaselineAt))
	if err != nil {
		return fmt.Errorf("store: put query %q: %w", q.Key, err)
	}
	return nil
}

const queryCols = `key, configured, ids, complete, fetched_at, fresh_until, last_success_at, last_error, baseline_at`

func scanQuery(sc interface{ Scan(...any) error }) (QueryRow, error) {
	var (
		q                        QueryRow
		configured, complete     int
		ids                      string
		fetched, fresh, lastSucc sql.NullInt64
		baseline                 sql.NullInt64
	)
	if err := sc.Scan(&q.Key, &configured, &ids, &complete, &fetched, &fresh, &lastSucc, &q.LastError, &baseline); err != nil {
		return QueryRow{}, err
	}
	if err := json.Unmarshal([]byte(ids), &q.IDs); err != nil {
		return QueryRow{}, fmt.Errorf("decode ids of query %q: %w", q.Key, err)
	}
	q.Configured, q.Complete = configured != 0, complete != 0
	q.FetchedAt, q.FreshUntil, q.LastSuccessAt = timeOf(fetched), timeOf(fresh), timeOf(lastSucc)
	q.BaselineAt = timeOf(baseline)
	return q, nil
}

// GetQuery returns the row for key; ok is false when there is none.
func (s *Store) GetQuery(ctx context.Context, key string) (QueryRow, bool, error) {
	return getQuery(ctx, s.db, key)
}

func getQuery(ctx context.Context, db dbtx, key string) (q QueryRow, ok bool, err error) {
	q, err = scanQuery(db.QueryRowContext(ctx, `SELECT `+queryCols+` FROM query WHERE key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return QueryRow{}, false, nil
	}
	if err != nil {
		return QueryRow{}, false, fmt.Errorf("store: get query %q: %w", key, err)
	}
	return q, true, nil
}

// ListQueries returns every query row ordered by key; configuredOnly keeps
// only the configured ones.
func (s *Store) ListQueries(ctx context.Context, configuredOnly bool) ([]QueryRow, error) {
	return listQueries(ctx, s.db, configuredOnly)
}

func listQueries(ctx context.Context, db dbtx, configuredOnly bool) ([]QueryRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+queryCols+` FROM query WHERE (? = 0 OR configured = 1) ORDER BY key`, boolInt(configuredOnly))
	if err != nil {
		return nil, fmt.Errorf("store: list queries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []QueryRow
	for rows.Next() {
		q, err := scanQuery(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list queries: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// DeleteQuery removes the row for key (no error when absent).
func (s *Store) DeleteQuery(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM query WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete query %q: %w", key, err)
	}
	return nil
}

// ---- alias -----------------------------------------------------------

// AliasRow maps an opaque node id to a human reference (owner/repo#n).
type AliasRow struct {
	NodeID string
	Ref    string
	SeenAt time.Time
}

// PutAlias inserts or replaces the row for a.NodeID.
func (s *Store) PutAlias(ctx context.Context, a AliasRow) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO alias (node_id, ref, seen_at) VALUES (?, ?, ?)
ON CONFLICT (node_id) DO UPDATE SET ref = excluded.ref, seen_at = excluded.seen_at`,
		a.NodeID, a.Ref, a.SeenAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put alias %q: %w", a.NodeID, err)
	}
	return nil
}

func (s *Store) getAlias(ctx context.Context, col, val string) (a AliasRow, ok bool, err error) {
	var seen int64
	err = s.db.QueryRowContext(ctx,
		`SELECT node_id, ref, seen_at FROM alias WHERE `+col+` = ? ORDER BY seen_at DESC LIMIT 1`, val).
		Scan(&a.NodeID, &a.Ref, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return AliasRow{}, false, nil
	}
	if err != nil {
		return AliasRow{}, false, fmt.Errorf("store: get alias by %s %q: %w", col, val, err)
	}
	a.SeenAt = time.UnixMilli(seen).UTC()
	return a, true, nil
}

// AliasByNodeID returns the alias of a node id.
func (s *Store) AliasByNodeID(ctx context.Context, nodeID string) (AliasRow, bool, error) {
	return s.getAlias(ctx, "node_id", nodeID)
}

// AliasByRef returns the alias for an owner/repo#n reference (the most
// recently seen one when several node ids carry it).
func (s *Store) AliasByRef(ctx context.Context, ref string) (AliasRow, bool, error) {
	return s.getAlias(ctx, "ref", ref)
}

// AliasByRefFold is AliasByRef matching the reference case-insensitively (ASCII
// folding, which covers GitHub owner and repository names), the most recently
// seen row first. It does not use the alias_ref index; the table holds one row
// per entity the daemon has seen, which is small enough for a scan.
func (s *Store) AliasByRefFold(ctx context.Context, ref string) (AliasRow, bool, error) {
	return s.getAlias(ctx, "lower(ref)", strings.ToLower(ref))
}

// DeleteAlias removes the row for nodeID (no error when absent).
func (s *Store) DeleteAlias(ctx context.Context, nodeID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM alias WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("store: delete alias %q: %w", nodeID, err)
	}
	return nil
}

// ---- entity ----------------------------------------------------------

// EntityRow is the per-entity bookkeeping row.
type EntityRow struct {
	Kind               string
	ID                 string
	Version            int64
	LastAccess         time.Time
	KeepAlive          bool
	LastKnownMergeable string
	TombstonedAt       time.Time
}

// PutEntity inserts or replaces the row for (e.Kind, e.ID).
func (s *Store) PutEntity(ctx context.Context, e EntityRow) error {
	return putEntity(ctx, s.db, e)
}

func putEntity(ctx context.Context, db dbtx, e EntityRow) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO entity (kind, id, version, last_access, keep_alive, last_known_mergeable, tombstoned_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (kind, id) DO UPDATE SET
  version = excluded.version, last_access = excluded.last_access, keep_alive = excluded.keep_alive,
  last_known_mergeable = excluded.last_known_mergeable, tombstoned_at = excluded.tombstoned_at`,
		e.Kind, e.ID, e.Version, msOrNull(e.LastAccess), boolInt(e.KeepAlive),
		e.LastKnownMergeable, msOrNull(e.TombstonedAt))
	if err != nil {
		return fmt.Errorf("store: put entity %s/%s: %w", e.Kind, e.ID, err)
	}
	return nil
}

// TouchEntity records an access to (kind, id): last_access = at and keep_alive
// = true, creating the row when there is none. It changes no other column, in
// one statement, so a concurrent writer of another column (the summary's
// last_known_mergeable) is never overwritten by a stale copy of the row, which
// a read-modify-write PutEntity can do.
func (s *Store) TouchEntity(ctx context.Context, kind, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO entity (kind, id, version, last_access, keep_alive, last_known_mergeable, tombstoned_at)
VALUES (?, ?, 0, ?, 1, '', NULL)
ON CONFLICT (kind, id) DO UPDATE SET last_access = excluded.last_access, keep_alive = 1`,
		kind, id, msOrNull(at))
	if err != nil {
		return fmt.Errorf("store: touch entity %s/%s: %w", kind, id, err)
	}
	return nil
}

// EnsureEntity creates an empty row for (kind, id) when there is none and
// leaves an existing row exactly as it is.
func (s *Store) EnsureEntity(ctx context.Context, kind, id string) error {
	return ensureEntity(ctx, s.db, kind, id)
}

func ensureEntity(ctx context.Context, db dbtx, kind, id string) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO entity (kind, id, version, last_access, keep_alive, last_known_mergeable, tombstoned_at)
VALUES (?, ?, 0, NULL, 0, '', NULL)
ON CONFLICT (kind, id) DO NOTHING`, kind, id)
	if err != nil {
		return fmt.Errorf("store: ensure entity %s/%s: %w", kind, id, err)
	}
	return nil
}

const entityCols = `kind, id, version, last_access, keep_alive, last_known_mergeable, tombstoned_at`

func scanEntity(sc interface{ Scan(...any) error }) (EntityRow, error) {
	var (
		e            EntityRow
		access, tomb sql.NullInt64
		keep         int
	)
	if err := sc.Scan(&e.Kind, &e.ID, &e.Version, &access, &keep, &e.LastKnownMergeable, &tomb); err != nil {
		return EntityRow{}, err
	}
	e.LastAccess, e.KeepAlive, e.TombstonedAt = timeOf(access), keep != 0, timeOf(tomb)
	return e, nil
}

// GetEntity returns the row for (kind, id); ok is false when there is none.
func (s *Store) GetEntity(ctx context.Context, kind, id string) (EntityRow, bool, error) {
	return getEntity(ctx, s.db, kind, id)
}

func getEntity(ctx context.Context, db dbtx, kind, id string) (e EntityRow, ok bool, err error) {
	e, err = scanEntity(db.QueryRowContext(ctx,
		`SELECT `+entityCols+` FROM entity WHERE kind = ? AND id = ?`, kind, id))
	if errors.Is(err, sql.ErrNoRows) {
		return EntityRow{}, false, nil
	}
	if err != nil {
		return EntityRow{}, false, fmt.Errorf("store: get entity %s/%s: %w", kind, id, err)
	}
	return e, true, nil
}

// ListEntities returns every entity row of one kind ordered by id.
func (s *Store) ListEntities(ctx context.Context, kind string) ([]EntityRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+entityCols+` FROM entity WHERE kind = ? ORDER BY id`, kind)
	if err != nil {
		return nil, fmt.Errorf("store: list entities %s: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	var out []EntityRow
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list entities %s: %w", kind, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteEntity removes the entity row AND its groups, in one transaction.
func (s *Store) DeleteEntity(ctx context.Context, kind, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete entity %s/%s: %w", kind, id, err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM entity_group WHERE kind = ? AND id = ?`,
		`DELETE FROM entity WHERE kind = ? AND id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, kind, id); err != nil {
			return fmt.Errorf("store: delete entity %s/%s: %w", kind, id, err)
		}
	}
	return tx.Commit()
}

// ---- entity_group ----------------------------------------------------

// GroupRow is one cached field group of one entity.
type GroupRow struct {
	Kind           string
	ID             string
	Group          string
	Content        []byte
	Fingerprint    string
	FetchedAt      time.Time
	FreshUntil     time.Time
	MaxAgeAt       time.Time
	LastError      string
	Generation     int64
	FetchedForHead string
	FetchedForBase string
}

// PutGroup inserts or replaces the row for (g.Kind, g.ID, g.Group).
func (s *Store) PutGroup(ctx context.Context, g GroupRow) error {
	return putGroup(ctx, s.db, g)
}

func putGroup(ctx context.Context, db dbtx, g GroupRow) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO entity_group (kind, id, group_name, content, fingerprint, fetched_at, fresh_until,
                          max_age_at, last_error, generation, fetched_for_head, fetched_for_base)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (kind, id, group_name) DO UPDATE SET
  content = excluded.content, fingerprint = excluded.fingerprint, fetched_at = excluded.fetched_at,
  fresh_until = excluded.fresh_until, max_age_at = excluded.max_age_at, last_error = excluded.last_error,
  generation = excluded.generation, fetched_for_head = excluded.fetched_for_head,
  fetched_for_base = excluded.fetched_for_base`,
		g.Kind, g.ID, g.Group, g.Content, g.Fingerprint, msOrNull(g.FetchedAt), msOrNull(g.FreshUntil),
		msOrNull(g.MaxAgeAt), g.LastError, g.Generation, g.FetchedForHead, g.FetchedForBase)
	if err != nil {
		return fmt.Errorf("store: put group %s/%s/%s: %w", g.Kind, g.ID, g.Group, err)
	}
	return nil
}

const groupCols = `kind, id, group_name, content, fingerprint, fetched_at, fresh_until, max_age_at,
last_error, generation, fetched_for_head, fetched_for_base`

func scanGroup(sc interface{ Scan(...any) error }) (GroupRow, error) {
	var (
		g                  GroupRow
		fetched, fresh, mx sql.NullInt64
	)
	if err := sc.Scan(&g.Kind, &g.ID, &g.Group, &g.Content, &g.Fingerprint, &fetched, &fresh, &mx,
		&g.LastError, &g.Generation, &g.FetchedForHead, &g.FetchedForBase); err != nil {
		return GroupRow{}, err
	}
	g.FetchedAt, g.FreshUntil, g.MaxAgeAt = timeOf(fetched), timeOf(fresh), timeOf(mx)
	return g, nil
}

// GetGroup returns one group; ok is false when there is none.
func (s *Store) GetGroup(ctx context.Context, kind, id, group string) (GroupRow, bool, error) {
	return getGroup(ctx, s.db, kind, id, group)
}

func getGroup(ctx context.Context, db dbtx, kind, id, group string) (g GroupRow, ok bool, err error) {
	g, err = scanGroup(db.QueryRowContext(ctx,
		`SELECT `+groupCols+` FROM entity_group WHERE kind = ? AND id = ? AND group_name = ?`, kind, id, group))
	if errors.Is(err, sql.ErrNoRows) {
		return GroupRow{}, false, nil
	}
	if err != nil {
		return GroupRow{}, false, fmt.Errorf("store: get group %s/%s/%s: %w", kind, id, group, err)
	}
	return g, true, nil
}

// ListGroups returns every group of one entity ordered by group name.
func (s *Store) ListGroups(ctx context.Context, kind, id string) ([]GroupRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+groupCols+` FROM entity_group WHERE kind = ? AND id = ? ORDER BY group_name`, kind, id)
	if err != nil {
		return nil, fmt.Errorf("store: list groups %s/%s: %w", kind, id, err)
	}
	defer func() { _ = rows.Close() }()
	var out []GroupRow
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list groups %s/%s: %w", kind, id, err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteGroup removes one group (no error when absent).
func (s *Store) DeleteGroup(ctx context.Context, kind, id, group string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM entity_group WHERE kind = ? AND id = ? AND group_name = ?`, kind, id, group); err != nil {
		return fmt.Errorf("store: delete group %s/%s/%s: %w", kind, id, group, err)
	}
	return nil
}

// ---- budget ----------------------------------------------------------

// BudgetSample is one observation of a rate-limit bucket.
type BudgetSample struct {
	Bucket    string
	At        time.Time
	Remaining int64
	ResetAt   time.Time
	Spent     int64
	Purpose   string
}

// AppendBudget records one sample (the table is an append-only series).
func (s *Store) AppendBudget(ctx context.Context, b BudgetSample) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO budget (bucket, at, remaining, reset_at, spent, purpose) VALUES (?, ?, ?, ?, ?, ?)`,
		b.Bucket, b.At.UnixMilli(), b.Remaining, msOrNull(b.ResetAt), b.Spent, b.Purpose)
	if err != nil {
		return fmt.Errorf("store: append budget %q: %w", b.Bucket, err)
	}
	return nil
}

func scanBudget(sc interface{ Scan(...any) error }) (BudgetSample, error) {
	var (
		b       BudgetSample
		at      int64
		resetAt sql.NullInt64
	)
	if err := sc.Scan(&b.Bucket, &at, &b.Remaining, &resetAt, &b.Spent, &b.Purpose); err != nil {
		return BudgetSample{}, err
	}
	b.At, b.ResetAt = time.UnixMilli(at).UTC(), timeOf(resetAt)
	return b, nil
}

// LatestBudget returns the newest sample of a bucket (ties broken by
// insertion order); ok is false when the bucket has none.
func (s *Store) LatestBudget(ctx context.Context, bucket string) (b BudgetSample, ok bool, err error) {
	b, err = scanBudget(s.db.QueryRowContext(ctx, `
SELECT bucket, at, remaining, reset_at, spent, purpose FROM budget
WHERE bucket = ? ORDER BY at DESC, seq DESC LIMIT 1`, bucket))
	if errors.Is(err, sql.ErrNoRows) {
		return BudgetSample{}, false, nil
	}
	if err != nil {
		return BudgetSample{}, false, fmt.Errorf("store: latest budget %q: %w", bucket, err)
	}
	return b, true, nil
}

// BudgetSince returns a bucket's samples at or after since, oldest first.
func (s *Store) BudgetSince(ctx context.Context, bucket string, since time.Time) ([]BudgetSample, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT bucket, at, remaining, reset_at, spent, purpose FROM budget
WHERE bucket = ? AND at >= ? ORDER BY at, seq`, bucket, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("store: budget since %q: %w", bucket, err)
	}
	defer func() { _ = rows.Close() }()
	var out []BudgetSample
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, fmt.Errorf("store: budget since %q: %w", bucket, err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PruneBudgetBefore deletes samples older than t and returns how many.
func (s *Store) PruneBudgetBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM budget WHERE at < ?`, t.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: prune budget: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune budget: %w", err)
	}
	return n, nil
}
