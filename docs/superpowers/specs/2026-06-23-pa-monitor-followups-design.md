# pa-monitor epic follow-ups: dashboard panel, config-gating fix, emitter coverage

**Status**: Approved (design)
**Date**: 2026-06-23
**Deciders**: Phillip Green II
**Tracks**: pg2-w6us.19, pg2-w6us.20, pg2-w6us.22 (children of epic pg2-w6us)
**Branch**: `pa-monitor-followups`

## Context

The `pg2-w6us` epic ("friendly cmux-bridge output + daemon-connection alerting") is
10/13 complete and merged to `main`. Three small, independent follow-ups remain:

- **.19 (P3)** — surface the daemon-connection signal on the Grafana dashboard (the
  original spec D7 was alert-rule-only; the dashboard panel was deferred).
- **.20 (P3)** — a gating asymmetry: a host that sets `daemon.enable` without
  `enable`/`claude.enable` runs the daemon but writes no config file, so daemon OTel
  is silently off.
- **.22 (P4)** — the exporter-construction and provider-shutdown error branches of the
  `otel.ConnEmitter` are unhit (`NewConnectionEmitter` 65%, `Shutdown` 80%).

The three touch disjoint files (Grafana JSON / a Nix HM module / a Go package) and can
be implemented, reviewed, and committed independently. This document is the single
design for all three; the implementation plan carries one self-contained task group per
ticket.

A skeptical subagent review verified every claim below against the actual sources
(dashboard geometry via `jq`; the OTel SDK shutdown/export call chains in
`otlpmetricgrpc@v1.44.0`, `otlploggrpc@v0.20.0`, `sdk/metric`, `sdk/log`; the Nix attr
paths). Its must-fix refinements are folded into the relevant sections below.

## Decision

### Group A — pg2-w6us.19: "Daemon connection" paired top banner

**File:** `packages/pa-monitor/grafana/pa-monitor-overview.json` (only).

The metric is `pa_monitor_daemon_connected` (Prometheus name of the OTel observable
gauge `pa_monitor.daemon.connected`; dots→underscores, no `_total`/unit suffix), with a
`component` label in `{cmux-bridge, tui}`. `1` = connected, `0` = disconnected.

The panel it is told to "mirror" — `id: 105` "Authentication" — is a full-width
(`w: 24`) banner at the very top (`x:0, y:0, h:3`), **above** the "Current status" row
(`id: 100`, `y: 3`). `jq` confirms it is the only panel at `y:0`. We pair a daemon-
connection banner beside it rather than reflow the (already-full) 4-tile status row:

