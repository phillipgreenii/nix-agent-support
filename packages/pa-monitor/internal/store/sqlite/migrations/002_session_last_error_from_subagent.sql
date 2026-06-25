-- pa-monitor schema, migration 002.
-- Persist LastError.FromSubagent so the TUI '(in subagent)' provenance label
-- survives the daemon DB round-trip and the gRPC GetState/WatchState path
-- (previously dropped — see pg2-kg8u). Defaults 0 for existing rows.

ALTER TABLE sessions ADD COLUMN last_error_from_subagent INTEGER NOT NULL DEFAULT 0;
