// Package store owns pg-ccaudit's SQLite database: the schema, connection
// opening (read-write for the ingester, statement-level read-only for the query
// path) and the default on-disk location.
//
// The schema below is the CORRECTED DDL carried by bead pg2-xnnab's own
// description, reproduced column-for-column. Three columns are load-bearing
// corrections against an earlier draft and MUST NOT be dropped:
//
//	files.resume_offset       parse only the appended delta            (T-1a)
//	files.complete            0 = still being appended, 1 = final      (T-15)
//	tool_results.content_len  always populated; content NULL unless
//	                          is_error = 1                             (T-3a)
//
// T-3a is the single biggest sizing decision in the design: measured on a
// stratified sample of the corpus, successful tool-result bodies scale to
// ~322 MB while error bodies scale to ~1.8 MB — 180x the volume for no
// analytical value, because a census of FAILURES never reads a SUCCESS body.
// So a successful result contributes its length and nothing else.
//
// # Schema 2 — user_text (bead pg2-oisvb)
//
// The mistake census needs the HUMAN side of the conversation, which schema 1
// did not capture at all: assistant prose was stored, user prose was not. The
// added table applies T-3a's rule rather than reopening it — measured over the
// whole corpus (2,428 transcripts, 90,424 type=="user" records):
//
//	prompt_source=='typed'    1,183 records      414,876 runes   ← stored in full
//	prompt_source=='queued'      26 records        2,205 runes   ← stored in full
//	prompt_source=='system'     776 records    4,464,317 runes   ← length only
//	prompt_source=='sdk'        329 records    6,355,070 runes   ← length only
//	no prompt_source, prose   3,693 records   24,354,130 runes   ← length only
//	no prompt_source, tool
//	  results               84,417 records          534 runes   ← not a turn at all
//
// So the human turns — the only records a correction census reads — cost 0.4 MB,
// while capturing every prose record in full would cost ~35 MB, most of it the
// same skill and slash-command boilerplate re-injected thousands of times (the
// unlabelled prose bucket averages 6.6 KB/record and its largest members are
// verbatim skill bodies). Every record still contributes text_len, so the
// unstored buckets remain COUNTABLE and the criterion-1 denominator is provable
// rather than asserted.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo), as used by ccpool
)

// DriverName is the database/sql driver this package registers through
// modernc.org/sqlite.
const DriverName = "sqlite"

// SchemaVersion is written to PRAGMA user_version. It is deliberately tracked
// in the pragma rather than in a metadata TABLE so the table list stays
// exactly the DDL the bead specifies.
//
// 1 — pg2-xnnab's DDL.
// 2 — pg2-oisvb adds user_text.
const SchemaVersion = 2

