package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// SchemaVersion is the newest schema version this binary knows. A store
// above it is refused (ErrSchemaNewer); a store below it is migrated up.
const SchemaVersion = 9

// builtinMigrations is the ladder; len(builtinMigrations) == SchemaVersion
// (asserted by a test). A migration, once shipped, MUST NOT change: add a
// new step and a fixture store for the version it produces under testdata/.
var builtinMigrations = []Migration{
	{Version: 1, Name: "query, alias, entity, entity_group, budget", Apply: migrateV1},
	{Version: 2, Name: "job", Apply: migrateV2},
	{Version: 3, Name: "pending_change, change_log, consumer", Apply: migrateV3},
	{Version: 4, Name: "store identity", Apply: migrateV4},
	{Version: 5, Name: "watched flag and log snapshot", Apply: migrateV5},
	{Version: 6, Name: "consumer cursor per consumer", Apply: migrateV6},
	{Version: 7, Name: "entity closed-at", Apply: migrateV7},
	{Version: 8, Name: "alias folded-ref index", Apply: migrateV8},
	{Version: 9, Name: "posted_review", Apply: migrateV9},
}

// BuiltinMigrations returns a copy of the built-in ladder. A caller that wants
// to wrap a step (a test that makes one step slow, to watch the daemon answer
// "migrating:" meanwhile) passes the edited copy as Options.Migrations; the
// steps' Version numbers must stay 1..n.
func BuiltinMigrations() []Migration {
	return append([]Migration(nil), builtinMigrations...)
}

// Times are INTEGER Unix milliseconds (NULL = unset). Booleans are INTEGER
// 0/1. A group's `content` is an opaque BLOB owned by the group's Strategy.
//
// Version 1 holds the five tables below. The job table arrives with version 2
// (schemaV2); pending_change, change_log and consumer arrive by later
// migrations.
const schemaV1 = `
CREATE TABLE query (
    key             TEXT    NOT NULL PRIMARY KEY,
    configured      INTEGER NOT NULL DEFAULT 0,
    ids             TEXT    NOT NULL DEFAULT '[]',
    complete        INTEGER NOT NULL DEFAULT 0,
    fetched_at      INTEGER,
    fresh_until     INTEGER,
    last_success_at INTEGER,
    last_error      TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE alias (
    node_id  TEXT    NOT NULL PRIMARY KEY,
    ref      TEXT    NOT NULL,
    seen_at  INTEGER NOT NULL
);
CREATE INDEX alias_ref ON alias (ref);

CREATE TABLE entity (
    kind                 TEXT    NOT NULL,
    id                   TEXT    NOT NULL,
    version              INTEGER NOT NULL DEFAULT 0,
    last_access          INTEGER,
    keep_alive           INTEGER NOT NULL DEFAULT 0,
    last_known_mergeable TEXT    NOT NULL DEFAULT '',
    tombstoned_at        INTEGER,
    PRIMARY KEY (kind, id)
);

CREATE TABLE entity_group (
    kind             TEXT    NOT NULL,
    id               TEXT    NOT NULL,
    group_name       TEXT    NOT NULL,
    content          BLOB,
    fingerprint      TEXT    NOT NULL DEFAULT '',
    fetched_at       INTEGER,
    fresh_until      INTEGER,
    max_age_at       INTEGER,
    last_error       TEXT    NOT NULL DEFAULT '',
    generation       INTEGER NOT NULL DEFAULT 0,
    fetched_for_head TEXT    NOT NULL DEFAULT '',
    fetched_for_base TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (kind, id, group_name)
);
CREATE INDEX entity_group_due ON entity_group (fresh_until);

CREATE TABLE budget (
    seq       INTEGER PRIMARY KEY AUTOINCREMENT,
    bucket    TEXT    NOT NULL,
    at        INTEGER NOT NULL,
    remaining INTEGER NOT NULL,
    reset_at  INTEGER,
    spent     INTEGER NOT NULL DEFAULT 0,
    purpose   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX budget_bucket_at ON budget (bucket, at);
`

func migrateV1(tx *sql.Tx, report func(string)) error {
	report("creating the version 1 tables")
	if _, err := tx.Exec(schemaV1); err != nil {
		return fmt.Errorf("create version 1 schema: %w", err)
	}
	return nil
}

