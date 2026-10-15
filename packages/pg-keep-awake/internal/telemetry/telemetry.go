// Package telemetry is the keeper's OTel emitter (pg-keep-awake design section
// 12). The keeper is the only emitter: the shim and the CLI never initialise
// OTel.
//
// The emitter is an interface (Emitter) with two implementations: the real
// one, built by New when an endpoint is configured, and Noop, returned when it
// is not. Callers hold an Emitter and never branch on whether OTel is on.
//
// Export is non-blocking by construction: the SDK's periodic reader and batch
// log processor ship data from their own goroutines, so a recording call only
// updates in-memory aggregation and a collector refusal cannot stall the
// keeper's tick. Metric names are dotted and become underscored series, as in
// the pa-monitor emitter this package is derived from (see resource.go and
// health.go for the copied pieces).
package telemetry

import (
	"context"
	"os"
	"sort"
	"strings"
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

// MaxOwners is the number of distinct owner label values kept; every owner
// beyond it is labelled OtherOwner.
const MaxOwners = 16

// OtherOwner is the label value for owners beyond MaxOwners.
const OtherOwner = "other"

// exportInterval is how often the periodic reader pushes metrics.
const exportInterval = 15 * time.Second

// Fixed label value sets, so no counter label can grow without bound.
var (
	// GateStates are the values of the gate.state gauge's state label.
	GateStates = []string{"ok", "no_spawn", "released", "unknown", "disabled"}
	// ReapReasons are the values of leases.reaped_total's reason label.
	ReapReasons = []string{"expired", "client_dead", "watched_dead", "orphan"}
	// RejectReasons are the values of leases.rejected_total's reason label.
	RejectReasons = []string{"unreadable", "too_large", "over_cap"}
	// BatteryReleaseReasons are the values of battery.releases_total's reason label.
	BatteryReleaseReasons = []string{"release_threshold", "blind_threshold", "spawn_blocked", "watchdog"}
	// AnchorExitReasons are the values of anchor.exits_total's reason label.
	AnchorExitReasons = []string{"release_threshold", "blind_threshold", "watchdog", "unknown"}
)

// Emitter records the keeper's metrics and structured log events. All methods
// are safe for concurrent use and never block on the collector.
type Emitter interface {
	// RecordKeeperUp sets keeper.up (1 when up).
	RecordKeeperUp(up bool)
	// RecordTick records one keeper tick: tick.duration and
	// keeper.last_tick_timestamp_seconds.
	RecordTick(d time.Duration, at time.Time)
	// RecordLeasesLive replaces the live-lease gauge: owner -> live count.
	// Owners beyond MaxOwners are summed under OtherOwner; an owner seen
	// earlier but absent now is reset to 0.
	RecordLeasesLive(byOwner map[string]int)
	// IncLeasesReaped counts reaped leases by reason (ReapReasons).
	IncLeasesReaped(reason string)
	// IncLeasesRejected counts rejected lease files by reason (RejectReasons).
	IncLeasesRejected(reason string)
	// RecordAssertion sets assertion.held and the per-flag assertion.flags
	// gauge (1 per held flag, 0 for a flag that was held and no longer is).
	RecordAssertion(held bool, flags []string)
	// RecordGateState sets gate.state: 1 for state, 0 for every other state.
	RecordGateState(state string)
	// RecordBattery sets battery.percent and battery.on_battery.
	RecordBattery(percent int, onBattery bool)
	// RecordReleasePct sets battery.release_pct, the configured release level.
	RecordReleasePct(pct int)
	// IncBatteryRelease counts battery-driven releases by reason
	// (BatteryReleaseReasons).
	IncBatteryRelease(reason string)
	// IncAnchorExit counts anchor exits by reason (AnchorExitReasons).
	IncAnchorExit(reason string)

	// LogLeaseCreated logs a lease creation.
	LogLeaseCreated(id, owner string)
	// LogLeaseReaped logs a lease reap with its reason.
	LogLeaseReaped(id, owner, reason string)
	// LogAssertionSpawned logs a new caffeinate assertion.
	LogAssertionSpawned(pid int)
	// LogAssertionAdopted logs that the keeper adopted an existing assertion.
	LogAssertionAdopted(pid int)
	// LogAssertionReleased logs an assertion release with its reason.
	LogAssertionReleased(reason string)
	// LogGateTransition logs a gate state change.
	LogGateTransition(from, to string)
	// LogConfigError logs a configuration error.
	LogConfigError(err error)
	// LogAnchorExit logs an anchor exit as read from anchor-exit.json (the
	// anchor is autonomous, so the keeper reports it after the fact).
	LogAnchorExit(reason string, percent, pid int, at time.Time)

	// Close flushes and shuts the exporters down, bounded by ctx.
	Close(ctx context.Context) error
}

// Options configures New.
type Options struct {
	// Endpoint is the OTLP gRPC collector. Either host:port (insecure) or a
	// URL such as http://localhost:4317. Empty selects the no-op emitter.
	Endpoint       string
	ServiceName    string
	ServiceVersion string
}

// Noop is the emitter used when no endpoint is configured. Every method does
// nothing.
type Noop struct{}

var _ Emitter = Noop{}

func (Noop) RecordKeeperUp(bool)                       {}
func (Noop) RecordTick(time.Duration, time.Time)       {}
func (Noop) RecordLeasesLive(map[string]int)           {}
func (Noop) IncLeasesReaped(string)                    {}
func (Noop) IncLeasesRejected(string)                  {}
func (Noop) RecordAssertion(bool, []string)            {}
func (Noop) RecordGateState(string)                    {}
func (Noop) RecordBattery(int, bool)                   {}
func (Noop) RecordReleasePct(int)                      {}
func (Noop) IncBatteryRelease(string)                  {}
func (Noop) IncAnchorExit(string)                      {}
func (Noop) LogLeaseCreated(string, string)            {}
func (Noop) LogLeaseReaped(string, string, string)     {}
func (Noop) LogAssertionSpawned(int)                   {}
func (Noop) LogAssertionAdopted(int)                   {}
func (Noop) LogAssertionReleased(string)               {}
func (Noop) LogGateTransition(string, string)          {}
func (Noop) LogConfigError(error)                      {}
func (Noop) LogAnchorExit(string, int, int, time.Time) {}
func (Noop) Close(context.Context) error               { return nil }

// New returns the OTLP-backed emitter, or Noop when opts.Endpoint is empty.
// A constructor failure is returned (with a Noop emitter) so the keeper can
// log it and carry on; telemetry is never a reason to stop holding the
// assertion.
func New(ctx context.Context, opts Options) (Emitter, error) {
	if opts.Endpoint == "" {
		return Noop{}, nil
	}
	res, err := buildResource(ctx, opts.ServiceName, opts.ServiceVersion)
	if err != nil {
		return Noop{}, err
	}
	health := newExportHealth(time.Now, defaultHealthThrottle)

	var (
		mopts []otlpmetricgrpc.Option
		lopts []otlploggrpc.Option
	)
	if strings.Contains(opts.Endpoint, "://") {
		mopts = append(mopts, otlpmetricgrpc.WithEndpointURL(opts.Endpoint))
		lopts = append(lopts, otlploggrpc.WithEndpointURL(opts.Endpoint))
	} else {
		mopts = append(mopts, otlpmetricgrpc.WithEndpoint(opts.Endpoint), otlpmetricgrpc.WithInsecure())
		lopts = append(lopts, otlploggrpc.WithEndpoint(opts.Endpoint), otlploggrpc.WithInsecure())
	}
	metricExp, err := otlpmetricgrpc.New(ctx, mopts...)
	if err != nil {
		return Noop{}, err
	}
	logExp, err := otlploggrpc.New(ctx, lopts...)
	if err != nil {
		_ = metricExp.Shutdown(ctx)
		return Noop{}, err
	}
	reader := sdkmetric.NewPeriodicReader(
		&healthMetricExporter{Exporter: metricExp, health: health, out: os.Stderr},
		sdkmetric.WithInterval(exportInterval),
	)
	proc := sdklog.NewBatchProcessor(&healthLogExporter{Exporter: logExp, health: health, out: os.Stderr})
	return newEmitter(reader, proc, res)
}

// NewWithReader builds an Emitter whose metrics are served by reader (for
// example an sdkmetric.ManualReader) and whose logs go to proc, instead of the
// OTLP exporters. It exists so tests in other packages can observe recordings
// without a collector; production code uses New. Either argument may be nil.
func NewWithReader(reader sdkmetric.Reader, proc sdklog.Processor) (Emitter, error) {
	return newEmitter(reader, proc, nil)
}

type emitter struct {
	mp     *sdkmetric.MeterProvider
	lp     *sdklog.LoggerProvider
	logger otellog.Logger

	up          metric.Int64Gauge
	lastTick    metric.Float64Gauge
	tickDur     metric.Float64Histogram
	leasesLive  metric.Int64Gauge
	reaped      metric.Int64Counter
	rejected    metric.Int64Counter
	held        metric.Int64Gauge
	flags       metric.Int64Gauge
	gate        metric.Int64Gauge
	percent     metric.Int64Gauge
	onBattery   metric.Int64Gauge
	releasePct  metric.Int64Gauge
	battRelease metric.Int64Counter
	anchorExits metric.Int64Counter

	mu         sync.Mutex
	owners     *labelCap
	flagLabels *labelCap
	liveSeen   map[string]struct{} // owner labels emitted by the last RecordLeasesLive
	flagsSeen  map[string]struct{} // flag labels emitted by the last RecordAssertion
}

var _ Emitter = (*emitter)(nil)

func newEmitter(reader sdkmetric.Reader, proc sdklog.Processor, res *resource.Resource) (*emitter, error) {
	var mpOpts []sdkmetric.Option
	if reader != nil {
		mpOpts = append(mpOpts, sdkmetric.WithReader(reader))
	}
	var lpOpts []sdklog.LoggerProviderOption
	if proc != nil {
		lpOpts = append(lpOpts, sdklog.WithProcessor(proc))
	}
	if res != nil {
		mpOpts = append(mpOpts, sdkmetric.WithResource(res))
		lpOpts = append(lpOpts, sdklog.WithResource(res))
	}
	e := &emitter{
		mp:         sdkmetric.NewMeterProvider(mpOpts...),
		lp:         sdklog.NewLoggerProvider(lpOpts...),
		owners:     newLabelCap(MaxOwners),
		flagLabels: newLabelCap(MaxOwners),
		liveSeen:   map[string]struct{}{},
		flagsSeen:  map[string]struct{}{},
	}
	e.logger = e.lp.Logger("pg-keep-awake")
	if err := e.register(); err != nil {
		_ = e.mp.Shutdown(context.Background())
		_ = e.lp.Shutdown(context.Background())
		return nil, err
	}
	return e, nil
}

func (e *emitter) register() error {
	m := e.mp.Meter("pg-keep-awake")
	var err error
	i64g := func(name, desc string) metric.Int64Gauge {
		if err != nil {
			return nil
		}
		var g metric.Int64Gauge
		g, err = m.Int64Gauge(name, metric.WithDescription(desc))
		return g
	}
	i64c := func(name, desc string) metric.Int64Counter {
		if err != nil {
			return nil
		}
		var c metric.Int64Counter
		c, err = m.Int64Counter(name, metric.WithDescription(desc))
		return c
	}
	e.up = i64g("pg_keep_awake.keeper.up", "1 while the keeper is running")
	if err == nil {
		e.lastTick, err = m.Float64Gauge("pg_keep_awake.keeper.last_tick_timestamp_seconds",
			metric.WithUnit("s"), metric.WithDescription("unix time of the last keeper tick"))
	}
	if err == nil {
		e.tickDur, err = m.Float64Histogram("pg_keep_awake.tick.duration",
			metric.WithUnit("s"), metric.WithDescription("keeper tick duration"))
	}
	e.leasesLive = i64g("pg_keep_awake.leases.live", "live leases by owner")
	e.reaped = i64c("pg_keep_awake.leases.reaped_total", "leases reaped by reason")
	e.rejected = i64c("pg_keep_awake.leases.rejected_total", "lease files rejected by reason")
	e.held = i64g("pg_keep_awake.assertion.held", "1 while the keep-awake assertion is held")
	e.flags = i64g("pg_keep_awake.assertion.flags", "1 per held assertion flag")
	e.gate = i64g("pg_keep_awake.gate.state", "1 for the current battery gate state, 0 for the others")
	e.percent = i64g("pg_keep_awake.battery.percent", "battery percentage")
	e.onBattery = i64g("pg_keep_awake.battery.on_battery", "1 while on battery power")
	e.releasePct = i64g("pg_keep_awake.battery.release_pct", "the configured release level")
	e.battRelease = i64c("pg_keep_awake.battery.releases_total", "battery-driven releases by reason")
	e.anchorExits = i64c("pg_keep_awake.anchor.exits_total", "anchor exits by reason")
	if err != nil {
		return err
	}
	// Birth each fixed-reason counter series at zero so increase() over a
	// range that includes emitter creation captures the first event (the same
	// reason pa-monitor adds 0 at startup).
	ctx := context.Background()
	for _, r := range ReapReasons {
		e.reaped.Add(ctx, 0, reasonAttr(r))
	}
	for _, r := range RejectReasons {
		e.rejected.Add(ctx, 0, reasonAttr(r))
	}
	for _, r := range BatteryReleaseReasons {
		e.battRelease.Add(ctx, 0, reasonAttr(r))
	}
	for _, r := range AnchorExitReasons {
		e.anchorExits.Add(ctx, 0, reasonAttr(r))
	}
	return nil
}

func reasonAttr(r string) metric.AddOption {
	return metric.WithAttributes(attribute.String("reason", r))
}

// clamp returns v when it is in the allowed set, else fallback, so a counter
// label can never carry a free-form string.
func clamp(v string, allowed []string, fallback string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return fallback
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (e *emitter) RecordKeeperUp(up bool) {
	e.up.Record(context.Background(), b2i(up))
}

func (e *emitter) RecordTick(d time.Duration, at time.Time) {
	ctx := context.Background()
	e.tickDur.Record(ctx, d.Seconds())
	e.lastTick.Record(ctx, float64(at.UnixNano())/1e9)
}

func (e *emitter) RecordLeasesLive(byOwner map[string]int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(byOwner))
	for o := range byOwner {
		names = append(names, o)
	}
	sort.Strings(names) // deterministic cap assignment within one call
	counts := map[string]int64{}
	for _, o := range names {
		counts[e.owners.label(o)] += int64(byOwner[o])
	}
	ctx := context.Background()
	for label, n := range counts {
		e.leasesLive.Record(ctx, n, metric.WithAttributes(attribute.String("owner", label)))
	}
	for label := range e.liveSeen {
		if _, ok := counts[label]; !ok {
			e.leasesLive.Record(ctx, 0, metric.WithAttributes(attribute.String("owner", label)))
		}
	}
	e.liveSeen = map[string]struct{}{}
	for label := range counts {
		e.liveSeen[label] = struct{}{}
	}
}

func (e *emitter) IncLeasesReaped(reason string) {
	e.reaped.Add(context.Background(), 1, reasonAttr(clamp(reason, ReapReasons, "other")))
}

func (e *emitter) IncLeasesRejected(reason string) {
	e.rejected.Add(context.Background(), 1, reasonAttr(clamp(reason, RejectReasons, "other")))
}

func (e *emitter) RecordAssertion(held bool, flags []string) {
	ctx := context.Background()
	e.held.Record(ctx, b2i(held))
	e.mu.Lock()
	defer e.mu.Unlock()
	sorted := append([]string(nil), flags...)
	sort.Strings(sorted)
	now := map[string]struct{}{}
	for _, f := range sorted {
		label := e.flagLabels.label(f)
		now[label] = struct{}{}
		e.flags.Record(ctx, 1, metric.WithAttributes(attribute.String("flag", label)))
	}
	for label := range e.flagsSeen {
		if _, ok := now[label]; !ok {
			e.flags.Record(ctx, 0, metric.WithAttributes(attribute.String("flag", label)))
		}
	}
	e.flagsSeen = now
}

func (e *emitter) RecordGateState(state string) {
	cur := clamp(state, GateStates, "unknown")
	ctx := context.Background()
	for _, s := range GateStates {
		e.gate.Record(ctx, b2i(s == cur), metric.WithAttributes(attribute.String("state", s)))
	}
}

func (e *emitter) RecordBattery(percent int, onBattery bool) {
	ctx := context.Background()
	e.percent.Record(ctx, int64(percent))
	e.onBattery.Record(ctx, b2i(onBattery))
}

func (e *emitter) RecordReleasePct(pct int) {
	e.releasePct.Record(context.Background(), int64(pct))
}

func (e *emitter) IncBatteryRelease(reason string) {
	e.battRelease.Add(context.Background(), 1, reasonAttr(clamp(reason, BatteryReleaseReasons, "other")))
}

func (e *emitter) IncAnchorExit(reason string) {
	e.anchorExits.Add(context.Background(), 1, reasonAttr(clamp(reason, AnchorExitReasons, "unknown")))
}

func (e *emitter) Close(ctx context.Context) error {
	err := e.mp.Shutdown(ctx)
	if lerr := e.lp.Shutdown(ctx); err == nil {
		err = lerr
	}
	return err
}

// labelCap hands out label values for an unbounded input set while bounding
// the number of distinct labels: the first max distinct values are kept as is
// and every later one is labelled OtherOwner. The empty value is "unknown".
type labelCap struct {
	max  int
	seen map[string]struct{}
}

func newLabelCap(max int) *labelCap { return &labelCap{max: max, seen: map[string]struct{}{}} }

func (c *labelCap) label(v string) string {
	if v == "" {
		v = "unknown"
	}
	if _, ok := c.seen[v]; ok {
		return v
	}
	if len(c.seen) >= c.max {
		return OtherOwner
	}
	c.seen[v] = struct{}{}
	return v
}

// logEvent emits one structured log record. Metric labels never carry lease
// ids or pids, but log attributes may, so a record can be traced to a lease.
func (e *emitter) logEvent(name string, sev otellog.Severity, attrs ...attribute.KeyValue) {
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(sev)
	rec.SetBody(attribute.StringValue(name))
	rec.AddAttributes(attribute.String("event_name", name))
	for _, kv := range attrs {
		switch kv.Value.Type() {
		case attribute.INT64:
			rec.AddAttributes(attribute.Int64(string(kv.Key), kv.Value.AsInt64()))
		default:
			rec.AddAttributes(attribute.String(string(kv.Key), kv.Value.AsString()))
		}
	}
	e.logger.Emit(context.Background(), rec)
}

func (e *emitter) LogLeaseCreated(id, owner string) {
	e.logEvent("lease.created", otellog.SeverityInfo,
		attribute.String("lease_id", id), attribute.String("owner", owner))
}

func (e *emitter) LogLeaseReaped(id, owner, reason string) {
	e.logEvent("lease.reaped", otellog.SeverityInfo,
		attribute.String("lease_id", id), attribute.String("owner", owner), attribute.String("reason", reason))
}

func (e *emitter) LogAssertionSpawned(pid int) {
	e.logEvent("assertion.spawned", otellog.SeverityInfo, attribute.Int("pid", pid))
}

func (e *emitter) LogAssertionAdopted(pid int) {
	e.logEvent("assertion.adopted", otellog.SeverityInfo, attribute.Int("pid", pid))
}

func (e *emitter) LogAssertionReleased(reason string) {
	e.logEvent("assertion.released", otellog.SeverityInfo, attribute.String("reason", reason))
}

func (e *emitter) LogGateTransition(from, to string) {
	e.logEvent("gate.transition", otellog.SeverityInfo,
		attribute.String("from", from), attribute.String("to", to))
}

func (e *emitter) LogConfigError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	e.logEvent("config.error", otellog.SeverityError, attribute.String("error", msg))
}

func (e *emitter) LogAnchorExit(reason string, percent, pid int, at time.Time) {
	e.logEvent("anchor.exit", otellog.SeverityWarn,
		attribute.String("reason", reason), attribute.Int("percent", percent),
		attribute.Int("pid", pid), attribute.String("at", at.UTC().Format(time.RFC3339)))
}
