// Package obs is the observability of the pg-task-focus daemon: the
// Prometheus catalog served on /metrics, the structured log with its trace
// fields, and the OpenTelemetry tracing and OTLP log setup.
//
// Observability is best-effort: the daemon starts and serves with no
// collector present, and OTLP goes only to a loopback collector. Nothing in
// this package ever records a free-text field (a note, a key/value value, a
// skip or correction reason, a period label): metric labels are route
// templates or closed enumerations, and span attributes come from an explicit
// allow-list.
package obs

import (
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// Namespace prefixes every metric.
const Namespace = "pg_task_focus"

// The closed sets of label values.
var (
	// Clients is the closed set of the client label; anything else is
	// recorded as "unknown".
	Clients = []string{"cli", "web", "connector", "swiftbar", "unknown"}
	// AppendStages is the closed set of the stage label of append failures.
	AppendStages = []string{"write", "fsync", "project"}
	// ReloadResults is the closed set of the result label of reloads.
	ReloadResults = []string{"ok", "invalid", "refused", "restart_required"}
	// Cadences is the closed set of the cadence label.
	Cadences = []string{"daily", "weekly", "sprint"}
	// TaskStatuses is the closed set of task states.
	TaskStatuses = []string{"open", "completed", "skipped", "missed", "withdrawn"}
	// Outcomes is the closed set of task resolutions.
	Outcomes = []string{"completed", "skipped", "missed", "withdrawn"}
)

// NormalizeClient maps an X-Client header value to the closed client set.
func NormalizeClient(v string) string {
	for _, c := range Clients {
		if v == c {
			return c
		}
	}
	return "unknown"
}

// Metrics is the catalog. The zero value is not usable; call New.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPRequests    *prometheus.CounterVec
	HTTPDuration    *prometheus.HistogramVec
	EventsAppended  *prometheus.CounterVec
	AppendDuration  prometheus.Histogram
	FsyncDuration   prometheus.Histogram
	AppendFailures  *prometheus.CounterVec
	Rejections      *prometheus.CounterVec
	ConfigReloads   *prometheus.CounterVec
	Corrections     *prometheus.CounterVec
	AlertsPlayed    *prometheus.CounterVec
	AlertFailures   prometheus.Counter
	SSEEventsSent   prometheus.Counter
	Ready           prometheus.Gauge
	StartTimestamp  prometheus.Gauge
	BuildInfo       *prometheus.GaugeVec
	StartupRecovery *prometheus.GaugeVec
	StoreSize       prometheus.Gauge
	StoreWritable   prometheus.Gauge
	StoreReadOnly   prometheus.Gauge
	ReplayDuration  prometheus.Gauge
	ReplayEvents    prometheus.Gauge
	LastAppend      prometheus.Gauge
	ConfigValid     prometheus.Gauge
	LastReloadOK    prometheus.Gauge
	SSEClients      prometheus.Gauge
	StateVersion    prometheus.Gauge

	lastAppendUnix atomic.Int64
	once           sync.Once
}

// httpBuckets run from 1 ms to 10 s.
var httpBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// storeBuckets suit an fsync: a healthy one takes well under a second.
var storeBuckets = []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 5}

