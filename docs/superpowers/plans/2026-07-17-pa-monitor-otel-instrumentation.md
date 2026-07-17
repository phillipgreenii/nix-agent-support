# pa-monitor OTel Instrumentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenTelemetry instrumentation to the pa-monitor daemon so poll-loop hotspots, gRPC server latency, OTLP export health, and scan/subprocess workload are observable in Grafana without manual profiling.

**Architecture:** Histograms + counters only (the emitter is metrics+logs, no tracer stack). All new recorders hang off the existing nil-safe `*otel.Emitter`. The poller gets a narrow `PhaseRecorder` interface (Dependency Inversion) so `internal/core/poller` never imports `internal/otel`. gRPC server instrumentation uses `otelgrpc`'s stats handler, given the emitter's `MeterProvider` explicitly (no global providers exist). A Grafana dashboard update surfaces the new series.

**Tech Stack:** Go 1.25, `go.opentelemetry.io/otel` v1.44.0 (metric + sdk/metric + log), `otelgrpc` (new), `google.golang.org/grpc` v1.82.1, gomod2nix, Grafana (raw JSON dashboard), Prometheus.

## Global Constraints

- **Go module:** `github.com/phillipgreenii/pa-monitor`; go 1.25; work inside `packages/pa-monitor/`.
- **OTel core stays at v1.44.0.** Adding `otelgrpc` MUST NOT upgrade `go.opentelemetry.io/otel*` past v1.44.0. If `go mod tidy` bumps core, STOP and report — do not proceed.
- **Dependency changes use gomod2nix** (never vendorHash): after any `go.mod` change run `go mod tidy && nix run github:nix-community/gomod2nix -- generate`, and commit `go.mod` + `go.sum` + `gomod2nix.toml` together.
- **Metric naming:** `pa_monitor.<area>.<name>`; counters end `_total`. Dotted names become underscores in Prometheus.
- **Cardinality:** NO `session_id` on any metric label/attribute. Session-scoped detail stays on log events only.
- **Durations recorded in MILLISECONDS** as `float64` (`float64(d.Microseconds()) / 1000.0`), matching otelgrpc. Default SDK buckets apply except the transcript-scan histogram, which gets one explicit-bucket View with a sub-ms floor.
- **Emitter nil-safety:** every new `*Emitter` method MUST start with `if e == nil { return }` and every new instrument field is nil-checked before use (established pattern, e.g. `emitter.go:475`, `:655`).
- **Typed-nil-interface:** the poller's `Rec PhaseRecorder` field MUST only be assigned when `opts.Emitter != nil` (a nil `*Emitter` stored in an interface is not interface-nil).
- **TDD:** failing test → run (see it fail) → minimal impl → run (pass) → commit. Frequent commits.
- **Commit style:** conventional commits with the bead id suffix, e.g. `feat(otel): add poll-phase histograms (pg2-sewtz)`. No `Refs:` line (the branch carries a beads id, not a Jira ticket).
- **Validation gates (run before declaring done):** `go test ./...` in `packages/pa-monitor`; `prek run --all-files` (or `pre-commit run --all-files`) at repo root; `nix build .#pa-monitor`; `pn workspace flake-check` (Tier 2). E2E metric-landing check is manual (documented in Task 6).

---

## File Structure