// schemaV2 adds the job table: the CI jobs of one run attempt, cached by
// (run_id, attempt). A completed attempt's jobs never change, so a row is
// written once and read on every later refresh of the run's group. run_id is
// TEXT because a CI run id is a decimal string on the wire; jobs is a JSON list
// of {id, name, status, conclusion, url} (see JobResult); fetched_at is Unix
// milliseconds, indexed so pruning old rows does not scan the table.
const schemaV2 = `
CREATE TABLE job (
    run_id     TEXT    NOT NULL,
    attempt    INTEGER NOT NULL,
    jobs       TEXT    NOT NULL DEFAULT '[]',
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (run_id, attempt)
);
CREATE INDEX job_fetched_at ON job (fetched_at);
`

func migrateV2(tx *sql.Tx, report func(string)) error {
	report("creating the job table")
	if _, err := tx.Exec(schemaV2); err != nil {
		return fmt.Errorf("create version 2 schema: %w", err)
	}
	return nil
}

// schemaV3 adds the change engine's persistence. The store keeps opaque kinds,
// ids and JSON; what a change kind means, and when a pending row settles, belong
// to the engine, not to this package.
//
//   - change_log: the append-only feed. seq is AUTOINCREMENT so a sequence number
//     is never reused, even after the newest rows were pruned, and a rolled-back
//     append gives its number back (sqlite_sequence is transactional). kind is
//     the log kind (pr or ci), id the wire reference, kinds a JSON list of change
//     kinds, fields an opaque JSON document the classifier owns, head_sha the
//     head commit recorded with the row.
//   - pending_change: one row per entity key awaiting its settle barrier; kinds
//     is a JSON list, fields and awaited are opaque JSON; head_sha, base_sha,
//     origin and origin_updated_at carry what the engine needs to append the row
//     later; detected_at and deadline are Unix milliseconds.
//   - consumer: a feed cursor, keyed (consumer, kind, query); query is ” for a
//     key with no query.
//   - store_meta: a small key-value table (the feed keeps its "store was moved
//     aside" marker there).
//
// query gains baseline_at: NULL until the first complete run of a configured
// query has established its membership baseline.
const schemaV3 = `
CREATE TABLE change_log (
    seq               INTEGER PRIMARY KEY AUTOINCREMENT,
    kind              TEXT    NOT NULL,
    id                TEXT    NOT NULL,
    version           INTEGER NOT NULL DEFAULT 0,
    head_sha          TEXT    NOT NULL DEFAULT '',
    kinds             TEXT    NOT NULL DEFAULT '[]',
    fields            TEXT    NOT NULL DEFAULT '',
    origin            TEXT    NOT NULL DEFAULT '',
    origin_updated_at INTEGER,
    at                INTEGER NOT NULL
);
CREATE INDEX change_log_at ON change_log (at);
CREATE INDEX change_log_entity ON change_log (kind, id, seq);

CREATE TABLE pending_change (
    kind              TEXT    NOT NULL,
    id                TEXT    NOT NULL,
    kinds             TEXT    NOT NULL DEFAULT '[]',
    fields            TEXT    NOT NULL DEFAULT '',
    awaited           TEXT    NOT NULL DEFAULT '',
    head_sha          TEXT    NOT NULL DEFAULT '',
    base_sha          TEXT    NOT NULL DEFAULT '',
    origin            TEXT    NOT NULL DEFAULT '',
    origin_updated_at INTEGER,
    detected_at       INTEGER,
    deadline          INTEGER,
    PRIMARY KEY (kind, id)
);
CREATE INDEX pending_change_deadline ON pending_change (deadline);

CREATE TABLE consumer (
    consumer  TEXT    NOT NULL,
    kind      TEXT    NOT NULL,
    query     TEXT    NOT NULL DEFAULT '',
    acked_seq INTEGER NOT NULL DEFAULT 0,
    seen_at   INTEGER NOT NULL,
    PRIMARY KEY (consumer, kind, query)
);
CREATE INDEX consumer_seen_at ON consumer (seen_at);

CREATE TABLE store_meta (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
);

ALTER TABLE query ADD COLUMN baseline_at INTEGER;
`

func migrateV3(tx *sql.Tx, report func(string)) error {
	report("creating the change log, pending change, consumer and meta tables")
	if _, err := tx.Exec(schemaV3); err != nil {
		return fmt.Errorf("create version 3 schema: %w", err)
	}
	return nil
}

