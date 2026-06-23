# pa-monitor bridge connection UX + daemon-connection alerting — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `cmux-bridge` pane output human-friendly (timestamped, prefix-less, noise-free), route low-level detail to a local log + OTel, and add a `pa_monitor.daemon.connected` signal from the bridge and TUI with a Grafana alert that fires when either cannot reach the daemon for >1m — all configured from the single shared XDG config file.

**Architecture:** OTel settings move into the existing `internal/config` TOML (`[otel]`), read by daemon + bridge + TUI via a new `config.ApplyOTelEnv` shim. A new minimal `otel.NewConnectionEmitter` publishes one gauge (`pa_monitor.daemon.connected{component}`). The bridge gains a connection state-machine driving both the terminal and the gauge; low-level detail goes to a bridge-named log file and OTel logs. The daemon plist OTel env is removed; agent-support derives `settings.otel` from `obs` and writes it into each daemon-enabled user's pa-monitor config. A Grafana rule alerts on `min by (component)(pa_monitor_daemon_connected) < 1 for 1m`.

**Tech Stack:** Go 1.x (BurntSushi/toml, OpenTelemetry SDK v1.44.0 + log exporters 0.20.0, bubbletea), Nix (nix-darwin + home-manager, `pkgs.formats.toml`), Grafana provisioned alerting YAML.

**Spec:** `docs/superpowers/specs/2026-06-23-pa-monitor-bridge-connection-ux-design.md`

**Conventions for every task:**

- Go work happens in `packages/pa-monitor/`. Run Go tests with `cd packages/pa-monitor && go test ./<pkg>/... -run <Name> -v`. Run the whole package: `go test ./...`.
- Before any commit: `prek run --all-files` (or `pre-commit run --all-files`) MUST pass; treefmt may reformat — re-`git add` and retry. For nix tasks, `nix flake check` MUST pass.
- Branch is already `pa-monitor-bridge-connection-ux`. Commit after each task.

---

## File Structure

| File                                                                       | Responsibility                                                 | Tasks   |
| -------------------------------------------------------------------------- | -------------------------------------------------------------- | ------- |
| `internal/config/config.go`                                                | `[otel]` schema, `OTelConfig`, `ApplyOTelEnv`                  | 1, 2    |
| `internal/config/config_test.go`                                           | round-trip + `ApplyOTelEnv` tests                              | 1, 2    |
| `internal/otel/resource.go`                                                | merge `OTEL_RESOURCE_ATTRIBUTES` (env)                         | 3       |
| `internal/otel/connection.go` (new)                                        | minimal `ConnEmitter` + `pa_monitor.daemon.connected`          | 4       |
| `internal/otel/connection_test.go` (new)                                   | nil-safety + construction                                      | 4       |
| `internal/tui/errorlog.go`                                                 | generalize with `FileName`                                     | 5       |
| `internal/tui/errorlog_test.go` (new)                                      | `FileName` behavior                                            | 5       |
| `cmd/pa-monitor/cmux_bridge.go`                                            | line formatter, phrases, conn state-machine, wiring            | 6, 7, 8 |
| `cmd/pa-monitor/bridge_log.go` (new)                                       | bridge terminal + detail logger                                | 7, 8    |
| `cmd/pa-monitor/cmux_bridge_test.go`                                       | updated phrases/format + transitions                           | 6, 7    |
| `cmd/pa-monitor/tui_remote.go`                                             | conn emitter + sample ticker                                   | 9       |
| `cmd/pa-monitor/daemon.go`                                                 | `ApplyOTelEnv` before `otel.New`                               | 10      |
| `home/programs/pa-monitor/default.nix`                                     | `settings` option → `xdg.configFile`                           | 11      |
| `darwin/modules/pa-monitor/default.nix`                                    | remove plist env; derive+write `settings.otel`; register alert | 12      |
| `grafana/alerting/daemon-connection.yaml` (new)                            | the alert rule                                                 | 13      |
| `docs/adr/0016-*.md`, `docs/adr/index.md`, `packages/pa-monitor/README.md` | docs                                                           | 14      |

---

## Task 1: Config `[otel]` schema

**Files:**

- Modify: `packages/pa-monitor/internal/config/config.go`
- Test: `packages/pa-monitor/internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `config_test.go`:

```go
func TestConfigOTelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[otel]
endpoint = "http://127.0.0.1:4317"

[otel.resource_attributes]
"deployment.environment" = "local"
"host.name" = "mbp-02"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OTel.Endpoint != "http://127.0.0.1:4317" {
		t.Errorf("OTel.Endpoint = %q", cfg.OTel.Endpoint)
	}
	if cfg.OTel.ResourceAttrs["host.name"] != "mbp-02" {
		t.Errorf("OTel.ResourceAttrs = %+v", cfg.OTel.ResourceAttrs)
	}
}

