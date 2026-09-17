# pg-desk — serve

`pg-desk serve` is a long-lived HTTP server, run as a launchd user agent by a generic
`services.pg-desk-serve` darwin module in this repo (with package, port, and log-path options).

**Soak-port only, this phase (D18, D27).** In Phase 9, `serve` MUST listen only on the soak port
(default `9819`) behind the ZR `soak.enable` option — never on the primary port `9818`, which
`pg-pr sync` still owns for both its dashboard and its own `/metrics` until the Phase 11 flip. The
port move to `9818` as the primary is out of scope for this doc set (see "Out of scope" below).

`serve` reads the store only and reloads its config on `SIGHUP`. It MUST NOT block a request on
the pipeline; it only ever reads committed rows.

## Behavior

`serve` MUST return `503` until the store has at least one interpretation. Once it has one, it
serves `GET /api/v1/dashboard` with today's payload contract — the five named selectors
(`mine_act_now`, `mine_awaiting_others`, `mine_awaiting_other_things`, `team_act_now`,
`team_blocked`) and the root fields `generated_at`, `age_seconds`, `stale`, `stale_after_seconds`,
`sync_interval_seconds`, `dropped_count` — plus the fields this phase adds: a `hidden` array,
`last_run_at`, `last_sweep_at`, `runs_failed_24h`, `errors[]`, and per-row `degraded`,
`sync_error`, and `ready_to_promote`. Freshness derives from `meta.last_heartbeat` with the same
two-heartbeat-period staleness bound `pg-pr` uses today, so a dead scheduler shows as stale within
two heartbeat periods.

## Exit codes

`serve` is a long-running process: it exits non-zero when it cannot open its config or its store
at startup, and `0` on a clean shutdown. No other exit code is assigned to it in Phase 9.

## Telemetry and logs

`serve` exposes a minimal `/metrics` endpoint whose sole purpose is keeping the existing
Prometheus scrape target from going red — it is not a dashboard-grade metrics surface. Beyond
that, `serve` emits nothing over OpenTelemetry in Phase 9 (D24); real OpenTelemetry export is a
later observability item the observability review (`pg2-7kizi`) decides. `serve` logs to the path
its launchd module configures, defaulting to `~/Library/Logs/pg-desk-serve.log`.

## Out of scope (Phase 9, narrowed by Phase 10)

The primary port (`9818`) and the removal of the temporary soak board are the Phase 11 flip.
`sync_error` is populated once sync runs and fails for a PR (Phase 10, see
[`sync.md`](sync.md)) — it is no longer unconditionally empty. Any payload field this dashboard
could eventually carry from cross-referencing is Phase 13.
