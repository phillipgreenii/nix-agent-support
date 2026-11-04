// Package obs is the recorder's small in-process metrics registry and event
// log. The shared exporter reads a Registry; events go to a slog.Logger at info
// level.
package obs

import (
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// Metric names exported by the store and copier.
const (
	MDroppedBytes      = "dropped_bytes_total"
	MClonePrivateMax   = "clone_private_size_max_bytes"
	MClonePrivateTotal = "clone_private_size_total_bytes"
	MClonePrivateFiles = "clone_private_files_total"
	EvClonePrivate     = "clone_private"
	MCopyLagBytes      = "copy_lag_bytes"
	MCopyLagSeconds    = "copy_lag_seconds"
	MCopyLagHistogram  = "copy_lag_seconds_histogram"
	EvCopyTrimmed      = "copy_trimmed_on_start"
	EvGenerationStart  = "generation_started"
	EvCopyMismatch     = "copy_mismatch"
	EvFileVanished     = "file_vanished"
	EvSnapshotMissed   = "snapshot_version_missed"
	EvCopyMethod       = "copy_method"
	ReasonVanished     = "file_vanished"
	ReasonGeneration   = "generation_restart"
	ReasonTrimmed      = "copy_trimmed"
	ReasonSnapMissed   = "snapshot_missed"
	ReasonRemovalRace  = "removal_race"
	histogramBucketsNS = 0
)

// Metric names exported by the serve composition and the listener.
const (
	MHeartbeat      = "last_sweep_timestamp_seconds"
	MStateUsedBytes = "state_used_bytes"
	MRequestsTotal  = "requests_total"
	MQueryDuration  = "query_duration_seconds"
	MQueryTimeouts  = "query_timeouts_total"
	// MRequestCancelled counts requests the server stopped early, by op and reason
	// (client_gone or deadline); EvRequestCancelled is the log event.
	MRequestCancelled  = "request_cancelled_total"
	EvRequestCancelled = "request_cancelled"
	MFilterFields      = "query_filter_fields_total"
	MFeedDuration      = "feed_latency_seconds"
	// MRequestsInFlight is the requests being served now, by op; EvSlowRequest is
	// the rate-capped WARN for a request over the slow threshold.
	MRequestsInFlight = "requests_inflight"
	EvSlowRequest     = "slow_request"
	MWaitDuration     = "wait_duration_seconds"
	MCopiedFiles      = "backfill_files_copied"
	MBackfillFiles    = "backfill_files_total"
	MCaughtUp         = "caught_up"
	MFeedWaiters      = "feed_waiters"
	MIndexRebuilds    = "index_rebuilds_total"
	EvIndexRebuilt    = "index_rebuilt"
	// EvRebuildRefused and MRebuildRefused: a rebuild stopped by the free-space
	// preflight before it moved anything. EvRebuildPreflight is a passed check.
	EvRebuildRefused   = "rebuild_refused_low_space"
	EvRebuildPreflight = "rebuild_preflight"
	MRebuildRefused    = "rebuild_refused_total"
	ClientOther        = "other"
	UnknownClientLbl   = "client"
)

// Metric and event names of the Process and Conversation facts.
const (
	MConversationEnd   = "conversation_end_total"
	MStaleDiscovery    = "registry_stale_at_discovery_total"
	MControlInbox      = "control_inbox_state"
	MNoInbox           = "processes_without_control_inbox"
	MEndFiles          = "end_reason_source_files_total"
	MControlRequests   = "control_requests_total"
	MStaleFifosRemoved = "stale_fifos_removed_total"
	EvStartupStale     = "startup_stale_registries"
	EvProcessExit      = "process_exit"
)

// Event names of the daemon's start: one startup_phase per phase (with
// startup_phase_begin before a phase that can run long) and one
// startup_complete when the daemon serves.
const (
	EvStartupPhaseBegin = "startup_phase_begin"
	EvStartupPhase      = "startup_phase"
	EvStartupComplete   = "startup_complete"
	// EvQueryTimeout is a request that hit the query deadline.
	EvQueryTimeout = "query_timeout"
)

// Metric and event names of the settle stage.
const (
	MConversationsByStage = "conversations_by_stage"
	MHeldConversations    = "held_conversations"
	MOldestHeldAge        = "oldest_held_age_seconds"
	MBytesPendingSettle   = "bytes_pending_settle"
	MTornTailAge          = "torn_tail_age_seconds"
	MLowSpace             = "state_dir_low_space"
	MSettled              = "conversations_settled_total"
	EvTornTailPending     = "torn_tail_pending"
	EvStageChanged        = "stage_changed"
	EvStateDirLow         = "state_dir_low"
	EvArchiveWritten      = "archive_written"
	EvSessionHeld         = "session_held"
	EvSettleDiscarded     = "settle_clone_discarded"
	EvSettleCrashRecovery = "settle_recovered_on_start"
)

// Metric and event names of the background loops. Durations are in SECONDS in
// the metrics and in MILLISECONDS (duration_ms) in the log events.
// conversations_by_stage is as of the last settle tick (up to 60 s old).
const (
	MSettleTickDuration      = "settle_tick_duration_seconds"
	MRefreshGaugesDuration   = "refresh_gauges_duration_seconds"
	MLoopRuns                = "loop_runs_total"
	MLoopDuration            = "loop_duration_seconds"
	MLoopLastTimestamp       = "loop_last_timestamp_seconds"
	MLoopInFlight            = "loop_in_flight_seconds"
	MSettleTickExamined      = "settle_tick_examined_total"
	MSettleTickTransitions   = "settle_tick_transitions_total"
	MSettleTickLastDuration  = "settle_tick_last_duration_seconds"
	MSettleTickLastTimestamp = "settle_tick_last_timestamp_seconds"
	// The wait for the settler's run lock (label op: tick, remove, where, extract,
	// recheck, recover): how long a caller queued behind another holder.
	MSettleRunLockWaits       = "settle_run_lock_wait_total"
	MSettleRunLockWaitSeconds = "settle_run_lock_wait_seconds_total"
	EvSettleTick              = "settle_tick"
	MSettleTickInFlight       = "settle_tick_in_flight_seconds"
	EvSettleTickStuck         = "settle_tick_stuck"
	EvSettleTickSlow          = "settle_tick_slow"
	EvWALTruncateStart        = "wal_truncate_start"
	EvWALTruncateDone         = "wal_truncate_done"
)

// Metric names of the process, Go runtime and WAL-size collectors.
const (
	MProcessCPUSeconds = "process_cpu_seconds_total"
	MGoGoroutines      = "go_goroutines"
	MGoHeapAlloc       = "go_memstats_heap_alloc_bytes"
	MGoAllocTotal      = "go_memstats_alloc_bytes_total"
	MGoGCPauseSeconds  = "go_gc_pause_seconds_total"
	MWALBytes          = "wal_bytes"
)

// Metric names of the database/sql connection pools (label pool: read or
// write). The wait figures say how long callers queued for a connection.
const (
	MDBOpenConns         = "db_open_connections"
	MDBInUseConns        = "db_in_use_connections"
	MDBIdleConns         = "db_idle_connections"
	MDBWaitCount         = "db_wait_total"
	MDBWaitSeconds       = "db_wait_seconds_total"
	MDBMaxLifetimeClosed = "db_max_lifetime_closed_total"
	MDBMaxIdleClosed     = "db_max_idle_closed_total"
)

// Metric names of the passive WAL checkpoint loop. wal_frames and
// wal_checkpointed_frames are in WAL pages (not bytes), as of the last passive
// checkpoint, and are cumulative until the WAL restarts.
const (
	MWALFrames             = "wal_frames"
	MWALCheckpointedFrames = "wal_checkpointed_frames"
	MWALCheckpointLastTime = "wal_checkpoint_last_timestamp_seconds"
	MWALCheckpointDuration = "wal_checkpoint_duration_seconds"
)

// Metric names of the per-pass work counters and the lock waits. The lock-wait
// histograms observe CONTENDED acquisitions only (an uncontended lock costs no
// registry lock), so their sample count is the number of times a caller had to
// queue, and the rate of ingest_results_total is the denominator for the
// fraction of ingests that queued.
const (
	MIngestResults          = "ingest_results_total"
	MSweepFiles             = "sweep_files_total"
	MLagRebuild             = "lag_rebuild_total"
	MCopierLockWait         = "copier_lock_wait_seconds"
	MStoreWriteLockWait     = "store_write_lock_wait_seconds"
	SweepWalked             = "walked"
	SweepUnchangedCandidate = "unchanged_candidate"
)

// LagRebuildResults are the values of the result label of lag_rebuild_total.
var LagRebuildResults = []string{"match", "drift"}

// IngestResults are the values of the result label of ingest_results_total:
// the copier.Result.Action strings with hyphens mapped to underscores, plus
// error (an ingest that returned an error).
var IngestResults = []string{
	"copied", "new_generation", "unchanged", "skipped", "snapshot", "link",
	"missing", "tombstone", "raced", ResultError,
}

// ResultError is the result value of an ingest that returned an error.
const ResultError = "error"

// SweepOutcomes are the values of the outcome label of sweep_files_total.
var SweepOutcomes = []string{SweepWalked, SweepUnchangedCandidate}

// IngestResultLabel maps a copier.Result.Action string to its result label
// value: hyphens become underscores (new-generation is new_generation).
func IngestResultLabel(action string) string {
	return strings.ReplaceAll(action, "-", "_")
}

// Loop and outcome label values of the loop metrics. removal_pass is nested
// inside settle_tick (its time is part of the tick's, not additive to it).
// copier_ingest is the one per-event loop: it has a duration observation only,
// no run count, last-timestamp or in-flight series.
const (
	LoopSettleTick    = "settle_tick"
	LoopRefreshGauges = "refresh_gauges"
	LoopWALCheckpoint = "wal_checkpoint"
	LoopWatcherSweep  = "watcher_sweep"
	LoopCopierIngest  = "copier_ingest"
	LoopParserDrain   = "parser_drain"
	LoopTrackerTick   = "tracker_tick"
	LoopTrackerPoll   = "tracker_poll"
	LoopStatusRefresh = "status_refresh"
	LoopDoctor        = "doctor"
	LoopRemovalPass   = "removal_pass"
	OutcomeOK         = "ok"
	OutcomeError      = "error"
)

// Loops are the 11 values of the loop label.
var Loops = []string{
	LoopSettleTick, LoopRefreshGauges, LoopWALCheckpoint, LoopWatcherSweep, LoopCopierIngest,
	LoopParserDrain, LoopTrackerTick, LoopTrackerPoll, LoopStatusRefresh, LoopDoctor, LoopRemovalPass,
}

// PassLoops are the loops that count runs and carry a last-timestamp and an
// in-flight series: every loop except the per-event copier_ingest.
func PassLoops() []string {
	out := make([]string, 0, len(Loops))
	for _, l := range Loops {
		if l != LoopCopierIngest {
			out = append(out, l)
		}
	}
	return out
}

// Metric and event names of removal, quarantine, extract and doctor.
const (
	MQuarantineFiles      = "quarantine_files"
	MQuarantineBytes      = "quarantine_bytes"
	MQuarantineOldestAge  = "quarantine_oldest_age_seconds"
	MQuarantineMismatch   = "removal_quarantine_mismatch_total"
	MRemovalBacklogConvs  = "removal_backlog_conversations"
	MRemovalBacklogBytes  = "removal_backlog_bytes"
	MRemovedTotal         = "conversations_removed_total"
	MSkippedFiles         = "skipped_files_total"
	MDoctorLastRun        = "doctor_last_run_timestamp"
	MDoctorMismatchFiles  = "doctor_mismatch_files"
	EvQuarantined         = "quarantined"
	EvQuarantineRestored  = "quarantine_restored"
	EvQuarantineOrphan    = "quarantine_orphan_found_on_start"
	EvRemoved             = "conversation_removed"
	EvExtracted           = "conversation_extracted"
	EvDoctorDone          = "doctor_done"
	EvRemovalRecovered    = "removal_recovered_on_start"
	ReasonQuarantineUnavl = "quarantine_unavailable"
)

// SkipReasons are the removal reasons of skipped_files_total{reason}; each is
// registered at zero at start, so the first increase fires.
var SkipReasons = []string{ReasonQuarantineUnavl, "holder_found", "write_after_rename", "file_replaced", "restore_conflict", "live_process", "registry_names_session", "no_archive", "unlink_failed"}

// ControlVerbs, ControlOutcomes, InboxStates and EndFileOutcomes are the bounded
// label values of the control metrics; anything else is bucketed as "other".
var (
	ControlVerbs     = []string{"submit", "interrupt", "run", "read", "ping"}
	ControlOutcomes  = []string{"ok", "rejected", "duplicate", "refused"}
	InboxStates      = []string{"present", "gone", "never_loaded"}
	EndFileOutcomes  = []string{"ingested", "removed", "invalid"}
	boolLabelStrings = []string{"false", "true"}
)

// LagBuckets are the upper bounds in seconds of the copy-lag histogram.
var LagBuckets = []float64{0.1, 0.5, 1, 5, 30, 300}

// LoopBuckets are the upper bounds in seconds of the background-loop duration
// histograms; 60 s is the settle tick interval, so it has its own edge.
var LoopBuckets = []float64{0.01, 0.05, 0.25, 1, 5, 15, 30, 60, 120, 300, 600, 1800}

// RequestBuckets are the upper bounds in seconds of the query and feed
// histograms; WaitBuckets those of the wait histogram.
var (
	RequestBuckets = []float64{0.001, 0.005, 0.025, 0.1, 0.5, 2, 10}
	WaitBuckets    = []float64{0.1, 1, 5, 15, 30, 60, 120}
)

// Event is one logged event.
type Event struct {
	Name  string
	Attrs map[string]string
}

// Obs bundles the logger, the registry and a captured event list.
type Obs struct {
	Log *slog.Logger
	Reg *Registry

	mu     sync.Mutex
	events []Event
	nocap  bool
}

// DisableCapture stops Event from retaining events in memory; a long-running
// daemon calls it, tests keep the default.
func (o *Obs) DisableCapture() {
	o.mu.Lock()
	o.nocap = true
	o.events = nil
	o.mu.Unlock()
}

// New returns an Obs. A nil logger discards logs.
func New(log *slog.Logger) *Obs {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	o := &Obs{Log: log, Reg: NewRegistry()}
	// Registered at zero so the series exists before the removal packet fires it.
	o.RegisterStandard()
	return o
}

// AllReasons lists every drop reason; RegisterStandard registers each at zero.
var AllReasons = []string{ReasonVanished, ReasonGeneration, ReasonTrimmed, ReasonSnapMissed, ReasonRemovalRace}

// RegisterStandard registers every reason series at zero and the daemon's
// gauges and histograms (empty), so each series exists from start and an alert
// never sees an absent series as a healthy one. It is idempotent.
func (o *Obs) RegisterStandard() {
	for _, r := range AllReasons {
		o.Reg.Add(MDroppedBytes, map[string]string{"reason": r}, 0)
	}
	o.Reg.Add(MIndexRebuilds, nil, 0)
	o.Reg.Add(MRebuildRefused, nil, 0)
	o.Reg.Add(MQueryTimeouts, map[string]string{"op": "unknown"}, 0)
	o.Reg.Add(MStaleDiscovery, nil, 0)
	o.Reg.Add(MStaleFifosRemoved, nil, 0)
	for _, rp := range boolLabelStrings {
		for _, eo := range boolLabelStrings {
			for _, ce := range boolLabelStrings {
				for _, ee := range boolLabelStrings {
					o.Reg.Add(MConversationEnd, map[string]string{"registry_present": rp, "exit_observed": eo, "clear_entry": ce, "exit_entry": ee}, 0)
				}
			}
		}
	}
	for _, out := range EndFileOutcomes {
		o.Reg.Add(MEndFiles, map[string]string{"outcome": out}, 0)
	}
	for _, v := range append(append([]string(nil), ControlVerbs...), ClientOther) {
		for _, out := range append(append([]string(nil), ControlOutcomes...), ClientOther) {
			o.Reg.Add(MControlRequests, map[string]string{"verb": v, "outcome": out}, 0)
		}
	}
	for _, st := range InboxStates {
		o.Reg.Set(MControlInbox, map[string]string{"state": st}, 0)
	}
	o.Reg.Set(MNoInbox, nil, 0)
	for _, st := range []string{"live", "quiet", "held", "settled", "removal_pending", "removed"} {
		o.Reg.Set(MConversationsByStage, map[string]string{"stage": st}, 0)
	}
	o.Reg.Set(MOldestHeldAge, nil, 0)
	o.Reg.Set(MBytesPendingSettle, nil, 0)
	o.Reg.Set(MLowSpace, nil, 0)
	o.Reg.Add(MSettled, nil, 0)
	for _, g := range []string{MQuarantineFiles, MQuarantineBytes, MQuarantineOldestAge, MRemovalBacklogConvs, MRemovalBacklogBytes, MDoctorLastRun, MDoctorMismatchFiles} {
		o.Reg.Set(g, nil, 0)
	}
	o.Reg.Add(MQuarantineMismatch, nil, 0)
	o.Reg.Add(MRemovedTotal, nil, 0)
	for _, r := range SkipReasons {
		o.Reg.Add(MSkippedFiles, map[string]string{"reason": r}, 0)
	}
	for _, g := range []string{
		MHeartbeat, MStateUsedBytes, MCopyLagBytes, MCopyLagSeconds,
		MCopiedFiles, MBackfillFiles, MCaughtUp, MFeedWaiters,
	} {
		if _, ok := o.Reg.Gauge(g, nil); !ok {
			o.Reg.Set(g, nil, 0)
		}
	}
	for _, r := range IngestResults {
		o.Reg.Add(MIngestResults, map[string]string{"result": r}, 0)
	}
	for _, r := range LagRebuildResults {
		o.Reg.Add(MLagRebuild, map[string]string{"result": r}, 0)
	}
	for _, oc := range SweepOutcomes {
		o.Reg.Add(MSweepFiles, map[string]string{"outcome": oc}, 0)
	}
	for _, l := range PassLoops() {
		for _, out := range []string{OutcomeOK, OutcomeError} {
			o.Reg.Add(MLoopRuns, map[string]string{"loop": l, "outcome": out}, 0)
		}
		o.Reg.Set(MLoopLastTimestamp, map[string]string{"loop": l}, 0)
		o.Reg.Set(MLoopInFlight, map[string]string{"loop": l}, 0)
	}
	loopSets := make([]map[string]string, 0, len(Loops))
	for _, l := range Loops {
		loopSets = append(loopSets, map[string]string{"loop": l})
	}
	o.Reg.RegisterHistL(MLoopDuration, LoopBuckets, loopSets)
	o.Reg.Add(MSettleTickExamined, nil, 0)
	o.Reg.Add(MSettleTickTransitions, nil, 0)
	o.Reg.Set(MSettleTickLastDuration, nil, 0)
	o.Reg.Set(MSettleTickLastTimestamp, nil, 0)
	o.Reg.Set(MSettleTickInFlight, nil, 0)
	o.Reg.Set(MWALFrames, nil, 0)
	o.Reg.Set(MWALCheckpointedFrames, nil, 0)
	o.Reg.Set(MWALCheckpointLastTime, nil, 0)
	for _, h := range []struct {
		name   string
		bounds []float64
	}{
		{MCopyLagHistogram, LagBuckets},
		{MWaitDuration, WaitBuckets},
		{MSettleTickDuration, LoopBuckets},
		{MRefreshGaugesDuration, LoopBuckets},
		{MWALCheckpointDuration, LoopBuckets},
		{MCopierLockWait, LoopBuckets},
		{MStoreWriteLockWait, LoopBuckets},
	} {
		o.Reg.RegisterHist(h.name, h.bounds)
	}
	// The request histograms are labelled by op only: the bounds are registered
	// here and each op's series is registered when the operation is (the server).
	o.Reg.RegisterHistL(MQueryDuration, RequestBuckets, nil)
	o.Reg.RegisterHistL(MFeedDuration, RequestBuckets, nil)
	for name, text := range helpTexts {
		o.Reg.Help(name, text)
	}
}

// ClientLabel maps a caller-supplied client name to the bounded label value:
// the name when it is on the whitelist, otherwise "other".
func ClientLabel(name string, whitelist []string) string {
	for _, w := range whitelist {
		if w == name {
			return name
		}
	}
	return ClientOther
}

// Event logs name at info level with attribute pairs and records it.
func (o *Obs) Event(name string, kv ...string) {
	attrs := map[string]string{}
	args := []any{}
	for i := 0; i+1 < len(kv); i += 2 {
		attrs[kv[i]] = kv[i+1]
		args = append(args, kv[i], kv[i+1])
	}
	o.mu.Lock()
	if !o.nocap {
		o.events = append(o.events, Event{Name: name, Attrs: attrs})
	}
	o.mu.Unlock()
	o.Log.Info(name, args...)
}

// Events returns a copy of the recorded events.
func (o *Obs) Events() []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Event(nil), o.events...)
}

