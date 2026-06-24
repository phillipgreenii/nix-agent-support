# pa-monitor epic follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land three independent follow-ups to the `pg2-w6us` pa-monitor epic — a Grafana daemon-connection banner (.19), a config-gating fix so a daemon-only host writes its OTel config (.20), and deterministic coverage of the connection emitter's error paths (.22).

**Architecture:** Three disjoint changes, each in its own file set and its own commit: a Grafana dashboard JSON edit, a Home-Manager Nix module fix guarded by a new pure-eval flake check, and a dependency-injection test seam in a Go package. They share the `pa-monitor-followups` branch but have no code dependency on each other.

**Tech Stack:** Grafana dashboard JSON (schemaVersion 39) + `jq`; Nix (flake-parts, Home-Manager module, `lib.evalModules`); Go 1.x with the OpenTelemetry SDK (`go.opentelemetry.io/otel/sdk/metric`, `.../sdk/log`).

**Design doc:** `docs/superpowers/specs/2026-06-23-pa-monitor-followups-design.md`

**Working directory for all commands:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` (the repo root). Branch: `pa-monitor-followups` (already created).

---

## File Structure

| File                                                   | Task    | Responsibility                                                                  |
| ------------------------------------------------------ | ------- | ------------------------------------------------------------------------------- |
| `packages/pa-monitor/grafana/pa-monitor-overview.json` | 1 (.19) | Add panel 107 "Daemon connection"; shrink panel 105 to half-width.              |
| `home/programs/pa-monitor/default.nix`                 | 2 (.20) | Render `config.toml` on `daemon.enable`, not only on `claude.enable && enable`. |
| `flake.nix`                                            | 2 (.20) | New `checks.<sys>.test-pa-monitor-config-gating` pure-eval regression guard.    |
| `packages/pa-monitor/internal/otel/connection.go`      | 3 (.22) | Add `connDeps` seam + internal `newConnectionEmitter`.                          |
| `packages/pa-monitor/internal/otel/connection_test.go` | 3 (.22) | Stub exporters + error-branch tests.                                            |

Each task closes its bead at the end. Because `flake.nix` checks and the Go build gate CI, each task commits its test/guard **together** with its implementation (never commit a red guard onto the branch).

---

## Task 1 — pg2-w6us.19: "Daemon connection" Grafana banner

**Files:**

- Modify: `packages/pa-monitor/grafana/pa-monitor-overview.json` (panel id 105 width; append panel id 107)

This is a config file, so the "test" is a `jq` assertion on the resulting structure rather than a unit test.

- [ ] **Step 1: Claim the bead**

Run:

```bash
bd update pg2-w6us.19 --claim
```

- [ ] **Step 2: Write the failing structural assertion (run it first — it must FAIL)**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
jq -e '
  (([.panels[] | select(.gridPos.y == 0) | .id] | sort) == [105, 107])
  and ((.panels[] | select(.id == 105) | .gridPos.w) == 12)
  and ((.panels[] | select(.id == 105) | .gridPos.x) == 0)
  and ((.panels[] | select(.id == 107) | .gridPos) == {x:12, y:0, w:12, h:3})
' packages/pa-monitor/grafana/pa-monitor-overview.json && echo "ASSERT-PASS" || echo "ASSERT-FAIL"
```