1. **Edit** panel `id: 105`: `gridPos.w` `24 → 12` (keep `x:0, y:0, h:3`).
2. **Add** panel `id: 107` "Daemon connection" at `{x:12, y:0, w:12, h:3}`, mirroring
   105's stat style:
   - **target:** `expr: "min by (component) (pa_monitor_daemon_connected)"`,
     `legendFormat: "{{component}}"` — one tile per present component. (This is the
     same expression the alert rule uses, `daemon-connection.yaml:31`, so the panel and
     the alert agree: a single `0` reading pulls a component's tile to red.)
   - **options:** `colorMode: "background"`, `graphMode: "none"`,
     `textMode: "value_and_name"` (so each tile reads `<component> / <mapped text>`).
   - **mappings:** `1 → "✓ Connected"` (color green), `0 → "⊘ DISCONNECTED"` (color
     red). We set the mapping `color` explicitly to mirror panel 105 exactly
     (belt-and-suspenders; with `colorMode:background` the threshold already drives the
     tile background).
   - **thresholds (inverted vs Authentication):** `steps: [{value: null, color: "red"},
{value: 1, color: "green"}]`. Connected polarity is high=good, the opposite of
     Authentication's high=bad — so the green/red steps flip. (The Caffeinated/Auto-Nudge
     panels, `id:103/104`, already prove the "threshold drives background, mapping drives
     text" pattern in this schema.)
   - **`fieldConfig.defaults.noValue: "—"`** and **no `or vector(0)`**: when the metric
     is wholly absent (closed panes / exited processes) the tile shows "—", never a fake
     red. This matches the alert rule's deliberate choice (`daemon-connection.yaml:29-30`)
     that absence ≠ disconnected.

**Known behaviors (not bugs):** `min by (component)` yields one tile per _present_
component, so if only `tui` has ever reported you see one tile, not a fixed two-up;
`noValue` fires only on a fully-empty result, not a single missing label. This is correct
for a live-status tile.

**Verify:** `jq` asserts (a) panel 105 now has `w == 12`, (b) panel 107 exists at
`x:12, y:0, w:12, h:3`, (c) no two panels share an `(x,y)` origin. Then `nix flake check`
(the darwin module references this file via `phillipgreenii.observability.dashboardProviders`).
Because no in-repo panel uses `value_and_name`/`noValue`, the implementer should eyeball
the rendered tiles in a live Grafana if one is available (non-blocking).

### Group B — pg2-w6us.20: render config.toml on `daemon.enable`

**File:** `home/programs/pa-monitor/default.nix` (only).

**Confirmed bug:** the module gates the _entire_ `config` block — including
`xdg.configFile."pa-monitor/config.toml"` — on `claude.enable && cfg.enable`
(`default.nix:46`). The darwin module, however, writes `settings.otel` via
`home-manager.sharedModules` gated only on `daemon.enable`
(`darwin/modules/pa-monitor/default.nix:73-80`) and registers the LaunchAgent on
`daemonEnabledByAnyUser` (`:89`). So `daemon.enable = true` with `enable = false` ⇒
settings populated, LaunchAgent running, but **no** config.toml ⇒ daemon OTel silently
off. The spec itself calls `daemon.enable` "the operative gate" for the daemon.

**Fix** — split the single gated block into a `mkMerge` so `home.packages` keeps its
existing gate while `config.toml` also renders on the daemon path:

```nix
config = lib.mkMerge [
  (lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    home.packages = [ cfg.package ];
  })
  # config.toml is the daemon's single source of OTel settings. Render it
  # whenever settings were supplied AND the full program OR the daemon is
  # enabled — decoupled from `claude.enable && enable` so a daemon-only host
  # (the LaunchAgent gate is daemon.enable-only) still gets its config file.
  # Intentionally NOT coupled to home.packages: the LaunchAgent runs the daemon
  # from the Nix store path, so the binary need not be on the user's PATH.
  (lib.mkIf
    (cfg.settings != { }
      && ((config.phillipgreenii.programs.claude.enable && cfg.enable) || cfg.daemon.enable)) {
    xdg.configFile."pa-monitor/config.toml".source =
      tomlFormat.generate "pa-monitor-config.toml" cfg.settings;
  })
];
```

**Why this is correct and safe:**

- For the TUI path (`daemon.enable = false`) the new disjunction collapses to the
  original `claude.enable && cfg.enable` condition — behavior is unchanged.
- `cfg.daemon.enable` is the right attr path (`cfg = config.phillipgreenii.programs.pa-monitor`,
  `daemon.enable` declared at `default.nix:17`).
- No recursion risk: this reads only the user's own `cfg.settings`; it does not
  enumerate `config.home-manager.users` (the read-then-write pattern the darwin module
  carefully avoids).
- `xdg.enable` is on by default on darwin HM and is used unconditionally by sibling
  modules (`ccpool`, `tuicr`); no extra wiring needed.
- Per-decision: **no assertion** — the fix makes the standalone-daemon path _work_
  rather than forbid it.

**Verify:** `nix flake check`. Because flake check only catches eval errors (not the
_behavioral_ regression), add a minimal eval guard: instantiate the HM module with
`daemon.enable = true, enable = false, settings.otel.endpoint = "…"` and assert
`xdg.configFile."pa-monitor/config.toml"` is set; and a second case
(`daemon.enable = false, enable = false`) asserting it is _absent_. Wire it into the
flake's `checks` if an HM-eval harness exists; otherwise add it as a standalone
`nix eval` assertion documented in the plan. Update `home/programs/pa-monitor` README/
module docs to note `daemon.enable` now renders config independent of the TUI.

### Group C — pg2-w6us.22: cover ConnEmitter exporter-error paths

**Files:** `packages/pa-monitor/internal/otel/connection.go` + `connection_test.go`.

The gRPC exporter `New()` funcs swallow bad-endpoint/env errors (they fall back to the
default target and surface parse errors via the global error handler, not the return
value), so a "bad endpoint" cannot deterministically hit the construction-error branches.
We add a small dependency-injection seam.

```go
// connDeps holds the constructors NewConnectionEmitter depends on so tests can
// inject failures into the otherwise-unreachable error branches.
type connDeps struct {
    buildResource func(ctx context.Context, name, version string) (*resource.Resource, error)
    newMetricExp  func(ctx context.Context) (sdkmetric.Exporter, error)
    newLogExp     func(ctx context.Context) (sdklog.Exporter, error)
    registerGauge func(e *ConnEmitter, mp *sdkmetric.MeterProvider) error
}

func defaultConnDeps() connDeps {
    return connDeps{
        buildResource: buildResource,
        // adapt the variadic constructors to the ctx-only seam signature:
        newMetricExp:  func(ctx context.Context) (sdkmetric.Exporter, error) { return otlpmetricgrpc.New(ctx) },
        newLogExp:     func(ctx context.Context) (sdklog.Exporter, error) { return otlploggrpc.New(ctx) },
        registerGauge: (*ConnEmitter).registerGauge, // method expression; exact type match
    }
}
```

`NewConnectionEmitter` keeps the `OTEL_EXPORTER_OTLP_ENDPOINT == ""` early-return and
delegates the rest to an internal `newConnectionEmitter(ctx, opts, defaultConnDeps())`
that holds the real construction logic (using `deps.*` at each step). Tests call
`newConnectionEmitter` directly (no env needed) and inject `connDeps`.

**Why this is type-correct (verified against the SDK):**

- `otlpmetricgrpc.New` returns `*Exporter` (implements `sdkmetric.Exporter`);
  `otlploggrpc.New` returns `*Exporter` (implements `sdklog.Exporter`). The seam's
  interface-typed func fields accept both the real exporters and stubs.
- `sdkmetric.NewPeriodicReader` and `sdklog.NewBatchProcessor` take those interfaces, so
  stub exporters drop in.
- `(*ConnEmitter).registerGauge` has type `func(*ConnEmitter, *sdkmetric.MeterProvider) error`,
  matching the field exactly — compiles as a method expression.
- The seam holds no package-level mutable state: `defaultConnDeps()` returns a fresh
  struct per call and tests pass per-call deps, so internal-path tests need no `t.Setenv`
  and may run in parallel.

**Test cases (new, in `connection_test.go`):**

1. `buildResource` fails → error returned (inject failing `buildResource`).
2. `newMetricExp` fails → error returned.
3. `newLogExp` fails → error returned, and the metric provider's `Shutdown` cleanup runs
   (inject a real/stub metric exporter + a failing `newLogExp`).
4. `registerGauge` fails → error returned and both providers shut down
   (covers `connection.go:78-82`).
5. `Shutdown` — metric provider errors (stub metric exporter whose `Shutdown` errors).
6. `Shutdown` — metric ok, log provider errors (covers the `firstErr == nil` branch,
   `connection.go:154-156`).

**Stub-exporter requirements (must-fix from review):**

- Both providers call `exporter.Export` (and may `ForceFlush`) _during_ `Shutdown`. To
  drive **only** the shutdown-error path, the stub's `Export`/`ForceFlush` must return
  `nil` and only `Shutdown` returns a sentinel. Tests assert with `errors.Is(err,
sentinel)`, **not** `==` (the provider returns `errors.Join(...)`).
- The metric stub must return **valid** `Temporality`/`Aggregation` by delegating to the
  SDK default selectors (`metric.DefaultTemporalitySelector(k)` /
  `metric.DefaultAggregationSelector(k)`); a zero-value `Temporality` is invalid and can
  fail the gauge collect before reaching `Shutdown`.

Exact interface method sets the stubs must implement:

- `sdkmetric.Exporter`: `Temporality(InstrumentKind) metricdata.Temporality`,
  `Aggregation(InstrumentKind) Aggregation`, `Export(context.Context,
*metricdata.ResourceMetrics) error`, `ForceFlush(context.Context) error`,
  `Shutdown(context.Context) error`.
- `sdklog.Exporter`: `Export(context.Context, []Record) error`,
  `Shutdown(context.Context) error`, `ForceFlush(context.Context) error`.

**Verify:** `go test ./internal/otel/` then `go test ./internal/otel/
-coverprofile=…` + `go tool cover -func=…` asserting **`NewConnectionEmitter` and
`Shutdown` at/near 100% per-function** (grep the function rows — do not rely on the
package total, which gains can mask). Full `go test ./...`.

## Consequences

### Positive

- The daemon's reachability becomes visible at a glance (Group A) and the alert it pairs
  with has a dashboard home.
- The daemon-only host configuration now actually emits OTel (Group B); the Group A panel
  doubles as the end-to-end cross-check that config rendering works.
- The defensive error paths in the connection emitter are covered deterministically,
  guarding future refactors (Group C).

### Negative

- Group C adds a small test-only DI seam to production code (`connDeps`,
  `newConnectionEmitter`). Justified: it's the only reliable way to exercise branches the
  SDK otherwise makes unreachable, and it stays unexported.
- Group A introduces a Grafana stat configuration (`value_and_name`, `noValue`) with no
  in-repo precedent; mitigated by the live-eyeball verification step.

### Neutral

- The three changes are independent; each can be committed on its own and reviewed in
  isolation. They share the `pa-monitor-followups` branch only for convenience.

## Alternatives Considered

- **Group A — tile inside the Current status row / full-width second banner.** Rejected:
  the status row is already full (4×`w:6`) so a 5th tile needs a rebalance, and a second
  full-width banner shifts every panel's `y` down by 3 (large mechanical diff). The
  paired half-width banner is minimal-churn and visually pairs the two health signals.
- **Group A — companion timeseries.** Deferred (YAGNI): the stat + the existing
  daemon-connection alert + the OTel log-stream panel already cover historical flaps.
- **Group B — assert `daemon.enable → enable`.** Rejected in favor of making the
  standalone path work; the LaunchAgent gate is already `daemon.enable`-only, so forbidding
  the combo would contradict it.
- **Group C — real synchronous fault (bad endpoint) / cancelled-context only.** Rejected:
  the gRPC `New()` funcs swallow endpoint errors, and a cancelled context cannot isolate
  the `firstErr == nil` log-shutdown branch. The DI seam is the only deterministic route.
- **Group C — close as won't-fix.** Rejected: the ticket exists and the seam is cheap and
  reusable.

## Related Decisions

- Epic spec: `docs/superpowers/specs/2026-06-23-pa-monitor-bridge-connection-ux-design.md`
  (D7 alert rule; the `settings`/`daemon.enable` gating discussion).
- Plan: `docs/superpowers/plans/2026-06-23-pa-monitor-bridge-connection-ux.md`.
