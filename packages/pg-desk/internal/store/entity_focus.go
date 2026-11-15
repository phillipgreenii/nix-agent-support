package store

import (
	"context"
	"database/sql"
	"fmt"
)

// entityFocusDoc returns the SQL expression of entity.focus_facts for the
// facts column reference facts (NEW.facts in a trigger): the small part of an
// entity's facts the focus health computation reads, as a JSON document of
// the same shape as the facts (pr_show, issue_show and thread_show objects
// holding only the keys the focus decoders read, so those decoders run on it
// unchanged).
//
// It is guarded by json_valid, so a row whose facts are empty or not JSON gets
// ” instead of failing the write. The `->` operator returns each member as
// JSON text, so booleans, arrays and objects survive intact inside
// json_object.
//
// Keys carried (each a member the focus decoders read; see focus.decodeFacts,
// focus.decodeEntityFacts, classify.SourceTerminal and classify.IssueStatusKind):
//
//	pr_show:     state, merged, review_requests, title
//	issue_show:  state, status_category, labels, metadata, assignee, parent,
//	             issue_type, deps, due_date, priority, created_at, title
//	thread_show: last_reply_at
//
// The issue comments are NOT carried (the explain verb reads them, through the
// full-facts reader).
func entityFocusDoc(facts string) string {
	m := func(path string) string { return facts + ` -> '` + path + `'` }
	return `CASE WHEN json_valid(` + facts + `) THEN json_object(
  'pr_show', json_object('state', ` + m("$.pr_show.state") + `, 'merged', ` + m("$.pr_show.merged") + `,
    'review_requests', ` + m("$.pr_show.review_requests") + `, 'title', ` + m("$.pr_show.title") + `),
  'issue_show', json_object('state', ` + m("$.issue_show.state") + `, 'status_category', ` + m("$.issue_show.status_category") + `,
    'labels', ` + m("$.issue_show.labels") + `, 'metadata', ` + m("$.issue_show.metadata") + `,
    'assignee', ` + m("$.issue_show.assignee") + `, 'parent', ` + m("$.issue_show.parent") + `,
    'issue_type', ` + m("$.issue_show.issue_type") + `, 'deps', ` + m("$.issue_show.deps") + `,
    'due_date', ` + m("$.issue_show.due_date") + `, 'priority', ` + m("$.issue_show.priority") + `,
    'created_at', ` + m("$.issue_show.created_at") + `, 'title', ` + m("$.issue_show.title") + `),
  'thread_show', json_object('last_reply_at', ` + m("$.thread_show.last_reply_at") + `)) ELSE '' END`
}

// The projection is a real column kept current by two triggers, not a
// generated column: SQLite never answers a query that selects a generated
// column from a covering index (it recomputes the value from the facts), so a
// generated column would still read every facts overflow page. Triggers (not
// writes from Go) keep it current for every writer of entity.facts, including
// raw SQL and an older binary opening the same file, and need no change to any
// writer.
var entityFocusTriggerDDL = []string{
	`CREATE TRIGGER IF NOT EXISTS entity_focus_facts_ins AFTER INSERT ON entity BEGIN
  UPDATE entity SET focus_facts = ` + entityFocusDoc("NEW.facts") + `
  WHERE repo = NEW.repo AND entity_type = NEW.entity_type AND entity_id = NEW.entity_id;
END`,
	`CREATE TRIGGER IF NOT EXISTS entity_focus_facts_upd AFTER UPDATE OF facts ON entity BEGIN
  UPDATE entity SET focus_facts = ` + entityFocusDoc("NEW.facts") + `
  WHERE repo = NEW.repo AND entity_type = NEW.entity_type AND entity_id = NEW.entity_id;
END`,
}

// entityFocusBackfillSQL fills focus_facts for rows that have none yet (rows
// that predate the column). It runs once, when the column is added.
var entityFocusBackfillSQL = `UPDATE entity SET focus_facts = ` + entityFocusDoc("facts") + ` WHERE focus_facts IS NULL`

// entityFocusIndexDDL is the covering index behind ListEntitiesForFocus: every
// column that query returns, so SQLite answers it from the index alone and
// never reads the facts overflow pages (about 37 KB per row).
const entityFocusIndexDDL = `CREATE INDEX IF NOT EXISTS entity_focus ON entity(
  repo, entity_type, entity_id, focus_facts, as_of, stale, content_hash, head_sha, version,
  hydrated_at, active, list_fp, first_seen_at, backend, watched, read_attempt_at, read_ok_at)`

