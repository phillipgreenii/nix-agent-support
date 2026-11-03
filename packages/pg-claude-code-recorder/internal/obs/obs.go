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
	MClonePrivateSize  = "clone_private_size_bytes"
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
	MHeartbeat       = "last_sweep_timestamp_seconds"
	MStateFreeBytes  = "state_free_bytes"
	MStateUsedBytes  = "state_used_bytes"
	MRequestsTotal   = "requests_total"
	MQueryDuration   = "query_duration_seconds"
	MQueryTimeouts   = "query_timeouts_total"
	MFeedDuration    = "feed_latency_seconds"
	MWaitDuration    = "wait_duration_seconds"
	MCopiedFiles     = "backfill_files_copied"
	MBackfillFiles   = "backfill_files_total"
	MCaughtUp        = "caught_up"
	MFeedWaiters     = "feed_waiters"
	MIndexRebuilds   = "index_rebuilds_total"
	EvIndexRebuilt   = "index_rebuilt"
	ClientOther      = "other"
	UnknownClientLbl = "client"
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
	MSettleTickExamined      = "settle_tick_examined_total"
	MSettleTickTransitions   = "settle_tick_transitions_total"
	MSettleTickLastDuration  = "settle_tick_last_duration_seconds"
	MSettleTickLastTimestamp = "settle_tick_last_timestamp_seconds"
	EvSettleTick             = "settle_tick"
	EvWALTruncateStart       = "wal_truncate_start"
	EvWALTruncateDone        = "wal_truncate_done"
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

// Loop and outcome label values of loop_runs_total.
const (
	LoopSettleTick    = "settle_tick"
	LoopRefreshGauges = "refresh_gauges"
	LoopWALCheckpoint = "wal_checkpoint"
	OutcomeOK         = "ok"
	OutcomeError      = "error"
)

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
		MHeartbeat, MStateFreeBytes, MStateUsedBytes, MCopyLagBytes, MCopyLagSeconds,
		MCopiedFiles, MBackfillFiles, MCaughtUp, MFeedWaiters,
	} {
		if _, ok := o.Reg.Gauge(g, nil); !ok {
			o.Reg.Set(g, nil, 0)
		}
	}
	for _, l := range []string{LoopSettleTick, LoopRefreshGauges} {
		for _, out := range []string{OutcomeOK, OutcomeError} {
			o.Reg.Add(MLoopRuns, map[string]string{"loop": l, "outcome": out}, 0)
		}
	}
	o.Reg.Add(MSettleTickExamined, nil, 0)
	o.Reg.Add(MSettleTickTransitions, nil, 0)
	o.Reg.Set(MSettleTickLastDuration, nil, 0)
	o.Reg.Set(MSettleTickLastTimestamp, nil, 0)
	for _, h := range []struct {
		name   string
		bounds []float64
	}{
		{MCopyLagHistogram, LagBuckets},
		{MQueryDuration, RequestBuckets},
		{MFeedDuration, RequestBuckets},
		{MWaitDuration, WaitBuckets},
		{MSettleTickDuration, LoopBuckets},
		{MRefreshGaugesDuration, LoopBuckets},
	} {
		o.Reg.RegisterHist(h.name, h.bounds)
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
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
	hist     map[string]*Histogram
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
	return &Registry{counters: map[string]float64{}, gauges: map[string]float64{}, hist: map[string]*Histogram{}}
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
	r.mu.Lock()
	r.counters[key(name, labels)] += v
	r.mu.Unlock()
}

// SetCounter sets a counter to the absolute value v, for a counter whose source
// is itself cumulative (process CPU, GC pause). A v below the current value is
// ignored so the counter never moves backwards.
func (r *Registry) SetCounter(name string, labels map[string]string, v float64) {
	k := key(name, labels)
	r.mu.Lock()
	if cur, ok := r.counters[k]; !ok || v > cur {
		r.counters[k] = v
	}
	r.mu.Unlock()
}

// Counter returns a counter value and whether the series exists.
func (r *Registry) Counter(name string, labels map[string]string) (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.counters[key(name, labels)]
	return v, ok
}

// Set sets a gauge.
func (r *Registry) Set(name string, labels map[string]string, v float64) {
	r.mu.Lock()
	r.gauges[key(name, labels)] = v
	r.mu.Unlock()
}

// Gauge returns a gauge value and whether it was set.
func (r *Registry) Gauge(name string, labels map[string]string) (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.gauges[key(name, labels)]
	return v, ok
}

// RegisterHist registers a histogram with no samples (a no-op when it exists).
func (r *Registry) RegisterHist(name string, bounds []float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hist[name] == nil {
		r.hist[name] = &Histogram{Bounds: bounds, Counts: make([]uint64, len(bounds)+1)}
	}
}

// Snapshot returns copies of every counter, gauge and histogram, keyed by
// "name" or "name{k=v,...}".
func (r *Registry) Snapshot() (counters, gauges map[string]float64, hists map[string]*Histogram) {
	r.mu.Lock()
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

// Observe records v in the named histogram using LagBuckets unless the
// histogram was registered with other bounds.
func (r *Registry) Observe(name string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.hist[name]
	if h == nil {
		h = &Histogram{Bounds: LagBuckets, Counts: make([]uint64, len(LagBuckets)+1)}
		r.hist[name] = h
	}
	i := sort.SearchFloat64s(h.Bounds, v)
	h.Counts[i]++
	h.Sum += v
	h.Samples++
}

// Hist returns a copy of the named histogram, or nil.
func (r *Registry) Hist(name string) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.hist[name]
	if h == nil {
		return nil
	}
	c := *h
	c.Counts = append([]uint64(nil), h.Counts...)
	return &c
}