Expected now: `ASSERT-FAIL` (panel 107 doesn't exist; panel 105 is still `w:24`). `jq -e` exits non-zero, so you'll see `ASSERT-FAIL`.

- [ ] **Step 3: Apply both edits with jq (shrink 105, append 107)**

Run (single jq invocation; `$p107` is the new panel as compact JSON):

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor/grafana
jq --argjson p107 '{"id":107,"type":"stat","title":"Daemon connection","datasource":{"type":"prometheus","uid":"prometheus"},"gridPos":{"x":12,"y":0,"w":12,"h":3},"targets":[{"expr":"min by (component) (pa_monitor_daemon_connected)","legendFormat":"{{component}}","refId":"A"}],"options":{"colorMode":"background","graphMode":"none","textMode":"value_and_name"},"fieldConfig":{"defaults":{"noValue":"—","mappings":[{"type":"value","options":{"0":{"text":"⊘ DISCONNECTED","color":"red"},"1":{"text":"✓ Connected","color":"green"}}}],"thresholds":{"mode":"absolute","steps":[{"value":null,"color":"red"},{"value":1,"color":"green"}]}}}}' \
  '(.panels[] | select(.id == 105) | .gridPos.w) |= 12 | .panels += [$p107]' \
  pa-monitor-overview.json > pa-monitor-overview.json.tmp \
  && mv pa-monitor-overview.json.tmp pa-monitor-overview.json
```

Rationale (see design doc Group A): panel 105 "Authentication" is the only panel at `y:0` and was full-width (`w:24`); shrinking it to `w:12` and adding 107 at `x:12,w:12,h:3` tiles the top row with two paired health banners. Panel 107 mirrors 105's `colorMode:background` stat style but with **inverted** thresholds (`null→red, 1→green`) because connected-polarity is high=good, and uses `min by (component)` (the same expression as the alert in `grafana/alerting/daemon-connection.yaml`) with `legendFormat "{{component}}"` for a per-component tile. `noValue:"—"` + no `or vector(0)` means absence shows "—", not a false red.

- [ ] **Step 4: Re-run the structural assertion — it must now PASS**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
jq -e '
  (([.panels[] | select(.gridPos.y == 0) | .id] | sort) == [105, 107])
  and ((.panels[] | select(.id == 105) | .gridPos.w) == 12)
  and ((.panels[] | select(.id == 105) | .gridPos.x) == 0)
  and ((.panels[] | select(.id == 107) | .gridPos) == {x:12, y:0, w:12, h:3})
' packages/pa-monitor/grafana/pa-monitor-overview.json && echo "ASSERT-PASS"
```

Expected: `ASSERT-PASS`.

- [ ] **Step 5: Confirm the file is still valid JSON and panel 107's fields are intact**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
jq -e '.panels[] | select(.id==107) | .options.textMode == "value_and_name" and .fieldConfig.defaults.noValue == "—" and (.targets[0].expr | test("min by \\(component\\)"))' \
  packages/pa-monitor/grafana/pa-monitor-overview.json && echo "PANEL-OK"
```

Expected: `PANEL-OK`.

- [ ] **Step 6: Commit**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pa-monitor/grafana/pa-monitor-overview.json
git commit -m "feat(pa-monitor): Grafana daemon-connection banner

Pairs a per-component pa_monitor_daemon_connected stat beside the
Authentication banner (panel 105 shrunk to half-width). Inverted
thresholds (connected=green/1, disconnected=red/0); noValue '—' and no
'or vector(0)' so absence is not a false red. Closes pg2-w6us.19."
```

(treefmt may reformat the JSON on commit; if the hook reports "files were modified", `git add` the file again and re-run the commit — see the repo CLAUDE.md.)

- [ ] **Step 7: Close the bead**

Run:

```bash
bd close pg2-w6us.19
```

> **Non-blocking manual check:** no in-repo panel uses `value_and_name`/`noValue`, so if a live Grafana is handy, load the dashboard and confirm each tile reads `<component> / ✓ Connected` (green) or `⊘ DISCONNECTED` (red), and `—` when no series.

---

## Task 2 — pg2-w6us.20: render config.toml on `daemon.enable`

**Files:**

- Modify: `home/programs/pa-monitor/default.nix:46-52` (the `config` block)
- Modify: `flake.nix` (add a check in the perSystem `checks = { … }` block, ~line 369)

The regression guard is a pure `lib.evalModules` flake check (no Home-Manager/NixOS harness needed). We write the guard first and watch it fail, then apply the fix.

- [ ] **Step 1: Claim the bead**

Run:

```bash
bd update pg2-w6us.20 --claim
```

- [ ] **Step 2: Add the failing flake check**

In `flake.nix`, inside the `checks = {` block (right after the `test-update-locks-lib = checksHelpers.testUpdateLocksLib { };` line, ~line 369), add:

```nix
            # Regression guard for pg2-w6us.20: the daemon's OTel config.toml
            # must render on daemon.enable even when the TUI (enable/
            # claude.enable) is off, and must NOT render when nothing is
            # enabled. Pure module eval — no HM/NixOS harness, no package build.
            test-pa-monitor-config-gating =
              let
                evalCfg =
                  cfg:
                  (lib.evalModules {
                    specialArgs = { inherit pkgs lib; };
                    modules = [
                      ./home/programs/pa-monitor/default.nix
                      (
                        { lib, ... }:
                        {
                          options.phillipgreenii.programs.claude.enable =
                            lib.mkEnableOption "claude (stub for pa-monitor eval test)";
                          options.home.packages = lib.mkOption {
                            type = lib.types.listOf lib.types.anything;
                            default = [ ];
                          };
                          options.xdg.configFile = lib.mkOption {
                            type = lib.types.attrsOf lib.types.anything;
                            default = { };
                          };
                        }
                      )
                      cfg
                    ];
                  }).config;
                hasConfig = c: c.xdg.configFile ? "pa-monitor/config.toml";
                endpoint = { otel.endpoint = "http://127.0.0.1:4317"; };
                daemonOnly = evalCfg {
                  phillipgreenii.programs.pa-monitor = {
                    daemon.enable = true;
                    settings = endpoint;
                  };
                };
                tuiOnly = evalCfg {
                  phillipgreenii.programs.claude.enable = true;
                  phillipgreenii.programs.pa-monitor = {
                    enable = true;
                    settings = endpoint;
                  };
                };
                neither = evalCfg {
                  phillipgreenii.programs.pa-monitor.settings = endpoint;
                };
              in
              assert hasConfig daemonOnly; # the fix: daemon.enable alone ⇒ config rendered
              assert hasConfig tuiOnly; # TUI path unchanged ⇒ still rendered
              assert !(hasConfig neither); # nothing enabled ⇒ no file
              pkgs.runCommand "pa-monitor-config-gating-ok" { } "touch $out";
```

- [ ] **Step 3: Run the check — it must FAIL on the first assertion**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
SYS=$(nix eval --impure --raw --expr 'builtins.currentSystem')
nix build ".#checks.$SYS.test-pa-monitor-config-gating" -L 2>&1 | tail -20
```

Expected: build/eval FAILS with an assertion failure (`hasConfig daemonOnly` is false today, because the module only renders `config.toml` under `claude.enable && enable`). If it instead fails with "option … does not exist" or a `lib`/`pkgs` error, the stub module is wrong — fix the stub before proceeding (do not touch the module yet).

- [ ] **Step 4: Apply the module fix**

In `home/programs/pa-monitor/default.nix`, replace the `config` block (currently lines 46-52):

```nix
  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    home.packages = [ cfg.package ];

    xdg.configFile."pa-monitor/config.toml" = lib.mkIf (cfg.settings != { }) {
      source = tomlFormat.generate "pa-monitor-config.toml" cfg.settings;
    };
  };
