# Spec: nix-managed Dolt maintenance for gc (breaker + self-gating flatten + OTel + dashboard)

**Date:** 2026-06-04
**Status:** Approved (brainstorming)
**Repo:** `phillipgreenii-nix-agent-support`

## Problem

gc's beads live in Dolt, which commits per write. The city's stock + workaround
orders generate high, continuous write volume, so hq's commit history and
storage grow without bound. Two failure modes result:

1. **Acute auto-import spiral.** bd ≤ 1.0.4 misreads a slow/empty query (common
   under deep history + load) as "empty database" and re-imports the entire
   `issues.jsonl` — a bulk commit that deepens history, slowing reads further.
   On 2026-06-04 this took hq from ~10k to ~20k commits in minutes and
   crash-looped the dolt server. Mitigated same day by HACK 18 (immutable-empty
   `issues.jsonl`), but that breaker was applied by hand and had been silently
   un-applied by a city restore — which is what triggered the incident.
2. **Chronic bloat.** The derived-stats journal grows ~1 GB/week; `DOLT_GC()`
   alone never purges it. The real compactor (`hack-archive-and-compact`,
   HACK 10) **stopped firing 2026-05-29** and is no longer materialized — gc's
   order dispatch dropped it, and `[[orders.overrides]]` can't be trusted in
   1.1.0 (HACK 12).

There is also no visibility into maintenance: we can't see what was observed or
done over time.

## Goals

- Keep the import breaker **durably applied** without manual intervention.
- Keep Dolt storage bounded (purge the stats journal) on a reliable schedule.
- Flatten commit history when it's worthwhile **and** safe, escaping gc's
  unreliable order dispatch by driving from **launchd**.
- **Emit OTLP telemetry** (metrics + logs) so each run is observable: what it
  **saw** (signals) and **did** (actions/outcomes).
- **Ship a Grafana dashboard** modeled on the gascity dashboard to view it.
- All logic in nix (`phillipgreenii-nix-agent-support`), tunable via module
  options, fully tested.

## Non-goals

- **Bringing the OTel collector + Grafana stack up.** It's nix-managed in
  support-apps but currently down; a separate agent will fix it during dashboard
  testing. This work only needs to **emit valid OTLP** to the standard endpoints
  (best-effort) and **ship the dashboard JSON**.
- Fixing the separate **archive-repo git push** failure ("JSONL push failed").
- Upgrading bd to remove the auto-import footgun (would retire breaker; HACK 3/18).
- True ephemeral/durable DB separation (larger architectural change; future).

## Decisions (from brainstorming, 2026-06-04)

| Decision                  | Choice                                                                           |
| ------------------------- | -------------------------------------------------------------------------------- |
| Placement                 | Everything in `phillipgreenii-nix-agent-support` (scripts + launchd + dashboard) |
| Flatten gate              | Multi-signal: need + safety + anti-thrash + max-age force                        |
| Breaker management        | home-manager **activation at switch** + manual command (NOT hourly re-assert)    |
| Run context               | **User** (launchd user agent / HM activation). Never root.                       |
| `gc` dependency           | Script **depends on gascity** (gc in runtimeDeps)                                |
| `maxFlattenIntervalHours` | **24**                                                                           |
| Telemetry                 | **OTLP gauges + logs via `curl`→OTLP/JSON** (no Go SDK in bash); best-effort     |
| Dashboard                 | Grafana JSON modeled on `gascity-overview.json`, provisioned via house option    |
| OTel stack up             | **Out of scope** (separate agent, during testing)                                |

## Components

All in `phillipgreenii-nix-agent-support`, built with `mkBashBuilders`,
`public = false` (internal ops tools). Follow existing examples
(`packages/agent-activity/…`, `packages/pa-monitor/…`).

### 1. `gc-bd-import-breaker` (mkBashScript)

The HACK 18 breaker. Logic ported from today's `tools/bd-import-breaker.sh`.

- Actions: `apply` (default), `--status`, `--revert`, `--city DIR`
  (default `$GC_CITY` else `$PWD`).
- `apply`: refuse unless `<city>/.beads/dolt` exists; back up a non-empty
  `issues.jsonl` to `issues.jsonl.breaker-backup-<UTC>`; replace with a 0-byte
  file; `chflags uchg`. Idempotent. macOS only.
- Emits one best-effort OTLP log (`action`=applied/already-applied/reverted,
  `backed_up`) via the shared emit helper.
- runtimeDeps: coreutils, curl.

### 2. `gc-dolt-maintenance` (mkBashScript, `.sh` + `.bash` split)

- **`gc-dolt-maintenance.bash`** — pure, unit-testable decision function:

  `should_flatten(commit_count, busy_proc_count, hours_since_last,
breaker_applied, has_remote, commit_threshold, busy_threshold,
min_interval_h, max_interval_h) -> "yes|no" + reason`. Logic, in order:
  1. `has_remote` → **no** (never flatten a remote-connected DB, e.g. zr)
  2. `!breaker_applied` → **no** (refuse: flatten runs `gc bd`, could re-spiral)
  3. `commit_count < commit_threshold` → **no** (below need)
  4. `hours_since_last ≥ max_interval_h` → **yes** (max-age force, even if busy)
  5. `hours_since_last < min_interval_h` → **no** (anti-thrash)
  6. `busy_proc_count ≥ busy_threshold` → **no** (busy)
  7. else → **yes**

