# pg-router and pg-desk observability: metrics, logs, and the Ops-board successor dashboards

## 1. Purpose and scope

This is the design `pg2-7kizi` ("pg-desk / pg-connector / pr-pool: telemetry inventory and
Ops-board successor") calls for: an inventory of what pg-router and pg-desk actually emit today,
a mapping of the pg-pr Ops board's 14 panels to a successor or a dropped verdict, and a decision
on the successor dashboards and what each tool's `/metrics` must expose beyond a scrape-keeps-green
stub. `docs/behavior/pg-desk/README.md`'s D24 declaration explicitly deferred real OpenTelemetry
export "to a later observability item the observability review (`pg2-7kizi`) decides" — this
document is that decision.

Scope is **three dashboards**, not two:

1. **pg-router** — a new Prometheus+Loki dashboard, successor to `pg-pr-ops.json`.
2. **pg-desk / My Work** — the existing triage-table view, promoted from its current
   `pg-pr-soak.json` incarnation, unchanged in content.
3. **pg-desk / Metrics** — a new Prometheus+Loki dashboard, distinct from My Work, tracking
   pg-desk `serve`'s own errors and heartbeat/freshness state.

Out of scope (recorded, not silently dropped — see §13): per-role session activity, budget
consumption, and usage-window/backoff state, which `INV-MON-2` also names but which live in
ccpool/pa-monitor, not pg-router's own metric catalog; wiring OTLP traces for either tool (only
metrics and logs were asked for); the Phase 11 port cutover itself (`pg-pr.json` retirement).

## 2. Decisions ledger

- **D1.** Three dashboards, not two: pg-desk's triage table ("My Work") and its own health/error
  telemetry ("Metrics") are different audiences and different data sources (a JSON triage payload
  vs. Prometheus/Loki) and MUST NOT be conflated into one dashboard.
- **D2.** pg-router's metrics export via a direct-scrape OTel `prometheus.Exporter` (matching
  pg-pr's own direct-scrape `/metrics`, not an OTLP push) — see §4.
- **D3.** Logs (both tools) export via OTLP push, mirroring pg-pr's existing `internal/telemetry`
  pattern (`Init` + `otelslog` bridge + `Fanout`) — NOT a JSONL-file-plus-pull-collection
  (`logSources`) approach. `pg-router`'s existing `events.jsonl` is an internal dispatch-outcome
  ledger, not an observability log stream, and is unaffected by this design.
- **D4.** The pg-router dashboard is scoped to pg-router's own already-built 10-metric catalog
  (`internal/metrics/metrics.go`) only. The INV-MON-2 signals that catalog does not cover
  (per-role activity, budget, usage-window/backoff) are recorded as a gap for ccpool/pa-monitor's
  own future dashboard, not pulled into this one.
- **D5.** pg-desk's `/metrics` stub (`pg_desk_up 1`, hardcoded) is replaced with a real catalog
  (§8), built the same way pg-router's already is (OTel metrics API + `prometheus.Exporter`).

## 3. Current state (the inventory)

| Component                 | Metrics today                                                                                                                                                                                                                                                                      | Logs today                                                                                                                                                        |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| pg-router                 | Full 10-member OTel catalog in `internal/metrics/metrics.go` (queue depth, failures by class, unconsumed-expired, unknown-type-rejected, throughput, backlog, liveness, dispatch latency, source failures, deduped) — but **no exporter/HTTP endpoint wired**; nothing scrapes it. | `slog` default text handler to stderr (unstructured); a separate `events.jsonl` dispatch-outcome ledger (structured, but not a log stream). Neither reaches Loki. |
| pg-desk `serve`           | One hardcoded gauge, `pg_desk_up 1` (D24 stub, deliberately not dashboard-grade).                                                                                                                                                                                                  | Plain text to `~/Library/Logs/pg-desk-serve.log`; not structured, not exported.                                                                                   |
| pg-desk `run`/`run issue` | N/A (one-shot, not scraped).                                                                                                                                                                                                                                                       | Already structured JSON to stderr (pg-router captures it as the handler's own output) — this part is NOT part of this design; only `serve`'s own logging changes. |

Both `home/programs/pg-router/default.nix` and ZR's `modules/zm/default.nix` have **no**
`OTEL_EXPORTER_OTLP_*` or scrape-target configuration for pg-router today — the gap is deployment
wiring, not missing library support (`resolveMeterProvider`'s external-provider seam already
exists in the code).

## 4. pg-router: metrics export

1. Add an OTel `go.opentelemetry.io/otel/exporters/prometheus` exporter as `cmd/pg-router`'s
   default `MeterProvider` in daemon mode (fills `resolveMeterProvider`'s existing
   deployment-bound-backend case).
2. Serve it over a new, small HTTP listener — no such listener exists in `cmd/pg-router` today,
   unlike pg-desk's `serve`. New CLI flag/config, e.g. `--metrics-addr` (default disabled;
   deployment opts in), mirroring pg-desk's `serve --addr`/`--port` convention.