func TestConfigOTelDefaultsEmpty(t *testing.T) {
	cfg := defaults()
	if cfg.OTel.Endpoint != "" || len(cfg.OTel.ResourceAttrs) != 0 {
		t.Errorf("OTel default must be empty, got %+v", cfg.OTel)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/config/ -run TestConfigOTel -v`
Expected: FAIL — `cfg.OTel undefined (type Config has no field or method OTel)`.

- [ ] **Step 3: Add the schema and parsing**

In `config.go`, add the type and field. After the `DecoratorConfig` type add:

```go
// OTelConfig is the [otel] block. Endpoint is the OTLP gRPC endpoint
// (an http:// scheme selects insecure gRPC). There is intentionally no
// protocol field: the emitters import the gRPC-only exporter packages, so
// transport is fixed at compile time and OTEL_EXPORTER_OTLP_PROTOCOL is a
// no-op. ResourceAttrs becomes OTEL_RESOURCE_ATTRIBUTES.
type OTelConfig struct {
	Endpoint      string
	ResourceAttrs map[string]string
}
```

Add to the `Config` struct (after `Decorators`):

```go
	OTel OTelConfig
```

Add to `tomlConfig` (after `Decorators`):

```go
	OTel *tomlOTel `toml:"otel"`
```

Add the toml shape (after `tomlDecorator`):

```go
type tomlOTel struct {
	Endpoint      *string           `toml:"endpoint"`
	ResourceAttrs map[string]string `toml:"resource_attributes"`
}
```

In `apply`, before the decorator loop, add:

```go
	if raw.OTel != nil {
		if raw.OTel.Endpoint != nil {
			cfg.OTel.Endpoint = *raw.OTel.Endpoint
		}
		if raw.OTel.ResourceAttrs != nil {
			cfg.OTel.ResourceAttrs = raw.OTel.ResourceAttrs
		}
	}
```

(No change to `defaults()` — the zero `OTelConfig` is already empty.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/config/ -run TestConfigOTel -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/internal/config/config.go packages/pa-monitor/internal/config/config_test.go
git commit -m "feat(pa-monitor): add [otel] section to config schema"
```

---

## Task 2: `config.ApplyOTelEnv`

**Files:**

- Modify: `packages/pa-monitor/internal/config/config.go`
- Test: `packages/pa-monitor/internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `config_test.go`:

```go
func TestApplyOTelEnvSetsWhenUnset(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	// t.Setenv sets to ""; unset for real so the only-if-unset path is exercised.
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	os.Unsetenv("OTEL_RESOURCE_ATTRIBUTES")
	ApplyOTelEnv(OTelConfig{
		Endpoint:      "http://127.0.0.1:4317",
		ResourceAttrs: map[string]string{"host.name": "mbp-02"},
	})
	if got := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://127.0.0.1:4317" {
		t.Errorf("endpoint env = %q", got)
	}
	if got := os.Getenv("OTEL_RESOURCE_ATTRIBUTES"); got != "host.name=mbp-02" {
		t.Errorf("resource attrs env = %q", got)
	}
}

func TestApplyOTelEnvLeavesExplicitEnv(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://explicit:4317")
	ApplyOTelEnv(OTelConfig{Endpoint: "http://config:4317"})
	if got := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://explicit:4317" {
		t.Errorf("explicit env must win, got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/config/ -run TestApplyOTelEnv -v`
Expected: FAIL — `undefined: ApplyOTelEnv`.

- [ ] **Step 3: Implement `ApplyOTelEnv`**

Add to `config.go` (and add `"sort"` + `"strings"` to imports):

```go
// ApplyOTelEnv exports the standard OTEL_* env vars from the config's [otel]
// block, but ONLY for env vars that are currently unset — an explicit env
// (e.g. a launchd plist) always wins. This lets the SDK-native otel
// constructors read endpoint/resource-attrs from env without bespoke exporter
// wiring. No OTEL_EXPORTER_OTLP_PROTOCOL is set: the gRPC exporters ignore it.
func ApplyOTelEnv(o OTelConfig) {
	setIfUnset := func(key, val string) {
		if val == "" {
			return
		}
		if _, ok := os.LookupEnv(key); ok {
			return
		}
		_ = os.Setenv(key, val)
	}
	setIfUnset("OTEL_EXPORTER_OTLP_ENDPOINT", o.Endpoint)
	setIfUnset("OTEL_RESOURCE_ATTRIBUTES", encodeResourceAttrs(o.ResourceAttrs))
}

// encodeResourceAttrs renders attrs as the W3C-baggage-style comma list the
// OTel SDK expects (key=value,key=value), sorted for determinism.
func encodeResourceAttrs(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/config/ -run TestApplyOTelEnv -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/internal/config/config.go packages/pa-monitor/internal/config/config_test.go
git commit -m "feat(pa-monitor): config.ApplyOTelEnv (config -> OTEL_* env, only-if-unset)"
```

---

## Task 3: `buildResource` merges `OTEL_RESOURCE_ATTRIBUTES`

**Files:**

- Modify: `packages/pa-monitor/internal/otel/resource.go`
- Test: `packages/pa-monitor/internal/otel/resource_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/otel/resource_test.go`:

```go
package otel

import (
	"context"
	"testing"

	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func TestBuildResourceMergesEnvAndKeepsServiceName(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "host.name=mbp-02")
	res, err := buildResource(context.Background(), "pa-monitor", "v1")
	if err != nil {
		t.Fatal(err)
	}
	attrs := res.Attributes()
	var sawHost, sawService bool
	for _, a := range attrs {
		if string(a.Key) == "host.name" && a.Value.AsString() == "mbp-02" {
			sawHost = true
		}
		if a.Key == semconv.ServiceNameKey && a.Value.AsString() == "pa-monitor" {
			sawService = true
		}
	}
	if !sawHost {
		t.Error("env resource attr host.name not merged")
	}
	if !sawService {
		t.Error("explicit service.name must survive the merge")
	}
}
```

Note: confirm the `semconv` import path/version against `go.mod` (`go.opentelemetry.io/otel/semconv/...`); if the repo uses `attribute.String("service.name", …)` directly (as `resource.go` does today), assert with `string(a.Key) == "service.name"` instead and drop the semconv import.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/otel/ -run TestBuildResourceMergesEnv -v`
Expected: FAIL — `host.name` not present (env detector not applied today).

- [ ] **Step 3: Add `WithFromEnv`, ordering so explicit wins**

Edit `resource.go`:

```go
func buildResource(ctx context.Context, serviceName, serviceVersion string) (*resource.Resource, error) {
	return resource.New(ctx,
		// WithFromEnv first so the explicit attributes below win on key
		// conflict (later options take precedence in the merge).
		resource.WithFromEnv(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", serviceVersion),
		),
	)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/otel/ -run TestBuildResourceMergesEnv -v`
Expected: PASS.

- [ ] **Step 5: Run the existing otel tests (no regression)**

Run: `cd packages/pa-monitor && go test ./internal/otel/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/internal/otel/resource.go packages/pa-monitor/internal/otel/resource_test.go
git commit -m "feat(pa-monitor): buildResource merges OTEL_RESOURCE_ATTRIBUTES (explicit service.name wins)"
```

---

## Task 4: `otel.NewConnectionEmitter`

**Files:**

- Create: `packages/pa-monitor/internal/otel/connection.go`
- Create: `packages/pa-monitor/internal/otel/connection_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/otel/connection_test.go`:

```go
package otel

import (
	"context"
	"testing"
)

func TestConnectionEmitterDisabledWhenNoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	e, err := NewConnectionEmitter(context.Background(), ConnOptions{
		ServiceName: "pa-monitor", Component: "cmux-bridge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e != nil {
		t.Fatalf("want nil emitter when endpoint unset, got %#v", e)
	}
	// nil receiver must be safe.
	e.RecordDaemonConnected(false)
	e.LogEvent("daemon.disconnect", map[string]string{"error": "x"})
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
}

func TestConnectionEmitterConstructsAndRecords(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	e, err := NewConnectionEmitter(context.Background(), ConnOptions{
		ServiceName: "pa-monitor", ServiceVersion: "test", Component: "tui",
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if e == nil {
		t.Fatal("want non-nil emitter when endpoint set")
	}
	defer e.Shutdown(context.Background())
	e.RecordDaemonConnected(true)
	e.RecordDaemonConnected(false)
	if got := e.connectedValue(); got != 0 {
		t.Errorf("connectedValue = %d, want 0", got)
	}
}
```

(`connectedValue()` is a tiny test-only accessor defined below.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/otel/ -run TestConnectionEmitter -v`
Expected: FAIL — `undefined: NewConnectionEmitter`.

- [ ] **Step 3: Implement the connection emitter**

Create `internal/otel/connection.go`:

```go
package otel

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
)

// connExportInterval is how often the PeriodicReader scrapes the connection
// gauge. Shorter than the daemon's default 60s so a connected=0 reading
// reaches Prometheus quickly and the `for: 1m` alert is responsive.
const connExportInterval = 15 * time.Second

// ConnOptions configures a ConnEmitter. Component is "cmux-bridge" or "tui".
type ConnOptions struct {
	ServiceName    string
	ServiceVersion string
	Component      string
}

// ConnEmitter is a minimal OTel emitter for the cmux-bridge and TUI. It owns
// exactly one observable gauge, pa_monitor.daemon.connected, plus a logger for
// low-level detail. A nil *ConnEmitter is a valid no-op (mirrors *Emitter).
type ConnEmitter struct {
	metricsProvider *sdkmetric.MeterProvider
	logProvider     *sdklog.LoggerProvider
	logger          otellog.Logger
	component        string

	mu             sync.Mutex
	connectedVal   int64
	connectedKnown bool
}

// NewConnectionEmitter returns (nil, nil) when OTEL_EXPORTER_OTLP_ENDPOINT is
// unset, matching otel.New's disabled-state contract.
func NewConnectionEmitter(ctx context.Context, opts ConnOptions) (*ConnEmitter, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, nil
	}
	res, err := buildResource(ctx, opts.ServiceName, opts.ServiceVersion)
	if err != nil {
		return nil, err
	}
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(connExportInterval))),
		sdkmetric.WithResource(res),
	)
	logExp, err := otlploggrpc.New(ctx)
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
	if err := e.registerGauge(mp); err != nil {
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
		return nil, err
	}
	return e, nil
}

func (e *ConnEmitter) registerGauge(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("pa-monitor")
	gauge, err := meter.Int64ObservableGauge("pa_monitor.daemon.connected")
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		e.mu.Lock()
		val, known := e.connectedVal, e.connectedKnown
		e.mu.Unlock()
		if known {
			o.ObserveInt64(gauge, val,
				metric.WithAttributes(attribute.String("component", e.component)))
		}
		return nil
	}, gauge)
	return err
}

// RecordDaemonConnected buffers the latest connection state for the gauge
// callback. nil-safe.
func (e *ConnEmitter) RecordDaemonConnected(connected bool) {
	if e == nil {
		return
	}
	v := int64(0)
	if connected {
		v = 1
	}
	e.mu.Lock()
	e.connectedVal = v
	e.connectedKnown = true
	e.mu.Unlock()
}

// LogEvent emits one info-level log record with event_name + component +
// attrs. nil-safe.
func (e *ConnEmitter) LogEvent(name string, attrs map[string]string) {
	if e == nil || e.logger == nil {
		return
	}
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(otellog.SeverityInfo)
	rec.SetBody(otellog.StringValue(name))
	rec.AddAttributes(otellog.String("event_name", name))
	rec.AddAttributes(otellog.String("component", e.component))
	for k, v := range attrs {
		if v == "" {
			continue
		}
		rec.AddAttributes(otellog.String(k, v))
	}
	e.logger.Emit(context.Background(), rec)
}

// Shutdown flushes both providers. nil-safe.
func (e *ConnEmitter) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	var firstErr error
	if e.metricsProvider != nil {
		if err := e.metricsProvider.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if e.logProvider != nil {
		if err := e.logProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// connectedValue is a test-only accessor for the buffered gauge value.
func (e *ConnEmitter) connectedValue() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.connectedVal
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/otel/ -run TestConnectionEmitter -v`
Expected: PASS (both). The construct test reaches a (likely unreachable) endpoint but exporter creation is lazy/non-blocking, so construction succeeds; Shutdown flushes best-effort.

- [ ] **Step 5: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/internal/otel/connection.go packages/pa-monitor/internal/otel/connection_test.go
git commit -m "feat(pa-monitor): otel.NewConnectionEmitter (pa_monitor.daemon.connected gauge)"
```

---

## Task 5: Generalize `tui.ErrorLogger` with `FileName`

**Files:**

- Modify: `packages/pa-monitor/internal/tui/errorlog.go`
- Test: `packages/pa-monitor/internal/tui/errorlog_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/tui/errorlog_test.go`:

```go
package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestErrorLoggerDefaultFileName(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir}
	l.LogString("hello")
	if _, err := os.Stat(filepath.Join(dir, "signal-errors.log")); err != nil {
		t.Fatalf("default file not created: %v", err)
	}
}

func TestErrorLoggerCustomFileName(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "cmux-bridge.log"}
	l.LogString("hello")
	if _, err := os.Stat(filepath.Join(dir, "cmux-bridge.log")); err != nil {
		t.Fatalf("custom file not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/tui/ -run TestErrorLogger -v`
Expected: FAIL — `unknown field FileName`.

- [ ] **Step 3: Add the `FileName` field**

In `errorlog.go`, add to the struct:

```go
type ErrorLogger struct {
	CacheDir string
	// FileName is the log file's basename; defaults to "signal-errors.log"
	// when empty (preserving existing callers).
	FileName string

	mu   sync.Mutex
	file io.WriteCloser
}
```

In `LogString`, replace the hard-coded filename:

```go
		name := e.FileName
		if name == "" {
			name = "signal-errors.log"
		}
		f, err := os.OpenFile(filepath.Join(e.CacheDir, name),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/tui/ -run TestErrorLogger -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/internal/tui/errorlog.go packages/pa-monitor/internal/tui/errorlog_test.go
git commit -m "feat(pa-monitor): ErrorLogger supports a custom FileName"
```

---

## Task 6: Bridge line formatter + lowercase phrases

**Files:**

- Modify: `packages/pa-monitor/cmd/pa-monitor/cmux_bridge.go`
- Test: `packages/pa-monitor/cmd/pa-monitor/cmux_bridge_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmux_bridge_test.go`:

```go
func TestFormatBridgeLine(t *testing.T) {
	ts := time.Date(2026, 6, 23, 15, 4, 5, 0, time.UTC)
	got := formatBridgeLine(ts, "Lost connection to daemon")
	want := "2026-06-23 15:04:05 Lost connection to daemon"
	if got != want {
		t.Errorf("formatBridgeLine = %q, want %q", got, want)
	}
}

func TestCaffeinatePhraseLowercase(t *testing.T) {
	if caffeinatePhrase(true) != "Caffeinated enabled" {
		t.Errorf("got %q", caffeinatePhrase(true))
	}
	if autoNudgePhrase(false) != "Auto Nudge disabled" {
		t.Errorf("got %q", autoNudgePhrase(false))
	}
}
```

Also UPDATE the existing assertions in `cmux_bridge_test.go` that check the old title-case strings — search for `"Caffeinated Enabled"`, `"Caffeinated Disabled"`, `"Auto Nudge Enabled"`, `"Auto Nudge Disabled"` (around lines 40, 55, 74, 77) and lowercase the verb to match (`"Caffeinated enabled"`, etc.). Also confirm those tests no longer expect a `cmux-bridge:` prefix (the diff functions take a `log func(string)` and tests pass their own collector, so the prefix is not asserted there — verify).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/ -run 'TestFormatBridgeLine|TestCaffeinatePhraseLowercase' -v`
Expected: FAIL — `undefined: formatBridgeLine`; phrase mismatch.

- [ ] **Step 3: Add formatter; lowercase phrases**

In `cmux_bridge.go`, add:

```go
// formatBridgeLine renders one operator-facing terminal line: a local
// date+time stamp and the message, with no "cmux-bridge:" prefix.
func formatBridgeLine(ts time.Time, msg string) string {
	return ts.Format("2006-01-02 15:04:05") + " " + msg
}
```

Edit `caffeinatePhrase` / `autoNudgePhrase` to lowercase the verb:

```go
func caffeinatePhrase(on bool) string {
	if on {
		return "Caffeinated enabled"
	}
	return "Caffeinated disabled"
}

func autoNudgePhrase(on bool) string {
	if on {
		return "Auto Nudge enabled"
	}
	return "Auto Nudge disabled"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/ -run 'TestFormatBridgeLine|TestCaffeinatePhrase|TestDiff|TestCmux' -v`
Expected: PASS (new + updated existing bridge tests).

- [ ] **Step 5: Run the renderer tests (must be untouched)**

Run: `cd packages/pa-monitor && go test ./internal/render/ -v`
Expected: PASS — `controls_test.go:22`'s `"Caffeinated Enabled"` strings are a negative (`wantNone`) assertion and are unaffected.

- [ ] **Step 6: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/cmd/pa-monitor/cmux_bridge.go packages/pa-monitor/cmd/pa-monitor/cmux_bridge_test.go
git commit -m "feat(pa-monitor): timestamped prefix-less bridge lines; lowercase state phrases"
```

---

## Task 7: Bridge connection announcer + detail logger

**Files:**

- Create: `packages/pa-monitor/cmd/pa-monitor/bridge_log.go`
- Test: `packages/pa-monitor/cmd/pa-monitor/cmux_bridge_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmux_bridge_test.go`:

```go
func TestConnAnnouncerTransitions(t *testing.T) {
	var term []string
	var details []string
	var gauge []bool
	a := &connAnnouncer{
		term:   func(s string) { term = append(term, s) },
		detail: func(event string, _ map[string]string) { details = append(details, event) },
		gauge:  func(c bool) { gauge = append(gauge, c) },
	}

	// Clean startup -> connected: no "restored" line, gauge true.
	a.connected()
	// Two disconnect rounds: exactly one "Lost" line, detail each time.
	a.disconnected(map[string]string{"error": "x"})
	a.disconnected(map[string]string{"error": "y"})
	// Reconnect: one "restored" line.
	a.connected()

	wantTerm := []string{"Lost connection to daemon", "Connection to daemon restored"}
	if !reflect.DeepEqual(term, wantTerm) {
		t.Errorf("term = %v, want %v", term, wantTerm)
	}
	if len(details) != 2 {
		t.Errorf("details = %v, want 2 disconnect details", details)
	}
	// gauge: connected(true), disconnected(false), connected(true) = [true,false,true]
	wantGauge := []bool{true, false, true}
	if !reflect.DeepEqual(gauge, wantGauge) {
		t.Errorf("gauge = %v, want %v", gauge, wantGauge)
	}
}
```

Add imports `reflect` to the test file if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/ -run TestConnAnnouncer -v`
Expected: FAIL — `undefined: connAnnouncer`.

- [ ] **Step 3: Implement `connAnnouncer` + `bridgeLogger`**

Create `cmd/pa-monitor/bridge_log.go`:

```go
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/otel"
	"github.com/phillipgreenii/pa-monitor/internal/tui"
)

// bridgeLogger fans bridge output to the right sink: Term() is the
// operator-facing pane line (timestamped, prefix-less, stderr); Detail() is
// low-level diagnostics that must never reach the pane — they go to a local
// log file (always) and OTel logs (when configured).
type bridgeLogger struct {
	now   func() time.Time
	file  *tui.ErrorLogger
	emit  *otel.ConnEmitter
	out   *os.File // stderr; injectable for tests
}

func newBridgeLogger(cacheDir string, emit *otel.ConnEmitter) *bridgeLogger {
	return &bridgeLogger{
		now:  time.Now,
		file: &tui.ErrorLogger{CacheDir: cacheDir, FileName: "cmux-bridge.log"},
		emit: emit,
		out:  os.Stderr,
	}
}

func (l *bridgeLogger) Term(msg string) {
	fmt.Fprintln(l.out, formatBridgeLine(l.now(), msg))
}

func (l *bridgeLogger) Detail(event string, fields map[string]string) {
	// Local file: one flattened line.
	line := event
	for k, v := range fields {
		line += fmt.Sprintf(" %s=%q", k, v)
	}
	l.file.LogString(line)
	l.emit.LogEvent(event, fields)
}

// connAnnouncer turns daemon connect/disconnect events into idempotent
// terminal lines + a connection gauge + low-level detail. The dependencies are
// plain funcs so it is unit-testable without real I/O or an emitter.
type connAnnouncer struct {
	announcedLost bool
	term          func(string)
	detail        func(event string, fields map[string]string)
	gauge         func(connected bool)
}

// disconnected records a failure. The "Lost connection to daemon" line is
// emitted at most once per disconnect episode; detail is logged every time.
func (c *connAnnouncer) disconnected(fields map[string]string) {
	if !c.announcedLost {
		c.term("Lost connection to daemon")
		c.announcedLost = true
		c.gauge(false)
	}
	c.detail("daemon.disconnect", fields)
}

// connected records that the stream is (re)established. "Connection to daemon
// restored" is emitted only if a loss was previously announced (no spurious
// line at clean startup). The gauge is set true every (re)connect.
func (c *connAnnouncer) connected() {
	if c.announcedLost {
		c.term("Connection to daemon restored")
		c.announcedLost = false
	}
	c.gauge(true)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/ -run TestConnAnnouncer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/cmd/pa-monitor/bridge_log.go packages/pa-monitor/cmd/pa-monitor/cmux_bridge_test.go
git commit -m "feat(pa-monitor): bridge connection announcer + detail logger"
```

---

## Task 8: Wire bridge output + emitter into the run loop

**Files:**

- Modify: `packages/pa-monitor/cmd/pa-monitor/cmux_bridge.go`

This task has no new unit test of its own (the logic is covered by Tasks 6–7); it is wiring. Verification is "package compiles + all bridge tests still pass + manual".

- [ ] **Step 1: Replace `runCmuxBridge` to build the logger/emitter/announcer**

Edit `runCmuxBridge` in `cmux_bridge.go`. Add imports: `"github.com/phillipgreenii/pa-monitor/internal/config"`, `"github.com/phillipgreenii/pa-monitor/internal/otel"`, `"path/filepath"`. Replace the body:

```go
func runCmuxBridge(args []string) {
	ws := os.Getenv("CMUX_WORKSPACE_ID")
	if ws == "" {
		fmt.Fprintln(os.Stderr, "CMUX_WORKSPACE_ID not set; nothing to bridge")
		os.Exit(2)
	}
	_ = args

	// OTel from the shared config file (single source of truth).
	cfg, _ := config.Load(config.DefaultPath())
	config.ApplyOTelEnv(cfg.OTel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emit, err := otel.NewConnectionEmitter(ctx, otel.ConnOptions{
		ServiceName:    "pa-monitor",
		ServiceVersion: version,
		Component:      "cmux-bridge",
	})
	if err != nil {
		emit = nil // best-effort; never block the sidebar on OTel
	}
	defer emit.Shutdown(ctx)

	home, _ := os.UserHomeDir()
	log := newBridgeLogger(filepath.Join(home, ".cache", "pa-monitor"), emit)

	reporter := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable: true,
		// Reporter errors are low-level (e.g. "cmux set-status: signal: killed")
		// -> detail sink, never the pane.
		Logf: func(s string) { log.Detail("cmux.reporter", map[string]string{"msg": s}) },
	})
	defer reporter.Clear()

	announcer := &connAnnouncer{
		term:   log.Term,
		detail: log.Detail,
		gauge:  emit.RecordDaemonConnected,
	}

	logBridgeVersions(ctx, log)

	for {
		if err := streamOnce(ctx, ws, reporter, log, announcer); err != nil {
			if ctx.Err() != nil {
				return
			}
			reporter.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateUnknown})
			announcer.disconnected(map[string]string{"error": err.Error()})
			time.Sleep(2 * time.Second)
			continue
		}
		return
	}
}
```

- [ ] **Step 2: Update `logBridgeVersions` to the friendly banner**

Replace `logBridgeVersions`:

```go
// logBridgeVersions prints the startup banner only when the daemon is
// reachable. If unreachable it stays silent on the pane (detail to log) — the
// reconnect loop will surface "Lost connection to daemon" instead.
func logBridgeVersions(ctx context.Context, log *bridgeLogger) {
	dialCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	client, err := rpcclient.Dial(dialCtx)
	if err != nil {
		log.Detail("bridge.version_probe", map[string]string{"version": version, "error": err.Error()})
		return
	}
	defer client.Close()
	state, err := client.C.GetState(dialCtx, &pb.GetStateRequest{})
	if err != nil {
		log.Detail("bridge.version_probe", map[string]string{"version": version, "error": err.Error()})
		return
	}
	log.Term(fmt.Sprintf("pa-monitor bridge v%s (daemon v%s)", version, state.GetDaemonVersion()))
}
```

- [ ] **Step 3: Update `registerBridge` to use the detail sink**

Replace the error logging inside `registerBridge` (keep the signature otherwise; add a `log *bridgeLogger` param):

```go
func registerBridge(ctx context.Context, client *rpcclient.Client, ws string, log *bridgeLogger) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := client.C.RegisterBridge(cctx, &pb.RegisterBridgeRequest{
		WorkspaceId: ws,
		BridgePid:   int32(os.Getpid()),
	}); err != nil {
		log.Detail("bridge.register_failed", map[string]string{"error": err.Error()})
	}
}
```

- [ ] **Step 4: Update `streamOnce` signature + connection/term wiring**

Change `streamOnce` to accept the logger + announcer, route the state/session diff lines through `log.Term`, call `announcer.connected()` on the first received message, and send push-missed/recv errors to detail (the returned error drives `announcer.disconnected` in the loop):

```go
func streamOnce(ctx context.Context, ws string, reporter cmuxstatus.Reporter, log *bridgeLogger, announcer *connAnnouncer) error {
	client, err := rpcclient.Dial(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	registerBridge(ctx, client, ws, log)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go func() {
		t := time.NewTicker(bridgeHeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-t.C:
				registerBridge(heartbeatCtx, client, ws, log)
			}
		}
	}()

	stream, err := client.C.WatchState(ctx, &pb.WatchStateRequest{PushIntervalMs: 2000})
	if err != nil {
		return err
	}

	const pushBudget = 4 * time.Second
	type recvResult struct {
		msg *pb.DaemonState
		err error
	}
	recvCh := make(chan recvResult, 1)
	next := func() {
		go func() {
			m, e := stream.Recv()
			recvCh <- recvResult{m, e}
		}()
	}
	next()

	var prev bridgeState
	var prevSessions bridgeSessions
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pushBudget):
			log.Detail("bridge.push_missed", map[string]string{"budget": pushBudget.String()})
			return fmt.Errorf("push missed: no message in %s", pushBudget)
		case r := <-recvCh:
			if r.err != nil {
				return r.err
			}
			next()
			if r.msg == nil {
				continue
			}
			// First successful message of this stream => (re)connected.
			announcer.connected()
			prev = diffAndLog(prev, stateFromDaemon(r.msg), log.Term)
			prevSessions = diffSessionsAndLog(prevSessions, sessionsFromDaemon(r.msg, ws), log.Term)
			snap := snapshotForWorkspace(r.msg, ws)
			reporter.Push(snap)
		}
	}
}
```

Remove the now-dead `logChange` closure. Note `announcer.connected()` is called on every message; it is idempotent (only the first after a loss prints "restored"; the gauge set-true is cheap). If you prefer to call it once per stream, guard with a local `bool` — not required.

- [ ] **Step 5: Build + run all bridge tests**

Run: `cd packages/pa-monitor && go build ./... && go test ./cmd/pa-monitor/ -v`
Expected: PASS. Fix any signature mismatch in tests that call `streamOnce`/`registerBridge`/`logBridgeVersions` directly (update them to pass a `newBridgeLogger(t.TempDir(), nil)` and a `&connAnnouncer{...}` with no-op funcs, or `t.Skip` integration-only ones).

- [ ] **Step 6: Manual smoke (no daemon)**

Run: `cd packages/pa-monitor && CMUX_WORKSPACE_ID=test go run ./cmd/pa-monitor cmux-bridge` for ~5s.
Expected on stderr: no `cmux-bridge:` prefix; a single timestamped `… Lost connection to daemon`; no repeated `stream lost` lines. Ctrl-C to stop. Confirm `~/.cache/pa-monitor/cmux-bridge.log` contains the low-level detail lines.

- [ ] **Step 7: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/cmd/pa-monitor/cmux_bridge.go packages/pa-monitor/cmd/pa-monitor/cmux_bridge_test.go
git commit -m "feat(pa-monitor): friendly cmux-bridge output + daemon.connected signal"
```

---

## Task 9: TUI connection emitter + sample ticker

**Files:**

- Modify: `packages/pa-monitor/cmd/pa-monitor/tui_remote.go`

- [ ] **Step 1: Add OTel wiring to `runTUIRemote`**

In `tui_remote.go`, add imports `"github.com/phillipgreenii/pa-monitor/internal/otel"` and `"time"` (already present) and `"context"` (already present). After `cfg` is loaded and `rp` is created, insert:

```go
	config.ApplyOTelEnv(cfg.OTel)
	emitCtx, emitCancel := context.WithCancel(context.Background())
	defer emitCancel()
	connEmit, err := otel.NewConnectionEmitter(emitCtx, otel.ConnOptions{
		ServiceName:    "pa-monitor",
		ServiceVersion: version,
		Component:      "tui",
	})
	if err != nil {
		connEmit = nil
	}
	defer connEmit.Shutdown(emitCtx)

	// Sample the poller's connection state on a ticker and publish the gauge.
	// IsOffline() is reliable: every backoff in RemotePoller is preceded by
	// client=nil, so client==nil holds throughout a disconnect window.
	go func() {
		const sample = 10 * time.Second
		t := time.NewTicker(sample)
		defer t.Stop()
		announced := false
		for {
			select {
			case <-emitCtx.Done():
				return
			case <-t.C:
				connected := !rp.IsOffline()
				connEmit.RecordDaemonConnected(connected)
				if !connected && !announced {
					announced = true
					connEmit.LogEvent("daemon.disconnect", map[string]string{"component": "tui"})
				}
				if connected && announced {
					announced = false
					connEmit.LogEvent("daemon.reconnect", map[string]string{"component": "tui"})
				}
			}
		}
	}()
```

(`rp` is `*rpcclient.RemotePoller`, which has `IsOffline()`. The `version` symbol is the package-level `var version`.)

- [ ] **Step 2: Build + run TUI-adjacent tests**

Run: `cd packages/pa-monitor && go build ./... && go test ./cmd/pa-monitor/ -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/cmd/pa-monitor/tui_remote.go
git commit -m "feat(pa-monitor): TUI emits pa_monitor.daemon.connected from poller state"
```

---

## Task 10: Daemon reads OTel from config

**Files:**

- Modify: `packages/pa-monitor/cmd/pa-monitor/daemon.go`

- [ ] **Step 1: Apply config OTel env before `otel.New`**

`buildRunOptions` receives `cfg config.Config`. Immediately before the `otel.New(...)` call (`daemon.go:120`), add:

```go
	config.ApplyOTelEnv(cfg.OTel)
```

Confirm `config` is already imported in `daemon.go` (it is — `runDaemon` calls `config.Load`). If `buildRunOptions` is in a different file/scope without the import, add it.

- [ ] **Step 2: Build + run daemon tests**

Run: `cd packages/pa-monitor && go build ./... && go test ./cmd/pa-monitor/ -run Daemon -v`
Expected: PASS.

- [ ] **Step 3: Run the full package test suite**

Run: `cd packages/pa-monitor && go test ./...`
Expected: PASS (all packages).

- [ ] **Step 4: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/cmd/pa-monitor/daemon.go
git commit -m "feat(pa-monitor): daemon sources OTel from the shared config file"
```

---

## Task 11: nix — `settings` option renders the XDG config

**Files:**

- Modify: `home/programs/pa-monitor/default.nix`

- [ ] **Step 1: Add the option + render**

Replace `home/programs/pa-monitor/default.nix` with:

```nix
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pa-monitor;
  tomlFormat = pkgs.formats.toml { };
in
{
  options.phillipgreenii.programs.pa-monitor = {
    enable = lib.mkEnableOption "pa-monitor (per-user Claude agents daemon + TUI)";
    package = lib.mkPackageOption pkgs "pa-monitor" { };

    daemon.enable = lib.mkEnableOption ''
      pa-monitor-daemon LaunchAgent — runs the daemon continuously at
      login. Disabled by default; opt in per host.

      The LaunchAgent itself is registered via the canonical
      `phillipgreenii.system.launchdServices.userAgents` helper from
      darwin/modules/pa-monitor/default.nix (system scope). This HM
      option only exists as the public-facing enable flag; the darwin
      module reads it across `config.home-manager.users.<u>`.
    '';

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      example = {
        otel.endpoint = "http://127.0.0.1:4317";
      };
      description = ''
        Written to `~/.config/pa-monitor/config.toml`. Keys must match
        pa-monitor's TOML schema (e.g. `otel.endpoint`,
        `otel.resource_attributes`, `plan_tier`, `[[decorator]]`). When empty,
        no file is written and pa-monitor uses its built-in defaults.
      '';
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    home.packages = [ cfg.package ];

    xdg.configFile."pa-monitor/config.toml" = lib.mkIf (cfg.settings != { }) {
      source = tomlFormat.generate "pa-monitor-config.toml" cfg.settings;
    };
  };
}
```

- [ ] **Step 2: Eval the option + format**

Run: `nix fmt` then `nix flake check 2>&1 | tail -20`
Expected: eval succeeds (no "option does not exist"); formatting clean.

- [ ] **Step 3: Commit**

```bash
nix run .#install-pre-commit-hooks   # only if .pre-commit-config changed; harmless otherwise
prek run --all-files
git add home/programs/pa-monitor/default.nix
git commit -m "feat(pa-monitor): HM settings option renders ~/.config/pa-monitor/config.toml"
```

---

## Task 12: nix — derive `settings.otel` from obs; drop plist env; register alert

**Files:**

- Modify: `darwin/modules/pa-monitor/default.nix`

- [ ] **Step 1: Add the derivation + cross-scope write; remove plist env; register the alert file**

In `darwin/modules/pa-monitor/default.nix`:

(a) In the `let` block, after `emitterEnv`, add:

```nix
  # OTel settings for the shared config file, derived from the same emitterEnv
  # the daemon used to receive via its plist. The ENDPOINT key is present iff
  # obs.enable, so it is the correct gate. We drop the rest: no `protocol`
  # config field (exporters are gRPC-only), OTEL_SERVICE_NAME is set in Go, and
  # the module passes no resourceAttrs so OTEL_RESOURCE_ATTRIBUTES is absent.
  otelSettings = lib.optionalAttrs (emitterEnv ? OTEL_EXPORTER_OTLP_ENDPOINT) {
    otel.endpoint = emitterEnv.OTEL_EXPORTER_OTLP_ENDPOINT;
  };

  # Users who opted into the daemon — the same predicate as daemonEnabledByAnyUser.
  daemonEnabledUsers = lib.filterAttrs (
    _: u: u.phillipgreenii.programs.pa-monitor.daemon.enable or false
  ) hmUsers;