```

with:

```nix
  config = lib.mkMerge [
    (lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
      home.packages = [ cfg.package ];
    })
    # config.toml is the daemon's single source of OTel settings. Render it
    # whenever settings were supplied AND the full program OR the daemon is
    # enabled — decoupled from `claude.enable && enable` so a daemon-only host
    # (the LaunchAgent gate in darwin/modules/pa-monitor is daemon.enable-only)
    # still gets its config file. Intentionally NOT coupled to home.packages:
    # the LaunchAgent runs the daemon from the Nix store path, so the binary
    # need not be on the user's PATH.
    (lib.mkIf (
      cfg.settings != { }
      && ((config.phillipgreenii.programs.claude.enable && cfg.enable) || cfg.daemon.enable)
    ) {
      xdg.configFile."pa-monitor/config.toml".source =
        tomlFormat.generate "pa-monitor-config.toml" cfg.settings;
    })
  ];
```

- [ ] **Step 5: Re-run the check — it must now PASS**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
SYS=$(nix eval --impure --raw --expr 'builtins.currentSystem')
nix build ".#checks.$SYS.test-pa-monitor-config-gating" -L 2>&1 | tail -5
echo "exit=$?"
```

Expected: build succeeds (`exit=0`), producing the `pa-monitor-config-gating-ok` result symlink. All three assertions hold.

