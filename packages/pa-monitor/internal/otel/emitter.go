// Package otel owns OpenTelemetry SDK initialisation and emission helpers
// for pa-monitor. The disabled-state contract: when OTEL_EXPORTER_OTLP_ENDPOINT
// is unset, New returns (nil, nil) and every method on a *Emitter accepts a
// nil receiver and does nothing. Callers do not branch on whether OTel is on.
package otel

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
)

// Options configures the emitter.
type Options struct {
	ServiceName    string
	ServiceVersion string
}

// stateObs is the per-state count observation, snapshotted by the
// observable gauge callback.
type stateObs struct {
	state string
	count int64
	attrs []attribute.KeyValue
}

// sessionInfoObs is a per-session row observation. The info gauge always
// emits 1; tokens / cost share the same key set so a Grafana table can
// join them by session_id. Snapshotted by the meter callback.
type sessionInfoObs struct {
	sessionID string
	tokens    int64
	costUSD   float64
	attrs     []attribute.KeyValue
}

// SessionInfo carries the per-session label set and metric values the
// emitter publishes on the pa_monitor.session.info /
// pa_monitor.session.tokens / pa_monitor.session.cost.usd gauges.
//
// Only the columns the dashboard table needs are present. ErrorKind is
// the empty string when the session has no terminal error.
type SessionInfo struct {
	SessionID    string
	SessionName  string
	Cwd          string
	TerminalHost string // already-abbreviated host (CMUX/TMUX/...)
	Status       string // session.Status.String() ("working"/"blocked"/"idle")
	Blocker      string // ADR 0024 blocker (empty unless Status == "blocked")
	Model        string
	ErrorKind    string  // empty when no terminal error
	Tokens       int64   // SessionEnrichment.SessionTokens
	CostUSD      float64 // SessionEnrichment.CostUSD
	// Labels carries extra attributes merged into the info gauge attrs —
	// typically the workspace.scope / plan_tier baseline so Grafana variables
	// can filter the table. Keys with empty values are dropped by attrsToKV.
	Labels map[string]string
}