```

(b) In the daemon LaunchAgent `serviceConfig`, **remove** the `EnvironmentVariables = emitterEnv;` line:

```nix
        serviceConfig = {
          StandardErrorPath = "${stateHome}/pa-monitor/launchd-stderr.log";
          StandardOutPath = "${stateHome}/pa-monitor/launchd-stdout.log";
        };
```

(c) Add the alert file to the existing `alertRuleFiles` list (inside the `obs.enable` mkIf branch):

```nix
      phillipgreenii.observability.alertRuleFiles = [
        ../../../packages/pa-monitor/grafana/alerting/auth-failure.yaml
        ../../../packages/pa-monitor/grafana/alerting/daemon-connection.yaml
      ];
```

(d) Add a new branch to the top-level `config = lib.mkMerge [ ... ]` list that writes the derived settings to each daemon-enabled user. Set it as a contribution (the module system merges it with any user-supplied `settings`); do NOT read `…settings` back:

```nix
    (lib.mkIf (daemonEnabledByAnyUser && otelSettings != { }) {
      home-manager.users = lib.mapAttrs (_: _: {
        phillipgreenii.programs.pa-monitor.settings = otelSettings;
      }) daemonEnabledUsers;
    })
```

- [ ] **Step 2: Eval — watch for recursion / option errors**

Run: `nix flake check 2>&1 | tail -30`
Expected: succeeds. If it reports infinite recursion, the cause is reading `…settings` while writing it — confirm step (d) only WRITES `settings` and that `daemonEnabledUsers` filters on `.daemon.enable` (a different option), which is safe.

- [ ] **Step 3: Confirm the daemon plist no longer carries OTEL env**

Run: `nix eval --raw .#darwinConfigurations.<host>.config.launchd... ` is brittle; instead inspect the rendered plist after a build, or grep the module:
Run: `grep -n "EnvironmentVariables" darwin/modules/pa-monitor/default.nix`
Expected: no match.