// Schema is the CORRECTED DDL from bead pg2-xnnab, plus pg2-oisvb's user_text.
// `IF NOT EXISTS` is the only addition to the specified columns: it makes Open
// idempotent without changing a single one.
const Schema = `
CREATE TABLE IF NOT EXISTS files (
  path          TEXT PRIMARY KEY,
  project_dir   TEXT NOT NULL,
  size          INTEGER NOT NULL,
  mtime         INTEGER NOT NULL,
  resume_offset INTEGER NOT NULL DEFAULT 0,  -- FIX 1 (T-1a): parse only the delta
  complete      INTEGER NOT NULL DEFAULT 0,  -- FIX 3 (T-15): 0 = still being appended
  lines_ok      INTEGER NOT NULL,
  lines_bad     INTEGER NOT NULL,            -- T-2: coverage is provable
  ingested_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  path            TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  seq             INTEGER NOT NULL,          -- T-4: line ordinal preserves order
  uuid            TEXT,
  parent_uuid     TEXT,
  session_id      TEXT,                      -- load-bearing: scopes "same session"
  ts              TEXT,
  type            TEXT,                      -- user | assistant | system | ...
  is_sidechain    INTEGER,                   -- T-4: subagent attribution
  cwd             TEXT,                      -- T-5
  git_branch      TEXT,
  permission_mode TEXT,
  duration_ms     INTEGER,                   -- T-5: measured, not estimated
  hook_count      INTEGER,
  hook_errors     TEXT,
  prompt_source   TEXT,                      -- typed | system | sdk | queued
  user_type       TEXT,
  source_tool_assistant_uuid TEXT,
  prompt_id       TEXT,
  entrypoint      TEXT,
  PRIMARY KEY (path, seq)
);

CREATE TABLE IF NOT EXISTS tool_calls (
  tool_use_id TEXT PRIMARY KEY,
  path        TEXT NOT NULL,
  seq         INTEGER NOT NULL,
  tool_name   TEXT NOT NULL,
  input_json  TEXT NOT NULL,                 -- T-3: untruncated
  lead_cmd    TEXT                           -- Bash only; precomputed
);

CREATE TABLE IF NOT EXISTS tool_results (
  tool_use_id TEXT PRIMARY KEY,
  path        TEXT NOT NULL,
  seq         INTEGER NOT NULL,
  is_error    INTEGER NOT NULL,              -- T-9: 1 only when present AND true
  content_len INTEGER NOT NULL,              -- FIX 2 (T-3a): always populated
  content     TEXT,                          -- FIX 2 (T-3a): NULL unless is_error = 1
  signature   TEXT                           -- T-6: normalized at ingest
);

CREATE TABLE IF NOT EXISTS assistant_text (  -- error -> narration adjacency
  path TEXT NOT NULL, seq INTEGER NOT NULL, text TEXT NOT NULL,
  PRIMARY KEY (path, seq)
);

CREATE TABLE IF NOT EXISTS user_text (       -- pg2-oisvb: the HUMAN side
  path        TEXT NOT NULL,
  seq         INTEGER NOT NULL,
  text_len    INTEGER NOT NULL,              -- always populated (runes)
  text        TEXT,                          -- NULL unless a human turn (T-3a rule)
  interrupted INTEGER NOT NULL DEFAULT 0,    -- 1 = Claude Code's interruption sentinel
  PRIMARY KEY (path, seq)
);

CREATE INDEX IF NOT EXISTS idx_calls_name  ON tool_calls(tool_name);
-- (path, seq) is tool_calls' ORDER, not its identity, so it needs an index of its
-- own: pg2-oisvb's Tier 1 queries ask "the nearest tool call BEFORE line N of this
-- transcript" once per human turn, and without this index that is a full scan of
-- the 84k-row table per turn. Measured: the query does not finish in 10 minutes
-- without it and returns in well under a second with it.
CREATE INDEX IF NOT EXISTS idx_calls_pathseq ON tool_calls(path, seq);
CREATE INDEX IF NOT EXISTS idx_res_err     ON tool_results(is_error, signature);
CREATE INDEX IF NOT EXISTS idx_events_side ON events(is_sidechain, type);
CREATE INDEX IF NOT EXISTS idx_events_src  ON events(prompt_source);
CREATE INDEX IF NOT EXISTS idx_user_int    ON user_text(interrupted);
`

// ThinkingSchema is created ONLY when ingest runs with thinking capture enabled
// (T-16: "SHOULD be ingestible behind a flag, DEFAULT OFF").
//
// It is deliberately NOT part of Schema. The bead's DDL is the authoritative
// table list and contains no thinking table, because — in the bead's own words —
// "no query below needs them yet". Keeping the optional capture in its own
// conditionally-created table means the DEFAULT database matches the specified
// DDL exactly, table for table, while the flag still has somewhere to put
// ~94 MB of thinking blocks for whoever eventually wants them.
const ThinkingSchema = `
CREATE TABLE IF NOT EXISTS thinking (
  path TEXT NOT NULL, seq INTEGER NOT NULL, text TEXT NOT NULL,
  PRIMARY KEY (path, seq)
);
`

// CanonicalTables is the exact table set the DDL declares — pg2-xnnab's five
// plus pg2-oisvb's user_text. The schema test asserts equality against a freshly
// opened database, so an accidental extra table (or a dropped one) fails the
// build rather than silently shipping a schema that a later migration would have
// to unpick. Adding a table here is deliberate and MUST come with a
// SchemaVersion bump, because Open re-ingests from zero on an upgrade.
var CanonicalTables = []string{
	"assistant_text",
	"events",
	"files",
	"tool_calls",
	"tool_results",
	"user_text",
}

// DefaultDBPath resolves the global database location. The database is global
// rather than per-project on purpose: cross-project analysis over every project
// directory is the whole point of the index. The location follows the
// ~/.local/share/beads-dolt precedent.
func DefaultDBPath() (string, error) {
	if p := os.Getenv("PG_CCAUDIT_DB"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "pg-ccaudit", "transcripts.db"), nil
}