- [ ] **Step 6: Run `nix flake check` (eval-only) to confirm nothing else broke**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix flake check 2>&1 | tail -25
```

Expected: completes without error (the new check is included). If it is slow, the targeted `nix build` in Step 5 is the authoritative gate for this task.

- [ ] **Step 7: Update the module doc note**

In `home/programs/pa-monitor/default.nix`, the `settings` option description (lines ~34-39) ends with "When empty, no file is written…". Append one sentence so a reader knows the daemon path now renders it. Change:

```nix
        pa-monitor's TOML schema (e.g. `otel.endpoint`,
        `otel.resource_attributes`, `plan_tier`, `[[decorator]]`). When empty,
        no file is written and pa-monitor uses its built-in defaults.
```

to:

```nix
        pa-monitor's TOML schema (e.g. `otel.endpoint`,
        `otel.resource_attributes`, `plan_tier`, `[[decorator]]`). When empty,
        no file is written and pa-monitor uses its built-in defaults. The file
        is rendered whenever the program (`enable`) **or** the daemon
        (`daemon.enable`) is enabled, so a daemon-only host still gets its
        config.
```

- [ ] **Step 8: Commit**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add home/programs/pa-monitor/default.nix flake.nix
git commit -m "fix(pa-monitor): render config.toml on daemon.enable

A host with daemon.enable but not enable/claude.enable ran the daemon
(LaunchAgent gate is daemon.enable-only) but wrote no config.toml, so
daemon OTel was silently off. Decouple config rendering from the TUI
gate; add a pure lib.evalModules flake check guarding all three cases.
Closes pg2-w6us.20."
```

(If treefmt reformats `flake.nix`/the module on commit, `git add` again and re-run.)

- [ ] **Step 9: Close the bead**

Run:

```bash
bd close pg2-w6us.20
```

---

## Task 3 — pg2-w6us.22: cover ConnEmitter exporter-error paths

**Files:**

- Modify: `packages/pa-monitor/internal/otel/connection.go` (add `connDeps`, `defaultConnDeps`, internal `newConnectionEmitter`; route `NewConnectionEmitter` through it)
- Modify: `packages/pa-monitor/internal/otel/connection_test.go` (stub exporters + 6 error tests)

TDD: the new tests reference `connDeps`/`newConnectionEmitter`, which don't exist yet, so the package won't compile (red). We add the seam to go green.

- [ ] **Step 1: Claim the bead**

Run:

```bash
bd update pg2-w6us.22 --claim
```

- [ ] **Step 2: Append the stub exporters and the six failing tests to `connection_test.go`**

First, update the import block at the top of `packages/pa-monitor/internal/otel/connection_test.go`. Replace:

```go
import (
	"context"
	"testing"
)
```

with:

```go
import (
	"context"
	"errors"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)
```

Then append to the end of the file:

```go
// stubMetricExporter implements sdkmetric.Exporter. Export/ForceFlush succeed
// so that, during a provider Shutdown, only the injected shutdownErr surfaces.
// Temporality/Aggregation delegate to the SDK defaults — a zero-value
// Temporality is invalid and would fail gauge collection before Shutdown.
type stubMetricExporter struct{ shutdownErr error }

func (s stubMetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (s stubMetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (s stubMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error { return nil }
func (s stubMetricExporter) ForceFlush(context.Context) error                          { return nil }
func (s stubMetricExporter) Shutdown(context.Context) error                            { return s.shutdownErr }

// stubLogExporter implements sdklog.Exporter; same contract as above.
type stubLogExporter struct{ shutdownErr error }

func (s stubLogExporter) Export(context.Context, []sdklog.Record) error { return nil }
func (s stubLogExporter) Shutdown(context.Context) error                { return s.shutdownErr }
func (s stubLogExporter) ForceFlush(context.Context) error              { return nil }

func TestNewConnectionEmitterBuildResourceError(t *testing.T) {
	wantErr := errors.New("resource boom")
	deps := defaultConnDeps()
	deps.buildResource = func(context.Context, string, string) (*resource.Resource, error) {
		return nil, wantErr
	}
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestNewConnectionEmitterMetricExporterError(t *testing.T) {
	wantErr := errors.New("metric exporter boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return nil, wantErr }
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestNewConnectionEmitterLogExporterError(t *testing.T) {
	wantErr := errors.New("log exporter boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return stubMetricExporter{}, nil }
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) { return nil, wantErr }
	// Covers the mp.Shutdown(ctx) cleanup on log-exporter failure.
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestNewConnectionEmitterRegisterGaugeError(t *testing.T) {
	wantErr := errors.New("register boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return stubMetricExporter{}, nil }
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) { return stubLogExporter{}, nil }
	deps.registerGauge = func(*ConnEmitter, *sdkmetric.MeterProvider) error { return wantErr }
	// Covers the registerGauge error branch (both providers shut down).
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestConnectionEmitterShutdownMetricError(t *testing.T) {
	wantErr := errors.New("metric shutdown boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) {
		return stubMetricExporter{shutdownErr: wantErr}, nil
	}
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) { return stubLogExporter{}, nil }
	e, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Provider may wrap/join the exporter error — assert with errors.Is, not ==.
	if err := e.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown err = %v, want %v", err, wantErr)
	}
}

func TestConnectionEmitterShutdownLogError(t *testing.T) {
	wantErr := errors.New("log shutdown boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return stubMetricExporter{}, nil }
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) {
		return stubLogExporter{shutdownErr: wantErr}, nil
	}
	e, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Metric Shutdown succeeds, so this exercises the firstErr==nil log branch.
	if err := e.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown err = %v, want %v", err, wantErr)
	}
}
```

- [ ] **Step 3: Run the tests — they must FAIL to compile (seam doesn't exist yet)**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor
go test ./internal/otel/ 2>&1 | tail -15
```

Expected: compile error — `undefined: defaultConnDeps`, `undefined: newConnectionEmitter`.

- [ ] **Step 4: Add the DI seam to `connection.go`**

In `packages/pa-monitor/internal/otel/connection.go`, add `"go.opentelemetry.io/otel/sdk/resource"` to the import block (alphabetical position is fine; gofmt/treefmt will order it). The block becomes:

```go
import (
	"context"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)
```

Then replace the whole `NewConnectionEmitter` function (currently lines 44-84, from the doc comment through its closing brace) with:

```go
// connDeps holds the constructors NewConnectionEmitter depends on. Splitting
// them out lets tests inject failures into the exporter-construction and
// gauge-registration error branches, which are otherwise unreachable: the gRPC
// exporter New() funcs swallow bad-endpoint errors (they fall back to the
// default target and report parse errors via the global handler, not the
// return value).
type connDeps struct {
	buildResource func(ctx context.Context, name, version string) (*resource.Resource, error)
	newMetricExp  func(ctx context.Context) (sdkmetric.Exporter, error)
	newLogExp     func(ctx context.Context) (sdklog.Exporter, error)
	registerGauge func(e *ConnEmitter, mp *sdkmetric.MeterProvider) error
}

func defaultConnDeps() connDeps {
	return connDeps{
		buildResource: buildResource,
		newMetricExp:  func(ctx context.Context) (sdkmetric.Exporter, error) { return otlpmetricgrpc.New(ctx) },
		newLogExp:     func(ctx context.Context) (sdklog.Exporter, error) { return otlploggrpc.New(ctx) },
		registerGauge: (*ConnEmitter).registerGauge,
	}
}

// NewConnectionEmitter returns (nil, nil) when OTEL_EXPORTER_OTLP_ENDPOINT is
// unset, matching otel.New's disabled-state contract.
func NewConnectionEmitter(ctx context.Context, opts ConnOptions) (*ConnEmitter, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, nil
	}
	return newConnectionEmitter(ctx, opts, defaultConnDeps())
}