- [ ] **Step 4: Commit**

```bash
prek run --all-files
git add darwin/modules/pa-monitor/default.nix
git commit -m "feat(pa-monitor): config file is single OTel source (drop daemon plist env); register daemon-connection alert"
```

---

## Task 13: Grafana alert rule

**Files:**

- Create: `packages/pa-monitor/grafana/alerting/daemon-connection.yaml`

- [ ] **Step 1: Write the rule**

Create the file with a **distinct group name** (`pa-monitor-connection`) so it does not clobber the `pa-monitor` group defined by `auth-failure.yaml`:

```yaml
apiVersion: 1
groups:
  - orgId: 1
    name: pa-monitor-connection
    folder: Claude Agents
    interval: 1m
    rules:
      - uid: pa-monitor-daemon-connection-lost
        title: pa-monitor lost connection to daemon
        condition: C
        for: 1m # alert only after >1m of inability to reach the daemon
        noDataState: OK # a closed pane / exited process => stale series => no alarm
        execErrState: Error
        labels:
          severity: warning
        annotations:
          summary: "pa-monitor cannot reach the daemon (>1m)"
          description: "A pa-monitor component (cmux-bridge or tui) has not reached the daemon for over a minute. Check that the pa-monitor daemon is running (launchctl / pa-monitor status)."
        data:
          - refId: A
            relativeTimeRange:
              from: 600
              to: 0
            datasourceUid: prometheus
            model:
              refId: A
              editorMode: code
              instant: true
              # NOTE: deliberately no `or vector(0)` — absence must fall through
              # to noDataState: OK, not synthesize a 0 and fire.
              expr: min by (component) (pa_monitor_daemon_connected)
          - refId: B
            datasourceUid: __expr__
            model:
              refId: B
              type: reduce
              expression: A
              reducer: last
          - refId: C
            datasourceUid: __expr__
            model:
              refId: C
              type: threshold
              expression: B
              conditions:
                - evaluator:
                    type: lt
                    params: [1]
```