// EventsNamed returns the recorded events with the given name.
func (o *Obs) EventsNamed(name string) []Event {
	var out []Event
	for _, e := range o.Events() {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

// Registry holds counters, gauges and histograms keyed by name and labels.
type Registry struct {
	// OnLock, when non-nil, is called once per registry lock acquisition. It is a
	// test seam (nil in production) so a test can count acquisitions; set it
	// before the registry is shared.
	OnLock func()

	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
	hist     map[string]*Histogram
	bounds   map[string][]float64 // bucket bounds per histogram name, for label sets not registered up front
	help     map[string]string
}

// lock takes the registry mutex and reports the acquisition to OnLock.
func (r *Registry) lock() {
	r.mu.Lock()
	if r.OnLock != nil {
		r.OnLock()
	}
}

// Histogram is a cumulative-bucket histogram.
type Histogram struct {
	Bounds  []float64
	Counts  []uint64 // len(Bounds)+1, last is +Inf
	Sum     float64
	Samples uint64
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{counters: map[string]float64{}, gauges: map[string]float64{}, hist: map[string]*Histogram{}, bounds: map[string][]float64{}, help: map[string]string{}}
}

func key(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	ks := make([]string, 0, len(labels))
	for k := range labels {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range ks {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k + "=" + labels[k])
	}
	b.WriteByte('}')
	return b.String()
}

// Add increments a counter (registering it at zero when v is 0).
func (r *Registry) Add(name string, labels map[string]string, v float64) {
	r.lock()
	r.counters[key(name, labels)] += v
	r.mu.Unlock()
}

// SetCounter sets a counter to the absolute value v, for a counter whose source
// is itself cumulative (process CPU, GC pause). A v below the current value is
// ignored so the counter never moves backwards.
func (r *Registry) SetCounter(name string, labels map[string]string, v float64) {
	k := key(name, labels)
	r.lock()
	if cur, ok := r.counters[k]; !ok || v > cur {
		r.counters[k] = v
	}
	r.mu.Unlock()
}

// Counter returns a counter value and whether the series exists.
func (r *Registry) Counter(name string, labels map[string]string) (float64, bool) {
	r.lock()
	defer r.mu.Unlock()
	v, ok := r.counters[key(name, labels)]
	return v, ok
}

// Set sets a gauge.
func (r *Registry) Set(name string, labels map[string]string, v float64) {
	r.lock()
	r.gauges[key(name, labels)] = v
	r.mu.Unlock()
}

// AddGauge adds delta to a gauge (registering it at zero first when absent).
func (r *Registry) AddGauge(name string, labels map[string]string, delta float64) {
	r.lock()
	r.gauges[key(name, labels)] += delta
	r.mu.Unlock()
}

// Gauge returns a gauge value and whether it was set.
func (r *Registry) Gauge(name string, labels map[string]string) (float64, bool) {
	r.lock()
	defer r.mu.Unlock()
	v, ok := r.gauges[key(name, labels)]
	return v, ok
}

// RegisterHist registers an unlabelled histogram with no samples (a no-op when
// it exists).
func (r *Registry) RegisterHist(name string, bounds []float64) {
	r.RegisterHistL(name, bounds, []map[string]string{nil})
}

// RegisterHistL registers the histogram name with the given bucket bounds and
// one empty series per label set (a nil or empty set is the unlabelled series).
// An existing series is kept. Later ObserveL calls with a label set not listed
// here use the same bounds.
func (r *Registry) RegisterHistL(name string, bounds []float64, labelSets []map[string]string) {
	r.lock()
	defer r.mu.Unlock()
	if _, ok := r.bounds[name]; !ok {
		r.bounds[name] = bounds
	}
	for _, ls := range labelSets {
		k := key(name, ls)
		if r.hist[k] == nil {
			r.hist[k] = &Histogram{Bounds: r.bounds[name], Counts: make([]uint64, len(r.bounds[name])+1)}
		}
	}
}

// Help sets the one-line help text exported for the metric name.
func (r *Registry) Help(name, text string) {
	r.lock()
	r.help[name] = text
	r.mu.Unlock()
}

// HelpTexts returns a copy of the help texts keyed by metric name.
func (r *Registry) HelpTexts() map[string]string {
	r.lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.help))
	for k, v := range r.help {
		out[k] = v
	}
	return out
}