| File                                   | Responsibility                                   | Change                                                                                                                                                                            |
| -------------------------------------- | ------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/otel/emitter.go`             | SDK init + all daemon-side instruments/recorders | Modify: new instruments, scan View, `MeterProvider()` accessor, `RecordPhase`/`RecordTickDuration`/`RecordScan`/`RecordSubprocess`; back-patch export instruments into decorators |
| `internal/otel/emitter_test.go`        | Emitter unit tests                               | Modify: registration + recorder tests                                                                                                                                             |
| `internal/otel/health.go`              | OTLP export-health decorators                    | Modify: export-duration histogram + attempts counter, timed in `Export`                                                                                                           |
| `internal/otel/health_test.go`         | Decorator tests                                  | Modify: duration/outcome assertions                                                                                                                                               |
| `internal/core/transcript/snapshot.go` | Transcript scanning                              | Modify: `ScanIncremental` returns `ScanStats{BytesFolded, Mode}`                                                                                                                  |
| `internal/core/transcript/*_test.go`   | Transcript tests                                 | Modify: assert bytes + mode                                                                                                                                                       |
| `internal/core/poller/poller.go`       | Daemon poll snapshot                             | Modify: `Rec PhaseRecorder` field + `PhaseRecorder` interface; time discover/pricer/aggregate/db-write/scan/subprocess                                                            |
| `internal/core/poller/poller_test.go`  | Poller tests                                     | Modify: fake recorder assertions                                                                                                                                                  |
| `internal/daemon/lifecycle.go`         | Daemon tick loop                                 | Modify: wire `poller.Rec`; time tick total + limits/weekly/db_write_block/nudge; thread `MeterProvider` into `serve`                                                              |
| `internal/daemon/server.go`            | gRPC server                                      | Modify: `serve` takes `*sdkmetric.MeterProvider`; add stats handler when non-nil                                                                                                  |
| `internal/daemon/server_test.go`       | Server tests                                     | Modify: handler-present / metric-emitted test                                                                                                                                     |
| `go.mod` / `go.sum` / `gomod2nix.toml` | Deps                                             | Modify: add otelgrpc                                                                                                                                                              |
| `grafana/pa-monitor-overview.json`     | Dashboard                                        | Modify: new rows/panels                                                                                                                                                           |

### Resolved metric taxonomy (removes the v-doc phase/scan/subprocess overlap)

- `pa_monitor.poll.phase.duration{phase}` covers **once-per-tick** phases only:
  `discover`, `pricer`, `aggregate_build`, `db_write_sessions` (poller) and
  `limits`, `weekly`, `db_write_block`, `nudge` (lifecycle). No per-session accumulation.
- Per-**file** transcript work → `pa_monitor.transcript.scan.duration{mode}` +
  `scan.files_total` + `scan.bytes_total`. (`transcript_scan` is intentionally NOT a `phase`.)
- Per-**spawn** subprocess work → `pa_monitor.subprocess.duration{kind}` + `spawns_total{kind}`.
  (`terminal_host`/`subshell`/`git_branch`/`pr_lookup` are NOT `phase`s.)

---

## Task 1: Emitter instruments, scan View, MeterProvider accessor, recorders

**Files:**

- Modify: `internal/otel/emitter.go`
- Test: `internal/otel/emitter_test.go`

**Interfaces:**

- Produces (consumed by Tasks 2, 4, 5):
  - `func (e *Emitter) MeterProvider() *sdkmetric.MeterProvider` (nil-safe → nil)
  - `func (e *Emitter) RecordTickDuration(d time.Duration)`
  - `func (e *Emitter) RecordPhase(phase string, d time.Duration)`
  - `func (e *Emitter) RecordScan(mode string, d time.Duration, bytes int64)`
  - `func (e *Emitter) RecordSubprocess(kind string, d time.Duration)`
  - unexported instrument fields incl. `exportDur metric.Float64Histogram`, `exportAttempts metric.Int64Counter` (Task 5 back-patches these into the decorators)

- [ ] **Step 1: Add a helper for ms conversion and the new instrument fields.**
      In the `Emitter` struct add fields:

```go
	// Instrumentation histograms/counters (pg2-sewtz). Nil when SDK uninitialised.
	pollTickDuration  metric.Float64Histogram
	pollPhaseDuration metric.Float64Histogram
	scanDuration      metric.Float64Histogram
	scanFiles         metric.Int64Counter
	scanBytes         metric.Int64Counter
	subprocessDur     metric.Float64Histogram
	subprocessSpawns  metric.Int64Counter
	exportDur         metric.Float64Histogram
	exportAttempts    metric.Int64Counter
```

- [ ] **Step 2: Write failing tests** in `emitter_test.go`. Use the existing test pattern (set `OTEL_EXPORTER_OTLP_ENDPOINT`, construct via `New`; or exercise recorders on a `nil` emitter for nil-safety). Add:

```go
func TestRecordersAreNilSafe(t *testing.T) {
	var e *Emitter // nil
	// must not panic
	e.RecordTickDuration(time.Second)
	e.RecordPhase("discover", time.Millisecond)
	e.RecordScan("full", time.Millisecond, 1024)
	e.RecordSubprocess("git_branch", time.Millisecond)
	if e.MeterProvider() != nil {
		t.Fatal("nil emitter MeterProvider must be nil")
	}
}

func TestNewRegistersInstrumentsAndMeterProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	e, err := New(context.Background(), Options{ServiceName: "pa-monitor"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = e.Shutdown(context.Background()) }()
	if e.MeterProvider() == nil {
		t.Fatal("MeterProvider must be non-nil when enabled")
	}
	// recorders must not panic against a live emitter
	e.RecordPhase("discover", 2*time.Millisecond)
	e.RecordScan("incremental", time.Millisecond, 512)
	e.RecordSubprocess("terminal_host", 3*time.Millisecond)
	e.RecordTickDuration(10 * time.Millisecond)
}
```

- [ ] **Step 3: Run tests to confirm failure.**
      Run: `go test ./internal/otel/ -run 'TestRecorders|TestNewRegisters' -v`
      Expected: FAIL (undefined methods).

- [ ] **Step 4: Register the instruments** in `registerMetrics` (after the existing counters, before `RegisterCallback`):

```go
	if e.pollTickDuration, err = meter.Float64Histogram("pa_monitor.poll.tick.duration",
		metric.WithUnit("ms"), metric.WithDescription("total poll-tick duration")); err != nil {
		return err
	}
	if e.pollPhaseDuration, err = meter.Float64Histogram("pa_monitor.poll.phase.duration",
		metric.WithUnit("ms"), metric.WithDescription("per-phase poll duration")); err != nil {
		return err
	}
	if e.scanDuration, err = meter.Float64Histogram("pa_monitor.transcript.scan.duration",
		metric.WithUnit("ms"), metric.WithDescription("per-file transcript scan duration")); err != nil {
		return err
	}
	if e.scanFiles, err = meter.Int64Counter("pa_monitor.transcript.scan.files_total"); err != nil {
		return err
	}
	if e.scanBytes, err = meter.Int64Counter("pa_monitor.transcript.scan.bytes_total"); err != nil {
		return err
	}
	if e.subprocessDur, err = meter.Float64Histogram("pa_monitor.subprocess.duration",
		metric.WithUnit("ms"), metric.WithDescription("subprocess spawn duration")); err != nil {
		return err
	}
	if e.subprocessSpawns, err = meter.Int64Counter("pa_monitor.subprocess.spawns_total"); err != nil {
		return err
	}
	if e.exportDur, err = meter.Float64Histogram("pa_monitor.otel.export.duration",
		metric.WithUnit("ms"), metric.WithDescription("OTLP export call duration")); err != nil {
		return err
	}
	if e.exportAttempts, err = meter.Int64Counter("pa_monitor.otel.export.attempts_total"); err != nil {
		return err
	}
```

- [ ] **Step 5: Add the scan-duration View** at `NewMeterProvider` in `New` (transcript scans are µs–ms per pg2-ksh27; default ms buckets would collapse them):

```go
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			&healthMetricExporter{Exporter: metricExp, health: health, out: os.Stderr},
		)),
		sdkmetric.WithResource(res),
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: "pa_monitor.transcript.scan.duration"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 250},
			}},
		)),
	)
```

(Task 5 will refactor the `healthMetricExporter{...}` literal into a named `metricDec` variable — leave it inline for now.)

- [ ] **Step 6: Add the accessor + recorder methods** at the end of `emitter.go`:

```go
// MeterProvider exposes the SDK meter provider so callers (e.g. the gRPC stats
// handler) can attach instrumentation to the same provider. nil-safe → nil.
func (e *Emitter) MeterProvider() *sdkmetric.MeterProvider {
	if e == nil {
		return nil
	}
	return e.metricsProvider
}

func durMS(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// RecordTickDuration records the total poll-tick duration. nil-safe.
func (e *Emitter) RecordTickDuration(d time.Duration) {
	if e == nil || e.pollTickDuration == nil {
		return
	}
	e.pollTickDuration.Record(context.Background(), durMS(d))
}

// RecordPhase records one once-per-tick phase duration under a `phase` attr. nil-safe.
func (e *Emitter) RecordPhase(phase string, d time.Duration) {
	if e == nil || e.pollPhaseDuration == nil {
		return
	}
	e.pollPhaseDuration.Record(context.Background(), durMS(d),
		metric.WithAttributes(attribute.String("phase", phase)))
}

// RecordScan records one transcript scan: duration under `mode`, and (for real
// scans, not cache hits) the files_total + bytes_total workload counters. nil-safe.
func (e *Emitter) RecordScan(mode string, d time.Duration, bytes int64) {
	if e == nil || e.scanDuration == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("mode", mode))
	e.scanDuration.Record(context.Background(), durMS(d), attrs)
	if mode != "cache_hit" {
		e.scanFiles.Add(context.Background(), 1, attrs)
		e.scanBytes.Add(context.Background(), bytes, attrs)
	}
}

// RecordSubprocess records one subprocess spawn duration + count under `kind`. nil-safe.
func (e *Emitter) RecordSubprocess(kind string, d time.Duration) {
	if e == nil || e.subprocessDur == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("kind", kind))
	e.subprocessDur.Record(context.Background(), durMS(d), attrs)
	e.subprocessSpawns.Add(context.Background(), 1, attrs)
}
```

- [ ] **Step 7: Run tests to confirm pass.**
      Run: `go test ./internal/otel/ -run 'TestRecorders|TestNewRegisters' -v` then `go test ./internal/otel/`
      Expected: PASS (all otel tests).

- [ ] **Step 8: Commit.**

```bash
git add internal/otel/emitter.go internal/otel/emitter_test.go
git commit -m "feat(otel): add poll/scan/subprocess instruments + MeterProvider accessor (pg2-sewtz)"
```

---

## Task 2: gRPC server stats handler (+ otelgrpc dependency)

**Files:**

- Modify: `internal/daemon/server.go` (`serve`, `:469-470`), `internal/daemon/lifecycle.go` (`serve` call `:330`)
- Modify: `go.mod`, `go.sum`, `gomod2nix.toml`
- Test: `internal/daemon/server_test.go`

**Interfaces:**

- Consumes: `(*otel.Emitter).MeterProvider()` from Task 1.
- Produces: `serve(...)` gains a trailing `mp *sdkmetric.MeterProvider` parameter.

- [ ] **Step 1: Add the otelgrpc dependency and verify no core upgrade.**

```bash
go get go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc
go mod tidy
grep 'go.opentelemetry.io/otel ' go.mod   # MUST still show v1.44.0
```

If `go.opentelemetry.io/otel` is no longer `v1.44.0`, STOP and report (Global Constraints). Otherwise regenerate the nix lock:

```bash
nix run github:nix-community/gomod2nix -- generate
```

- [ ] **Step 2: Write a failing test** in `server_test.go` that asserts a server built with a meter provider exposes gRPC server metrics. The lightest reliable assertion is that `serve` accepts a provider and a driven RPC records `rpc.server.duration`. If wiring a full metric-reader assertion is heavy, assert the narrower contract: `serve(..., mp)` with a real provider starts and answers an RPC without error, and `serve(..., nil)` also starts (nil path). Example nil-path guard test:

```go
func TestServeNilMeterProviderStarts(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	st := newSharedState()
	_, stop := serve(lis, st, "test", "", "", nil, nil, time.Second, nil, nil, nil)
	defer stop()
	// dial + a trivial RPC must succeed (handler installed conditionally, no panic)
	// ... existing test helpers for dialing the daemon ...
}
```

(Match the real `serve` signature and the repo's existing server-test dialing helpers — read `server_test.go` first.)

- [ ] **Step 3: Run test to confirm failure.**
      Run: `go test ./internal/daemon/ -run TestServeNilMeterProvider -v`
      Expected: FAIL (arg count mismatch — `serve` has no `mp` param yet).

- [ ] **Step 4: Add the `mp` parameter and conditional stats handler** in `server.go`:

```go
import (
	// ...
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func serve(lis net.Listener, state *sharedState, version, planTier, autoResumeMessage string, writeService *service.WriteService, bridges *bridge.Registry, snapshotInterval time.Duration, onDeliverResult func(id string, ok bool, errStr, reason string, timedOut bool), onStreamClosed func(serverPID int), mp *sdkmetric.MeterProvider) (*grpc.Server, func()) {
	var gsOpts []grpc.ServerOption
	if mp != nil {
		gsOpts = append(gsOpts, grpc.StatsHandler(otelgrpc.NewServerHandler(
			otelgrpc.WithMeterProvider(mp),
			otelgrpc.WithTracerProvider(tracenoop.NewTracerProvider()),
		)))
	}
	gs := grpc.NewServer(gsOpts...)
	// ... rest unchanged ...
}
```

- [ ] **Step 5: Thread the provider from the daemon loop** in `lifecycle.go:330`:

```go
	_, stop := serve(lis, state, version, opts.PlanTier, opts.AutoResumeMessage, opts.WriteService, bridgeReg, snapshotInterval, tr.resolve, tr.failServer, opts.Emitter.MeterProvider())
```

(`opts.Emitter.MeterProvider()` is nil-safe; when OTEL is off it returns nil and no handler is installed.)

- [ ] **Step 6: Run tests.**
      Run: `go test ./internal/daemon/ ./internal/otel/`
      Expected: PASS.

- [ ] **Step 7: Capture the ACTUAL emitted metric name + unit** (version-dependent — do NOT assume). The resolved otelgrpc version determines whether the histogram is `rpc.server.duration` (ms, older semconv) or `rpc.server.request.duration` (seconds, semconv ≥1.24), and whether the status attr is `rpc.grpc.status_code`. Determine it from the resolved package source:

```bash
otelgrpc_dir=$(go list -m -f '{{.Dir}}' go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc)
grep -rn 'rpc.server\|Duration\|WithUnit\|status_code' "$otelgrpc_dir"/*.go | grep -i 'name\|duration\|unit\|status' | head
```

Record the exact Prometheus series name (dots→underscores), unit, and status-code attribute in a note at the top of Task 6 — the Grafana panels MUST use these, not the placeholders.

- [ ] **Step 8: Verify the nix build picks up the new dep.**
      Run: `nix build .#pa-monitor` (from repo root)
      Expected: builds (proves gomod2nix.toml is correct).

- [ ] **Step 9: Commit.**

```bash
git add internal/daemon/server.go internal/daemon/server_test.go internal/daemon/lifecycle.go go.mod go.sum gomod2nix.toml
git commit -m "feat(otel): instrument gRPC server via otelgrpc stats handler (pg2-sewtz)"
```

---

## Task 3: Transcript scan returns bytes + mode

**Files:**

- Modify: `internal/core/transcript/snapshot.go` (`ScanIncremental` `:349`, `Scan` `:338`)
- Test (existing callers to update — `snapshot_test.go` does NOT call `ScanIncremental`):
  `internal/core/transcript/incremental_test.go` (~18 call sites) and
  `internal/core/transcript/bench_test.go` (`:58`, and `:65` which uses the reuse form
  `if _, acc, err = ScanIncremental(path, acc)` → must become `if _, acc, _, err = ...`).
- Modify callers: `internal/core/poller/poller.go:164` (update the call to compile here; Task 4 adds recording).

**Interfaces:**

- Produces (consumed by Task 4):

```go
type ScanMode string
const (
	ScanModeFull        ScanMode = "full"
	ScanModeIncremental ScanMode = "incremental"
)
type ScanStats struct {
	BytesFolded int64
	Mode        ScanMode
}
func ScanIncremental(path string, prev *Accumulator) (Snapshot, *Accumulator, ScanStats, error)
```

- [ ] **Step 1: Write failing tests** asserting bytes + mode. Example:

```go
func TestScanIncrementalReportsModeAndBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	line := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil { t.Fatal(err) }

	_, acc, stats, err := ScanIncremental(p, nil)
	if err != nil { t.Fatal(err) }
	if stats.Mode != ScanModeFull { t.Fatalf("first scan mode=%q want full", stats.Mode) }
	if stats.BytesFolded != int64(len(line)) { t.Fatalf("bytes=%d want %d", stats.BytesFolded, len(line)) }

	// append one more line, incremental fold
	line2 := `{"type":"assistant","message":{"role":"assistant","content":"yo"}}` + "\n"
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(line2); _ = f.Close()

	_, _, stats2, err := ScanIncremental(p, acc)
	if err != nil { t.Fatal(err) }
	if stats2.Mode != ScanModeIncremental { t.Fatalf("second scan mode=%q want incremental", stats2.Mode) }
	if stats2.BytesFolded != int64(len(line2)) { t.Fatalf("bytes=%d want %d", stats2.BytesFolded, len(line2)) }
}
```

- [ ] **Step 2: Run to confirm failure.**
      Run: `go test ./internal/core/transcript/ -run TestScanIncrementalReportsModeAndBytes -v`
      Expected: FAIL (compile error — 3 return values).

- [ ] **Step 3: Change the signature** in `snapshot.go`. Add the types above near `Accumulator`. In `ScanIncremental`, capture mode from `fresh` and bytes from `n`:

```go
func ScanIncremental(path string, prev *Accumulator) (Snapshot, *Accumulator, ScanStats, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, newAccumulator(), ScanStats{Mode: ScanModeFull}, nil
		}
		return Snapshot{}, nil, ScanStats{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return Snapshot{}, nil, ScanStats{}, err
	}

	acc := prev
	fresh := acc == nil || !acc.ready
	if !fresh {
		switch {
		case acc.info == nil || !os.SameFile(acc.info, info):
			fresh = true
		case info.Size() < acc.offset:
			fresh = true
		case acc.offset > 0 && !newlineAt(f, acc.offset-1):
			fresh = true
		}
	}
	if fresh {
		acc = newAccumulator()
	}
	if _, err := f.Seek(acc.offset, io.SeekStart); err != nil {
		return Snapshot{}, nil, ScanStats{}, err
	}
	n, err := acc.st.foldReader(f)
	if err != nil {
		return Snapshot{}, nil, ScanStats{}, err
	}
	acc.offset += n
	acc.info = info
	acc.ready = true
	// ... existing finalize/return of the Snapshot ...
	mode := ScanModeIncremental
	if fresh {
		mode = ScanModeFull
	}
	return <snapshot>, acc, ScanStats{BytesFolded: n, Mode: mode}, nil
}
```

(Preserve the existing snapshot-finalize logic below `acc.ready = true`; only the return tuple changes. Read the current tail of the function `:387-410` and thread `ScanStats` through every return.)

- [ ] **Step 4: Update `Scan`** (`:338`):

```go
func Scan(path string) (Snapshot, error) {
	snap, _, _, err := ScanIncremental(path, nil)
	return snap, err
}
```

- [ ] **Step 5: Update ALL callers so every package compiles.** Run `grep -rn "ScanIncremental" --include=*.go .` first and update each site to the 4-value form:
  - `internal/core/poller/poller.go:164` → `snap, acc, _, _ = transcript.ScanIncremental(path, prevAcc)` (Task 4 replaces the discards with recording).
  - `internal/core/transcript/incremental_test.go` — ~18 call sites; add a `_` for the new `ScanStats` return.
  - `internal/core/transcript/bench_test.go:58` and `:65` — note `:65` uses `=` reuse: `if _, acc, _, err = ScanIncremental(path, acc); err != nil`.
    Do NOT edit `snapshot_test.go` (it does not call `ScanIncremental`).

- [ ] **Step 6: Compile + run tests.**
      Run: `go build ./... && go test ./internal/core/transcript/ ./internal/core/poller/`
      Expected: builds, PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/core/transcript/ internal/core/poller/poller.go
git commit -m "feat(transcript): ScanIncremental reports bytes folded + full/incremental mode (pg2-sewtz)"
```

---

## Task 4: Phase timing + workload recording in poller & lifecycle

**Files:**

- Modify: `internal/core/poller/poller.go` (add `PhaseRecorder` + `Rec` field; time phases/scan/subprocess)
- Test: `internal/core/poller/poller_test.go`
- Modify: `internal/daemon/lifecycle.go` (wire `poller.Rec`; time tick + lifecycle phases)

**Interfaces:**

- Consumes: `RecordPhase`/`RecordScan`/`RecordSubprocess`/`RecordTickDuration` (Task 1); `transcript.ScanStats` (Task 3).
- Produces (poller package):

```go
type PhaseRecorder interface {
	RecordPhase(phase string, d time.Duration)
	RecordScan(mode string, d time.Duration, bytes int64)
	RecordSubprocess(kind string, d time.Duration)
}
```

`*otel.Emitter` satisfies it structurally.

- [ ] **Step 1: Write failing test** with a fake recorder in `poller_test.go`:

```go
type fakeRec struct {
	phases   map[string]int
	scans    map[string]int
	spawns   map[string]int
}
func newFakeRec() *fakeRec { return &fakeRec{map[string]int{}, map[string]int{}, map[string]int{}} }
func (f *fakeRec) RecordPhase(p string, _ time.Duration)          { f.phases[p]++ }
func (f *fakeRec) RecordScan(m string, _ time.Duration, _ int64)  { f.scans[m]++ }
func (f *fakeRec) RecordSubprocess(k string, _ time.Duration)     { f.spawns[k]++ }

func TestSnapshotRecordsPhases(t *testing.T) {
	// build a Poller over a temp SessionsDir with >=1 discoverable session
	// (reuse the existing poller_test harness/fixtures), set p.Rec = newFakeRec()
	rec := newFakeRec()
	p := newTestPoller(t) // existing helper; adapt
	p.Rec = rec
	_, _, err := p.Snapshot(context.Background())
	if err != nil { t.Fatal(err) }
	if rec.phases["discover"] == 0 { t.Error("discover phase not recorded") }
	if rec.phases["aggregate_build"] == 0 { t.Error("aggregate_build phase not recorded") }
}
```

(Read `poller_test.go` for the real fixture/constructor; adapt `newTestPoller`.)

- [ ] **Step 2: Run to confirm failure.**
      Run: `go test ./internal/core/poller/ -run TestSnapshotRecordsPhases -v`
      Expected: FAIL (no `Rec` field).

- [ ] **Step 3: Add the interface + field** in `poller.go` (interface near the top after imports; field in the `Poller` struct):

```go
// PhaseRecorder receives per-tick phase/scan/subprocess timings. *otel.Emitter
// satisfies it; nil disables recording. Defined here (not imported from otel) so
// internal/core/poller has no dependency on internal/otel.
type PhaseRecorder interface {
	RecordPhase(phase string, d time.Duration)
	RecordScan(mode string, d time.Duration, bytes int64)
	RecordSubprocess(kind string, d time.Duration)
}
```

Add `Rec PhaseRecorder` to the `Poller` struct with a doc comment. Add a nil-safe helper so call sites stay terse:

```go
func (p *Poller) phase(name string, start time.Time) {
	if p.Rec != nil {
		p.Rec.RecordPhase(name, time.Since(start))
	}
}
```

- [ ] **Step 4: Time the once-per-tick phases + per-file scan + subprocess** in `Snapshot`:
  - discover (`:121`): `t0 := now; sessions, err := disc.Discover(); p.phase("discover", t0)` (record even on error, before the return).
  - git_branch (`:145`): wrap `session.GitBranch(s.Cwd)` — time it and `if p.Rec != nil { p.Rec.RecordSubprocess("git_branch", elapsed) }`.
  - transcript scan (`:155-171`): on the **cache-hit** branch record `RecordScan("cache_hit", 0, 0)`; on the **miss** branch time `ScanIncremental`, then `if p.Rec != nil { p.Rec.RecordScan(string(stats.Mode), scanElapsed, stats.BytesFolded) }` (capture `stats` from Task 3's return).
  - subshell count (`:165`, miss branch): time `subshellCounter.Count(s.PID)` → `RecordSubprocess("subshell", elapsed)`.
  - terminal_host (`:189`, miss branch only, i.e. the `else` that calls `detectTerminalHost`): time → `RecordSubprocess("terminal_host", elapsed)`.
  - pr_lookup (`:329`): time each `p.PRLookupFn(...)` → `RecordSubprocess("pr_lookup", elapsed)`.
  - pricer (`:355-358`): wrap the `p.Pricer.ActiveBlock`/`Probed` block → `p.phase("pricer", t0)`.
  - aggregate_build (`:360`): `t0 := now; tree := aggregate.Build(...); p.phase("aggregate_build", t0)`.
  - db_write_sessions (`:364-430`): `t0 := now` before the `if p.WriteService != nil` block, `p.phase("db_write_sessions", t0)` after it.

  Use `time.Now()` for timing (real wall clock; `p.Now()` is the injectable logical clock used for burn-rate math — do NOT reuse it for durations). Example scan-miss recording:

```go
			scanStart := time.Now()
			var acc *transcript.Accumulator
			var stats transcript.ScanStats
			snap, acc, stats, _ = transcript.ScanIncremental(path, prevAcc)
			if p.Rec != nil {
				p.Rec.RecordScan(string(stats.Mode), time.Since(scanStart), stats.BytesFolded)
			}
			shells, _ = subshellCounter.Count(s.PID) // wrap with subprocess timing per above
```

- [ ] **Step 5: Wire `poller.Rec` in the daemon WITHOUT importing `core/poller`.** `lifecycle.go` deliberately talks to the poller only through interfaces (`SessionLabeler`, `BlockWeekIDSetter` at `:122-146,457`) and does NOT import `internal/core/poller`. A `opts.Poller.(*poller.Poller)` assertion — and even a `PhaseRecorder`-typed setter assertion — would force that import (Go interface satisfaction needs the exact named type). Keep the boundary with an `any`-typed setter.
      In `poller.go`, add:

```go
// SetPhaseRecorder wires a PhaseRecorder. Takes any so the daemon can call it
// through an anonymous interface without importing this package.
func (p *Poller) SetPhaseRecorder(r any) {
	if pr, ok := r.(PhaseRecorder); ok {
		p.Rec = pr
	}
}
```

In `lifecycle.go`, near the `SetLabeler` block (`:457`):

```go
	if opts.Emitter != nil {
		if s, ok := opts.Poller.(interface{ SetPhaseRecorder(any) }); ok {
			s.SetPhaseRecorder(opts.Emitter)
		}
	}
```

(Guarded by `opts.Emitter != nil` — the typed-nil-interface rule; `*otel.Emitter` satisfies `poller.PhaseRecorder` structurally inside the setter.)

- [ ] **Step 6: Time the tick total + lifecycle phases** in `RunWith`'s `case <-t.C:` arm:
  - Total: capture at the top of the arm and record via a closure so the early `continue` paths (no-poller `:508`, snapshot-error `:512`) are still counted:

```go
			tickStart := time.Now()
			func() {
				defer func() { opts.Emitter.RecordTickDuration(time.Since(tickStart)) }()
				// ... existing tick body moved inside, OR keep the arm as-is and
				// place the RecordTickDuration on each exit path. Simplest: wrap the
				// arm body in an inline func with the defer. Emitter is nil-safe.
			}()
```

(If wrapping the whole arm body is disruptive, instead add `defer`-style recording by extracting the tick body into a `doTick()` helper. Either way, error/no-poller ticks MUST record.)

- limits (`:520-524`): wrap → `opts.Emitter.RecordPhase("limits", elapsed)`.
- weekly (`:525-530`): wrap the `fetchWeek` block.
- db_write_block (`:535-556`): wrap the block/week persist.
- nudge (`:729-804`): wrap the nudger section.
  Add a tiny local helper in `RunWith`:

```go
	phase := func(name string, start time.Time) { opts.Emitter.RecordPhase(name, time.Since(start)) }
```

- [ ] **Step 7: Run tests.**
      Run: `go test ./internal/core/poller/ ./internal/daemon/ ./internal/otel/`
      Expected: PASS.

- [ ] **Step 8: Commit.**

```bash
git add internal/core/poller/ internal/daemon/lifecycle.go
git commit -m "feat(otel): record poll-tick/phase/scan/subprocess timings (pg2-sewtz)"
```

---

## Task 5: OTLP export-health metrics

**Files:**

- Modify: `internal/otel/health.go` (decorator structs + `Export`), `internal/otel/emitter.go` (back-patch instruments after `registerMetrics`)
- Test: `internal/otel/health_test.go`

**Interfaces:**

- Consumes: `e.exportDur`, `e.exportAttempts` (Task 1).

- [ ] **Step 1: Write failing tests** in `health_test.go` asserting the decorator records duration + outcome. Use a fake embedded exporter that returns success then failure, and an instrument spy. The simplest reliable spy is a real meter provider with a manual reader; but if that is heavy, assert the smaller contract: the decorator holds non-nil instruments after `New`, and `Export` still returns nil (swallow) and still writes the health line. Example structural test:

```go
func TestExportDecoratorRecordsWhenInstrumentsSet(t *testing.T) {
	// build a metric decorator with stub instruments and a stub exporter that errors;
	// assert Export swallows the error (returns nil) and the health line is written,
	// and that recording does not panic when instruments are nil (disabled path).
}
```

(Read `health_test.go` for the existing fake-exporter helpers and reuse them.)

- [ ] **Step 2: Run to confirm failure.**
      Run: `go test ./internal/otel/ -run TestExportDecorator -v`
      Expected: FAIL.

- [ ] **Step 3: Add instrument fields + timing** in `health.go`:

```go
type healthMetricExporter struct {
	sdkmetric.Exporter
	health   *exportHealth
	out      io.Writer
	dur      metric.Float64Histogram // nil until back-patched
	attempts metric.Int64Counter
}

func (e *healthMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	start := time.Now()
	err := e.Exporter.Export(ctx, rm)
	recordExport(e.dur, e.attempts, "metric", time.Since(start), err)
	e.health.record(err, e.out)
	return nil
}
```

Add the log counterpart fields to `healthLogExporter` and time its `Export` with `signal="log"`. Add the shared helper:

```go
func recordExport(dur metric.Float64Histogram, attempts metric.Int64Counter, signal string, d time.Duration, err error) {
	if dur == nil || attempts == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	dur.Record(context.Background(), float64(d.Microseconds())/1000.0,
		metric.WithAttributes(attribute.String("signal", signal)))
	attempts.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("signal", signal), attribute.String("outcome", outcome)))
}
```

Add imports: `metric "go.opentelemetry.io/otel/metric"`, `attribute "..."`, `time`.

- [ ] **Step 4: Back-patch the instruments** in `New` (`emitter.go`). Refactor the inline decorator literals into named vars, then assign after `registerMetrics`:

```go
	metricDec := &healthMetricExporter{Exporter: metricExp, health: health, out: os.Stderr}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricDec)),
		sdkmetric.WithResource(res),
		sdkmetric.WithView( /* scan view from Task 1 */ ),
	)
	// ...
	logDec := &healthLogExporter{Exporter: logExp, health: health, out: os.Stderr}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logDec)), sdklog.WithResource(res))
	// ... build e, then:
	if err := e.registerMetrics(mp); err != nil { /* shutdown */ return nil, err }
	metricDec.dur, metricDec.attempts = e.exportDur, e.exportAttempts
	logDec.dur, logDec.attempts = e.exportDur, e.exportAttempts
	return e, nil