// schemaV4 gives the store its identity: one store_meta row, store_id, written
// when absent (INSERT OR IGNORE, so a store that somehow has one keeps it). It is
// data only, no DDL. The value is 128 random bits as 32 lowercase hex characters,
// bound as the parameter.
const schemaV4 = `INSERT OR IGNORE INTO store_meta (key, value) VALUES ('store_id', ?)`

// storeIDBytes is the identity's entropy: 16 bytes, 32 hex characters.
const storeIDBytes = 16

func migrateV4(tx *sql.Tx, report func(string)) error {
	report("recording the store identity")
	b := make([]byte, storeIDBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("create version 4 store identity: %w", err)
	}
	if _, err := tx.Exec(schemaV4, hex.EncodeToString(b)); err != nil {
		return fmt.Errorf("create version 4 store identity: %w", err)
	}
	return nil
}

// schemaV5 adds the per-entity watched flag and the log row's snapshot of it.
//
//   - entity.watched_at: Unix milliseconds the flag was first set; NULL means
//     not watched. entity.watched_via records the cause (empty while unwatched).
//   - change_log.watched: whether the entity's flag was set when the row was
//     appended, 0 for every row written before this rung.
//   - entity_watched: the partial index the watched listing and count read.
const schemaV5 = `
ALTER TABLE entity ADD COLUMN watched_at INTEGER;
ALTER TABLE entity ADD COLUMN watched_via TEXT NOT NULL DEFAULT '';
ALTER TABLE change_log ADD COLUMN watched INTEGER NOT NULL DEFAULT 0;
CREATE INDEX entity_watched ON entity (kind, watched_at) WHERE watched_at IS NOT NULL;
`

func migrateV5(tx *sql.Tx, report func(string)) error {
	report("adding the watched flag and the log snapshot")
	if _, err := tx.Exec(schemaV5); err != nil {
		return fmt.Errorf("create version 5 schema: %w", err)
	}
	return nil
}

// schemaV6 rebuilds consumer so a consumer holds ONE cursor: the primary key
// becomes (consumer) and the old kind and query columns become feed_view and
// scope. feed_view is the registered view name; scope is the query filter fixed
// when the cursor was created (” for an unscoped consumer).
//
// The rebuild runs in Go (migrateV6) because a consumer that held several rows
// is collapsed to one: the row with the lowest acked_seq is kept so no consumer
// moves past a row it has not acknowledged.
const schemaV6Table = `
CREATE TABLE consumer_v6 (
    consumer  TEXT    NOT NULL PRIMARY KEY,
    feed_view TEXT    NOT NULL,
    scope     TEXT    NOT NULL DEFAULT '',
    acked_seq INTEGER NOT NULL DEFAULT 0,
    seen_at   INTEGER NOT NULL
)`

// ConsumerCollapsedPrefix is the store_meta key prefix under which rung v6
// records the rows it dropped from a multi-row consumer; the key is the prefix
// followed by the consumer name and the value a JSON list of
// {"view", "scope", "acked_seq"}.
const ConsumerCollapsedPrefix = "consumer_collapsed."

// CollapsedRow is one dropped cursor in a consumer_collapsed.<consumer> record.
type CollapsedRow struct {
	View     string `json:"view"`
	Scope    string `json:"scope"`
	AckedSeq int64  `json:"acked_seq"`
}

type v5ConsumerRow struct {
	kind, query string
	ackedSeq    int64
	seenAt      int64
}

