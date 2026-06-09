# pg-pr Sync Error Log Panel Design

**Status**: Draft
**Date**: 2026-06-09
**Deciders**: phillipg

## Context

The `pg-pr / Ops` Grafana dashboard (`pg-pr-ops`, uid `pg-pr-ops`, in
`phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json`)
has a headline "Sync errors / sec (by repo)" timeseries built from the
Prometheus counter `pg_pr_sync_errors_total`. That panel tells you _that_
syncs are failing and at what rate, but not _what_ the errors are. When the
counter goes non-zero the operator has no way, from the dashboard, to see the
underlying messages (the kind of silent failure that let
"invalid issue type: feedback" sit unnoticed at a count of 253).

The error messages do exist, but only as local files. The
`pg-pr sync --daemon` process logs structured records via `log/slog`:

- `log.Warn("sync iteration finished with errors", …, "error_details", […])`
  — per-repo errors; this is what `pg_pr_sync_errors_total` counts.
- `log.Error("sync iteration failed", "err", …)` — a whole-iteration failure.

The `pg-pr-sync` launchd agent
(`phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix`) runs the
daemon with `--log-json`, which builds a JSON slog handler writing to
**stderr**. launchd redirects stderr to
`~/Library/Logs/pg-pr-sync.err`. Nothing ships that file anywhere.

The local observability stack already runs Prometheus, Loki, Tempo, and
Grafana on 127.0.0.1. otelcol (`darwin/services/otelcol/config.yaml.nix`) has
a logs pipeline — but its only receiver is OTLP, and it exports to Loki via
`otlp_http/loki`. So Grafana _can_ show logs, but only for services that push
OTLP log records into otelcol. `pg-pr` does not: `telemetry.Init`
(`internal/telemetry/telemetry.go`) configures a trace provider only, and the
`pg-pr-sync` service sets no `OTEL_*` env vars. The daemon's logs therefore
never reach Loki.

The established workspace convention for log panels (see
`pa-monitor-overview.json`, `gascity-overview.json`) is a Grafana `logs` panel
backed by the Loki datasource (uid `loki`), selecting on the `service_name`
stream label that Loki derives from the OTLP `service.name` resource
attribute.

## Decision

Make the `pg-pr` daemon emit its logs as OTLP records (additively — stderr
logging is preserved), wire the `pg-pr-sync` service to point at the local
otelcol, and add a scrolling Loki `logs` panel to the Ops dashboard directly
below the sync-errors chart.

The daemon's logs will be exported under `service.name = "pg-pr-sync"` (set
via `OTEL_SERVICE_NAME`), so the panel selects `{service_name="pg-pr-sync"}`.
This also relabels the daemon's _traces_ to `pg-pr-sync` in Tempo, separating
the long-running daemon from one-shot `pg-pr` CLI invocations — a deliberate
and welcome side effect.

This was chosen over two alternatives:

- **otelcol `filelog` receiver tailing `pg-pr-sync.err`** — rejected. Smaller
  (no Go change) but introduces a host-path-coupled file tail and re-parses
  JSON that the daemon already has structured in memory. The native OTLP path
  is the architecturally correct counterpart to the existing trace pipeline.
- **Dashboard-only panel as a forward contract** (à la pa-monitor) — rejected.
  The panel would be empty until ingestion is wired separately, which does not
  satisfy the goal of _seeing the errors now_.

## Components

Five changes across three flakes.

### 1. `telemetry.Init` also stands up an OTLP LoggerProvider

File: `phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry/telemetry.go`

`Init(ctx, serviceName, version)` currently: if `OTEL_EXPORTER_OTLP_ENDPOINT`
is empty, install a no-op tracer provider and return a no-op shutdown;
otherwise build an OTLP trace exporter + batching `sdktrace` provider and
return its `Shutdown`.

Mirror that for logs in the same function, sharing the same `resource` (so
`service.name`/`service.version` match traces):

