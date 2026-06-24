-- pg2-01ys: arbitrary KV metadata per session, keyed to sessions.external_id
-- (the caller's stable handle, ADR 0015). One row per (external_id, key); a key
-- holds exactly one value (set replaces). external_id is NOT a FK to sessions
-- (sessions can be pruned/recreated under the same external_id; metadata is
-- cleaned up explicitly by store.Delete). Indexed on (key, value) so a
-- filter "role=worker" is an indexed lookup, and external_id is the PK prefix so
-- per-session fetch + cascade-delete are cheap.
CREATE TABLE session_metadata (
    external_id TEXT NOT NULL,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (external_id, key)
);
CREATE INDEX session_metadata_key_value ON session_metadata(key, value);