// Snapshot returns copies of every counter, gauge and histogram, keyed by
// "name" or "name{k=v,...}".
func (r *Registry) Snapshot() (counters, gauges map[string]float64, hists map[string]*Histogram) {
	r.lock()
	defer r.mu.Unlock()
	counters = make(map[string]float64, len(r.counters))
	for k, v := range r.counters {
		counters[k] = v
	}
	gauges = make(map[string]float64, len(r.gauges))
	for k, v := range r.gauges {
		gauges[k] = v
	}
	hists = make(map[string]*Histogram, len(r.hist))
	for k, h := range r.hist {
		c := *h
		c.Counts = append([]uint64(nil), h.Counts...)
		hists[k] = &c
	}
	return counters, gauges, hists
}

// Observe records v in the unlabelled histogram name; it is ObserveL(name, nil, v).
func (r *Registry) Observe(name string, v float64) {
	r.ObserveL(name, nil, v)
}

// ObserveL records v in the histogram series name{labels}, using the bounds the
// name was registered with and LagBuckets when it never was. It takes the
// registry lock once.
func (r *Registry) ObserveL(name string, labels map[string]string, v float64) {
	k := key(name, labels)
	r.lock()
	defer r.mu.Unlock()
	h := r.hist[k]
	if h == nil {
		b, ok := r.bounds[name]
		if !ok {
			b = LagBuckets
		}
		h = &Histogram{Bounds: b, Counts: make([]uint64, len(b)+1)}
		r.hist[k] = h
	}
	i := sort.SearchFloat64s(h.Bounds, v)
	h.Counts[i]++
	h.Sum += v
	h.Samples++
}

// Hist returns a copy of the unlabelled histogram name, or nil.
func (r *Registry) Hist(name string) *Histogram { return r.HistL(name, nil) }

// HistL returns a copy of the histogram series name{labels}, or nil.
func (r *Registry) HistL(name string, labels map[string]string) *Histogram {
	k := key(name, labels)
	r.lock()
	defer r.mu.Unlock()
	h := r.hist[k]
	if h == nil {
		return nil
	}
	c := *h
	c.Counts = append([]uint64(nil), h.Counts...)
	return &c
}
