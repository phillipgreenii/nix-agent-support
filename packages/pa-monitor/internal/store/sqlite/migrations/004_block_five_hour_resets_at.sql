-- pa-monitor schema, migration 004.
-- Persist the authoritative status-line five_hour (5h block) reset instant on the
-- active 5h block, per ADR 0021 §6 / §5. Migration 003 added five_hour_pct,
-- seven_day_pct, seven_day_resets_at, and limits_captured_at but NOT the 5h
-- window's own reset — that gap is filled here so the block-level
-- usage.percentage / resets_at OTel gauges (ADR 0021 §5) have a 5h reset to emit.
--
-- Like the 003 columns this is deliberately NULLABLE: NULL means "unknown / stale",
-- explicitly distinct from 0 (a real reading) and MUST NOT read back as a 1970
-- timestamp. It is DISTINCT from the daemon's pause concept (rate_limit_resets_at).

ALTER TABLE blocks ADD COLUMN five_hour_resets_at TEXT;