// DefaultTranscriptRoot resolves the Claude Code transcript root.
func DefaultTranscriptRoot() (string, error) {
	if p := os.Getenv("PG_CCAUDIT_ROOT"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// Open opens the database for WRITING, creating it (and its parent directory)
// if absent, and applies the schema.
//
// WAL is mandatory, not a tuning choice (T-12): the query path must never block
// the ingester and the ingester must never block a query.
func Open(path string, withThinking bool) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory %s: %w", dir, err)
		}
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(wal)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(normal)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// The ingester is single-instance by construction (an advisory lock gates
	// the whole run), so a single connection keeps every statement on one
	// SQLite handle and removes any chance of a self-inflicted write conflict.
	db.SetMaxOpenConns(1)
	// The stored version must be read BEFORE the DDL runs: applying the schema is
	// what makes an older database structurally indistinguishable from a current
	// one, and the pragma is the only surviving evidence of which it was.
	prior, err := userVersion(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(Schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db, prior, withThinking); err != nil {
		_ = db.Close()
		return nil, err
	}
	if withThinking {
		if _, err := db.Exec(ThinkingSchema); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply thinking schema: %w", err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set user_version: %w", err)
	}
	return db, nil
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

// migrate brings a database written by an EARLIER schema version up to this one
// by clearing it, so the next sweep re-ingests every file from zero.
//
// Re-ingest — not a column backfill — is the only correct migration for this
// index, and the reason is structural: every schema bump so far ADDS captured
// data, and the data being added was never written to the database in the first
// place. It lives only in the transcripts. So there is nothing to backfill FROM;
// a `SELECT` over the existing rows cannot manufacture user prose that ingest
// never stored. Leaving the rows in place would instead produce the worst
// outcome available — a database that answers the new queries with structurally
// valid, silently incomplete numbers, which is exactly the class of miscount
// this index exists to eliminate.
//
// Clearing `files` is what makes the re-ingest happen: `decide` classifies a file
// with no recorded state as `fresh` and parses it whole, whereas resetting the
// resume offsets in place would leave (size, mtime) matching and every file
// would be skipped as `unchanged`. A full corpus re-ingest is affordable —
// measured 2,430 files / 1.3 GB in 35 s — so this is a cheap correctness
// guarantee, not a painful one.
//
// A version of 0 is a database this package never finished writing (Open sets the
// pragma last), so it is left alone: there is nothing to clear.
func migrate(db *sql.DB, prior int, withThinking bool) error {
	if prior == 0 || prior >= SchemaVersion {
		return nil
	}
	stmts := []string{
		`DELETE FROM user_text`,
		`DELETE FROM assistant_text`,
		`DELETE FROM tool_results`,
		`DELETE FROM tool_calls`,
		`DELETE FROM events`,
		`DELETE FROM files`,
	}
	if withThinking {
		stmts = append([]string{`DELETE FROM thinking`}, stmts...)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("migrate schema %d -> %d (%s): %w", prior, SchemaVersion, s, err)
		}
	}
	return nil
}

// OpenReadOnly opens an EXISTING database for the query path (T-13). Every
// statement on the returned handle is rejected if it would write.
//
// It opens the file read-WRITE at the OS level and then latches
// `PRAGMA query_only = 1`, rather than using SQLite's `mode=ro`. That is
// deliberate: a `mode=ro` connection to a WAL database cannot initialise the
// -shm file, so it fails outright (SQLITE_CANTOPEN) whenever the shared-memory
// file is absent — which is exactly the state a freshly written database is in.
// `query_only` blocks writes at the statement layer instead, so the main
// database file is never modified (the query-path test asserts its size and
// mtime are unchanged across a query) while WAL reads still work.
func OpenReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("index not found at %s (run `pg-ccaudit ingest`, or let the scheduled sweep do it): %w", path, err)
	}
	dsn := "file:" + path +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=query_only(1)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	var queryOnly int
	if err := db.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify query_only: %w", err)
	}
	if queryOnly != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("refusing to query: query_only pragma did not latch (got %d)", queryOnly)
	}
	return db, nil
}

// HasThinkingTable reports whether the optional T-16 thinking table exists.
func HasThinkingTable(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='thinking'`,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