- Endpoint empty → install a no-op `LoggerProvider`
  (`go.opentelemetry.io/otel/log/noop`) via `global.SetLoggerProvider`, no
  log exporter.
- Endpoint set → build an `otlploghttp` (or `otlploggrpc` when
  `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`) exporter, wrap in a batching
  `sdklog.LoggerProvider` with the shared resource, register it via
  `global.SetLoggerProvider`, and fold its `Shutdown` into the returned
  `ShutdownFunc` (which already flushes traces).
- Exporter init failure stays best-effort: log one stderr warning, install the
  no-op logger provider, continue. `Init` must keep its "never returns an
  error in practice, never blocks startup" contract.

Protocol selection reuses the existing `OTEL_EXPORTER_OTLP_PROTOCOL` switch
pattern (`grpc` vs default `http/protobuf`).

### 2. Daemon logger fans out to stderr _and_ OTLP

Files: `internal/telemetry/telemetry.go` (new helper) and `cmd/pg-pr/sync.go`
(wiring).

Add `telemetry.NewSlogHandler(name string) slog.Handler`, returning an
`otelslog` bridge handler (`go.opentelemetry.io/contrib/bridges/otelslog`)
bound to the global `LoggerProvider`. When logs are off the global provider is
the no-op, so the handler is a cheap no-op — safe to always include.

Add a minimal fan-out `slog.Handler` (dispatches `Enabled`/`Handle`/`WithAttrs`/
`WithGroup` to a list of handlers) in the `telemetry` package.

In `sync.go`, the daemon path currently does:

```go
var logger = sync.NewTextLogger()
if syFlags.logJSON {
    logger = sync.NewJSONLogger()
}
```

Change so the chosen stderr handler is composed with the otel handler:

```go
base := stderrHandler(syFlags.logJSON)        // JSON or text, Level=Info, → stderr
h := telemetry.Fanout(base, telemetry.NewSlogHandler("pg-pr-sync"))
logger := slog.New(h)
```

`pg-pr-sync.err` keeps receiving the exact same stderr stream it does today —
OTLP is purely additive. slog `LevelWarn`/`LevelError` map through the
`otelslog` bridge to OTel severity `WARN`/`ERROR`, so both the "finished with
errors" (WARN) and "iteration failed" (ERROR) events land in Loki. The base
handler keeps `Level: slog.LevelInfo`, matching today's `NewJSONLogger`.

The exact placement of the `stderrHandler` helper (sync package vs cmd
package) is an implementation detail for the plan; the daemon already imports
`telemetry` (for `MetricsHandler`), so either layering works.

**Deliberate divergence from pa-monitor.** `pa-monitor`
(`internal/otel/emitter.go`) emits OTLP logs via the _raw_ `otellog.Record`
API at a fixed `SeverityInfo`, using a gRPC exporter, and does not use the
`otelslog` bridge. pg-pr takes the `otelslog`-bridge route instead because
pg-pr's daemon is already `slog`-native (every log site is `log.Warn`/
`log.Error` with structured attrs): the bridge requires **zero** changes to
those call sites and, critically, preserves the slog level → OTel severity
mapping (`WARN`/`ERROR`), which the panel relies on to distinguish errors.
Adopting pa-monitor's fixed-`SeverityInfo` pattern would either flatten the
severity we need or force rewriting every log site. The cost is one new
contrib dependency (`otelslog`) not yet vendored in the workspace.

### 3. Go dependencies

Files: `packages/pg-pr/go.mod`, `go.sum`, and the package derivation's
`vendorHash`.

The OTel-Go **log** signal modules version on their own `v0.x` line, _not_ the
`v1.44.0` core line. The sibling `pa-monitor` package already ships OTLP logs
against the same otel core `v1.44.0` (`packages/pa-monitor/go.mod` lines
12–19), which is the authoritative reference for the version set:

- `go.opentelemetry.io/otel/log` — **`v0.20.0`**
- `go.opentelemetry.io/otel/sdk/log` — **`v0.20.0`**
- `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp` — **`v0.20.0`**
  (HTTP, to match pg-pr's existing `otlptracehttp` transport and `mkEmitterEnv`'s
  default `http/protobuf`; add `otlploggrpc v0.20.0` too if the grpc protocol
  branch is wired, mirroring the trace exporter's protocol switch)
- `go.opentelemetry.io/contrib/bridges/otelslog` — version resolved via
  `go get` against `otel/log v0.20.0` (the contrib bridges track the `v0.x` log
  train, not core `v1.44.0`; not yet vendored anywhere in the workspace, so the
  exact tag is determined at implementation time).

Regenerate `vendorHash` via the package's existing `packages/pg-pr/update-deps.sh`
(do **not** hand-edit the hash in `default.nix`).

### 4. Wire OTLP env onto the `pg-pr-sync` service

File: `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix`

Use the existing emitter helper
`config.phillipgreenii.observability.mkEmitterEnv { serviceName = "pg-pr-sync"; }`,
which returns `{ OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL,
OTEL_SERVICE_NAME }` (and `{}` when observability is disabled).

Mechanism follows the settled precedent in `gc-dolt-maintenance`
(`phillipg-nix-ziprecruiter/darwin/modules/gascity/...` resolves `emitterEnv`
in the module and passes `serviceConfig.EnvironmentVariables = emitterEnv`).
The `launchdServices.userAgents` submodule exposes **no** `environment` attr —
only a pass-through `serviceConfig` — so the values go via
`serviceConfig.EnvironmentVariables`, not `export` lines in the `script`. Copy
gc-dolt-maintenance's defensive guard
(`emitterEnv = if obs ? mkEmitterEnv then obs.mkEmitterEnv { … } else { }`)
rather than calling `mkEmitterEnv` unconditionally, so the module still
evaluates if the observability option is ever absent.

This also enables the daemon's trace export as a free side benefit.

### 5. The Loki logs panel

File: `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json`

Current layout: stat panels 1–3 at `y:0` (`h:4`); the "Sync errors / sec"
panel 4 at `y:4` (`w:24, h:8`, bottom at `y:12`); panels 5 and 6 side by side
at `y:12` (`w:12, h:8` each).

Insert a new panel directly below the sync-errors chart and push 5 & 6 down:

- New panel `id: 7`, `type: "logs"`, `gridPos: { x:0, y:12, w:24, h:8 }`.
- `datasource: { type: "loki", uid: "loki" }` (matching the sibling
  dashboards' Loki datasource).
- Target: `expr: {service_name="pg-pr-sync"} | severity_text =~ "WARN|ERROR"`,
  `queryType: "range"`, `maxLines: 200`.
- `options`: `sortOrder: "Descending"`, `wrapLogMessage: true`,
  `enableLogDetails: true`, `showTime: true`, `dedupStrategy: "none"`.
- Panels 5 and 6 move from `y:12` to `y:20`.

The `datasource`/`type`/`options`/`queryType` shape matches the existing Loki
`logs` panels in `gascity-overview.json` and `pa-monitor-overview.json`. The
**only** unproven element is the severity filter: no existing panel filters by
level (pa-monitor emits everything at `SeverityInfo`, so it never could). Since
pg-pr's `otelslog` bridge _does_ set per-record severity, filtering is both
meaningful and the stated requirement ("show the messages of the errors").
Therefore the filter stays the primary query, but its exact field name is a
**required verification step** in the plan: confirm in live Loki Explore which
field carries OTel severity for this Loki version (candidates: `severity_text`,
`detected_level`) and adjust the LogQL accordingly. Only if no severity field
is filterable at all does it fall back to the bare `{service_name="pg-pr-sync"}`
selector (relying on the level column for visual scanning).

Note: the per-repo error messages are carried as a single `error_details`
**string-slice attribute** on one WARN record per iteration (see `daemon.go`
lines 208–214), not one log line per failing repo. With `enableLogDetails` the
operator expands the row to read them; the panel description should say so.

A Grafana `logs` panel is fixed-height and scrolls its content internally, so
it cannot grow the dashboard unbounded — this satisfies "the panel should
scroll so it doesn't grow too large."

Title: "Sync error log". Description points the reader to the sync-errors chart
above, notes the panel requires the Loki datasource + the pg-pr OTLP log
pipeline, and that per-repo details live in the expandable `error_details`
field.

## Error handling

- No OTLP endpoint configured (e.g. observability disabled, or running the CLI
  outside the service): `Init` installs no-op providers; the daemon logs only
  to stderr exactly as today. No errors, no behavioral change.
- otelcol unreachable: the OTLP exporter retries/drops per SDK defaults; the
  daemon keeps running and stderr logging is unaffected. Telemetry is
  best-effort by design.
- Loki severity-field uncertainty: handled by the required live-Loki
  verification step described in component §5 (confirm the severity field name;
  fall back to the unfiltered selector only if no severity field is filterable).

## Testing

- **telemetry (Go, unit):** with `OTEL_EXPORTER_OTLP_ENDPOINT` unset, `Init`
  returns a no-op shutdown and `global.GetLoggerProvider()` is the no-op
  provider; the fan-out handler still routes to the stderr base handler. With a
  fake endpoint, `Init` installs an SDK logger provider and the returned
  shutdown flushes without error. Mirror the existing trace tests in
  `telemetry_test.go`.
- **fan-out handler (Go, unit):** records written to a fan-out of two
  capturing handlers reach both; `WithAttrs`/`WithGroup` propagate to both.
- **build:** `nix build .#pg-pr` succeeds with the regenerated `vendorHash`;
  `nix flake check` passes in `phillipgreenii-nix-agent-support`.
- **service / dashboard (manual):** after `darwin-rebuild switch`, confirm the
  daemon's logs appear in Grafana Explore under `{service_name="pg-pr-sync"}`,
  then confirm the new Ops panel renders WARN/ERROR lines and scrolls. JSON
  validity of `pg-pr-ops.json` is checked by the build that provisions it.

## Documentation & repo hygiene

Each affected repo's CLAUDE.md mandates updating docs after a change:

- `phillipgreenii-nix-agent-support`: note the new "daemon emits OTLP logs via
  the otelslog bridge" capability where the telemetry surface is documented
  (README and/or the `otel-emitter-onboarding.md` referenced from
  `telemetry.go`). Consider whether this rises to an ADR (it establishes a
  reusable pattern: slog-native services bridge to OTLP via `otelslog`,
  distinct from pa-monitor's raw-record approach) — recommend a short ADR.
- `phillipg-nix-ziprecruiter`: no service-registry/`/etc/hosts` change (no new
  web UI), so the localhost-service rule does not apply; mention the new
  emitter wiring in `TODO.md`/README only if that repo tracks per-service
  telemetry.
- No `flake.nix` pre-commit-hook block changes in any repo, so
  `nix run .#install-pre-commit-hooks` is **not** required. State this in the
  plan so the hard "reinstall hooks" rule is consciously cleared, not missed.

## Out of scope

- Shipping pg-pr's metrics or PR-snapshot data through any new path (unchanged).
- Retention/volume tuning of Loki.
- Alerting on the error log (the existing counter threshold on the chart stays
  the alerting signal).

## Affected repositories

- `phillipgreenii-nix-agent-support` — Go telemetry change, deps, vendorHash.
- `phillipg-nix-ziprecruiter` — `pg-pr-sync` service env.
- `phillipgreenii-nix-support-apps` — `pg-pr-ops.json` panel.

## Related decisions

- See also: phillipgreenii-nix-agent-support
  docs/superpowers/specs/2026-05-26-pg-pr-dashboard-design.md (established the
  Ops dashboard and the daemon's HTTP/metrics/trace surfaces).
- Reuses `phillipgreenii.observability.mkEmitterEnv`
  (`phillipgreenii-nix-support-apps/darwin/modules/observability/emitter.nix`)
  and the otelcol OTLP→Loki logs pipeline
  (`darwin/services/otelcol/config.yaml.nix`).
