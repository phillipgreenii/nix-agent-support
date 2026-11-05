package store

// SchemaVersion is the index.db schema version. A change is migrated or the
// derived part is rebuilt.
//
// Version 2 interns field names (field_names) and stores fields WITHOUT ROWID
// keyed (rec_id, name_id). A version 1 database is not migrated in place: Open
// refuses it with a SchemaOlderError and the derived part is rebuilt from the
// journal and the copies (the rebuild command or serve --rebuild), which sets
// the old file aside as index.db.pre-rebuild and deletes nothing.
//
// Version 3 is the layout described in docs/schema-v3-design.md: records carry
// session_ref (an integer into sessions) instead of the session text, the
// per-session constants are interned in the lk_ tables, the usage counters of an
// assistant line live in ext_assistant_usage, and there is no trigram index
// (opaque fields are indexed in the word index). A version 2 database is not
// migrated: Open refuses it with a SchemaOlderError and the rebuild builds the
// version 3 layout (Options.V3Layout, which the daemon always sets), after the
// free-space preflight (see rebuildpreflight.go).
//
// Version 4 adds records.own_time, a marker that the line carried its own
// timestamp (so records.time_ns is that timestamp, not the time the recorder saw
// the bytes). The parser then stores no timestamp fields row and the query layer
// serves the timestamp field from time_ns where the marker is set. A version 3
// database is not migrated: Open refuses it with a SchemaOlderError and the
// rebuild builds the version 4 layout.
//
// The seen table (the last observation of each file by any actor, see seen.go)
// was added to version 4 without a version bump: a new table needs no rebuild,
// and Open only refuses a version below SchemaVersion.
//
// files.last_copy_ns (the time of the file's newest copy commit, the settler's
// last-seen signal for an append-mode file) was added to version 4 without a
// version bump: Open adds the column in place and backfills it once from the
// commits table (addLastCopyColumn). Replaying the journal reproduces the same
// value, so a migrated and a rebuilt database agree.
const SchemaVersion = 4

const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS files(
  id INTEGER PRIMARY KEY,
  source TEXT NOT NULL,
  mode TEXT NOT NULL,
  dev INTEGER NOT NULL DEFAULT 0,
  ino INTEGER NOT NULL DEFAULT 0,
  birth_ns INTEGER NOT NULL DEFAULT 0,
  first_hash INTEGER NOT NULL DEFAULT 0,
  link_target TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'ok',
  first_seen_ns INTEGER NOT NULL,
  last_size INTEGER NOT NULL DEFAULT 0,
  last_copy_ns INTEGER NOT NULL DEFAULT 0,
  cur_gen INTEGER NOT NULL DEFAULT 0,
  deleted_ns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS files_ident ON files(dev, ino);
CREATE TABLE IF NOT EXISTS paths(
  file_id INTEGER NOT NULL,
  path TEXT NOT NULL,
  first_seen_ns INTEGER NOT NULL,
  closed_ns INTEGER,
  PRIMARY KEY(file_id, path, first_seen_ns)
);
CREATE INDEX IF NOT EXISTS paths_path ON paths(path);
CREATE TABLE IF NOT EXISTS generations(
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  method TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  start_ns INTEGER NOT NULL,
  copy_offset INTEGER NOT NULL DEFAULT 0,
  parse_offset INTEGER NOT NULL DEFAULT 0,
  tail_hash INTEGER NOT NULL DEFAULT 0,
  tail_len INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(file_id, gen)
);
CREATE TABLE IF NOT EXISTS commits(
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  from_off INTEGER NOT NULL,
  to_off INTEGER NOT NULL,
  seen_ns INTEGER NOT NULL,
  PRIMARY KEY(file_id, gen, seq)
);
CREATE TABLE IF NOT EXISTS snapshots(
  file_id INTEGER NOT NULL,
  version INTEGER NOT NULL,
  hash INTEGER NOT NULL,
  size INTEGER NOT NULL,
  seen_ns INTEGER NOT NULL,
  PRIMARY KEY(file_id, version)
);
CREATE TABLE IF NOT EXISTS tombstones(
  file_id INTEGER NOT NULL,
  path TEXT NOT NULL,
  at_ns INTEGER NOT NULL,
  PRIMARY KEY(file_id, at_ns)
);
CREATE TABLE IF NOT EXISTS procs(
  pid INTEGER NOT NULL,
  proc_start TEXT NOT NULL,
  first_seen_ns INTEGER NOT NULL,
  last_seen_ns INTEGER NOT NULL DEFAULT 0,
  exit_ns INTEGER NOT NULL DEFAULT 0,
  exit_kind TEXT NOT NULL DEFAULT '',
  registry_present INTEGER NOT NULL DEFAULT 0,
  stale INTEGER NOT NULL DEFAULT 0,
  host TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(pid, proc_start)
);
CREATE TABLE IF NOT EXISTS resets(
  id INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL,
  from_gen INTEGER NOT NULL,
  to_gen INTEGER NOT NULL,
  reason TEXT NOT NULL,
  at_ns INTEGER NOT NULL,
  pinned INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS conv_stage(
  session TEXT PRIMARY KEY,
  stage TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  since_ns INTEGER NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS settled(
  session TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  at_ns INTEGER NOT NULL,
  method TEXT NOT NULL DEFAULT '',
  manifest TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS ranges(
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  from_off INTEGER NOT NULL,
  to_off INTEGER NOT NULL,
  session TEXT NOT NULL,
  seg TEXT NOT NULL,
  PRIMARY KEY(file_id, gen, from_off)
);
CREATE TABLE IF NOT EXISTS seen(
  path TEXT PRIMARY KEY,
  file_id INTEGER NOT NULL,
  dev INTEGER NOT NULL,
  ino INTEGER NOT NULL,
  birth_ns INTEGER NOT NULL,
  size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL,
  gen INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS torn_tails(
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  off INTEGER NOT NULL,
  len INTEGER NOT NULL,
  at_ns INTEGER NOT NULL,
  PRIMARY KEY(file_id, gen)
);
`