```

- [ ] **Step 5: Run tests.**
      Run: `go test ./internal/otel/`
      Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/otel/health.go internal/otel/health_test.go internal/otel/emitter.go
git commit -m "feat(otel): record OTLP export duration + success/failure attempts (pg2-sewtz)"
```

---

## Task 6: Grafana dashboard + validation gates

**Files:**

- Modify: `grafana/pa-monitor-overview.json`

- [ ] **Step 1: Read the current dashboard structure.** Note: raw Grafana JSON, `schemaVersion 39`, `uid pa-monitor-overview`, prometheus datasource uid `prometheus`, existing rows use `gridPos` + `targets:[{expr, legendFormat, refId}]`. Pick the next free `id` and place new rows at the bottom (increment `gridPos.y`).

- [ ] **Step 2: Add three rows of panels** (dotted→underscore metric names; new series are account-global — no `$scope`/`$plan_tier`). Panels:
  - **Poll performance**: `histogram_quantile(0.95, sum by (le) (rate(pa_monitor_poll_tick_duration_bucket[5m])))` (tick p95, ms); `histogram_quantile(0.95, sum by (le,phase) (rate(pa_monitor_poll_phase_duration_bucket[5m])))` legend `{{phase}}`; `histogram_quantile(0.95, sum by (le,mode) (rate(pa_monitor_transcript_scan_duration_bucket[5m])))` legend `{{mode}}`; `rate(pa_monitor_transcript_scan_files_total[5m])` and `rate(pa_monitor_transcript_scan_bytes_total[5m])`; subprocess `histogram_quantile(0.95, sum by (le,kind) (rate(pa_monitor_subprocess_duration_bucket[5m])))` + `rate(pa_monitor_subprocess_spawns_total[5m])`.
  - **gRPC (server)**: use the EXACT series name/unit/status-attr captured in Task 2 Step 7 — do NOT assume `rpc_server_duration`. If it resolved to `rpc_server_duration` (ms): `histogram_quantile(0.95, sum by (le,rpc_method) (rate(rpc_server_duration_bucket[5m])))` and `sum by (rpc_grpc_status_code) (rate(rpc_server_duration_count[5m]))`. If it resolved to `rpc_server_request_duration` (seconds): substitute that name and label the panel seconds. Match `rpc_method`/`rpc_grpc_status_code` to the captured attribute names.
  - **Export health**: `histogram_quantile(0.95, sum by (le,signal) (rate(pa_monitor_otel_export_duration_bucket[5m])))`; `sum by (outcome) (rate(pa_monitor_otel_export_attempts_total[5m]))`. Add a note panel: these surface _intermittent_ failures; a total outage shows as a scrape gap + the stderr UNHEALTHY line.

