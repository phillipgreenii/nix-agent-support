# pg-router and pg-desk observability: metrics, logs, and the Ops-board successor dashboards

## 1. Purpose and scope

This is the design `pg2-7kizi` ("pg-desk / pg-connector / pr-pool: telemetry inventory and
Ops-board successor") calls for: an inventory of what pg-router and pg-desk actually emit today,
a mapping of the pg-pr Ops board's 14 panels to a successor or a dropped verdict, and a decision
on the successor dashboards and what each tool's `/metrics` must expose beyond a scrape-keeps-green
stub. `docs/behavior/pg-desk/serve.md`'s D24 declaration explicitly deferred real OpenTelemetry
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
metrics and logs were asked for); the Phase 11 port cutover itself (`pg-pr.json` retirement);
actually deleting `pg-pr-ops.json`/`testdata/pgpr-baseline.yml` (Phase 12's own job — see D7).

## 2. Decisions ledger

- **D1.** Three dashboards, not two: pg-desk's triage table ("My Work") and its own health/error
  telemetry ("Metrics") are different audiences and different data sources (a JSON triage payload
  vs. Prometheus/Loki) and MUST NOT be conflated into one dashboard.
- **D2.** pg-router's metrics export via a new OTel `go.opentelemetry.io/otel/exporters/prometheus`
  bridge, exposing a directly-scraped `/metrics` endpoint. This is a **new dependency for the
  workspace** (absent everywhere today) — it is OUTCOME-equivalent to pg-pr's own direct-scrape
  `/metrics` (both end up Prometheus-scraped, no OTLP push involved for metrics), but NOT the same
  underlying mechanism: pg-pr's `/metrics` is hand-rolled on raw `client_golang`
  (`packages/pg-pr/internal/telemetry/metrics.go`), whereas pg-router's metric catalog is already
  OTel-API-native (`internal/metrics/metrics.go`), so bridging it through OTel's own Prometheus
  exporter is the natural fit for pg-router specifically, not a copy of pg-pr's code.
- **D3.** Logs (both tools) export via OTLP push, mirroring pg-pr's existing `internal/telemetry`
  pattern (`Init` + `otelslog` bridge + `Fanout`) for OPERATIONAL logs (the `slog` stream —
  warnings, errors, config issues). This is genuinely a different mechanism from pg-router's
  EXISTING `logSources.pg-router` registration (`darwin/modules/pg-router/default.nix`), which
  already pull-collects `events.jsonl` — pg-router's internal dispatch-outcome ledger — into Loki
  under `service_name="pg-router"` **today**, live on the deployed machine. That existing
  registration is narrower than what an "error log" panel needs (it only carries dispatch
  outcomes, not the operational warnings that currently only reach stderr as unstructured text)
  and, left unchanged, would collide on the same `service_name` label once the new OTLP push
  claims it. Resolution: relabel the EXISTING `logSources.pg-router` entry's `serviceName` to
  `pg-router-events` (one line, preserves its current narrow visibility under a now-distinct
  label) and let the NEW OTLP-push path own the plain `pg-router` service name — the one the
  error-log panel actually queries. `events.jsonl` itself is untouched as a file; only its Loki
  label changes. pg-desk's `serve` has no equivalent registration to reconcile — it gets the OTLP
  push pattern fresh.
- **D4.** The pg-router dashboard is scoped to pg-router's own already-built 10-metric catalog
  (`internal/metrics/metrics.go`) only. The INV-MON-2 signals that catalog does not cover
  (per-role activity, budget, usage-window/backoff) are recorded as a gap for ccpool/pa-monitor's
  own future dashboard, not pulled into this one.
- **D5.** pg-desk's `/metrics` stub (`pg_desk_up 1`, hardcoded) is replaced with a real catalog
  (§8), built the same way pg-router's already is (OTel metrics API + `prometheus.Exporter`).
- **D6.** OTLP env wiring (`OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_SERVICE_NAME`) for BOTH daemons goes
  through `phillipgreenii.observability.mkEmitterEnv` — an existing, already-used helper
  (`phillipgreenii-nix-support-apps/darwin/modules/observability/emitter.nix`; consumed today by
  `pa-monitor`'s own darwin module) — called from EACH tool's OWN darwin module
  (`darwin/modules/pg-router/default.nix`, `darwin/modules/pg-desk-serve/default.nix`, both in
  `phillipgreenii-nix-agent-support`), mirroring `pa-monitor`'s precedent exactly. This is
  `phillipgreenii-nix-agent-support`'s own work, NOT something ZR's `modules/zm/default.nix` can
  do on its own — ZR only sets home-manager option VALUES for these tools, it does not own either
  module's LaunchAgent plist/script rendering, so there is no ZR-side env var to patch here.
- **D7.** Retiring the OLD board (`pg-pr-ops.json`, `testdata/pgpr-baseline.yml`, and their
  `ui.nix`/`dashboardProviders.pg-pr` entries) is Phase 12's own packet, gated on `pg2-7kizi`
  closing — NOT this document's implementation scope. This design's job is to make the successor
  exist and be decided; landing it is what UNBLOCKS Phase 12 to perform the actual deletion.

## 3. Current state (the inventory)

Per `pg2-7kizi`'s own "What" item (1), this inventory covers all four named components, not just
the two with new design work:

| Component                                                                    | Metrics today                                                                                                                                                                                                                                                                      | Logs today                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| ---------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| pg-router (`pr-pool`)                                                        | Full 10-member OTel catalog in `internal/metrics/metrics.go` (queue depth, failures by class, unconsumed-expired, unknown-type-rejected, throughput, backlog, liveness, dispatch latency, source failures, deduped) — but **no exporter/HTTP endpoint wired**; nothing scrapes it. | `events.jsonl` (the dispatch-outcome ledger) is **already** pull-collected into Loki today via `logSources.pg-router` (`darwin/modules/pg-router/default.nix`), live on the deployed machine — but that only carries dispatch outcomes. Operational warnings (`internal/discover/discover.go`'s `slog.Warn` on source failures, config issues, etc.) go to the default `slog` text handler on stderr only — unstructured, not exported anywhere. |
| pg-connector's delta ledger and `changes`/`ledger show`/`ledger clear` verbs | None. Explicitly declared in `packages/pg-connector/docs/behavior/interfaces.md`'s own D24 telemetry notes: emits nothing over OTel/Prometheus, logs nothing structured.                                                                                                           | None (same declaration).                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `pg-router-source-pg-connector` (the query-side adapter)                     | None. `packages/pg-router-source-pg-connector/docs/behavior/README.md`'s own "Telemetry (D24)" section: "emits no OpenTelemetry or Prometheus telemetry of any kind."                                                                                                              | Stderr-only, and only on a failure path (same section).                                                                                                                                                                                                                                                                                                                                                                                          |
| pg-desk `serve`                                                              | One hardcoded gauge, `pg_desk_up 1` (D24 stub, deliberately not dashboard-grade).                                                                                                                                                                                                  | Plain text to `~/Library/Logs/pg-desk-serve.log`; not structured, not exported, no `logSources` registration.                                                                                                                                                                                                                                                                                                                                    |
| pg-desk `run`/`run issue`                                                    | N/A (one-shot, not scraped).                                                                                                                                                                                                                                                       | Already structured JSON to stderr (pg-router captures it as the handler's own output) — out of scope; only `serve`'s own logging changes in this design.                                                                                                                                                                                                                                                                                         |
| pg-desk `status` (and `doctor`/`heartbeat`)                                  | None. `docs/behavior/pg-desk/operator-commands.md`'s "Telemetry and logs" section: all five commands emit nothing over OTel/Prometheus in Phase 9 (D24).                                                                                                                           | Ordinary CLI report/error text only.                                                                                                                                                                                                                                                                                                                                                                                                             |

The three "emits nothing" rows above (pg-connector's ledger, the source adapter, and pg-desk's
`status`/`doctor`/`heartbeat`) satisfy `pg2-7kizi`'s inventory ask for those components as-is — no
new design work follows from them; they are recorded here because the bead asked for the
inventory to be explicit, not because anything needs building for them.

Both `home/programs/pg-router/default.nix` and ZR's `modules/zm/default.nix` have **no**
`OTEL_EXPORTER_OTLP_*` or scrape-target configuration for pg-router today — the gap is deployment
wiring, not missing library support (`resolveMeterProvider`'s external-provider seam already
exists in the code).

## 4. pg-router: metrics export

1. Add an OTel `go.opentelemetry.io/otel/exporters/prometheus` exporter as `cmd/pg-router`'s
   default `MeterProvider` in daemon mode (fills `resolveMeterProvider`'s existing
   deployment-bound-backend case). This is a new dependency for the workspace — see D2.
2. Serve it over a new, small HTTP listener — no such listener exists in `cmd/pg-router` today,
   unlike pg-desk's `serve`. New CLI flag/config, e.g. `--metrics-addr` (default disabled;
   deployment opts in), mirroring pg-desk's `serve --addr`/`--port` convention.
3. Default port: **`9820`** (next free after pg-pr's `9818`/pg-desk's `9819`; verified free
   workspace-wide).
4. ZR sets `phillipgreenii.observability.metricsTargets.pg-router = { port = 9820; }` directly in
   `modules/zm/default.nix` — the same pattern ZR already uses for `logSources` (e.g.
   `observability.logSources.zr-local-proxy-probe = { };` in
   `darwin/services/local-proxy/default.nix`). This is NOT the same shape as pg-pr's own bridge
   (`registration.nix`'s `metricsTargets.pg-pr = lib.mkIf cfg.dashboards.pgPr.enable { ... }`,
   which lives entirely inside `phillipgreenii-nix-support-apps` behind a dedicated toggle option)
   — pg-router doesn't need an equivalent toggle option added to `nix-support-apps`; ZR can set the
   target directly, exactly as it already does for its own `logSources` entries.

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
4. Env wiring: `darwin/modules/pg-router/default.nix` calls
   `obs.mkEmitterEnv { serviceName = "pg-router"; protocol = "grpc"; }` (or the workspace's default
   protocol choice — match `pa-monitor`'s own call exactly) and merges the result into the
   LaunchAgent's `EnvironmentVariables`, mirroring `pa-monitor`'s existing module precedent (D6).
   No ZR-side change needed for this piece.
5. Relabel the EXISTING `logSources.pg-router` entry in that same file to
   `logSources.pg-router-events = { serviceName = "pg-router-events"; }` (or equivalent — keep the
   glob/path unchanged, only the label moves) so it no longer collides with the new OTLP-push
   stream's `service_name="pg-router"` (D3). This is the one file this design touches in
   `phillipgreenii-nix-agent-support`'s darwin modules for pg-router's logs.

## 6. pg-router dashboard (`pg-router.json`)

New file, `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-router.json`,
own folder `pg-router` in `ui.nix` (not `pg-pr`'s folder). Every stat panel uses
`colorMode: "background"` with a green/yellow/red (or green/red) threshold ladder, matching
`pg-pr-ops.json`'s existing convention — applied uniformly here rather than panel-by-panel, so no
implementer leaves one stat unstyled relative to its neighbors.

Panels, in the order they should actually be laid out (cause-then-detail adjacency, matching
`pg-pr-ops.json`'s proven layout — the failure signal sits directly above its own log, not at the
bottom of the dashboard):

| Panel                                 | Query                                                                                                   | Type                                | Notes                                                                                                                                                                                                                                                                                                                                                                              |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------- | ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Liveness                              | `pg_router_liveness`                                                                                    | stat, 0/1 green/red                 | mirrors "Snapshot present"                                                                                                                                                                                                                                                                                                                                                         |
| Backlog (total)                       | `pg_router_backlog`                                                                                     | stat                                |                                                                                                                                                                                                                                                                                                                                                                                    |
| Queue depth (by type)                 | `pg_router_queue_depth`                                                                                 | timeseries                          |                                                                                                                                                                                                                                                                                                                                                                                    |
| **Failure rate / sec (by class)**     | `sum by (class) (rate(pg_router_failures_total[$__rate_interval]))`                                     | timeseries, red threshold           | `OQ-ZR-METRICS`'s named failure-rate alert. Only 2 possible `class` values today (`declined`, `dispatch-failure` — per `metrics.go`), so legend cardinality is low; this assumption should be revisited if the catalog grows.                                                                                                                                                      |
| **Error log**                         | Loki, `{service_name="pg-router"} \| severity_text =~ "WARN\|ERROR"`                                    | logs, respects dashboard time range | Carries the SAME caveat `pg-pr-ops.json`'s own "Sync error log" panel documents in its `description` field: the `severity_text` filter depends on Loki's OTLP-severity config; if empty unexpectedly, check whether the field is actually named `detected_level` before assuming nothing is wrong. Copy that description verbatim (service name adjusted) rather than omitting it. |
| Unconsumed-expired / sec (by type)    | `sum by (type) (rate(pg_router_unconsumed_expired_total[$__rate_interval]))`                            | timeseries                          | `OQ-ZR-METRICS`'s named "no event misses" panel                                                                                                                                                                                                                                                                                                                                    |
| Unknown-type-rejected / sec (by type) | `sum by (type) (rate(pg_router_unknown_type_rejected_total[$__rate_interval]))`                         | timeseries                          |                                                                                                                                                                                                                                                                                                                                                                                    |
| Throughput / sec (by type)            | `sum by (type) (rate(pg_router_throughput_total[$__rate_interval]))`                                    | timeseries                          | **Not yet wired to a live call site** (see §13) — panel `description` MUST say so explicitly ("registered, no live call site wired yet — expected empty, not broken") so an empty panel is never mistaken for a broken one from inside Grafana itself, not just from this document.                                                                                                |
| Dispatch latency p50/p95              | `histogram_quantile(0.5/0.95, sum by (le) (rate(pg_router_dispatch_latency_bucket[$__rate_interval])))` | timeseries                          | Same "not yet wired" caveat and `description` requirement as Throughput.                                                                                                                                                                                                                                                                                                           |
| Source failures / sec (by source)     | `sum by (source) (rate(pg_router_source_failures_total[$__rate_interval]))`                             | timeseries                          |                                                                                                                                                                                                                                                                                                                                                                                    |
| Deduped / sec (by type)               | `sum by (type) (rate(pg_router_deduped_total[$__rate_interval]))`                                       | timeseries                          |                                                                                                                                                                                                                                                                                                                                                                                    |

## 7. pg-desk / My Work dashboard (promotion, not redesign)

Rename `pg-pr-soak.json` → `pg-desk.json`. Retitle `"pg-desk / My Work"`, `uid: pg-desk-my-work`,
tags `["pg-desk"]` (drop `pg-pr`/`pg-desk-soak`). Move to its own `pg-desk` folder in `ui.nix`
(currently grouped under the `pg-pr` folder). Content is unchanged — it already correctly queries
port `9819`'s `/api/v1/dashboard`: Team PRs Act Now/Blocked, My PRs Act Now/Awaiting
Others/Awaiting Other Things, Data Age, Hidden/Dropped (7 panels total). Its collapsed "Dropped PR
Count" panel and the new pg-desk / Metrics dashboard's top-level `pg_desk_dropped` stat (§10) show
the SAME underlying count from two different surfaces (JSON payload vs. Prometheus gauge) — this
is expected and the two should always agree; see §10's note.

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
`metricsTargets.pg-desk` (ZR sets this directly, same as §4's pg-router entry).

`pg_desk_dashboard_stale`'s value mapping MUST be explicit, not analogized from another panel:
`0 = fresh (green)`, `1 = stale (red)`. This is the OPPOSITE polarity from `pg-pr-ops.json`'s
"Snapshot present" panel (where `1` is the good state) — do not copy that panel's mapping by
analogy; a naive "present/absent-style" copy inverts the signal and would show green while the
dashboard data is actually stale, which is the one failure mode a freshness panel exists to catch.

`pg_desk_dashboard_age_seconds`'s Grafana thresholds MUST be derived from pg-desk's own dynamic
staleness bound (`server.go`'s `staleAfterSeconds := syncIntervalSeconds * boundIntervals`), NOT
copied from `pg-pr-ops.json`'s hardcoded fingerprint-freshness numbers (600s/900s, tuned for a
different tool's polling cadence). Concretely: yellow at `0.75 * staleAfterSeconds`, red at
`staleAfterSeconds` itself, computed from the SAME value `pg_desk_dashboard_stale` uses — so the
two adjacent stat panels can never visually disagree (one green while the other reads red for the
same underlying condition).

## 9. pg-desk: logs export

Same treatment as §5, scoped to `serve` only (`run`/`run issue` already log structured JSON and
are out of scope here): new `internal/telemetry` package in `pg-desk`, `Init` +
`slog.SetDefault(Fanout(...))` wired into `serve`'s startup. `OTEL_SERVICE_NAME=pg-desk-serve`
(mirrors pg-pr's `-sync` per-subsystem suffix convention — `serve` is the long-running daemon
subsystem, distinct in identity from one-shot `run`/`run issue` invocations). Env wiring goes
through `darwin/modules/pg-desk-serve/default.nix` calling `obs.mkEmitterEnv { serviceName =
"pg-desk-serve"; ... }`, the same `pa-monitor`-mirroring pattern as §5.4 (D6) — a DIFFERENT
LaunchAgent/module from pg-router's (`com.phillipg.pg-desk-serve`, not
`com.phillipg.pg-router-daemon`), starting from a clean slate with no existing `logSources`
registration to reconcile, unlike pg-router's §5.5.

## 10. pg-desk / Metrics dashboard (`pg-desk-metrics.json`)

New file, same `phillipgreenii-nix-support-apps` location, `pg-desk` folder (alongside the
promoted My Work dashboard, but a separate file/uid/title — pinned explicitly here since the two
dashboards sharing one folder means the title is the ONLY thing distinguishing them in Grafana's
list view):

- `title: "pg-desk / Metrics"`
- `uid: pg-desk-metrics`
- `tags: ["pg-desk"]`

Same stat-panel styling rule as §6 (uniform `colorMode: "background"` + threshold ladder).

| Panel                           | Query                                                                    | Type                                       | Notes                                                                                                                                   |
| ------------------------------- | ------------------------------------------------------------------------ | ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- |
| Liveness                        | `pg_desk_liveness`                                                       | stat, 0/1 green/red                        | mirrors `pg_router_liveness`                                                                                                            |
| Stale                           | `pg_desk_dashboard_stale`                                                | stat, explicit 0=fresh(green)/1=stale(red) | see §8 — do NOT copy "Snapshot present"'s polarity                                                                                      |
| Dashboard data age (s)          | `pg_desk_dashboard_age_seconds`                                          | stat                                       | thresholds derived from `staleAfterSeconds`, not copied from `pg-pr-ops` — see §8                                                       |
| Dropped PRs                     | `pg_desk_dropped`                                                        | stat                                       | same underlying value as My Work's collapsed "Dropped PR Count" (§7) — expected to always agree                                         |
| **Sync errors / sec (by repo)** | `sum by (repo) (rate(pg_desk_sync_errors_total[$__rate_interval]))`      | timeseries, red threshold                  |                                                                                                                                         |
| **Error log**                   | Loki, `{service_name="pg-desk-serve"} \| severity_text =~ "WARN\|ERROR"` | logs, respects dashboard time range        | Same `severity_text`/`detected_level` fragility caveat as §6's pg-router error-log panel — carry the `description` text, don't omit it. |

Sync errors and Error log are placed adjacently (cause directly above detail), matching
`pg-pr-ops.json`'s proven layout — the same adjacency §6 now also follows for pg-router.

## 11. Files and nix wiring summary

| Repo                               | Change                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| ---------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `phillipgreenii-nix-agent-support` | `packages/pg-router`: `internal/telemetry` (new), metrics exporter + `--metrics-addr` wiring in `cmd/pg-router`. `packages/pg-desk`: `internal/telemetry` (new), real metrics catalog replacing the `/metrics` stub, `serve` logging wired to it. `darwin/modules/pg-router/default.nix`: add `obs.mkEmitterEnv` call (§5.4); relabel the existing `logSources.pg-router` entry to `pg-router-events` (§5.5). `darwin/modules/pg-desk-serve/default.nix`: add `obs.mkEmitterEnv` call (§9).               |
| `phillipg-nix-ziprecruiter`        | `modules/zm/default.nix`: pg-router's `--metrics-addr`/port `9820` daemon config; `metricsTargets.pg-router`/`.pg-desk` entries (§4.4, §8 — ZR sets these directly, same pattern it already uses for its own `logSources` entries). No OTLP env-var changes here — that's `mkEmitterEnv`'s job, entirely inside `phillipgreenii-nix-agent-support` (D6).                                                                                                                                                  |
| `phillipgreenii-nix-support-apps`  | `darwin/modules/observability/`: new `dashboards/pg-router.json`, `dashboards/pg-desk-metrics.json`; rename `dashboards/pg-pr-soak.json` → `dashboards/pg-desk.json`; `ui.nix` folder/dashboard-list updates for all of the above. **Not in scope here** (Phase 12's job, D7): removing `dashboards/pg-pr-ops.json`, `darwin/services/prometheus/testdata/pgpr-baseline.yml`, or their `ui.nix`/`dashboardProviders.pg-pr` entries — this design's landing unblocks that deletion, it doesn't perform it. |

## 12. pg-pr-ops.json panel mapping (14 panels → successor or dropped)

Ordered by the panels' own `id` field in `pg-pr-ops.json` (ascending), not by their visual
position, to avoid the out-of-sequence numbering an earlier draft of this table had:

| #   | pg-pr-ops panel                        | Verdict                                                                                                                                                          |
| --- | -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Fingerprint freshness (s)              | Dropped — GitHub-polling-specific; pg-router's Liveness/Backlog cover "is it alive" instead                                                                      |
| 2   | Snapshot present                       | Dropped — no snapshot concept in pg-router                                                                                                                       |
| 3   | PRs synced / sec                       | → pg-router Throughput / sec (by type)                                                                                                                           |
| 4   | Sync errors / sec (by repo)            | → pg-router Failure rate / sec (by class) — granularity shifts from repo to failure class                                                                        |
| 5   | Feedback beads created / sec (by kind) | → pg-router Throughput / sec (by type) — "kind" folds into the existing type dimension                                                                           |
| 6   | Per-PR sync duration (p50/p95)         | → pg-router Dispatch latency p50/p95                                                                                                                             |
| 7   | Sync error log                         | → pg-router Error log panel (§6)                                                                                                                                 |
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
  call site (§6) — their panels will be empty until a separate task wires that, and MUST carry an
  in-Grafana `description` saying so (§6), not just a note in this document.
- Env wiring (`mkEmitterEnv`, `--metrics-addr`) touches TWO separate LaunchAgents/modules —
  `com.phillipg.pg-router-daemon` (`darwin/modules/pg-router/default.nix`, the same module
  `pg2-p2ty2` — closed, fixed via absolute-path argv rewrites — and `pg2-8o2cg` — open, missing
  `BD_JSON_ENVELOPE` — already found separate gaps in) and `com.phillipg.pg-desk-serve`
  (`darwin/modules/pg-desk-serve/default.nix`, untouched by either prior bead, starting clean).
  Implementers should check pg-router's module once for this design's own change plus whatever
  `pg2-8o2cg` lands, rather than three uncoordinated passes at the same file; pg-desk's module has
  no such coordination need.
- Traces: not requested, not designed here.
- The Phase 11 port cutover (`pg-pr.json` retirement, port `9818` handoff) is unaffected and
  unblocked by this design — it proceeds on its own documented schedule.
- Actually deleting the old board and its `ui.nix` wiring is Phase 12's job, not this document's
  (D7) — landing this design is what unblocks that phase.

## 14. Acceptance criteria

- [ ] `pg-router` daemon serves real Prometheus metrics on port `9820` (all 10 catalog members,
      `throughput`/`dispatch_latency` present but may read zero pending their own call-site work,
      with an explicit in-panel `description` saying so).
- [ ] `pg-router`'s WARN/ERROR-level operational log lines are queryable in Loki as
      `{service_name="pg-router"}`, with the pre-existing dispatch-outcome ledger relabeled to
      `pg-router-events` so the two do not collide under one label.
- [ ] `pg-router.json` dashboard renders all panels in §6, laid out cause-then-detail (failure
      rate directly above its error log, matching `pg-pr-ops.json`'s proven layout).
- [ ] `pg-desk.json` (promoted) renders identically to today's `pg-pr-soak.json` content, just
      retitled/retagged/refoldered.
- [ ] `pg-desk` `serve` exposes the §8 catalog on its existing metrics port, registered via
      `metricsTargets.pg-desk`, with `pg_desk_dashboard_stale`'s polarity and
      `pg_desk_dashboard_age_seconds`'s thresholds exactly as specified in §8 (not copied by
      analogy from a differently-poled or differently-tuned pg-pr-ops panel).
- [ ] `pg-desk`'s WARN/ERROR-level `serve` log lines are queryable in Loki as
      `{service_name="pg-desk-serve"}`.
- [ ] `pg-desk-metrics.json` dashboard renders all panels in §10 under its own pinned
      title/uid/tags, distinguishable from `pg-desk.json` in Grafana's dashboard list.
- [ ] `pg2-7kizi`'s own acceptance criteria (inventory of all four named components, panel
      mapping, successor decision) are satisfied by §3, §12, and this document as a whole.

## Appendix: provenance

Brainstormed interactively with the operator, 2026-09-17, following the operator's own framing
(two — later corrected to three — dashboards, "see what's in pg-pr and create the equivalent").
Corrections made mid-session: `events.jsonl` is not the right vehicle for an operational error-log
panel even though it is, narrowly, already Loki-visible (operator correction plus independent
subagent-review finding, §3/§5); the workspace's actual OTLP-log convention for operational
warnings is `pg-pr`'s `internal/telemetry` pattern, not a JSONL-file/`logSources`-only approach
(operator correction, §5, §9).

Reviewed by three independent read-only subagents (correctness, completeness, UX) against the
first drafted revision. Findings incorporated into this revision: the pre-existing
`logSources.pg-router` collision and its relabel-to-`pg-router-events` resolution (correctness);
the pre-existing `mkEmitterEnv` helper replacing ad-hoc ZR-side env wiring, the two missing
inventory rows (pg-connector's ledger, the source adapter, pg-desk `status`), and the explicit
Phase-12-deletion deferral, D7 (completeness); the `pg_desk_dashboard_stale` polarity bug, the
`pg_desk_dashboard_age_seconds` threshold-derivation fix, the carried-forward Loki-severity
caveat, the explicit "not yet wired" panel descriptions, the pinned `pg-desk-metrics.json`
identity, and the cause-then-detail panel reordering (UX). Metric names, current wiring gaps, and
every cited file/pattern were verified directly against source by both the original brainstorm and
the three review passes — not assumed.
