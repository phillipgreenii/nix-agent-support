-- ADR 0072 (Decision 4): ccpool records WHY it closed a session, stamped on
-- the row BEFORE tmux teardown. Legacy rows default to an empty reason and a
-- zero timestamp, which report exactly as "not yet closed by ccpool".
ALTER TABLE sessions ADD COLUMN close_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN closed_at INTEGER NOT NULL DEFAULT 0;
