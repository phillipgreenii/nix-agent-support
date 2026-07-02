-- pa-monitor schema, migration 003.
-- Persist the authoritative status-line rate_limits windows (5h / 7d) on the
-- active 5h block, per ADR 0021 §6.
--
-- These columns are the account-global "used_percentage" + reset for the two
-- status-line windows, NOT the daemon's pause / limit-hit concept (that stays
-- in the existing rate_limit_resets_at / cap_hit_at columns). They are
-- deliberately NULLABLE: NULL means "unknown / stale", explicitly distinct
-- from 0 (a real "unused" reading) and MUST NOT read back as a 1970 timestamp.
--
-- Phase 0 observed seven_day entirely absent on this grandfathered-enterprise
-- account, so seven_day_pct / seven_day_resets_at MUST tolerate being
-- long-lived NULL (the common case, not an edge case). No consumer reads these
-- yet — this migration is persistence plumbing only.

ALTER TABLE blocks ADD COLUMN five_hour_pct REAL;
ALTER TABLE blocks ADD COLUMN seven_day_pct REAL;
ALTER TABLE blocks ADD COLUMN seven_day_resets_at TEXT;
ALTER TABLE blocks ADD COLUMN limits_captured_at TEXT;