// New builds the catalog on its own registry and, as the daemon must at
// startup, adds 0 to every series of the counters an alert reads, so each is
// present on the first scrape.
func New(version string, schemaVersion int, started time.Time) *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}
	opts := func(name, help string) prometheus.Opts {
		return prometheus.Opts{Namespace: Namespace, Name: name, Help: help}
	}
	counterVec := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts(opts(name, help)), labels)
	}
	gauge := func(name, help string) prometheus.Gauge {
		return prometheus.NewGauge(prometheus.GaugeOpts(opts(name, help)))
	}
	hist := func(name, help string, buckets []float64) prometheus.Histogram {
		return prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: Namespace, Name: name, Help: help, Buckets: buckets})
	}

	m.HTTPRequests = counterVec("http_requests_total", "HTTP requests handled, by route template, method, status and client.", "route", "method", "status", "client")
	m.HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: Namespace, Name: "http_request_duration_seconds", Buckets: httpBuckets,
		Help: "HTTP request duration by route template; /stream is excluded.",
	}, []string{"route"})
	m.EventsAppended = counterVec("events_appended_total", "Events appended to the log, by event type.", "type")
	m.AppendDuration = hist("append_duration_seconds", "Time an append took, write and fsync included.", storeBuckets)
	m.FsyncDuration = hist("fsync_duration_seconds", "Time the fsync of an append took.", storeBuckets)
	m.AppendFailures = counterVec("append_failures_total", "Appends that failed, by stage: write, fsync or project.", "stage")
	m.Rejections = counterVec("rejections_total", "Requests refused, by reason.", "reason")
	m.ConfigReloads = counterVec("config_reload_total", "Configuration reloads, by result.", "result")
	m.Corrections = counterVec("corrections_applied_total", "Corrections and retractions committed, by kind.", "kind")
	m.AlertsPlayed = counterVec("alerts_played_total", "Overtime alerts played, by kind: expiry or reminder.", "kind")
	m.AlertFailures = prometheus.NewCounter(prometheus.CounterOpts(opts("alert_failures_total", "Sounds or notifications that failed to play or send.")))
	m.SSEEventsSent = prometheus.NewCounter(prometheus.CounterOpts(opts("sse_events_sent_total", "Server-sent events written to /stream clients.")))
	m.Ready = gauge("ready", "1 when the daemon has replayed the log and serves, 0 during replay.")
	m.StartTimestamp = gauge("start_timestamp_seconds", "Unix time the daemon started.")
	m.BuildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts(opts("build_info", "Build information; the value is always 1.")), []string{"version", "schema_version", "go_version"})
	m.StartupRecovery = prometheus.NewGaugeVec(prometheus.GaugeOpts(opts("startup_recovery", "What startup recovery did to the end of the log, by kind: torn_tail or uncommitted_batch; this process's count at startup.")), []string{"kind"})
	m.StoreSize = gauge("store_size_bytes", "Length in bytes of the committed log.")
	m.StoreWritable = gauge("store_writable", "1 when a periodic probe could create and remove a temp file in the data directory and open events.jsonl for append, else 0.")
	m.StoreReadOnly = gauge("store_read_only", "1 when the engine has entered read-only mode (not a probe); it clears only on restart.")
	m.ReplayDuration = gauge("replay_duration_seconds", "Time the startup replay took.")
	m.ReplayEvents = gauge("replay_event_count", "Events the startup replay read.")
	m.LastAppend = gauge("last_append_timestamp_seconds", "Unix time of the last append; 0 before the first.")
	m.ConfigValid = gauge("config_valid", "1 when the last load or reload of the configuration was valid.")
	m.LastReloadOK = gauge("last_config_reload_success_timestamp_seconds", "Unix time of the last successful configuration load or reload.")
	m.SSEClients = gauge("sse_clients", "Clients connected to /stream.")
	m.StateVersion = gauge("state_version", "The number of lines in the log (the first member of the state version).")

	m.Registry.MustRegister(
		m.HTTPRequests, m.HTTPDuration, m.EventsAppended, m.AppendDuration, m.FsyncDuration, m.AppendFailures,
		m.Rejections, m.ConfigReloads, m.Corrections, m.AlertsPlayed, m.AlertFailures, m.SSEEventsSent,
		m.Ready, m.StartTimestamp, m.BuildInfo, m.StartupRecovery, m.StoreSize, m.StoreWritable, m.StoreReadOnly,
		m.ReplayDuration, m.ReplayEvents, m.LastAppend, m.ConfigValid, m.LastReloadOK, m.SSEClients, m.StateVersion,
	)

	for _, s := range AppendStages {
		m.AppendFailures.WithLabelValues(s).Add(0)
	}
	m.AlertFailures.Add(0)
	for _, k := range []string{"torn_tail", "uncommitted_batch"} {
		m.StartupRecovery.WithLabelValues(k).Set(0)
	}
	m.StartTimestamp.Set(float64(started.Unix()))
	m.BuildInfo.WithLabelValues(version, strconv.Itoa(schemaVersion), runtime.Version()).Set(1)
	m.StoreWritable.Set(1)
	return m
}