- [ ] **Step 2: Validate YAML**

Run: `cd packages/pa-monitor && yq '.groups[0].rules[0].uid' grafana/alerting/daemon-connection.yaml`
Expected: `pa-monitor-daemon-connection-lost`.

- [ ] **Step 3: Commit**

```bash
prek run --all-files
git add packages/pa-monitor/grafana/alerting/daemon-connection.yaml
git commit -m "feat(pa-monitor): Grafana alert — daemon connection lost >1m"
```

---

## Task 14: Documentation (ADR 0016, index, README)

**Files:**

- Create: `docs/adr/0016-pa-monitor-config-sourced-otel-and-connection-alert.md`
- Modify: `docs/adr/index.md`
- Modify: `packages/pa-monitor/README.md`

- [ ] **Step 1: Write ADR 0016**

Create `docs/adr/0016-pa-monitor-config-sourced-otel-and-connection-alert.md`:

```markdown
# pa-monitor OTel sourced from the shared config file + daemon-connection alert

**Status**: Accepted
**Date**: 2026-06-23
**Deciders**: Phillip

## Context

The cmux-bridge dumped low-level RPC/transport churn to its pane. Separately,
nothing alerted when the bridge or TUI could not reach the daemon. OTel was
configured only for the daemon, via its launchd plist `EnvironmentVariables`
(ADR 0011) — the bridge and TUI emitted nothing, and there was no single place
that defined OTel for all three processes.

## Decision

1. **OTel configuration is sourced from the shared XDG config file**
   (`~/.config/pa-monitor/config.toml`, `[otel]` block) for the daemon, the
   cmux-bridge, and the TUI. A `config.ApplyOTelEnv` shim exports the standard
   `OTEL_*` env vars (only if unset) so the SDK-native constructors consume
   them. The daemon's plist `EnvironmentVariables` is removed — the config file
   is the single source of truth. agent-support derives the `[otel]` values
   from `phillipgreenii.observability` and writes them into each
   daemon-enabled user's `settings` (a new generic HM passthrough rendered to
   the config file).
2. **The cmux-bridge and TUI publish `pa_monitor.daemon.connected{component}`**
   (1/0) via a minimal `otel.ConnEmitter`, and a Grafana rule alerts on
   `min by (component)(pa_monitor_daemon_connected) < 1 for 1m`
   (`noDataState: OK`). Low-level bridge detail moves off the pane to a local
   log file and OTel logs; the pane shows only timestamped, prefix-less,
   operator-facing lines (state changes, session roster, and Lost/Restored
   connection events).

There is intentionally no `protocol` config knob: the emitters import the
gRPC-only OTLP exporter packages, so transport is fixed and the endpoint URL
scheme (`http://` = insecure gRPC) is what selects behaviour.

