# pg-pr Sync Error Log Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface pg-pr sync error _messages_ (not just the counter) in a scrolling Loki logs panel on the `pg-pr / Ops` Grafana dashboard, by making the daemon emit OTLP logs.

**Architecture:** Extend `pg-pr`'s `telemetry.Init` to stand up an OTLP LoggerProvider beside the existing trace provider; fan the daemon's `slog` logger out to both stderr (unchanged) and an `otelslog` bridge → otelcol → Loki. Wire the `pg-pr-sync` launchd service to the local otelcol via the existing `mkEmitterEnv` helper (`service.name=pg-pr-sync`). Add a Loki `logs` panel under the sync-errors chart selecting `{service_name="pg-pr-sync"}`.

**Tech Stack:** Go 1.25, OpenTelemetry-Go (core `v1.44.0`, log signal `v0.20.0`), `log/slog`, nix-darwin / `buildGoModule`, Grafana + Loki (OTLP ingestion).

**Spec:** `docs/superpowers/specs/2026-06-09-pg-pr-sync-error-log-panel-design.md`

---

## Preconditions & branching

This change spans **three** git repos under `~/phillipg_mbp/`:

- `phillipgreenii-nix-agent-support` — Go capability + docs (Tasks 1–3, 6).
- `phillipg-nix-ziprecruiter` — `pg-pr-sync` service env (Task 4).
- `phillipgreenii-nix-support-apps` — dashboard panel (Task 5).

⚠️ `phillipgreenii-nix-agent-support` is currently on branch
`phillipg.pr-pool-orchestrator` with **unrelated uncommitted WIP**. Before
starting Tasks 1–3/6, coordinate that work (commit or stash it) and create a
dedicated branch off the intended base, e.g.:

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support
git status            # confirm what the WIP is; do NOT discard it
git switch -c phillipg.pg-pr-otlp-logs   # branch for this work
```

Do the same in the other two repos at the start of Tasks 4 and 5:

```bash
cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && git switch -c phillipg.pg-pr-otlp-logs
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps && git switch -c phillipg.pg-pr-otlp-logs
```

All Go commands run from `~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-pr`. Go must be on PATH (devbox shell or `nix develop`); the repo targets Go 1.25.

---

## File structure

- `packages/pg-pr/internal/telemetry/slog.go` _(new)_ — fan-out `slog.Handler` + `NewSlogHandler` otelslog bridge. One responsibility: slog↔OTel glue.
- `packages/pg-pr/internal/telemetry/slog_test.go` _(new)_ — unit tests for the above.
- `packages/pg-pr/internal/telemetry/telemetry.go` _(modify)_ — extend `Init` to build the OTLP LoggerProvider; extract `buildResource`; add `newOTLPLogExporter`.
- `packages/pg-pr/internal/telemetry/telemetry_test.go` _(modify)_ — add logger-provider contract tests.
- `packages/pg-pr/internal/sync/daemon.go` _(modify)_ — add stderr handler constructors; keep existing logger constructors as thin wrappers.
- `packages/pg-pr/internal/sync/daemon_test.go` _(modify; create if absent)_ — stderr handler format tests.
- `packages/pg-pr/cmd/pg-pr/sync.go` _(modify)_ — compose the daemon logger from stderr + otel handlers.
- `packages/pg-pr/go.mod`, `go.sum`, `packages/pg-pr/default.nix` _(modify)_ — new deps + refreshed `vendorHash`.
- `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix` _(modify)_ — `EnvironmentVariables = emitterEnv`.
- `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json` _(modify)_ — new logs panel; shift panels 5 & 6.
- `phillipgreenii-nix-agent-support` docs/ADR _(modify/new)_ — Task 6.

---

## Task 1: Fan-out slog handler

A `slog.Handler` that dispatches each record to every wrapped handler. Pure logic, no new dependencies — do this first.

**Files:**

- Create: `packages/pg-pr/internal/telemetry/slog.go`
- Test: `packages/pg-pr/internal/telemetry/slog_test.go`

- [ ] **Step 1: Write the failing tests**

Create `packages/pg-pr/internal/telemetry/slog_test.go`:

```go
package telemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestFanout_HandleReachesAllChildren(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	h := Fanout(slog.NewJSONHandler(&buf1, nil), slog.NewJSONHandler(&buf2, nil))
	slog.New(h).Info("hello", "k", "v")
	if !strings.Contains(buf1.String(), "hello") {
		t.Errorf("child 1 missing record: %q", buf1.String())
	}
	if !strings.Contains(buf2.String(), "hello") {
		t.Errorf("child 2 missing record: %q", buf2.String())
	}
}