- **`gc-dolt-maintenance.sh`** — orchestration, per local DB under
  `<city>/.beads/dolt/*/`:
  - **Cheap tier (always):** `CALL DOLT_STATS_PURGE()` then `CALL DOLT_GC()` via
    direct `dolt sql` (no `bd` → no import risk). Best-effort, logged.
  - **Flatten (gated):** measure signals → `should_flatten` → if yes,
    `gc bd flatten --force`; on success, write the per-DB timestamp.
  - Signals: `commit_count` = `SELECT COUNT(*) FROM dolt_log`; `busy_proc_count`
    = `pgrep -fc 'bin/bd '`; `hours_since_last` from
    `<stateDir>/<db>.last-flatten`; `breaker_applied` = `issues.jsonl` 0-byte +
    `uchg`; `has_remote` = `dolt remote -v` non-empty (auto-excludes zr).
  - Emits metrics + logs each run (see Observability).
  - `--no-flatten`, `--city DIR`.
  - runtimeDeps: **dolt**, **gascity** (`gc`), **curl**, coreutils, procps.

### 3. `gc-otlp-emit` (mkBashLibrary, `.bash`)

Shared best-effort OTLP emitter sourced by both scripts.

- `otlp_gauge NAME VALUE [k=v ...]` — builds an OTLP/JSON metrics payload
  (single gauge data point, resource `service.name`) and `curl`s it to
  `${OTEL_EXPORTER_OTLP_ENDPOINT}/v1/metrics` (or `GC_OTEL_METRICS_URL`).
- `otlp_log SEVERITY BODY [k=v ...]` — OTLP/JSON logs payload → `…/v1/logs`
  (or `GC_OTEL_LOGS_URL`).
- All calls use `curl --max-time <short>` and `|| true` — **never block or fail**
  the maintenance work. No-op cleanly when the collector is down (current state)
  or when `otelEnabled = false`.

### 4. Grafana dashboard (`grafana/dolt-maintenance-overview.json`)

Modeled on `gascity-overview.json` (same layout idiom, datasource uids
`prometheus`/`loki`). Provisioned via the house option, exactly like gascity:

```nix
phillipgreenii.observability.dashboardProviders.dolt-maintenance = {
  dashboards = [ ./grafana/dolt-maintenance-overview.json ];
};
```

Panels (title "Dolt Maintenance — Overview"):

- **Stat row:** breaker applied (expect 1), # DBs over commit threshold, hq
  size, hours-since-last-flatten (hq).
- **Timeseries (Prometheus gauges):** `dolt_maint_commit_count` by db;
  `dolt_maint_size_bytes` by db; `dolt_maint_hours_since_flatten` by db;
  `dolt_maint_busy_procs`.
- **Loki logs panel:** recent dolt-maintenance actions (decisions/reasons/
  outcomes) — the "Recent gascity logs" analog.

### 5. home-manager wiring (agent-support HM config)

- `launchd.agents.gc-dolt-maintenance`: hourly (`StartCalendarInterval` Minute
  0), logs → `~/.gc/dolt-maintenance.log`, `RunAtLoad = false`.
  `EnvironmentVariables` set `OTEL_EXPORTER_OTLP_ENDPOINT`,
  `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, and an `OTEL_SERVICE_NAME` /
  resource so emitted telemetry carries `service.name = gc-dolt-maintenance`.
- `home.activation.gcBdImportBreaker`: `gc-bd-import-breaker apply --city
<cityPath>` (idempotent) every switch.
- `phillipgreenii.observability.dashboardProviders.dolt-maintenance` provisions
  the dashboard.
- Module options (defaults): `cityPath = /Users/phillipg/gc`,
  `flattenCommitThreshold = 5000`, `busyProcThreshold = 4`,
  `minFlattenIntervalHours = 6`, `maxFlattenIntervalHours = 24`, `stateDir`,
  `logPath`, `otelEnabled = true`, `otlpEndpoint = http://127.0.0.1:4318`,
  `otelServiceName = gc-dolt-maintenance`. Injected via mkBashScript `config`.

## Observability (telemetry schema)

Emitted best-effort via `gc-otlp-emit` to gc's existing OTLP endpoints (reusing
gc's env). Because the job is **stateless hourly bash**, we emit **gauges**
(point-in-time signals) + **structured logs** (per-action detail) rather than
cumulative Prometheus counters; "rate"-style views come from gauge deltas and
LogQL `count_over_time` over the action logs. Metric prefix `dolt_maint_`.

