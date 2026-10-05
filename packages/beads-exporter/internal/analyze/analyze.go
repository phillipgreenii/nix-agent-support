// Package analyze turns the raw bd results of one database into the derived
// quantities the exporter publishes: the disjoint state categorisation, queue
// membership, the type/priority/label aggregates and the oldest-bead
// timestamps. Everything here is a pure function of its inputs; the clock is
// passed in.
package analyze

import (
	"sort"
	"strconv"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

// State is a derived bead state. The six states are disjoint and every
// not-closed bead lands in exactly one.
type State string

// The states, in rule-evaluation order (first match wins).
const (
	InProgress State = "in_progress"
	Deferred   State = "deferred"
	Blocked    State = "blocked"
	Ready      State = "ready"
	Tracking   State = "tracking"
	Other      State = "other"
)

// States lists every state in rule order.
func States() []State {
	return []State{InProgress, Deferred, Blocked, Ready, Tracking, Other}
}

// ReadyExcludedTypes is the set of issue types bd ready never returns although
// the default list view shows them. A bead of one of these types that is not
// claimed, deferred or blocked is "tracking" rather than "other". The contract
// suite pins this set against the real bd so an upgrade cannot silently move
// these beads into "other".
func ReadyExcludedTypes() []string { return []string{"merge-request", "molecule"} }

// Categorize returns the single state of b. ready and blocked are the id sets
// of bd ready and bd blocked. Rules, first match wins:
//
//	in_progress  stored in_progress or hooked
//	deferred     stored deferred, or defer_until strictly in the future
//	blocked      id in bd blocked, or stored blocked
//	ready        id in bd ready
//	tracking     a type bd ready never returns
//	other        anything else
func Categorize(b bd.Bead, ready, blocked map[string]struct{}, now time.Time) State {
	if b.Status == "in_progress" || b.Status == "hooked" {
		return InProgress
	}
	if b.Status == "deferred" || (b.DeferUntil != nil && b.DeferUntil.After(now)) {
		return Deferred
	}
	if _, ok := blocked[b.ID]; ok || b.Status == "blocked" {
		return Blocked
	}
	if _, ok := ready[b.ID]; ok {
		return Ready
	}
	for _, t := range ReadyExcludedTypes() {
		if b.IssueType == t {
			return Tracking
		}
	}
	return Other
}

// UnknownStatuses returns the stored statuses present in beads but absent from
// known, sorted and de-duplicated.
func UnknownStatuses(beads []bd.Bead, known []string) []string {
	have := make(map[string]struct{}, len(known))
	for _, k := range known {
		have[k] = struct{}{}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, b := range beads {
		if _, ok := have[b.Status]; ok {
			continue
		}
		if _, dup := seen[b.Status]; dup {
			continue
		}
		seen[b.Status] = struct{}{}
		out = append(out, b.Status)
	}
	sort.Strings(out)
	return out
}

// IDSet builds an id set from beads.
func IDSet(beads []bd.Bead) map[string]struct{} {
	s := make(map[string]struct{}, len(beads))
	for _, b := range beads {
		s[b.ID] = struct{}{}
	}
	return s
}

// QueueResult is one queue's candidate count and oldest creation time.
type QueueResult struct {
	Name       string
	Candidates int
	Oldest     *time.Time
}

// Input is everything Summarize needs.
type Input struct {
	// Beads is the default list view: every not-closed bead.
	Beads []bd.Bead
	// Ready and Blocked are the bd ready and bd blocked results.
	Ready   []bd.Bead
	Blocked []bd.Bead
	// ClosedCount is the closed total from bd count --by-status.
	ClosedCount int
	// Statuses is the stored-status name set used to zero-fill.
	Statuses []string
	// Queues are the configured queues; QueueBeads maps a spawn-only queue's
	// name to its spawned result. Client-side queues read Ready.
	Queues     []queue.Queue
	QueueBeads map[string][]bd.Bead
	LabelCap   int
	Now        time.Time
}

// LabelCount is one label series.
type LabelCount struct {
	Label string
	Count int
}

// Main is the main pass's derived data for one database.
type Main struct {
	States     map[State]int
	Oldest     map[State]time.Time
	Stored     map[string]int
	Queues     []QueueResult
	ByType     map[string]int
	ByPriority map[string]int
	ByLabel    []LabelCount
}

// Summarize computes the Main data.
func Summarize(in Input) Main {
	ready := IDSet(in.Ready)
	blocked := IDSet(in.Blocked)

	m := Main{
		States:     make(map[State]int, 6),
		Oldest:     map[State]time.Time{},
		Stored:     map[string]int{},
		ByType:     map[string]int{},
		ByPriority: map[string]int{},
	}
	for _, s := range States() {
		m.States[s] = 0
	}
	for _, s := range in.Statuses {
		m.Stored[s] = 0
	}
	m.Stored["closed"] = in.ClosedCount

	for _, b := range in.Beads {
		st := Categorize(b, ready, blocked, in.Now)
		m.States[st]++
		if ts := oldestKey(b, st); ts != nil {
			if cur, ok := m.Oldest[st]; !ok || ts.Before(cur) {
				m.Oldest[st] = *ts
			}
		}
		m.Stored[b.Status]++
		typ := b.IssueType
		if typ == "" {
			typ = "unknown"
		}
		m.ByType[typ]++
		m.ByPriority[strconv.Itoa(b.Priority)]++
	}
	m.ByLabel = LabelCap(in.Beads, in.LabelCap)

	for _, q := range in.Queues {
		var members []bd.Bead
		if q.Class == queue.ClientSide {
			members = q.Filter().Apply(in.Ready)
		} else {
			members = queue.DropTemplates(in.QueueBeads[q.Name])
		}
		qr := QueueResult{Name: q.Name, Candidates: len(members)}
		for _, b := range members {
			if b.CreatedAt != nil && (qr.Oldest == nil || b.CreatedAt.Before(*qr.Oldest)) {
				t := *b.CreatedAt
				qr.Oldest = &t
			}
		}
		m.Queues = append(m.Queues, qr)
	}
	return m
}

// oldestKey is the timestamp that orders "oldest" for a bead: its start time
// for an in-progress bead (falling back to creation when never started), its
// creation time otherwise.
func oldestKey(b bd.Bead, st State) *time.Time {
	if st == InProgress && b.StartedAt != nil {
		return b.StartedAt
	}
	return b.CreatedAt
}

// LabelCap counts, per label, the not-closed beads carrying it and keeps the
// limit most common. The tie-break is deterministic: higher count first, then
// ascending label name. Beads carrying any label outside the kept set are
// counted once, under metrics.OtherLabelValue. A real label that is itself
// named metrics.OtherLabelValue is never kept as its own series (it would
// collide with the remainder); its beads fall into the remainder.
func LabelCap(beads []bd.Bead, limit int) []LabelCount {
	perLabel := map[string]int{}
	for _, b := range beads {
		seen := map[string]struct{}{}
		for _, l := range b.Labels {
			if _, dup := seen[l]; dup {
				continue
			}
			seen[l] = struct{}{}
			perLabel[l]++
		}
	}
	type kv struct {
		label string
		n     int
	}
	var eligible []kv
	for l, n := range perLabel {
		if l == metrics.OtherLabelValue || l == "" {
			continue
		}
		eligible = append(eligible, kv{l, n})
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].n != eligible[j].n {
			return eligible[i].n > eligible[j].n
		}
		return eligible[i].label < eligible[j].label
	})
	if limit < 0 {
		limit = 0
	}
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	kept := make(map[string]struct{}, len(eligible))
	out := make([]LabelCount, 0, len(eligible)+1)
	for _, e := range eligible {
		kept[e.label] = struct{}{}
		out = append(out, LabelCount{Label: e.label, Count: e.n})
	}
	other := 0
	for _, b := range beads {
		for _, l := range b.Labels {
			if _, ok := kept[l]; !ok {
				other++
				break
			}
		}
	}
	if other > 0 {
		out = append(out, LabelCount{Label: metrics.OtherLabelValue, Count: other})
	}
	return out
}

