-- Per-run "metrics emitted" flag (pg2-om899.5). The emitter claims a run with a
-- conditional UPDATE ... WHERE metrics_emitted = 0 and records its lifecycle
-- metrics only if the claim won, so each run is emitted at most once.
-- Runs already ended when this migration applies default to 1: no burst of
-- historic emission on the first reaper sweep. Open runs (and runs ended later)
-- start at 0.
ALTER TABLE session_runs ADD COLUMN metrics_emitted INTEGER NOT NULL DEFAULT 0;
UPDATE session_runs SET metrics_emitted = 1 WHERE ended_at IS NOT NULL;
