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

// LagBuckets are the upper bounds in seconds of the copy-lag histogram.
var LagBuckets = []float64{0.1, 0.5, 1, 5, 30, 300}

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
	for _, g := range []string{
		MHeartbeat, MStateFreeBytes, MStateUsedBytes, MCopyLagBytes, MCopyLagSeconds,
		MCopiedFiles, MBackfillFiles, MCaughtUp, MFeedWaiters,
	} {
		if _, ok := o.Reg.Gauge(g, nil); !ok {
			o.Reg.Set(g, nil, 0)
		}
	}
	for _, h := range []struct {
		name   string
		bounds []float64
	}{{MCopyLagHistogram, LagBuckets}, {MQueryDuration, RequestBuckets}, {MFeedDuration, RequestBuckets}, {MWaitDuration, WaitBuckets}} {
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