// RecordStartup publishes what startup did: the replay, and the recovery.
func (m *Metrics) RecordStartup(events int, d time.Duration, rec store.Recovery) {
	m.ReplayEvents.Set(float64(events))
	m.ReplayDuration.Set(d.Seconds())
	if rec.TornTail {
		m.StartupRecovery.WithLabelValues("torn_tail").Set(1)
	}
	m.StartupRecovery.WithLabelValues("uncommitted_batch").Set(float64(rec.UncommittedBatches))
}

// Observer returns the engine.Observer that feeds the catalog. Every method
// recovers a panic of its own, so a defect in a metric never escapes into the
// engine's commit (which would skip the rest of that commit's notifications);
// onPanic, when set, is told.
func (m *Metrics) Observer(onPanic func(method string, recovered any)) *EngineObserver {
	return &EngineObserver{m: m, onPanic: onPanic}
}

// EngineObserver implements engine.Observer over the catalog.
type EngineObserver struct {
	m       *Metrics
	onPanic func(method string, recovered any)
}

func (o *EngineObserver) guard(method string) {
	if r := recover(); r != nil && o.onPanic != nil {
		func() {
			defer func() { _ = recover() }()
			o.onPanic(method, r)
		}()
	}
}

// Replayed records the replay.
func (o *EngineObserver) Replayed(n int, d time.Duration) {
	defer o.guard("Replayed")
	o.m.ReplayEvents.Set(float64(n))
	o.m.ReplayDuration.Set(d.Seconds())
}

// Recovered records what the store did to the end of the log.
func (o *EngineObserver) Recovered(r store.Recovery) {
	defer o.guard("Recovered")
	o.m.StartupRecovery.WithLabelValues("torn_tail").Set(boolFloat(r.TornTail))
	o.m.StartupRecovery.WithLabelValues("uncommitted_batch").Set(float64(r.UncommittedBatches))
}

// Appended counts one appended event. The engine reports the append's
// duration on the first event of a batch only, so the histograms observe it
// only when it is non-zero: every later event of a batch would otherwise count
// as a zero-length append.
func (o *EngineObserver) Appended(t event.Type, s store.AppendStats) {
	defer o.guard("Appended")
	o.m.EventsAppended.WithLabelValues(string(t)).Inc()
	if s.Duration > 0 {
		o.m.AppendDuration.Observe(s.Duration.Seconds())
	}
	if s.SyncDuration > 0 {
		o.m.FsyncDuration.Observe(s.SyncDuration.Seconds())
	}
	o.m.MarkAppend(time.Now())
}

// AppendFailed counts a failed append by stage.
func (o *EngineObserver) AppendFailed(stage string) {
	defer o.guard("AppendFailed")
	o.m.AppendFailures.WithLabelValues(stage).Inc()
}

// Rejected counts a refusal by reason.
func (o *EngineObserver) Rejected(r command.Reason) {
	defer o.guard("Rejected")
	o.m.Rejections.WithLabelValues(string(r)).Inc()
}

// Corrected counts a committed correction or retraction.
func (o *EngineObserver) Corrected(kind string) {
	defer o.guard("Corrected")
	o.m.Corrections.WithLabelValues(kind).Inc()
}

// MarkAppend records the instant of the latest append.
func (m *Metrics) MarkAppend(t time.Time) {
	m.lastAppendUnix.Store(t.UnixNano())
	m.LastAppend.Set(float64(t.UnixNano()) / 1e9)
}

// LastAppendTime is the instant of the latest append in this process; false
// before the first.
func (m *Metrics) LastAppendTime() (time.Time, bool) {
	n := m.lastAppendUnix.Load()
	if n == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, n), true
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// SetReadOnly publishes the engine's read-only mode.
func (m *Metrics) SetReadOnly(ro bool) { m.StoreReadOnly.Set(boolFloat(ro)) }

// SetWritable publishes the periodic probe's result and the log's size.
func (m *Metrics) SetWritable(ok bool, size int64) {
	m.StoreWritable.Set(boolFloat(ok))
	m.StoreSize.Set(float64(size))
}