// Emitter holds the OTel SDK handles. A nil *Emitter is a valid no-op
// emitter.
type Emitter struct {
	metricsProvider *sdkmetric.MeterProvider
	logProvider     *sdklog.LoggerProvider
	logger          otellog.Logger

	// health tracks OTLP export success/failure behind the metric and log
	// exporter decorators, surfacing a throttled UNHEALTHY / RECOVERED summary
	// to stderr in place of the SDK's unmanaged per-attempt spam (pg2-waji).
	health *exportHealth

	// Counters — sync (not observable). Nil when SDK uninitialised.
	blockLimitHits       metric.Int64Counter
	weekLimitHits        metric.Int64Counter
	caffeinateRounds     metric.Int64Counter
	caffeinateGrace      metric.Int64Counter
	contextLimitHits     metric.Int64Counter
	nudgesSent           metric.Int64Counter
	nudgeSendFailures    metric.Int64Counter
	nudgeSuppressed      metric.Int64Counter
	nudgeQueued          metric.Int64Counter
	nudgeDroppedNoBridge metric.Int64Counter
	apiErrorObserved     metric.Int64Counter
	signalerBinMissing   metric.Int64Counter

	mu                 sync.Mutex
	sessionsObs        []stateObs
	sessionsErroredObs map[string]int64 // kind -> count; replaced per tick
	// prevSessionTuples remembers the FULL attribute-tuples emitted with a
	// non-zero count on the PREVIOUS RecordSessionGroups call, keyed by a
	// canonical tupleKey. Drives carry-forward-zero (ADR 0024 D4/R6): a tuple
	// present last tick but absent now is observed once at 0, then forgotten —
	// so an orphaned {state,workspace.*,agent.*,plan_tier} series drops to 0
	// instead of persisting at its last value (OTLP has no staleness markers).
	prevSessionTuples map[string][]attribute.KeyValue
	// prevSessionsErroredKinds is the RecordSessionsErrored counterpart: the
	// set of error `kind`s emitted with a non-zero count last tick.
	prevSessionsErroredKinds map[string]struct{}
	caffeinateActiveVal      int64
	caffeinateAttrs          []attribute.KeyValue
	// caffeinateGraceVal buffers the grace-remaining countdown (seconds) for
	// the pa_monitor.caffeinate.grace_remaining_seconds gauge. Shares the
	// caffeinateAttrs label set; gated by the same caffeinateActiveKnown flag.
	caffeinateGraceVal int64
	// caffeinateActiveKnown gates observation. Without it the callback fires
	// once before any RecordCaffeinateActive call (the SDK collects shortly
	// after registration) and observes 0 with NO attrs, creating a permanent
	// label-less series in the exporter. After the first push the attrs map
	// is populated and subsequent observations carry plan_tier etc -- the
	// label-less ghost series sticks around forever as a stuck-at-0 line.
	caffeinateActiveKnown bool
	// autoResumeEnabledVal / autoResumeEnabledAttrs / autoResumeEnabledKnown
	// mirror the caffeinate triplet for the pa_monitor.auto_resume.enabled
	// gauge.
	autoResumeEnabledVal   int64
	autoResumeEnabledAttrs []attribute.KeyValue
	autoResumeEnabledKnown bool
	// blockCostUSD / weekCostUSD buffer the latest cost-in-USD for the active
	// 5h block and the active week. Pushed by RecordBlockCost / RecordWeekCost
	// (typically from the daemon's tick loop) and read by the gauge callback.
	// blockCostKnown / weekCostKnown gate observation — we don't emit a zero
	// reading just because the daemon hasn't completed its first tick yet.
	blockCostUSD   float64
	blockCostAttrs []attribute.KeyValue
	blockCostKnown bool
	weekCostUSD    float64
	weekCostAttrs  []attribute.KeyValue
	weekCostKnown  bool
	// sessionInfoObs buffers one row per active (non-Dormant) session. The
	// daemon's tick loop replaces the slice each tick via RecordSessionInfo;
	// the meter callback emits one Observe call per row across the three
	// session.* gauges (info=1, tokens, cost.usd) on each scrape.
	sessionInfoObs []sessionInfoObs

	// Authoritative status-line rate_limits usage gauges (ADR 0021 §5). These are
	// ACCOUNT-GLOBAL: the daemon pushes the newest LimitsSource reading each tick
	// via RecordBlockUsage / RecordWeekUsage, and the callback observes them with NO
	// session_id label. Each is gated by its own *Known flag AND by presence — an
	// unknown percentage (nil) / unset reset (zero time) is NOT observed at all, so
	// "unknown" never surfaces as a real 0% or a 1970 epoch. The *cost.usd gauges
	// are unaffected and keep emitting native cost.
	blockUsagePct      float64
	blockUsagePctKnown bool
	blockUsageResetsAt int64 // epoch seconds; 0 = unset
	blockUsageAttrs    []attribute.KeyValue
	weekUsagePct       float64
	weekUsagePctKnown  bool
	weekUsageResetsAt  int64 // epoch seconds; 0 = unset
	weekUsageAttrs     []attribute.KeyValue
}

// New constructs an Emitter if OTEL_EXPORTER_OTLP_ENDPOINT is set,
// otherwise returns (nil, nil).
func New(ctx context.Context, opts Options) (*Emitter, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, nil
	}

	res, err := buildResource(ctx, opts.ServiceName, opts.ServiceVersion)
	if err != nil {
		return nil, err
	}

	// Shared export-health tracker (pg2-waji). Both exporters are wrapped so
	// every export result flows through it; the decorators swallow the raw
	// export error (the data is already lost when the collector is down) and
	// the tracker emits a throttled UNHEALTHY / RECOVERED summary to stderr in
	// place of the SDK's per-attempt "failed to upload" spam.
	health := newExportHealth(time.Now, defaultHealthThrottle)

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			&healthMetricExporter{Exporter: metricExp, health: health, out: os.Stderr},
		)),
		sdkmetric.WithResource(res),
	)

	logExp, err := otlploggrpc.New(ctx)
	if err != nil {
		_ = mp.Shutdown(ctx)
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(
			&healthLogExporter{Exporter: logExp, health: health, out: os.Stderr},
		)),
		sdklog.WithResource(res),
	)

	e := &Emitter{
		metricsProvider: mp,
		logProvider:     lp,
		logger:          lp.Logger("pa-monitor"),
		health:          health,
	}
	if err := e.registerMetrics(mp); err != nil {
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
		return nil, err
	}
	return e, nil
}