// ensureEntityFocusStorage adds entity.focus_facts, its two triggers, a
// one-time backfill of the existing rows, and the entity_focus index. Like
// ensureIssueFeedStorage, which calls it, it is additive and idempotent, never
// touches user_version, tolerates a concurrent opener, and leaves a file with
// no entity table alone. The backfill reads and rewrites every existing row
// once (the live store's about 100 MB of facts, on the first open after the
// upgrade); later opens only check the index for a missing value.
func ensureEntityFocusStorage(ctx context.Context, s *Store) error {
	have, err := s.tableColumns(ctx, "entity")
	if err != nil {
		return err
	}
	if len(have) == 0 {
		return nil
	}
	if !have["focus_facts"] {
		if _, err := s.sql.ExecContext(ctx, `ALTER TABLE entity ADD COLUMN focus_facts TEXT`); err != nil {
			again, cerr := s.tableColumns(ctx, "entity")
			if cerr != nil || !again["focus_facts"] {
				return fmt.Errorf("add entity.focus_facts: %w", err)
			}
		}
	}
	// The triggers first, so a row written during the backfill is covered.
	for _, ddl := range entityFocusTriggerDDL {
		if _, err := s.sql.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create entity focus trigger: %w", err)
		}
	}
	if _, err := s.sql.ExecContext(ctx, entityFocusIndexDDL); err != nil {
		return fmt.Errorf("create entity_focus index: %w", err)
	}
	var missing int
	if err := s.sql.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entity INDEXED BY entity_focus WHERE focus_facts IS NULL)`).Scan(&missing); err != nil {
		return fmt.Errorf("check entity.focus_facts: %w", err)
	}
	if missing != 0 {
		if _, err := s.sql.ExecContext(ctx, entityFocusBackfillSQL); err != nil {
			return fmt.Errorf("backfill entity.focus_facts: %w", err)
		}
	}
	return nil
}

// ListEntitiesForFocus returns every entity row, ordered like ListEntities, but
// with Facts set to the pruned document entity.focus_facts (see
// entityFocusColumn) instead of the full facts. Every other field is the same
// as ListEntities returns. It is served from the covering index entity_focus
// and reads no facts bytes (INDEXED BY, because without statistics the planner
// would pick the primary key index and walk the table rows). On a store opened
// without that column or index (read-only or raw, predating it) it falls back to ListEntities, whose full Facts hold
// every key the pruned document would.
func (s *Store) ListEntitiesForFocus(ctx context.Context) ([]Entity, error) {
	have, err := s.tableColumns(ctx, "entity")
	if err != nil {
		return nil, err
	}
	var indexed int
	if err := s.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('entity') WHERE name = 'entity_focus'`).Scan(&indexed); err != nil {
		return nil, fmt.Errorf("store: read entity indexes: %w", err)
	}
	if !have["focus_facts"] || indexed == 0 {
		return s.ListEntities(ctx)
	}
	feedCols, err := s.issueFeedSelect(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.sql.QueryContext(
		ctx,
		`SELECT repo, entity_type, entity_id, focus_facts, as_of, stale, content_hash, head_sha, version, hydrated_at, active, list_fp, first_seen_at, `+feedCols+`
		 FROM entity INDEXED BY entity_focus ORDER BY repo, entity_type, entity_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list entities for focus: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Entity
	for rows.Next() {
		var e Entity
		var headSHA, hydratedAt, listFP, firstSeenAt, backend, readAttempt, readOK, facts sql.NullString
		var watched sql.NullInt64
		var active int
		if err := rows.Scan(&e.Repo, &e.EntityType, &e.EntityID, &facts, &e.AsOf, &e.Stale, &e.ContentHash, &headSHA, &e.Version, &hydratedAt, &active, &listFP, &firstSeenAt, &backend, &watched, &readAttempt, &readOK); err != nil {
			return nil, fmt.Errorf("store: scan entity focus row: %w", err)
		}
		e.Facts = facts.String
		e.HeadSHA = headSHA.String
		e.HydratedAt = hydratedAt.String
		e.Inactive = active == 0
		e.ListFP = listFP.String
		e.FirstSeenAt = firstSeenAt.String
		applyIssueFeedColumns(&e, backend, watched, readAttempt, readOK)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list entities for focus: %w", err)
	}
	return out, nil
}