## Consequences

### Positive

- One consistent OTel config for all pa-monitor processes; no plist/env drift.
- Daemon-down (or wedged) is now alertable within ~1m.
- The cmux-bridge pane is readable; diagnostics are still captured.

### Negative

- A brief first-activation window where the config file may not yet be written
  leaves the daemon's OTel off until its next restart (self-heals via
  `keepAlive`).
- Adding HTTP/protobuf transport later is separate, explicitly-scoped work.

### Neutral

- The bridge and TUI now construct OTel providers (gated off when no endpoint
  is configured, exactly like the daemon).

## Related Decisions

- Extends / partially supersedes `docs/adr/0011-pa-monitor-daemon-otel-split.md`
  (daemon-only emitter; OTel env was plist-sourced).
- Follows the alert-registration pattern of `grafana/alerting/auth-failure.yaml`.
```

- [ ] **Step 2: Add the index row**

In `docs/adr/index.md`, add a row after the `0015` line:

```markdown
| [0016](0016-pa-monitor-config-sourced-otel-and-connection-alert.md) | pa-monitor OTel from shared config + daemon-connection alert | Accepted | 2026-06-23 |
```

- [ ] **Step 3: Update the package README**

In `packages/pa-monitor/README.md`, add a section documenting: the `[otel]` config block (`endpoint`, `resource_attributes`; no `protocol`), that the daemon/bridge/TUI all read it, the friendly bridge output + `cmux-bridge.log` location, and the `pa_monitor.daemon.connected` metric + alert. (Match the README's existing heading style; keep technical tokens in backticks per the repo's prettier conventions.)

- [ ] **Step 4: Format + commit**

```bash
nix fmt
prek run --all-files
git add docs/adr/0016-pa-monitor-config-sourced-otel-and-connection-alert.md docs/adr/index.md packages/pa-monitor/README.md
git commit -m "docs(pa-monitor): ADR 0016 + README for config-sourced OTel + connection alert"
```

---

## Final verification

- [ ] **Full Go suite:** `cd packages/pa-monitor && go test ./...` → all PASS.
- [ ] **Build:** `nix build .#pa-monitor` → succeeds.
- [ ] **Flake check:** `nix flake check` → PASS (formatting, eval, package, statix).
- [ ] **Pre-commit:** `prek run --all-files` → PASS.
- [ ] **Manual (with local otel-stack up):** start daemon + a `cmux-bridge` pane; kill the daemon → pane shows one `… Lost connection to daemon`, `~/.cache/pa-monitor/cmux-bridge.log` has the detail, Prometheus shows `pa_monitor_daemon_connected{component="cmux-bridge"} 0`, Grafana fires after ~1m; restart daemon → `… Connection to daemon restored`, alert clears.
- [ ] **Finish the branch:** use `superpowers:finishing-a-development-branch`.

---

## Self-review notes (spec coverage)

- Spec D1 (format/scope) → Tasks 6, 8. D2 (state machine) → Task 7, 8. D3 (detail sink) → Tasks 5, 7, 8. D4 (connection emitter + TUI sampling + Shutdown) → Tasks 3, 4, 8, 9. D5 (config `[otel]` + ApplyOTelEnv + all entrypoints) → Tasks 1, 2, 8, 9, 10. D6 (nix settings, plist removal, derivation) → Tasks 11, 12. D7 (alert) → Tasks 12 (registration), 13 (rule). Docs → Task 14.
- No `protocol` field anywhere (spec M1). `defer Shutdown` on bridge (Task 8) and TUI (Task 9) (spec S1). `WithFromEnv` ordering test (Task 3) (spec S2). TUI uses `IsOffline()`, not a 1s window (Task 9) (spec S3). HM `settings` option (Task 11) precedes the darwin write (Task 12); write is a contribution, not read-then-write (spec S4). `emitterEnv` → endpoint only (Task 12) (spec S5). `formats.toml`-typed option (Task 11) (spec S6). Alert has no `or vector(0)`, distinct group name (Task 13) (spec N3 + clobber-avoidance).