func (e *Emitter) registerMetrics(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("pa-monitor")

	sessionsGauge, err := meter.Int64ObservableGauge("pa_monitor.sessions.count")
	if err != nil {
		return err
	}
	caffGauge, err := meter.Int64ObservableGauge("pa_monitor.caffeinate.active")
	if err != nil {
		return err
	}
	caffGraceGauge, err := meter.Int64ObservableGauge("pa_monitor.caffeinate.grace_remaining_seconds")
	if err != nil {
		return err
	}
	sessionsErroredGauge, err := meter.Int64ObservableGauge("pa_monitor.sessions.errored")
	if err != nil {
		return err
	}
	blockCostGauge, err := meter.Float64ObservableGauge("pa_monitor.block.cost.usd")
	if err != nil {
		return err
	}
	weekCostGauge, err := meter.Float64ObservableGauge("pa_monitor.week.cost.usd")
	if err != nil {
		return err
	}
	autoResumeGauge, err := meter.Int64ObservableGauge("pa_monitor.auto_resume.enabled")
	if err != nil {
		return err
	}
	// Per-session gauges. Cardinality contract: session_id is unbounded
	// over the lifetime of the daemon (every started session is a new id),
	// so the producer (RecordSessionInfo) MUST cap which sessions it
	// publishes — today we only emit rows for non-Dormant sessions, which
	// bounds the active series to the live process count and lets stale
	// session_ids age out of the exporter on the next scrape.
	sessionInfoGauge, err := meter.Int64ObservableGauge("pa_monitor.session.info")
	if err != nil {
		return err
	}
	sessionTokensGauge, err := meter.Int64ObservableGauge("pa_monitor.session.tokens")
	if err != nil {
		return err
	}
	sessionCostGauge, err := meter.Float64ObservableGauge("pa_monitor.session.cost.usd")
	if err != nil {
		return err
	}

	// Authoritative status-line rate_limits usage gauges (ADR 0021 §5). Account-
	// global (no session_id). The percentage gauges carry the server-side
	// used_percentage; the resets_at gauges carry the window reset as an epoch. The
	// existing *.cost.usd gauges are DELIBERATELY unchanged — a percentage is not a
	// cost, so these are NEW series, not a reuse of the dollar gauges.
	blockUsagePctGauge, err := meter.Float64ObservableGauge("pa_monitor.block.usage.percentage")
	if err != nil {
		return err
	}
	blockUsageResetsGauge, err := meter.Int64ObservableGauge("pa_monitor.block.usage.resets_at")
	if err != nil {
		return err
	}
	weekUsagePctGauge, err := meter.Float64ObservableGauge("pa_monitor.week.usage.percentage")
	if err != nil {
		return err
	}
	weekUsageResetsGauge, err := meter.Int64ObservableGauge("pa_monitor.week.usage.resets_at")
	if err != nil {
		return err
	}

	// Synchronous counters for transition events.
	if e.blockLimitHits, err = meter.Int64Counter("pa_monitor.block.usage.limit_hits_total"); err != nil {
		return err
	}
	if e.weekLimitHits, err = meter.Int64Counter("pa_monitor.week.usage.limit_hits_total"); err != nil {
		return err
	}
	if e.caffeinateRounds, err = meter.Int64Counter("pa_monitor.caffeinate.rounds_total"); err != nil {
		return err
	}
	if e.caffeinateGrace, err = meter.Int64Counter("pa_monitor.caffeinate.grace_expirations_total"); err != nil {
		return err
	}
	if e.contextLimitHits, err = meter.Int64Counter("pa_monitor.session.context.limit_hits_total"); err != nil {
		return err
	}
	if e.nudgesSent, err = meter.Int64Counter("pa_monitor.signal.sends_total"); err != nil {
		return err
	}
	if e.nudgeSendFailures, err = meter.Int64Counter("pa_monitor.signal.send_failures_total"); err != nil {
		return err
	}
	if e.nudgeSuppressed, err = meter.Int64Counter("pa_monitor.nudge.suppressed_total"); err != nil {
		return err
	}
	if e.nudgeQueued, err = meter.Int64Counter("pa_monitor.nudge.queued_total"); err != nil {
		return err
	}
	if e.nudgeDroppedNoBridge, err = meter.Int64Counter("pa_monitor.nudge.dropped_no_bridge_total"); err != nil {
		return err
	}
	if e.apiErrorObserved, err = meter.Int64Counter("pa_monitor.session.api_error.observed_total"); err != nil {
		return err
	}
	if e.signalerBinMissing, err = meter.Int64Counter("pa_monitor.signaler.binary_missing_total"); err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		e.mu.Lock()
		obs := e.sessionsObs
		erroredObs := e.sessionsErroredObs
		caffVal, caffAttrs, caffKnown := e.caffeinateActiveVal, e.caffeinateAttrs, e.caffeinateActiveKnown
		caffGrace := e.caffeinateGraceVal
		autoVal, autoAttrs, autoKnown := e.autoResumeEnabledVal, e.autoResumeEnabledAttrs, e.autoResumeEnabledKnown
		blockCost, blockAttrs, blockKnown := e.blockCostUSD, e.blockCostAttrs, e.blockCostKnown
		weekCost, weekAttrs, weekKnown := e.weekCostUSD, e.weekCostAttrs, e.weekCostKnown
		sessionInfo := e.sessionInfoObs
		blockUsagePct, blockUsagePctKnown := e.blockUsagePct, e.blockUsagePctKnown
		blockUsageResetsAt, blockUsageAttrs := e.blockUsageResetsAt, e.blockUsageAttrs
		weekUsagePct, weekUsagePctKnown := e.weekUsagePct, e.weekUsagePctKnown
		weekUsageResetsAt, weekUsageAttrs := e.weekUsageResetsAt, e.weekUsageAttrs
		e.mu.Unlock()
		for _, s := range obs {
			o.ObserveInt64(sessionsGauge, s.count, metric.WithAttributes(s.attrs...))
		}
		if caffKnown {
			o.ObserveInt64(caffGauge, caffVal, metric.WithAttributes(caffAttrs...))
			o.ObserveInt64(caffGraceGauge, caffGrace, metric.WithAttributes(caffAttrs...))
		}
		if autoKnown {
			o.ObserveInt64(autoResumeGauge, autoVal, metric.WithAttributes(autoAttrs...))
		}
		for kind, count := range erroredObs {
			o.ObserveInt64(sessionsErroredGauge, count,
				metric.WithAttributes(attribute.String("kind", kind)))
		}
		if blockKnown {
			o.ObserveFloat64(blockCostGauge, blockCost, metric.WithAttributes(blockAttrs...))
		}
		if weekKnown {
			o.ObserveFloat64(weekCostGauge, weekCost, metric.WithAttributes(weekAttrs...))
		}
		// Per-session rows: same attrs on all three gauges so Grafana
		// joins-by-field on session_id pull one row per session.
		for _, si := range sessionInfo {
			o.ObserveInt64(sessionInfoGauge, 1, metric.WithAttributes(si.attrs...))
			// tokens / cost gauges carry only session_id to keep their
			// series cardinality minimal — the info gauge holds the rest.
			idAttr := attribute.String("session_id", si.sessionID)
			o.ObserveInt64(sessionTokensGauge, si.tokens, metric.WithAttributes(idAttr))
			o.ObserveFloat64(sessionCostGauge, si.costUSD, metric.WithAttributes(idAttr))
		}
		// Account-global rate_limits usage gauges (ADR 0021 §5). Observe ONLY the
		// known windows — a nil percentage / zero reset is skipped, so unknown never
		// surfaces as 0 / 1970. No session_id is ever attached.
		if blockUsagePctKnown {
			o.ObserveFloat64(blockUsagePctGauge, blockUsagePct, metric.WithAttributes(blockUsageAttrs...))
		}
		if blockUsageResetsAt > 0 {
			o.ObserveInt64(blockUsageResetsGauge, blockUsageResetsAt, metric.WithAttributes(blockUsageAttrs...))
		}
		if weekUsagePctKnown {
			o.ObserveFloat64(weekUsagePctGauge, weekUsagePct, metric.WithAttributes(weekUsageAttrs...))
		}
		if weekUsageResetsAt > 0 {
			o.ObserveInt64(weekUsageResetsGauge, weekUsageResetsAt, metric.WithAttributes(weekUsageAttrs...))
		}
		return nil
	}, sessionsGauge, caffGauge, caffGraceGauge, autoResumeGauge, sessionsErroredGauge,
		blockCostGauge, weekCostGauge,
		sessionInfoGauge, sessionTokensGauge, sessionCostGauge,
		blockUsagePctGauge, blockUsageResetsGauge, weekUsagePctGauge, weekUsageResetsGauge)
	return err
}

