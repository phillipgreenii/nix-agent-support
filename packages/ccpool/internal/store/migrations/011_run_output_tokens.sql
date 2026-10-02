-- Per-run output-token high-water mark (pg2-om899.9). When an ended run's
-- lifecycle metrics are emitted, ccpool snapshots the session transcript's
-- total output_tokens here; the run's contribution to
-- ccpool_session_output_tokens_total is that total minus the largest snapshot
-- of the session's earlier runs (a resume appends to the SAME transcript).
-- Runs that already exist default to 0, so the first run of a pre-existing
-- session to be emitted after this migration attributes the whole transcript
-- to date (a one-time catch-up, bounded by the sessions alive at deploy).
ALTER TABLE session_runs ADD COLUMN output_tokens_total INTEGER NOT NULL DEFAULT 0;