// newConnectionEmitter is the dependency-injected core of NewConnectionEmitter.
// It assumes the endpoint gate has already been checked; tests call it directly
// with a fault-injecting connDeps to exercise the error branches.
func newConnectionEmitter(ctx context.Context, opts ConnOptions, deps connDeps) (*ConnEmitter, error) {
	res, err := deps.buildResource(ctx, opts.ServiceName, opts.ServiceVersion)
	if err != nil {
		return nil, err
	}
	metricExp, err := deps.newMetricExp(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(connExportInterval))),
		sdkmetric.WithResource(res),
	)
	logExp, err := deps.newLogExp(ctx)
	if err != nil {
		_ = mp.Shutdown(ctx)
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	e := &ConnEmitter{
		metricsProvider: mp,
		logProvider:     lp,
		logger:          lp.Logger("pa-monitor"),
		component:       opts.Component,
	}
	if err := deps.registerGauge(e, mp); err != nil {
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
		return nil, err
	}
	return e, nil
}
```

Leave the existing `registerGauge` method, `RecordDaemonConnected`, `LogEvent`, `Shutdown`, and `connectedValue` unchanged.

- [ ] **Step 5: Run the tests — they must now PASS**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor
go test ./internal/otel/ -run 'ConnectionEmitter|NewConnectionEmitter' -v 2>&1 | tail -30
```

Expected: all six new tests `PASS`, plus the pre-existing `TestConnectionEmitter*` tests still `PASS`.

- [ ] **Step 6: Verify per-function coverage reached ~100% for the two target functions**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor
go test ./internal/otel/ -coverprofile=/tmp/otel-cov.out >/dev/null 2>&1
go tool cover -func=/tmp/otel-cov.out | grep -E 'NewConnectionEmitter|connection.go:.*Shutdown'
```

Expected: `NewConnectionEmitter` at `100.0%` and the `connection.go` `Shutdown` at `100.0%` (up from 65% / 80%). Do **not** judge by the package total — grep the function rows. (`registerGauge` stays as-is; its own branches aren't part of this ticket.)

- [ ] **Step 7: Run the full package test suite**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor
go test ./... 2>&1 | tail -20
```

Expected: all packages `ok` / `PASS`.

- [ ] **Step 8: Commit**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pa-monitor/internal/otel/connection.go packages/pa-monitor/internal/otel/connection_test.go
git commit -m "test(pa-monitor): cover ConnEmitter exporter-error paths

Add a connDeps DI seam (exporter/resource/gauge constructors) so tests
deterministically hit the otherwise-unreachable construction and shutdown
error branches (the gRPC New() funcs swallow bad-endpoint errors). Stub
exporters drive Shutdown failures; errors.Is guards joined errors.
NewConnectionEmitter and Shutdown now ~100%. Closes pg2-w6us.22."
```

(If a golangci-lint / gofmt pre-commit hook reformats imports, `git add` again and re-run.)

- [ ] **Step 9: Close the bead**

Run:

```bash
bd close pg2-w6us.22
```

---

## Final verification (after all three tasks)

- [ ] **Step 1: Whole-repo gates pass**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
prek run --all-files 2>&1 | tail -30 || pre-commit run --all-files 2>&1 | tail -30
nix flake check 2>&1 | tail -25
```

Expected: pre-commit hooks all pass; `nix flake check` succeeds (includes `test-pa-monitor-config-gating`).

- [ ] **Step 2: Confirm the branch is clean and the three beads are closed**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git status --short
git log --oneline -4
bd show pg2-w6us | grep -E '\.19|\.20|\.22'
```

Expected: clean tree; three follow-up commits on `pa-monitor-followups`; beads .19/.20/.22 marked closed (✓).

- [ ] **Step 3: Integrate the branch**

Use the `superpowers:finishing-a-development-branch` skill to decide merge vs PR vs cleanup. Do not push or merge without confirming the user's preference.

---

## Notes / out of scope

- No ADR is warranted: these are a config addition, a gating bugfix, and a test seam — implementation details, not architectural decisions (per the repo's ADR criteria).
- `emitter.go`'s `Shutdown` (20%) is a different type (`Emitter`, not `ConnEmitter`) and is **not** part of pg2-w6us.22.
- Group A's companion timeseries was explicitly deferred (YAGNI); the stat + alert + OTel log-stream panel cover the need.