// ExportHealthy reports whether the most recent OTLP export attempt succeeded.
// A nil emitter (OTel disabled) and an emitter that has not yet attempted an
// export both report healthy. This is the local read-side of the export-health
// state a status/info surface can consult without an RPC round-trip. nil-safe.
func (e *Emitter) ExportHealthy() bool {
	if e == nil || e.health == nil {
		return true
	}
	return e.health.healthy()
}

// Shutdown flushes exporters. nil-safe.
func (e *Emitter) Shutdown(ctx context.Context) error {
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

// SessionGroup is one observation row for the sessions gauge: a count
// with its full label set. RecordSessionGroups emits one Observe call
// per group, allowing per-(state, workspace.*, agent.*) cardinalities
// in a single metric.
type SessionGroup struct {
	Count  int
	Labels map[string]string
}

// RecordSessionGroups replaces the latest sessions-by-* observation
// with the supplied groups. Each group becomes one Observe call when
// the meter callback next fires. baseAttrs are merged into every group's
// attrs (group labels win on conflict). nil-safe.
func (e *Emitter) RecordSessionGroups(groups []SessionGroup, baseAttrs map[string]string) {
	if e == nil {
		return
	}
	obs := make([]stateObs, 0, len(groups))
	current := make(map[string][]attribute.KeyValue, len(groups))
	for _, g := range groups {
		merged := map[string]string{}
		for k, v := range baseAttrs {
			merged[k] = v
		}
		for k, v := range g.Labels {
			merged[k] = v
		}
		kv := attrsToKV(merged)
		obs = append(obs, stateObs{
			state: merged["state"],
			count: int64(g.Count),
			attrs: kv,
		})
		// Only non-zero tuples are worth carrying forward — an explicit 0 is
		// already reported and needs no re-zeroing.
		if g.Count != 0 {
			current[tupleKey(kv)] = kv
		}
	}
	e.mu.Lock()
	// Carry-forward-zero over the FULL label-tuple (ADR 0024 D4/R6). `state` is
	// only one label of the tuple, so a vanished tuple must be zeroed WHOLE — a
	// bare {state} series would not match the orphan. Observe every tuple that
	// was live last tick but is absent now at 0, then remember only the current
	// non-zero tuples so each orphan is zeroed exactly once and then drops out.
	for key, attrs := range e.prevSessionTuples {
		if _, present := current[key]; !present {
			obs = append(obs, stateObs{count: 0, attrs: attrs})
		}
	}
	e.sessionsObs = obs
	e.prevSessionTuples = current
	e.mu.Unlock()
}

// RecordCaffeinateActive sets the caffeinate gauges. nil-safe. Until the
// first call, the gauges are not observed (avoiding a ghost label-less
// series at 0 before the daemon tick loop fires).
//
// The gauge VALUE is the auto-caffeinate MODE signal (1 when active). The
// `state` attribute carries the caffeination PROCESS state (off/on/grace/
// error) so dashboards can tell "armed but not holding" (the incident:
// active gauge can be 1 via the MODE while process=off) apart from "holding".
// process must be one of "off"/"on"/"grace"/"error"; graceRemainingSeconds
// feeds the companion grace gauge.
func (e *Emitter) RecordCaffeinateActive(active bool, process string, graceRemainingSeconds int, attrs map[string]string) {
	if e == nil {
		return
	}
	v := int64(0)
	if active {
		v = 1
	}
	merged := map[string]string{}
	for k, val := range attrs {
		merged[k] = val
	}
	if process != "" {
		merged["state"] = process
	}
	kv := attrsToKV(merged)
	e.mu.Lock()
	e.caffeinateActiveVal = v
	e.caffeinateGraceVal = int64(graceRemainingSeconds)
	e.caffeinateAttrs = kv
	e.caffeinateActiveKnown = true
	e.mu.Unlock()
}

// RecordAutoResumeEnabled sets the auto-resume gauge. nil-safe. Mirrors
// RecordCaffeinateActive: pushed each tick by the daemon, observed only
// after the first push.
func (e *Emitter) RecordAutoResumeEnabled(enabled bool, attrs map[string]string) {
	if e == nil {
		return
	}
	v := int64(0)
	if enabled {
		v = 1
	}
	kv := attrsToKV(attrs)
	e.mu.Lock()
	e.autoResumeEnabledVal = v
	e.autoResumeEnabledAttrs = kv
	e.autoResumeEnabledKnown = true
	e.mu.Unlock()
}

// RecordBlockCost sets the latest 5h-block cost-in-USD gauge value. The
// daemon's tick loop pushes this each tick after Poller.Snapshot returns;
// the OTel observable gauge then reports the buffered value when the SDK
// next collects. nil-safe.
func (e *Emitter) RecordBlockCost(usd float64, attrs map[string]string) {
	if e == nil {
		return
	}
	kv := attrsToKV(attrs)
	e.mu.Lock()
	e.blockCostUSD = usd
	e.blockCostAttrs = kv
	e.blockCostKnown = true
	e.mu.Unlock()
}

// RecordWeekCost is the weekly counterpart to RecordBlockCost. nil-safe.
func (e *Emitter) RecordWeekCost(usd float64, attrs map[string]string) {
	if e == nil {
		return
	}
	kv := attrsToKV(attrs)
	e.mu.Lock()
	e.weekCostUSD = usd
	e.weekCostAttrs = kv
	e.weekCostKnown = true
	e.mu.Unlock()
}

// RecordBlockUsage sets the authoritative 5h-block usage.percentage /
// usage.resets_at gauge values (ADR 0021 §5). ACCOUNT-GLOBAL: attrs MUST NOT carry
// a session_id (the caller — the daemon tick — passes only account-scope labels).
// pct is nil when the 5h used_percentage is unknown/stale, in which case the
// percentage gauge is not observed (unknown never surfaces as 0). resetsAt is the
// zero time when the reset is unknown, in which case the resets_at gauge is not
// observed (never a 1970 epoch). nil-safe.
func (e *Emitter) RecordBlockUsage(pct *float64, resetsAt time.Time, attrs map[string]string) {
	if e == nil {
		return
	}
	kv := attrsToKV(attrs)
	e.mu.Lock()
	e.blockUsageAttrs = kv
	if pct != nil {
		e.blockUsagePct = *pct
		e.blockUsagePctKnown = true
	} else {
		e.blockUsagePctKnown = false
	}
	if !resetsAt.IsZero() {
		e.blockUsageResetsAt = resetsAt.Unix()
	} else {
		e.blockUsageResetsAt = 0
	}
	e.mu.Unlock()
}

// RecordWeekUsage is the 7d-window counterpart to RecordBlockUsage (ADR 0021 §5).
// Same account-global / unknown-not-zero contract. nil-safe.
func (e *Emitter) RecordWeekUsage(pct *float64, resetsAt time.Time, attrs map[string]string) {
	if e == nil {
		return
	}
	kv := attrsToKV(attrs)
	e.mu.Lock()
	e.weekUsageAttrs = kv
	if pct != nil {
		e.weekUsagePct = *pct
		e.weekUsagePctKnown = true
	} else {
		e.weekUsagePctKnown = false
	}
	if !resetsAt.IsZero() {
		e.weekUsageResetsAt = resetsAt.Unix()
	} else {
		e.weekUsageResetsAt = 0
	}
	e.mu.Unlock()
}

// RecordBlockLimitHit increments pa_monitor.block.usage.limit_hits_total
// and emits the matching log event. nil-safe.
func (e *Emitter) RecordBlockLimitHit(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.blockLimitHits != nil {
		e.blockLimitHits.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("block.usage.limit_hit", attrs)
}

// RecordWeekLimitHit increments pa_monitor.week.usage.limit_hits_total
// and emits the matching log event. nil-safe.
func (e *Emitter) RecordWeekLimitHit(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.weekLimitHits != nil {
		e.weekLimitHits.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("week.usage.limit_hit", attrs)
}

// RecordCaffeinateRound increments pa_monitor.caffeinate.rounds_total
// and emits caffeinate.start. nil-safe.
func (e *Emitter) RecordCaffeinateRound(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.caffeinateRounds != nil {
		e.caffeinateRounds.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("caffeinate.start", attrs)
}

// RecordCaffeinateGraceExpired increments the grace-expiration counter
// and emits the matching log event. nil-safe.
func (e *Emitter) RecordCaffeinateGraceExpired(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.caffeinateGrace != nil {
		e.caffeinateGrace.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("caffeinate.grace_expired", attrs)
}

// RecordContextLimitHit increments pa_monitor.session.context.limit_hits_total
// and emits the matching log event. nil-safe.
func (e *Emitter) RecordContextLimitHit(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.contextLimitHits != nil {
		e.contextLimitHits.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("session.context.limit_hit", attrs)
}

// RecordNudgeSent increments pa_monitor.signal.sends_total and emits
// the nudge.sent log event. nil-safe.
func (e *Emitter) RecordNudgeSent(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.nudgesSent != nil {
		e.nudgesSent.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("nudge.sent", attrs)
}

// RecordNudgeSendFailed increments pa_monitor.signal.send_failures_total with
// the BOUNDED counterAttrs and emits the nudge.send_failed log event with the
// richer logAttrs. The split caps metric series cardinality: high-cardinality
// fields (session_id, the full error string) belong only on the log event, not
// on the counter. Fired when Signaler.Send returns an error so a failed
// delivery is observable rather than silently swallowed. nil-safe.
func (e *Emitter) RecordNudgeSendFailed(counterAttrs, logAttrs map[string]string) {
	if e == nil {
		return
	}
	if e.nudgeSendFailures != nil {
		e.nudgeSendFailures.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(counterAttrs)...))
	}
	e.LogEvent("nudge.send_failed", logAttrs)
}

// RecordNudgeSuppressed increments pa_monitor.nudge.suppressed_total and
// emits the nudge.suppressed log event. nil-safe.
func (e *Emitter) RecordNudgeSuppressed(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.nudgeSuppressed != nil {
		e.nudgeSuppressed.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("nudge.suppressed", attrs)
}

// RecordNudgeDroppedNoBridge increments pa_monitor.nudge.dropped_no_bridge_total
// and emits the nudge.dropped_no_bridge log event. Fired when a group of
// no-bridge intents ages past the dispatcher's noBridgeDropWindow with no live
// cmux-bridge to retry against — a permanent give-up, distinct from the sent/
// suppressed/send-failed outcomes. nil-safe.
func (e *Emitter) RecordNudgeDroppedNoBridge(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.nudgeDroppedNoBridge != nil {
		e.nudgeDroppedNoBridge.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("nudge.dropped_no_bridge", attrs)
}

// RecordNudgeQueued increments pa_monitor.nudge.queued_total and emits
// the nudge.queued log event. nil-safe.
func (e *Emitter) RecordNudgeQueued(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.nudgeQueued != nil {
		e.nudgeQueued.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("nudge.queued", attrs)
}

// RecordApiErrorObserved increments pa_monitor.session.api_error.observed_total
// and emits the session.api_error.observed log event. nil-safe.
func (e *Emitter) RecordApiErrorObserved(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.apiErrorObserved != nil {
		e.apiErrorObserved.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("session.api_error.observed", attrs)
}

// RecordSignalerBinaryMissing fires pa_monitor.signaler.binary_missing_total
// and logs a signaler.binary_missing event. Called once per missing binary at
// daemon startup so an unwrapped tmux/cmux dependency is loudly visible instead
// of silently disabling that terminal's detection and auto-resume.
func (e *Emitter) RecordSignalerBinaryMissing(attrs map[string]string) {
	if e == nil {
		return
	}
	if e.signalerBinMissing != nil {
		e.signalerBinMissing.Add(context.Background(), 1, metric.WithAttributes(attrsToKV(attrs)...))
	}
	e.LogEvent("signaler.binary_missing", attrs)
}

// RecordSessionInfo replaces the buffered per-session rows. Callers MUST
// pre-filter to active (non-Dormant) sessions so the cardinality of
// session_id is bounded by the live process count rather than session
// history; dormant rows quietly drop from the next scrape because they
// are absent from the new slice. nil-safe.
func (e *Emitter) RecordSessionInfo(rows []SessionInfo) {
	if e == nil {
		return
	}
	obs := make([]sessionInfoObs, 0, len(rows))
	for _, r := range rows {
		// Build the info gauge's attribute set: per-row label columns
		// (only those needed by the dashboard table) plus any baseline
		// labels the caller threaded through (workspace.scope, plan_tier).
		merged := map[string]string{
			"session_id":    r.SessionID,
			"session_name":  r.SessionName,
			"cwd":           r.Cwd,
			"terminal_host": r.TerminalHost,
			"status":        r.Status,
			"blocker":       r.Blocker,
			"model":         r.Model,
			"error_kind":    r.ErrorKind,
		}
		for k, v := range r.Labels {
			if v == "" {
				continue
			}
			// Caller labels do NOT override the per-row column keys.
			if _, taken := merged[k]; taken {
				continue
			}
			merged[k] = v
		}
		obs = append(obs, sessionInfoObs{
			sessionID: r.SessionID,
			tokens:    r.Tokens,
			costUSD:   r.CostUSD,
			attrs:     attrsToKV(merged),
		})
	}
	e.mu.Lock()
	e.sessionInfoObs = obs
	e.mu.Unlock()
}

// RecordSessionsErrored replaces the latest sessions-errored observation.
// counts maps error kind → session count. Called each tick. nil-safe.
func (e *Emitter) RecordSessionsErrored(counts map[string]int) {
	if e == nil {
		return
	}
	obs := make(map[string]int64, len(counts))
	current := make(map[string]struct{}, len(counts))
	for k, v := range counts {
		obs[k] = int64(v)
		if v != 0 {
			current[k] = struct{}{}
		}
	}
	e.mu.Lock()
	// Carry-forward-zero keyed by `kind` (ADR 0024 D4/R6): a kind present last
	// tick but absent now is observed once at 0 so the orphaned series drops to
	// 0 instead of persisting at its last value. Zeroed once, then forgotten.
	for k := range e.prevSessionsErroredKinds {
		if _, present := obs[k]; !present {
			obs[k] = 0
		}
	}
	e.sessionsErroredObs = obs
	e.prevSessionsErroredKinds = current
	e.mu.Unlock()
}

// LogEvent emits one log record at info level with the given attributes
// and an event_name attribute set to name. nil-safe.
func (e *Emitter) LogEvent(name string, attrs map[string]string) {
	if e == nil || e.logger == nil {
		return
	}
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(otellog.SeverityInfo)
	rec.SetBody(otellog.StringValue(name))
	rec.AddAttributes(otellog.String("event_name", name))
	for k, v := range attrs {
		if v == "" {
			continue
		}
		rec.AddAttributes(otellog.String(k, v))
	}
	e.logger.Emit(context.Background(), rec)
}

// tupleKey builds a stable canonical string for a full attribute tuple by
// sorting its key=value pairs, so two attrsToKV results with identical content
// but differing map-iteration order compare equal. Used to match a tuple
// emitted last tick against this tick for carry-forward-zero.
func tupleKey(attrs []attribute.KeyValue) string {
	parts := make([]string, 0, len(attrs))
	for _, kv := range attrs {
		parts = append(parts, string(kv.Key)+"="+kv.Value.AsString())
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x1f")
}

func attrsToKV(m map[string]string) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(m))
	for k, v := range m {
		if v == "" {
			continue
		}
		out = append(out, attribute.String(k, v))
	}
	return out
}