func TestFanout_WithAttrsPropagatesToAllChildren(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	log := slog.New(Fanout(
		slog.NewJSONHandler(&buf1, nil),
		slog.NewJSONHandler(&buf2, nil),
	)).With("svc", "pg-pr")
	log.Info("msg")
	for i, b := range []*bytes.Buffer{&buf1, &buf2} {
		if !strings.Contains(b.String(), `"svc":"pg-pr"`) {
			t.Errorf("child %d missing attr: %q", i+1, b.String())
		}
	}
}

func TestFanout_EnabledIfAnyChildEnabled(t *testing.T) {
	errorOnly := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})
	infoOK := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})
	if !Fanout(errorOnly, infoOK).Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected enabled at Info when one child is")
	}
	if Fanout(errorOnly).Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected disabled at Info when no child is")
	}
}

func TestFanout_PerChildLevelFiltering(t *testing.T) {
	var errBuf, infoBuf bytes.Buffer
	errorOnly := slog.NewJSONHandler(&errBuf, &slog.HandlerOptions{Level: slog.LevelError})
	infoOK := slog.NewJSONHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.New(Fanout(errorOnly, infoOK)).Info("only-info")
	if errBuf.Len() != 0 {
		t.Errorf("error-only child should have filtered Info: %q", errBuf.String())
	}
	if !strings.Contains(infoBuf.String(), "only-info") {
		t.Errorf("info child missing record: %q", infoBuf.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pg-pr && go test ./internal/telemetry/ -run TestFanout -v`
Expected: FAIL — `undefined: Fanout`.

- [ ] **Step 3: Implement the fan-out handler**

Create `packages/pg-pr/internal/telemetry/slog.go`:

```go
// Package telemetry — slog↔OTel glue.
//
// Fanout lets the daemon keep its stderr logging while additionally exporting
// every record over OTLP through the otelslog bridge. NewSlogHandler builds
// that bridge bound to the global LoggerProvider Init installs.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
)

// fanoutHandler dispatches each record to every wrapped handler. A child is
// only invoked when it reports Enabled for the record's level, so per-child
// level thresholds are respected.
type fanoutHandler struct {
	handlers []slog.Handler
}

// Fanout returns a slog.Handler that forwards to every handler passed in.
func Fanout(handlers ...slog.Handler) slog.Handler {
	return fanoutHandler{handlers: handlers}
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// Clone per the slog.Handler contract: a Record must not be shared
		// across handlers that may mutate it.
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return fanoutHandler{handlers: next}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return f
	}
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return fanoutHandler{handlers: next}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pg-pr && go test ./internal/telemetry/ -run TestFanout -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/telemetry/slog.go packages/pg-pr/internal/telemetry/slog_test.go
git commit -m "feat(pg-pr): add fan-out slog.Handler for telemetry"
```

---

## Task 2: OTLP LoggerProvider + otelslog bridge in telemetry

Add the OTel log dependencies, extend `Init` to install an OTLP LoggerProvider beside the trace provider, and add `NewSlogHandler`.

**Files:**

- Modify: `packages/pg-pr/internal/telemetry/telemetry.go`
- Modify: `packages/pg-pr/internal/telemetry/slog.go` (add `NewSlogHandler`)
- Modify: `packages/pg-pr/internal/telemetry/telemetry_test.go`
- Modify: `packages/pg-pr/go.mod`, `go.sum`, `packages/pg-pr/default.nix`

- [ ] **Step 1: Add the OTel log dependencies**

The log signal versions on its own `v0.x` line (verified against `packages/pa-monitor/go.mod`: core `v1.44.0` ↔ log `v0.20.0`). Run:

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-pr
go get go.opentelemetry.io/otel/log@v0.20.0
go get go.opentelemetry.io/otel/sdk/log@v0.20.0
go get go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp@v0.20.0
go get go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc@v0.20.0
go get go.opentelemetry.io/contrib/bridges/otelslog
```

If the last `go get` resolves a version incompatible with `otel/log v0.20.0`,
pin the otelslog bridge to the release that requires `otel/log v0.20.0` (check
its `go.mod` on pkg.go.dev). Do not let it bump `otel/log` past `v0.20.x`.

Expected: `go.mod` now lists `otel/log v0.20.0`, `otel/sdk/log v0.20.0`, both `otlplog/*` exporters at `v0.20.0`, and `contrib/bridges/otelslog`.

- [ ] **Step 2: Write the failing tests**

Add to `packages/pg-pr/internal/telemetry/telemetry_test.go`:

```go
func TestInit_NoEndpoint_InstallsNoopLoggerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background(), "pg-pr-test", "v0.0.0")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	lp := logglobal.GetLoggerProvider()
	if lp == nil {
		t.Fatal("nil global logger provider")
	}
	// Emitting through the installed (noop) provider must not panic.
	lg := lp.Logger("probe")
	var rec otellog.Record
	rec.SetBody(otellog.StringValue("probe"))
	lg.Emit(context.Background(), rec)
}

func TestNewSlogHandler_NoProvider_NoError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if _, err := Init(context.Background(), "pg-pr-test", "v0.0.0"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	h := NewSlogHandler()
	if h == nil {
		t.Fatal("NewSlogHandler returned nil")
	}
	// Logging through the bridge with the noop provider must not panic/error.
	slog.New(h).Warn("probe", "k", "v")
}
```

Add these imports to the test file's import block:

```go
	"log/slog"

	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd packages/pg-pr && go test ./internal/telemetry/ -run 'TestInit_NoEndpoint_InstallsNoopLoggerProvider|TestNewSlogHandler' -v`
Expected: FAIL — `undefined: NewSlogHandler` and the logger provider is never installed.

- [ ] **Step 4: Add `NewSlogHandler` to slog.go**

Append to `packages/pg-pr/internal/telemetry/slog.go` imports and body:

```go
// add to the import block:
//   "go.opentelemetry.io/contrib/bridges/otelslog"

// NewSlogHandler returns an slog.Handler that exports records to the global
// OTel LoggerProvider (installed by Init). When no OTLP endpoint is
// configured the global provider is a no-op, so this handler is a cheap
// no-op and is safe to include unconditionally. The instrumentation scope
// name is TracerName; it does NOT set service_name — that comes from the
// resource (OTEL_SERVICE_NAME) configured in Init.
func NewSlogHandler() slog.Handler {
	return otelslog.NewHandler(TracerName)
}
```

- [ ] **Step 5: Extend `telemetry.Init` to build the LoggerProvider**

In `packages/pg-pr/internal/telemetry/telemetry.go`:

(a) Add to the import block:

```go
	"errors"

	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
```

(b) Replace the body of `Init` (lines 65–108) with:

```go
func Init(ctx context.Context, serviceName, version string) (ShutdownFunc, error) {
	noopShutdown := func(context.Context) error { return nil }

	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		// No collector configured — install no-op providers so callers can
		// use otel.Tracer(...) and the global LoggerProvider without nil
		// worries.
		otel.SetTracerProvider(noop.NewTracerProvider())
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return noopShutdown, nil
	}

	res := buildResource(ctx, serviceName, version)
	var shutdowns []ShutdownFunc

	// Traces.
	if traceExp, err := newOTLPExporter(ctx); err != nil {
		fmt.Fprintf(os.Stderr,
			"pg-pr: OTel trace exporter init failed (%v); traces will be no-op\n", err)
		otel.SetTracerProvider(noop.NewTracerProvider())
	} else {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExp),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	// Logs.
	if logExp, err := newOTLPLogExporter(ctx); err != nil {
		fmt.Fprintf(os.Stderr,
			"pg-pr: OTel log exporter init failed (%v); logs will be no-op\n", err)
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
	} else {
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
			sdklog.WithResource(res),
		)
		logglobal.SetLoggerProvider(lp)
		shutdowns = append(shutdowns, lp.Shutdown)
	}

	return func(ctx context.Context) error {
		var errs []error
		for _, s := range shutdowns {
			if err := s(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

// buildResource constructs the shared OTel resource (service.name/version +
// env + runtime attrs), degrading to a schemaless resource if detection
// fails. service.name comes from OTEL_SERVICE_NAME, else serviceName.
func buildResource(ctx context.Context, serviceName, version string) *resource.Resource {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(envOr("OTEL_SERVICE_NAME", serviceName)),
			semconv.ServiceVersion(version),
		),
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
	)
	if err != nil {
		return resource.NewSchemaless(
			semconv.ServiceName(envOr("OTEL_SERVICE_NAME", serviceName)),
			semconv.ServiceVersion(version),
		)
	}
	return res
}

// newOTLPLogExporter builds the OTLP log exporter, mirroring
// newOTLPExporter's protocol switch (grpc vs default http/protobuf). Endpoint
// and TLS come from env vars honored by the exporter packages.
func newOTLPLogExporter(ctx context.Context) (sdklog.Exporter, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))) {
	case "grpc":
		return otlploggrpc.New(ctx)
	default:
		return otlploghttp.New(ctx)
	}
}
```

This preserves the existing trace behavior (the old inline resource block is now `buildResource`) — the existing `TestInit_*` trace tests must still pass.

- [ ] **Step 6: Tidy modules, refresh vendorHash, and build**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-pr
./update-deps.sh
```

Expected: `go mod tidy` succeeds, `nix-update` rewrites `vendorHash` in `default.nix`, and `nix build .#pg-pr` prints `✓ Success!`.

- [ ] **Step 7: Run the telemetry tests**

Run: `cd packages/pg-pr && go test ./internal/telemetry/ -v`
Expected: PASS — the new logger tests plus all pre-existing trace tests.

- [ ] **Step 8: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/telemetry/ packages/pg-pr/go.mod packages/pg-pr/go.sum packages/pg-pr/default.nix
git commit -m "feat(pg-pr): emit OTLP logs via global LoggerProvider + otelslog bridge"
```

---

## Task 3: Wire the daemon logger fan-out

Make the daemon log to both stderr (unchanged) and the OTLP bridge.

**Files:**

- Modify: `packages/pg-pr/internal/sync/daemon.go`
- Modify: `packages/pg-pr/internal/sync/daemon_test.go` (create if absent)
- Modify: `packages/pg-pr/cmd/pg-pr/sync.go`

- [ ] **Step 1: Write the failing tests**

Add to `packages/pg-pr/internal/sync/daemon_test.go` (create the file with `package sync` + imports if it does not exist):

```go
func TestNewStderrHandler_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newStderrHandler(&buf, true)).Error("boom", "err", "bad")
	out := buf.String()
	if !strings.Contains(out, `"msg":"boom"`) || !strings.Contains(out, `"err":"bad"`) {
		t.Fatalf("unexpected json log: %q", out)
	}
	if !strings.Contains(out, `"level":"ERROR"`) {
		t.Fatalf("missing level: %q", out)
	}
}