3. Default port: **`9820`** (next free after pg-pr's `9818`/pg-desk's `9819`).
4. Register that port as `phillipgreenii.observability.metricsTargets.pg-router` in ZR's config —
   the same one-line bridge pg-pr's dashboard toggle already uses
   (`darwin/modules/observability/registration.nix`'s existing pattern).

## 5. pg-router: logs export

1. New `internal/telemetry` package in `pg-router` (mirrors `pg-pr`'s `telemetry.go`/`slog.go`
   verbatim in shape — `Init(ctx, "pg-router", version)`, `NewSlogHandler()`, `Fanout(...)`).
   `internal/` means it cannot be imported across the module boundary from pg-pr, so this is a
   deliberate small duplication of a proven pattern, not a new one.
2. At startup, wrap the default `slog` logger:
   `slog.SetDefault(slog.New(telemetry.Fanout(existingStderrHandler, telemetry.NewSlogHandler())))`
   — stderr output is kept (no regression for interactive/manual runs) and every record is also
   pushed over OTLP once `Init` is called.
3. `OTEL_SERVICE_NAME=pg-router` (single binary, no sub-parts needing separate naming, unlike
   pg-pr's `-sync` suffix).
4. Deployment: set `OTEL_EXPORTER_OTLP_ENDPOINT` (pointing at the existing otel-stack's collector)
   in the LaunchAgent's `EnvironmentVariables` — the same class of gap `pg2-p2ty2`/`pg2-8o2cg`
   already found for this LaunchAgent's environment (see §13's note on sequencing).

## 6. pg-router dashboard (`pg-router.json`)

New file, `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-router.json`,
own folder `pg-router` in `ui.nix` (not `pg-pr`'s folder).

| Panel                                  | Query                                                                                                   | Type                                                                                                    |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Liveness                               | `pg_router_liveness`                                                                                    | stat, 0/1 (green/red, mirrors "Snapshot present")                                                       |
| Queue depth (by type)                  | `pg_router_queue_depth`                                                                                 | timeseries                                                                                              |
| Backlog (total)                        | `pg_router_backlog`                                                                                     | stat                                                                                                    |
| Throughput / sec (by type)             | `sum by (type) (rate(pg_router_throughput_total[$__rate_interval]))`                                    | timeseries                                                                                              |
| **Unconsumed-expired / sec (by type)** | `sum by (type) (rate(pg_router_unconsumed_expired_total[$__rate_interval]))`                            | timeseries — `OQ-ZR-METRICS`'s named "no event misses" panel                                            |
| **Failure rate / sec (by class)**      | `sum by (class) (rate(pg_router_failures_total[$__rate_interval]))`                                     | timeseries, red threshold like `pg-pr-ops`'s "Sync errors" — `OQ-ZR-METRICS`'s named failure-rate alert |
| Unknown-type-rejected / sec (by type)  | `sum by (type) (rate(pg_router_unknown_type_rejected_total[$__rate_interval]))`                         | timeseries                                                                                              |
| Dispatch latency p50/p95               | `histogram_quantile(0.5/0.95, sum by (le) (rate(pg_router_dispatch_latency_bucket[$__rate_interval])))` | timeseries                                                                                              |
| Source failures / sec (by source)      | `sum by (source) (rate(pg_router_source_failures_total[$__rate_interval]))`                             | timeseries                                                                                              |
| Deduped / sec (by type)                | `sum by (type) (rate(pg_router_deduped_total[$__rate_interval]))`                                       | timeseries                                                                                              |
| **Error log**                          | Loki, `{service_name="pg-router"} \| severity_text =~ "WARN\|ERROR"`                                    | logs, respects dashboard time range — the panel requested in this brainstorm                            |

`throughput` and `dispatch_latency` are registered in code but have **no live call site wired
yet** (per `metrics.go`'s own doc comments) — their panels will read empty/flat until that lands.
This is flagged explicitly rather than shipping a silently-empty panel with no explanation; §13
records it as a known, pre-existing gap this design does not itself close.

## 7. pg-desk / My Work dashboard (promotion, not redesign)

Rename `pg-pr-soak.json` → `pg-desk.json`. Retitle `"pg-desk / My Work"`, `uid: pg-desk-my-work`,
tags `["pg-desk"]` (drop `pg-pr`/`pg-desk-soak`). Move to its own `pg-desk` folder in `ui.nix`
(currently grouped under the `pg-pr` folder). Content is unchanged — it already correctly queries
port `9819`'s `/api/v1/dashboard`: Team PRs Act Now/Blocked, My PRs Act Now/Awaiting
Others/Awaiting Other Things, Data Age, Hidden/Dropped (7 panels total).

`pg-pr.json` (port `9818`) is untouched by this design — it retires at the Phase 11 cutover
per `options.nix`'s already-documented plan, not as part of this observability work.

## 8. pg-desk: metrics catalog

Replaces the `pg_desk_up` stub. New OTel metrics (same API/exporter approach as pg-router, §4):

| Metric                          | Type             | Source                                                                                                                                                            |
| ------------------------------- | ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pg_desk_liveness`              | gauge (0/1)      | mirrors `pg_router_liveness`                                                                                                                                      |
| `pg_desk_dashboard_age_seconds` | gauge            | promotes the existing `/api/v1/dashboard` payload's `age_seconds` field so it's alertable, not just displayed                                                     |
| `pg_desk_dashboard_stale`       | gauge (0/1)      | promotes the payload's `stale` field                                                                                                                              |
| `pg_desk_dropped`               | gauge            | promotes the payload's `dropped_count` (a point-in-time count, hence gauge not counter — matches the existing "Dropped PR Count" panel's `lastNotNull` semantics) |
| `pg_desk_sync_errors_total`     | counter, by repo | `sync_error` is populated once sync runs and fails for a PR (Phase 10) — mirrors `pg-pr-ops`'s "Sync errors" panel                                                |

Served on its own HTTP metrics endpoint (reuse `serve`'s existing listener and `/metrics` route —
no new port needed here, unlike pg-router which has none today) and registered as
`metricsTargets.pg-desk`.

## 9. pg-desk: logs export

Same treatment as §5, scoped to `serve` only (`run`/`run issue` already log structured JSON and
are out of scope here): new `internal/telemetry` package in `pg-desk`, `Init` +
`slog.SetDefault(Fanout(...))` wired into `serve`'s startup. `OTEL_SERVICE_NAME=pg-desk-serve`
(mirrors pg-pr's `-sync` per-subsystem suffix convention — `serve` is the long-running daemon
subsystem, distinct in identity from one-shot `run`/`run issue` invocations). Same
`OTEL_EXPORTER_OTLP_ENDPOINT` deployment-wiring requirement as §5.

## 10. pg-desk / Metrics dashboard (`pg-desk-metrics.json`)

New file, same `phillipgreenii-nix-support-apps` location, `pg-desk` folder (alongside the
promoted My Work dashboard, but a separate file/uid).

| Panel                       | Query                                                                    | Type                                                                 |
| --------------------------- | ------------------------------------------------------------------------ | -------------------------------------------------------------------- |
| Liveness                    | `pg_desk_liveness`                                                       | stat                                                                 |
| Dashboard data age (s)      | `pg_desk_dashboard_age_seconds`                                          | stat, same thresholds style as `pg-pr-ops`'s "Fingerprint freshness" |
| Stale                       | `pg_desk_dashboard_stale`                                                | stat, 0/1 mapped present/absent-style                                |
| Dropped PRs                 | `pg_desk_dropped`                                                        | stat                                                                 |
| Sync errors / sec (by repo) | `sum by (repo) (rate(pg_desk_sync_errors_total[$__rate_interval]))`      | timeseries, red threshold                                            |
| **Error log**               | Loki, `{service_name="pg-desk-serve"} \| severity_text =~ "WARN\|ERROR"` | logs, respects dashboard time range                                  |

## 11. Files and nix wiring summary

| Repo                               | Change                                                                                                                                                                                                                                                                                                 |
| ---------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `phillipgreenii-nix-agent-support` | `packages/pg-router`: `internal/telemetry` (new), metrics exporter + `--metrics-addr` wiring in `cmd/pg-router`. `packages/pg-desk`: `internal/telemetry` (new), real metrics catalog replacing the `/metrics` stub, `serve` logging wired to it.                                                      |
| `phillipg-nix-ziprecruiter`        | `modules/zm/default.nix`: `OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_SERVICE_NAME` in both LaunchAgents' `EnvironmentVariables`; pg-router's `--metrics-addr`/port `9820` in its daemon config.                                                                                                               |
| `phillipgreenii-nix-support-apps`  | `darwin/modules/observability/`: `metricsTargets.pg-router`/`.pg-desk` entries (`registration.nix`); new `dashboards/pg-router.json`, `dashboards/pg-desk-metrics.json`; rename `dashboards/pg-pr-soak.json` → `dashboards/pg-desk.json`; `ui.nix` folder/dashboard-list updates for all of the above. |

## 12. pg-pr-ops.json panel mapping (14 panels → successor or dropped)

| #   | pg-pr-ops panel                        | Verdict                                                                                                                                                          |
| --- | -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Fingerprint freshness (s)              | Dropped — GitHub-polling-specific; pg-router's Liveness/Backlog cover "is it alive" instead                                                                      |
| 2   | Snapshot present                       | Dropped — no snapshot concept in pg-router                                                                                                                       |
| 3   | PRs synced / sec                       | → pg-router Throughput / sec (by type)                                                                                                                           |
| 4   | Sync errors / sec (by repo)            | → pg-router Failure rate / sec (by class) — granularity shifts from repo to failure class                                                                        |
| 7   | Sync error log                         | → pg-router Error log panel (§6)                                                                                                                                 |
| 5   | Feedback beads created / sec (by kind) | → pg-router Throughput / sec (by type) — "kind" folds into the existing type dimension                                                                           |
| 6   | Per-PR sync duration (p50/p95)         | → pg-router Dispatch latency p50/p95                                                                                                                             |
| 8   | Refresh queue depth                    | → pg-router Queue depth (by type) + Backlog                                                                                                                      |
| 9   | Fingerprint poll rate                  | Dropped — GitHub-polling-specific, no pg-router analogue                                                                                                         |
| 10  | Fingerprint poll p95 (s)               | Dropped — same reason                                                                                                                                            |
| 11  | Changes detected (by kind)             | Dropped — closest available signal is Throughput; pg-router has no poll-diff concept                                                                             |
| 12  | Roster truncation rate                 | Dropped — GitHub pagination-specific                                                                                                                             |
| 13  | GraphQL rate remaining                 | Dropped — GitHub GraphQL-specific                                                                                                                                |
| 14  | GraphQL polling paused                 | Dropped — closest concept is usage-window/backoff state, which is explicitly out of pg-router's own catalog (D4/§13), not a person-facing pg-router signal today |

New pg-router-only panels with no pg-pr-ops ancestor: Liveness, Unconsumed-expired,
Unknown-type-rejected, Source failures, Deduped.

## 13. Out of scope / open gaps (recorded, not silently dropped)

- Per-role session activity, budget consumption, usage-window/backoff state (`INV-MON-2`) belong
  to ccpool/pa-monitor, not pg-router's own catalog — a future dashboard's concern (D4).
- `pg_router.throughput` and `pg_router.dispatch_latency` are registered but not yet fed by a live
  call site (§6) — their panels will be empty until a separate task wires that.
- Env wiring (`OTEL_EXPORTER_OTLP_ENDPOINT`, `--metrics-addr`) lands in the SAME LaunchAgent
  environment that `pg2-p2ty2` and `pg2-8o2cg` already found gaps in (missing `PATH`, missing
  `BD_JSON_ENVELOPE`) — implementers should check that plist once, not patch it three separate
  times.
- Traces: not requested, not designed here.
- The Phase 11 port cutover (`pg-pr.json` retirement, port `9818` handoff) is unaffected and
  unblocked by this design — it proceeds on its own documented schedule.

## 14. Acceptance criteria

- [ ] `pg-router` daemon serves real Prometheus metrics on port `9820` (all 10 catalog members,
      `throughput`/`dispatch_latency` present but may read zero pending their own call-site work).
- [ ] `pg-router`'s WARN/ERROR-level operational log lines are queryable in Loki as
      `{service_name="pg-router"}`.
- [ ] `pg-router.json` dashboard renders all panels in §6 against a live scrape (the two
      not-yet-wired metrics may show empty, clearly distinguishable from a broken panel).
- [ ] `pg-desk.json` (promoted) renders identically to today's `pg-pr-soak.json` content, just
      retitled/retagged/refoldered.
- [ ] `pg-desk` `serve` exposes the §8 catalog on its existing metrics port, registered via
      `metricsTargets.pg-desk`.
- [ ] `pg-desk`'s WARN/ERROR-level `serve` log lines are queryable in Loki as
      `{service_name="pg-desk-serve"}`.
- [ ] `pg-desk-metrics.json` dashboard renders all panels in §10.
- [ ] `pg2-7kizi`'s own acceptance criteria (inventory, panel mapping, successor decision) are
      satisfied by §3, §12, and this document as a whole.

## Appendix: provenance

Brainstormed interactively with the operator, 2026-09-17, following the operator's own framing
(two — later corrected to three — dashboards, "see what's in pg-pr and create the equivalent").
Corrections made mid-session: `events.jsonl` is not a log stream (operator correction, §5); the
workspace's actual OTLP-log convention is `pg-pr`'s `internal/telemetry` pattern, not a
JSONL-file/`logSources` pull-collection approach (operator correction, §5, §9). Metric names and
current wiring gaps verified directly against `packages/pg-router/internal/metrics/metrics.go`,
`packages/pg-desk/internal/httpapi/server.go`, `docs/behavior/pg-desk/README.md`'s D24
declaration, and the ZR/support-apps nix modules — not assumed.