func migrateV6(tx *sql.Tx, report func(string)) error {
	report("rebuilding the consumer table with one cursor per consumer")
	rows, err := tx.Query(`SELECT consumer, kind, query, acked_seq, seen_at FROM consumer ORDER BY consumer, kind, query`)
	if err != nil {
		return fmt.Errorf("create version 6 schema: read consumers: %w", err)
	}
	byConsumer := map[string][]v5ConsumerRow{}
	var names []string
	for rows.Next() {
		var (
			name string
			r    v5ConsumerRow
		)
		if err := rows.Scan(&name, &r.kind, &r.query, &r.ackedSeq, &r.seenAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("create version 6 schema: read consumers: %w", err)
		}
		if _, seen := byConsumer[name]; !seen {
			names = append(names, name)
		}
		byConsumer[name] = append(byConsumer[name], r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("create version 6 schema: read consumers: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("create version 6 schema: read consumers: %w", err)
	}

	if _, err := tx.Exec(schemaV6Table); err != nil {
		return fmt.Errorf("create version 6 schema: %w", err)
	}
	sort.Strings(names)
	for _, name := range names {
		group := byConsumer[name]
		// The kept row has the lowest acked_seq; ties go to the lowest kind,
		// then the lowest query (the rows arrive ordered by kind, query).
		keep := 0
		maxSeen := group[0].seenAt
		for i, r := range group {
			if r.ackedSeq < group[keep].ackedSeq {
				keep = i
			}
			if r.seenAt > maxSeen {
				maxSeen = r.seenAt
			}
		}
		kept := group[keep]
		if _, err := tx.Exec(
			`INSERT INTO consumer_v6 (consumer, feed_view, scope, acked_seq, seen_at) VALUES (?, ?, ?, ?, ?)`,
			name, kept.kind, kept.query, kept.ackedSeq, maxSeen,
		); err != nil {
			return fmt.Errorf("create version 6 schema: copy consumer %q: %w", name, err)
		}
		if len(group) < 2 {
			continue
		}
		dropped := make([]CollapsedRow, 0, len(group)-1)
		for i, r := range group {
			if i != keep {
				dropped = append(dropped, CollapsedRow{View: r.kind, Scope: r.query, AckedSeq: r.ackedSeq})
			}
		}
		b, err := json.Marshal(dropped)
		if err != nil {
			return fmt.Errorf("create version 6 schema: record collapse of %q: %w", name, err)
		}
		if err := putMeta(context.Background(), tx, ConsumerCollapsedPrefix+name, string(b)); err != nil {
			return fmt.Errorf("create version 6 schema: %w", err)
		}
	}
	for _, stmt := range []string{
		`DROP TABLE consumer`,
		`ALTER TABLE consumer_v6 RENAME TO consumer`,
		`CREATE INDEX consumer_seen_at ON consumer (seen_at)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("create version 6 schema: %w", err)
		}
	}
	return nil
}

// schemaV7 adds the entity's closed-at stamp.
//
//   - entity.closed_at: Unix milliseconds the entity was marked closed; NULL
//     means not closed. Only Store.SetClosedAt writes it: PutEntity never does.
//   - entity_closed_at: the partial index a listing of closed entities reads.
const schemaV7 = `
ALTER TABLE entity ADD COLUMN closed_at INTEGER;
CREATE INDEX entity_closed_at ON entity (kind, closed_at) WHERE closed_at IS NOT NULL;
`

func migrateV7(tx *sql.Tx, report func(string)) error {
	report("adding the entity closed-at stamp")
	if _, err := tx.Exec(schemaV7); err != nil {
		return fmt.Errorf("create version 7 schema: %w", err)
	}
	return nil
}

// schemaV8 adds the expression index the case-insensitive alias lookups read.
//
//   - alias_ref_fold: an index on lower(ref). AliasByRefFold and AliasesByRefFold
//     match `lower(ref) = ?` and `lower(ref) IN (...)`; without an index on that
//     exact expression SQLite scans the whole alias table for every statement
//     (the alias_ref index is on ref, which `lower(ref)` cannot use). The
//     queries MUST spell the expression exactly as below for the planner to use
//     the index. It is an index only: no column or row changes, so no data
//     moves.
const schemaV8 = `
CREATE INDEX alias_ref_fold ON alias (lower(ref));
`

func migrateV8(tx *sql.Tx, report func(string)) error {
	report("indexing the alias table by folded reference")
	if _, err := tx.Exec(schemaV8); err != nil {
		return fmt.Errorf("create version 8 schema: %w", err)
	}
	return nil
}

// schemaV9 adds the daemon's posted-review sidecar: the per-PR record of which
// review comments the daemon posted.
//
//   - posted_review: one row per (owner, repo, number). state is the daemon's
//     posted.State as JSON, stored verbatim (the store never reads it);
//     updated_at is Unix milliseconds of the last Save. The accessors live in
//     the daemon's own sidecar package, which reads and writes the table
//     through DB().
const schemaV9 = `
CREATE TABLE posted_review (
    owner      TEXT    NOT NULL,
    repo       TEXT    NOT NULL,
    number     INTEGER NOT NULL,
    state      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner, repo, number)
) WITHOUT ROWID;
`

func migrateV9(tx *sql.Tx, report func(string)) error {
	report("creating the posted-review table")
	if _, err := tx.Exec(schemaV9); err != nil {
		return fmt.Errorf("create version 9 schema: %w", err)
	}
	return nil
}