func TestNewStderrHandler_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newStderrHandler(&buf, false)).Warn("watch")
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "watch") {
		t.Fatalf("unexpected text log: %q", out)
	}
}
```

Ensure the test file imports `"bytes"`, `"log/slog"`, `"strings"`, `"testing"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run TestNewStderrHandler -v`
Expected: FAIL — `undefined: newStderrHandler`.

- [ ] **Step 3: Add stderr handler constructors in daemon.go**

In `packages/pg-pr/internal/sync/daemon.go`, add `"io"` to the import block, then replace the existing logger constructors (lines 236–245) with:

```go
// newStderrHandler builds the daemon's base slog handler at Info level.
// jsonFormat selects JSON vs human-readable text. The writer is injectable
// for tests; production callers use NewJSONHandler/NewTextHandler (os.Stderr).
func newStderrHandler(w io.Writer, jsonFormat bool) slog.Handler {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if jsonFormat {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// NewJSONHandler returns the stderr JSON slog.Handler used by the daemon when
// --log-json is set. CLI wiring composes it with the OTLP bridge handler.
func NewJSONHandler() slog.Handler { return newStderrHandler(os.Stderr, true) }

// NewTextHandler returns the stderr text slog.Handler (default daemon format).
func NewTextHandler() slog.Handler { return newStderrHandler(os.Stderr, false) }

// NewJSONLogger returns a slog.Logger writing structured JSON to stderr.
// Retained for back-compat / standalone use; the daemon path composes
// handlers directly (see cmd/pg-pr/sync.go).
func NewJSONLogger() *slog.Logger { return slog.New(NewJSONHandler()) }

// NewTextLogger returns a slog.Logger writing human-readable text to stderr.
func NewTextLogger() *slog.Logger { return slog.New(NewTextHandler()) }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run TestNewStderrHandler -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Compose the fan-out logger in sync.go**

In `packages/pg-pr/cmd/pg-pr/sync.go`, add `"log/slog"` and the telemetry import to the import block:

```go
	"log/slog"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
```

Then replace the daemon logger construction (lines 84–87):

```go
				var base = sync.NewTextHandler()
				if syFlags.logJSON {
					base = sync.NewJSONHandler()
				}
				// Fan out to stderr (preserves ~/Library/Logs/pg-pr-sync.err)
				// and the OTLP bridge (→ otelcol → Loki). The bridge is a
				// no-op when no OTLP endpoint is configured.
				logger := slog.New(telemetry.Fanout(base, telemetry.NewSlogHandler()))
```

(The downstream `engine.Daemon(ctx, sync.DaemonOpts{ … Logger: logger … })` call is unchanged.)

- [ ] **Step 6: Build and run the full package test suite**

Run: `cd packages/pg-pr && go build ./... && go test ./...`
Expected: PASS across all packages (confirms sync.go compiles with the new wiring and nothing else regressed).

- [ ] **Step 7: Verify the nix build still succeeds**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-agent-support && nix build .#pg-pr --no-link`
Expected: succeeds (no vendorHash change since no new deps were added in this task).

- [ ] **Step 8: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/sync/daemon.go packages/pg-pr/internal/sync/daemon_test.go packages/pg-pr/cmd/pg-pr/sync.go
git commit -m "feat(pg-pr): fan daemon logger out to stderr + OTLP"
```

- [ ] **Step 9: Run the repo's flake check before leaving this repo's Go work**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-agent-support && nix flake check`
Expected: PASS. (Per CLAUDE.md, this MUST pass before the change is considered complete. No `flake.nix` pre-commit block changed, so `nix run .#install-pre-commit-hooks` is NOT required.)

---

## Task 4: Wire OTLP env onto the pg-pr-sync service

Point the launchd daemon at the local otelcol with `service.name=pg-pr-sync`, using the established `gc-dolt-maintenance` pattern.

**Files:**

- Modify: `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix`

- [ ] **Step 1: Add the emitterEnv binding**

In the `let` block of `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix` (after the existing `homeDir` line), add:

```nix
  obs = config.phillipgreenii.observability;

  # OTel emitter env for the sync daemon. Resolved here where the
  # observability surface is declared; injected via EnvironmentVariables
  # (the userAgents submodule has no `environment` attr — only serviceConfig
  # pass-through). Guard against the option being absent so the module still
  # evaluates. Returns {} when observability is disabled.
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pg-pr-sync";
        protocol = "http/protobuf";
      }
    else
      { };
```

- [ ] **Step 2: Attach EnvironmentVariables to the service**

In the same file, change the `serviceConfig` block of
`phillipgreenii.system.launchdServices.userAgents.pg-pr-sync` from:

```nix
      serviceConfig = {
        StandardOutPath = "${homeDir}/Library/Logs/pg-pr-sync.log";
        StandardErrorPath = "${homeDir}/Library/Logs/pg-pr-sync.err";
      };
```

to:

```nix
      serviceConfig = {
        StandardOutPath = "${homeDir}/Library/Logs/pg-pr-sync.log";
        StandardErrorPath = "${homeDir}/Library/Logs/pg-pr-sync.err";
        EnvironmentVariables = emitterEnv;
      };
```

- [ ] **Step 3: Build-validate the darwin config**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && nix fmt && zn-self-build`
Expected: formats and builds without activating. If running sandboxed where activation is blocked, `zn-self-build` is the correct validation step (per the repo CLAUDE.md). Do NOT run `sudo darwin-rebuild` from an agent session.

- [ ] **Step 4: Commit**

```bash
cd ~/phillipg_mbp/phillipg-nix-ziprecruiter
git add darwin/services/pg-pr-sync/default.nix
git commit -m "feat(pg-pr-sync): export OTLP telemetry env (service.name=pg-pr-sync)"
```

---

## Task 5: Add the Loki logs panel to the Ops dashboard

**Files:**

- Modify: `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json`

- [ ] **Step 1: Shift panel 5 down to y:20**

In `pg-pr-ops.json`, change panel id 5's gridPos from:

```json
      "gridPos": { "x": 0, "y": 12, "w": 12, "h": 8 },
```

to:

```json
      "gridPos": { "x": 0, "y": 20, "w": 12, "h": 8 },
```

- [ ] **Step 2: Shift panel 6 down to y:20**

Change panel id 6's gridPos from:

```json
      "gridPos": { "x": 12, "y": 12, "w": 12, "h": 8 },
```

to:

```json
      "gridPos": { "x": 12, "y": 20, "w": 12, "h": 8 },
```

- [ ] **Step 3: Insert the new logs panel**

Insert this panel object into the `panels` array immediately after panel id 4's closing `}` and before panel id 5 (so it renders at `y:12`, directly under the sync-errors chart):

```json
    {
      "id": 7,
      "title": "Sync error log",
      "description": "Error messages behind the 'Sync errors / sec' chart above. Requires the Loki datasource (uid 'loki') and the pg-pr OTLP log pipeline (service.name=pg-pr-sync). Per-repo error text is in the expandable 'error_details' field of each WARN record.",
      "type": "logs",
      "gridPos": { "x": 0, "y": 12, "w": 24, "h": 8 },
      "datasource": { "type": "loki", "uid": "loki" },
      "targets": [
        {
          "refId": "A",
          "datasource": { "type": "loki", "uid": "loki" },
          "expr": "{service_name=\"pg-pr-sync\"} | severity_text =~ \"WARN|ERROR\"",
          "queryType": "range",
          "maxLines": 200
        }
      ],
      "options": {
        "showTime": true,
        "showLabels": false,
        "wrapLogMessage": true,
        "prettifyLogMessage": false,
        "enableLogDetails": true,
        "dedupStrategy": "none",
        "sortOrder": "Descending"
      }
    },
```

- [ ] **Step 4: Validate JSON**

Run: `jq -e . phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json > /dev/null && echo OK`
Expected: `OK` (valid JSON; jq is the workspace-preferred tool).

- [ ] **Step 5: Build-validate the observability module**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps && nix flake check`
Expected: PASS (this provisions/validates the dashboards). Per CLAUDE.md this MUST pass.

- [ ] **Step 6: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
git add darwin/modules/observability/dashboards/pg-pr-ops.json
git commit -m "feat(observability): add sync error log panel to pg-pr Ops dashboard"
```

- [ ] **Step 7: Live verification (after the daemon is rebuilt + running)**

Once `phillipg-nix-ziprecruiter` is activated (Task 4 applied) and the new `pg-pr` binary is in place, in Grafana:

1. Open the Loki datasource in Explore, query `{service_name="pg-pr-sync"}` — confirm the daemon's logs appear.
2. **Verify the severity field:** confirm `| severity_text =~ "WARN|ERROR"` filters correctly. If the running Loki exposes OTel severity under a different field (e.g. `detected_level`), update panel id 7's `expr` accordingly and re-commit. If no severity field is filterable, fall back to the bare `{service_name="pg-pr-sync"}` selector.
3. Open the `pg-pr / Ops` dashboard and confirm the "Sync error log" panel renders WARN/ERROR lines below the chart and scrolls within its fixed height.

---

## Task 6: Documentation & ADR

Per each repo's CLAUDE.md "MUST update docs" rule.

**Files:**

- Modify: `phillipgreenii-nix-agent-support/packages/pg-pr/README.md` (telemetry section, if present) and/or `docs/otel-emitter-onboarding.md` reference
- Create: `phillipgreenii-nix-agent-support/docs/adr/NNNN-pg-pr-otlp-logs-via-otelslog.md` (next sequential number) + update `docs/adr/index.md`

- [ ] **Step 1: Document the new capability**

Note in the pg-pr README (or the doc that describes its telemetry surface) that the daemon now emits OTLP logs additively to stderr, under `service.name` from `OTEL_SERVICE_NAME` (the `pg-pr-sync` service sets `pg-pr-sync`), queryable in Loki as `{service_name="pg-pr-sync"}`.

- [ ] **Step 2: Write the ADR**

This establishes a reusable pattern distinct from pa-monitor's raw-record approach. Use the repo's ADR template (Status: Accepted; Date: 2026-06-09; Deciders: phillipg). Decision: slog-native services bridge to OTLP via `go.opentelemetry.io/contrib/bridges/otelslog` + a global LoggerProvider in `telemetry.Init`, fanned out with stderr; chosen over the raw `otellog.Record` API (pa-monitor) because it requires zero log-site changes and preserves slog severity. Cross-reference the spec. Add the row to `docs/adr/index.md`.

- [ ] **Step 3: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/README.md docs/adr/
git commit -m "docs(pg-pr): record OTLP-logs-via-otelslog pattern (ADR + README)"
```

---

## Definition of done

- [ ] `go test ./...` passes in `packages/pg-pr`.
- [ ] `nix build .#pg-pr` succeeds with the refreshed `vendorHash`.
- [ ] `nix flake check` passes in `phillipgreenii-nix-agent-support` and `phillipgreenii-nix-support-apps`; `zn-self-build` succeeds in `phillipg-nix-ziprecruiter`.
- [ ] After activation: the daemon's logs appear in Loki under `{service_name="pg-pr-sync"}`, and the "Sync error log" panel shows WARN/ERROR lines and scrolls.
- [ ] `pg-pr-sync.err` still receives the same stderr stream (OTLP is additive — confirm a tail of the file still shows JSON lines).
- [ ] Severity-field filter verified against live Loki (Task 5 Step 7) or the documented fallback applied.