// Samples implements metrics.Emitter.
func (m Main) Samples(db string) []metrics.Sample {
	var out []metrics.Sample
	add := func(family string, v float64, kv ...string) {
		labels := append([]metrics.Label{{Name: "db", Value: db}}, metrics.SeriesLabel(kv...)...)
		out = append(out, metrics.Sample{Family: family, Labels: labels, Value: v})
	}
	for _, s := range States() {
		add(metrics.FamIssues, float64(m.States[s]), "state", string(s))
	}
	for status, n := range m.Stored {
		add(metrics.FamIssuesStored, float64(n), "status", status)
	}
	for _, q := range m.Queues {
		add(metrics.FamQueueCandidates, float64(q.Candidates), "queue", q.Name)
		if q.Oldest != nil {
			add(metrics.FamQueueOldest, Seconds(*q.Oldest), "queue", q.Name)
		}
	}
	for typ, n := range m.ByType {
		add(metrics.FamByType, float64(n), "type", typ)
	}
	for p, n := range m.ByPriority {
		add(metrics.FamByPriority, float64(n), "priority", p)
	}
	for _, l := range m.ByLabel {
		add(metrics.FamByLabel, float64(l.Count), "bead_label", l.Label)
	}
	for _, s := range States() {
		if ts, ok := m.Oldest[s]; ok {
			add(metrics.FamOldest, Seconds(ts), "state", string(s))
		}
	}
	return out
}

// Seconds converts a time to Unix seconds with sub-second precision.
func Seconds(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}

// Throughput is the throughput pass's data for one database.
type Throughput struct {
	Created int
	Closed  int
}

// Samples implements metrics.Emitter.
func (t Throughput) Samples(db string) []metrics.Sample {
	l := []metrics.Label{{Name: "db", Value: db}}
	return []metrics.Sample{
		{Family: metrics.FamCreated24h, Labels: l, Value: float64(t.Created)},
		{Family: metrics.FamClosed24h, Labels: l, Value: float64(t.Closed)},
	}
}
