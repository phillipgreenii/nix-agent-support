-- pa-monitor schema, migration 005.
-- Persist the ADR 0024 per-session blocker alongside the observable status.
-- status now carries the closed {working, blocked, idle} enum; blocker names
-- WHY a blocked session cannot proceed (human_input | human_authn |
-- usage_limit | error), and is empty for non-blocked sessions.
--
-- The DB does NOT persist a PER-SESSION RateLimitResetsAt (there is no
-- sessions.rate_limit_resets_at column), so the stored blocker is what lets the
-- DB-path bucketer (service.BuildDirectories) render usage_limit for a session
-- blocked on a rate limit (ADR 0024 R9). Only the account-global aggregate is
-- persisted, on blocks.rate_limit_resets_at (migration 001) — which carries the
-- tree-level WindowResetsAt and cannot be attributed back to a session.
--
-- Forward-only ADD COLUMN, following the 003/004 pattern. Nullable WITH a
-- default of '' so pre-existing rows read back as "no blocker" rather than NULL.

ALTER TABLE sessions ADD COLUMN blocker TEXT DEFAULT '';