- **Metrics (gauges; "saw"), label `db` where per-DB:**
  `dolt_maint_commit_count{db}`, `dolt_maint_size_bytes{db}`,
  `dolt_maint_hours_since_flatten{db}`, `dolt_maint_breaker_applied` (0/1),
  `dolt_maint_has_remote{db}` (0/1), `dolt_maint_busy_procs`,
  `dolt_maint_last_run_timestamp`.
- **Logs ("did"), attributes:** `db`, `action`
  (`stats_purge`|`gc`|`flatten_decision`|`flatten_exec`), `outcome`
  (`ok`|`fail`|`timeout`|`skip`), `bytes_before`, `bytes_after`,
  `bytes_reclaimed`, `decision`, `reason`, `duration_ms`. Severity INFO, or
  WARN/ERROR on failure.

## Run context

Everything runs as the **user** (`phillipg`), never root: home-manager
`launchd.agents` are per-user; the activation runs as the user during
`home-manager switch`; `chflags uchg` on a user-owned file needs no privilege;
dolt/gc/curl run as the user. (User context is also _required_ for `uchg`.)

## Testing

Per the bash-scripting skill plus explicit isolation requirements.

### Unit

- `test-gc-dolt-maintenance-lib.bats`: drive `should_flatten` across a truth
  table (remote / breaker-off / below-threshold / max-age-force / anti-thrash /
  busy / happy-path), asserting decision **and** reason. No dolt.
- `test-gc-otlp-emit.bats`: with `curl` mocked, assert `otlp_gauge`/`otlp_log`
  build well-formed OTLP/JSON (metric name, `db` label, value, `service.name`;
  log body + attributes) and POST to the right endpoint; and that
  `otelEnabled = false` / missing endpoint emit nothing.

### Integration (`test-gc-dolt-maintenance.bats`, `test-gc-bd-import-breaker.bats`)

**Isolation (hard requirements):**

- `TEST_DIR="$(mktemp -d)"`; override `HOME="$TEST_DIR"`.
- **Wipe all real env** that could point at a live server/collector: unset/clear
  `GC_*`, `BEADS_*`, `DOLT_*`, `BEADS_DOLT_*`, `GC_CITY`, `GC_HOME`, `OTEL_*`.
- A test needing a real Dolt DB **creates a fresh one in `TEST_DIR`** and starts
  a `dolt sql-server` on `127.0.0.1`, an **ephemeral/random port**, `--data-dir`
  in `TEST_DIR`. **Never** connect to `24158`/`24159`. The test asserts it's the
  **test DB** (seed a sentinel and verify) before exercising logic.
- **Teardown always kills the test server, even on error:** capture the PID,
  `kill` + wait in `teardown` (bats runs it unconditionally), `rm -rf
"$TEST_DIR"`; PID-file + `trap`-guarded helper so a mid-test failure can't leak
  a dolt process.
- Mock `gc`, `bd` (where the cheap tier isn't under test), and `curl` (PATH
  shims; the curl shim records args so emitted OTLP can be asserted without a
  collector).
- Do not test builder-injected behavior (`--version`, shebang).

## Cleanup / migration

- Delete from the gc repo: `tools/bd-import-breaker.sh`,
  `tools/gc-dolt-maintenance.sh`, `tools/com.phillipg.gc-dolt-maintenance.plist`.
- Unload + remove the hand-installed
  `~/Library/LaunchAgents/com.phillipg.gc-dolt-maintenance.plist`.
- Update HACKS.md HACK 18 to reference the nix command `gc-bd-import-breaker`.
- The launchd job **supersedes** the never-firing HACK 10 gc-order flatten (keep
  its doctor check; order stays disabled).

## Wiring / flake

- Add the two scripts + the library to agent-support's package set and module
  wiring (shellcheck lists, `nix flake check`, pre-commit), per the
  bash-scripting wiring reference and existing `packages/*` examples.
- Provision the dashboard via `phillipgreenii.observability.dashboardProviders`
  (as gascity/pa-monitor do).
- Ensure `gascity` is a flake input so `gc` resolves as a runtimeDep; add if absent.

## Risks & mitigations

- **Flatten still times out under load.** Best-effort + logged + retried next
  hour; cheap tier (the real size win) always runs; max-age force eventually runs.
- **Breaker off when the job runs.** `breaker_applied` precondition → skip
  flatten (+ warn) rather than risk a spiral.
- **Remote divergence.** `has_remote` detection excludes zr from flatten.
- **Activation-only breaker gap** between a mid-cycle un-apply and the next
  switch. Accepted per brainstorming; next switch re-asserts; the job won't
  flatten while the breaker is off.
- **Collector down** (current state). Emission is best-effort (short timeout,
  errors swallowed) — telemetry lost, maintenance unaffected. The dashboard will
  populate once the separate agent brings the stack up.
- **Gauges, not cumulative counters.** Chosen for the stateless bash model;
  dashboard panels use gauge timeseries + LogQL over action logs instead of
  `rate(..._total)`. Faithful to the gascity _style_, not its exact counter
  queries.