- [ ] **Step 3: Validate the JSON parses.**
      Run: `jq -e '.panels | length' grafana/pa-monitor-overview.json`
      Expected: prints the new panel count (no jq error).

- [ ] **Step 4: Commit.**

```bash
git add grafana/pa-monitor-overview.json
git commit -m "feat(grafana): pa-monitor OTel poll/gRPC/export panels (pg2-sewtz)"
```

- [ ] **Step 5: Full validation gates** (repo root):

```bash
cd packages/pa-monitor && go test ./... && cd -
prek run --all-files || pre-commit run --all-files
nix build .#pa-monitor
pn workspace flake-check
```

Expected: all pass. If any fail, fix in place and re-run that gate.

- [ ] **Step 6: (Manual, documented — not blocking the plan) E2E metric-landing.** With `OTEL_EXPORTER_OTLP_ENDPOINT` pointed at the local collector, run the freshly built daemon, drive any RPC (e.g. a `pa-monitor` control command / TUI connect) to exercise the server handler, and confirm in Prometheus that `pa_monitor_poll_tick_duration_bucket`, `pa_monitor_poll_phase_duration_bucket`, `rpc_server_duration_bucket`, `pa_monitor_otel_export_attempts_total`, and the scan counters appear, and the new Grafana panels render.

---

## Self-Review (author checklist — completed)

- **Spec coverage:** loop/phase histograms → Tasks 1,4. gRPC server + status → Task 2. export health → Tasks 1,5. workload (files/bytes/subprocess) → Tasks 1,3,4. Grafana → Task 6. gates → Task 6. Client gRPC + `otel.export` tick-phase deliberately dropped (design sign-off D3/D6). ✔
- **Placeholders:** none — every code step has concrete code; test steps that reuse existing fixtures say to read the specific test file first. ✔
- **Type consistency:** `PhaseRecorder`/`RecordPhase`/`RecordScan`/`RecordSubprocess`/`RecordTickDuration` signatures identical across Tasks 1 & 4; `ScanStats{BytesFolded int64, Mode ScanMode}` identical in Tasks 3 & 4; `serve(...)` gains exactly one `mp *sdkmetric.MeterProvider` param in Task 2. ✔
