package ledger

import (
	"context"
	"database/sql"
	"fmt"
)

// migration is one forward-only schema step. Version is its target
// PRAGMA user_version; steps apply in order, each in its own transaction.
type migration struct {
	version int
	up      func(ctx context.Context, tx *Tx) error
}

var migrations = []migration{
	{version: 1, up: func(ctx context.Context, tx *Tx) error {
		_, err := tx.exec(ctx, schemaV1)
		return err
	}},
}

// SchemaVersion is the user_version of a database created by this release.
func SchemaVersion() int { return migrations[len(migrations)-1].version }

func (l *Ledger) userVersion(ctx context.Context) (int, error) {
	var v int
	err := l.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

func (l *Ledger) migrate(ctx context.Context, migs []migration) error {
	cur, err := l.userVersion(ctx)
	if err != nil {
		return err
	}
	latest := migs[len(migs)-1].version
	if cur > latest {
		return fmt.Errorf("%w: have %d, support %d", ErrSchemaTooNew, cur, latest)
	}
	for _, m := range migs {
		if m.version <= cur {
			continue
		}
		err := l.WithTx(ctx, func(tx *Tx) error {
			// Re-check under the write lock: another process may have migrated.
			var v int
			if err := tx.queryRow(ctx, "PRAGMA user_version").Scan(&v); err != nil {
				return err
			}
			if v >= m.version {
				return nil
			}
			if err := m.up(ctx, tx); err != nil {
				return err
			}
			_, err := tx.exec(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version))
			return err
		})
		if err != nil {
			return fmt.Errorf("ledger: migration to version %d: %w", m.version, err)
		}
	}
	return nil
}

var _ = sql.ErrNoRows

const schemaV1 = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE subject (
  defined_in TEXT NOT NULL,
  key        TEXT NOT NULL,
  type       TEXT NOT NULL DEFAULT '',
  first_seen TEXT NOT NULL,
  last_seen  TEXT NOT NULL,
  PRIMARY KEY (defined_in, key)
);

CREATE TABLE topic (
  topic_id          TEXT PRIMARY KEY,
  defined_in        TEXT NOT NULL,
  subject_key       TEXT NOT NULL,
  kind              TEXT NOT NULL,
  lens              TEXT NOT NULL,
  state             TEXT NOT NULL CHECK (state IN ('Pending','Adopted','Deferred','Rejected','Waiting')),
  revision          INTEGER NOT NULL DEFAULT 1,
  version_sensitive INTEGER NOT NULL DEFAULT 0,
  seq               INTEGER NOT NULL,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  FOREIGN KEY (defined_in, subject_key) REFERENCES subject(defined_in, key)
);
CREATE INDEX topic_state ON topic(state, seq);
CREATE INDEX topic_lens ON topic(lens);

CREATE TABLE run (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  seq              INTEGER NOT NULL,
  lens             TEXT NOT NULL,
  scope            TEXT NOT NULL,
  window_from      TEXT NOT NULL DEFAULT '',
  window_to        TEXT NOT NULL DEFAULT '',
  run_key          TEXT NOT NULL DEFAULT '',
  schedule_id      TEXT NOT NULL DEFAULT '',
  trigger          TEXT NOT NULL DEFAULT '',
  state            TEXT NOT NULL CHECK (state IN ('Running','AwaitingAnalyst','Filed','Skipped','Failed')),
  analyst          TEXT NOT NULL DEFAULT '',
  profile          TEXT NOT NULL DEFAULT '',
  model            TEXT NOT NULL DEFAULT '',
  prompt_hash      TEXT NOT NULL DEFAULT '',
  elapsed_minutes  REAL NOT NULL DEFAULT 0,
  pid              INTEGER NOT NULL DEFAULT 0,
  started_at       TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  ended_at         TEXT NOT NULL DEFAULT '',
  staging_path     TEXT NOT NULL DEFAULT '',
  evidence_dir     TEXT NOT NULL DEFAULT '',
  session_id       TEXT NOT NULL DEFAULT '',
  skip_reason      TEXT NOT NULL DEFAULT '',
  query_count      INTEGER NOT NULL DEFAULT 0,
  repair_count     INTEGER NOT NULL DEFAULT 0,
  items_filed      INTEGER NOT NULL DEFAULT 0,
  items_deduplicated INTEGER NOT NULL DEFAULT 0,
  items_skipped    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX run_key ON run(run_key);
CREATE INDEX run_lens ON run(lens, seq);

CREATE TABLE run_lock (
  lens       TEXT NOT NULL,
  scope      TEXT NOT NULL,
  run_id     INTEGER NOT NULL REFERENCES run(id),
  pid        INTEGER NOT NULL,
  started_at TEXT NOT NULL,
  acquired_at TEXT NOT NULL,
  PRIMARY KEY (lens, scope)
);

CREATE TABLE revision (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  topic_id         TEXT NOT NULL REFERENCES topic(topic_id),
  run_id           INTEGER NOT NULL REFERENCES run(id),
  topic_revision   INTEGER NOT NULL,
  writeup_json     TEXT NOT NULL,
  writeup_rendered TEXT NOT NULL,
  created_at       TEXT NOT NULL
);
CREATE INDEX revision_topic ON revision(topic_id);

CREATE TABLE occurrence (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  topic_id      TEXT NOT NULL REFERENCES topic(topic_id),
  run_id        INTEGER NOT NULL REFERENCES run(id),
  scope         TEXT NOT NULL,
  lens          TEXT NOT NULL,
  evidence_refs TEXT NOT NULL DEFAULT '[]',
  metrics       TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL,
  UNIQUE (topic_id, run_id)
);
CREATE INDEX occurrence_run ON occurrence(run_id);

CREATE TABLE decision (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  seq                  INTEGER NOT NULL,
  topic_id             TEXT NOT NULL REFERENCES topic(topic_id),
  ruling               TEXT NOT NULL CHECK (ruling IN ('Adopted','Deferred','Rejected','Reopen')),
  decided              TEXT NOT NULL,
  rationale            TEXT NOT NULL DEFAULT '',
  reconsider_when      TEXT NOT NULL DEFAULT '',
  rate_at_decision     TEXT NOT NULL DEFAULT '',
  useful               TEXT NOT NULL DEFAULT '',
  target_tracker       TEXT NOT NULL DEFAULT '',
  repo_label           TEXT NOT NULL DEFAULT '',
  bead_state           TEXT NOT NULL DEFAULT 'none' CHECK (bead_state IN ('none','pending','filing','filed','failed')),
  bead_idempotency_key TEXT,
  bead_attempts        INTEGER NOT NULL DEFAULT 0,
  bead_lease_expires_at TEXT NOT NULL DEFAULT '',
  work_bead            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX decision_topic ON decision(topic_id, id);
CREATE UNIQUE INDEX decision_idem ON decision(bead_idempotency_key) WHERE bead_idempotency_key IS NOT NULL;

CREATE TABLE view (
  topic_id      TEXT PRIMARY KEY REFERENCES topic(topic_id),
  revision_seen INTEGER NOT NULL,
  shown_at      TEXT NOT NULL
);

CREATE TABLE lens_resume (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  seq        INTEGER NOT NULL,
  lens       TEXT NOT NULL,
  resumed_at TEXT NOT NULL
);
CREATE INDEX lens_resume_lens ON lens_resume(lens, seq);
`
