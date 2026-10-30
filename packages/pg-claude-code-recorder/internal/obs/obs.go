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

// LagBuckets are the upper bounds in seconds of the copy-lag histogram.
var LagBuckets = []float64{0.1, 0.5, 1, 5, 30, 300}

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
}

// New returns an Obs. A nil logger discards logs.
func New(log *slog.Logger) *Obs {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	o := &Obs{Log: log, Reg: NewRegistry()}
	// Registered at zero so the series exists before the removal packet fires it.
	o.Reg.Add(MDroppedBytes, map[string]string{"reason": ReasonRemovalRace}, 0)
	return o
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
	o.events = append(o.events, Event{Name: name, Attrs: attrs})
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

// Observe records v in the named histogram using LagBuckets.
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
