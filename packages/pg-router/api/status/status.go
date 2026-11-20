// Package status is the published contract of the router's status reply (the
// cli.status-reply message): the one exported Reply type the core composes and
// the status command, doctor, explain and the TUI decode, the schema-version
// constant, and the codec.
//
// It is a contract package: it holds the message types, the constants and the
// codec and nothing else. It imports the standard library only; the closure
// test fails when that stops being true. The JSON Schema
// schemas/cli.status-reply.schema.json stays hand-written; the both-ways test
// in this package keeps the type and the schema in step.
package status

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the message-schema envelope version every router request and
// reply carries. It exists once, here; schemas, wireclient and query read it.
const SchemaVersion = "1"

// Reply is the cli.status-reply message. Optional wire properties are omitted
// when empty (tags omitempty/omitzero); the others are always present.
type Reply struct {
	SchemaVersion string     `json:"schemaVersion"`
	Deliveries    []Delivery `json:"deliveries"`
	Queues        []Queue    `json:"queues"`
	Config        Config     `json:"config"`

	Core CoreInfo `json:"core"`
	// Mode is the run mode; absent before the first tick.
	Mode string `json:"mode,omitempty"`

	Dispatch Dispatch `json:"dispatch"`
	QueueLog QueueLog `json:"queueLog"`

	// ResolvedConfig and the tick times are absent before the first tick.
	ResolvedConfig *ResolvedConfig `json:"resolvedConfig,omitempty"`
	// Counters is absent when no metrics reader is wired.
	Counters *Counters `json:"counters,omitempty"`

	// Levels is every resource's level in force; PausedRoles are the roles a
	// level requirement blocks right now.
	Levels      []Level      `json:"levels"`
	PausedRoles []PausedRole `json:"pausedRoles"`

	Registry  []Registration `json:"registry"`
	Listeners []Listener     `json:"listeners"`
	Sources   []Source       `json:"sources"`
	Busy      Busy           `json:"busy"`

	UnmatchedBindings []string        `json:"unmatchedBindings"`
	Activity          []ActivityEntry `json:"activity"`
	ActivityDropped   bool            `json:"activityDropped"`

	// AsOf is when the core composed this reply; the levels are read live at
	// that instant, so it doubles as their freshness stamp.
	AsOf       time.Time `json:"asOf"`
	LastTickAt time.Time `json:"lastTickAt,omitzero"`
	SnapshotAt time.Time `json:"snapshotAt,omitzero"`
	// TickIntervalMs is absent in drain-and-exit mode (no polling cadence).
	TickIntervalMs int64 `json:"tickIntervalMs,omitempty"`
}

// Delivery is one entry of deliveries[] (always empty today).
type Delivery struct {
	ID      string `json:"id"`
	Handler string `json:"handler"`
	Event   string `json:"event"`
}

// Queue is one entry of queues[]: a per-type depth.
type Queue struct {
	Type  string `json:"type"`
	Depth int    `json:"depth"`
}

// Config is the legacy config object: active source and handler counts.
type Config struct {
	Sources  int `json:"sources"`
	Handlers int `json:"handlers"`
}

// CoreInfo is the core object: the running core's identity and lifecycle state.
type CoreInfo struct {
	State string `json:"state,omitempty"`
	// Version is absent before the first tick.
	Version    string    `json:"version,omitempty"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	ConfigPath string    `json:"configPath"`
}

// Dispatch is the real-time fan-out concurrency pair.
type Dispatch struct {
	Busy  int `json:"busy"`
	Total int `json:"total"`
}

// QueueLog is the durable write-ahead log's size, compaction count and
// size-limit state.
type QueueLog struct {
	Bytes       int64 `json:"bytes"`
	Compactions int64 `json:"compactions"`
	// LimitBytes, SoftBytes and Percent are present only when a hard limit is
	// configured.
	LimitBytes     int64   `json:"limitBytes,omitempty"`
	SoftBytes      int64   `json:"softBytes,omitempty"`
	Percent        float64 `json:"percent,omitempty"`
	State          string  `json:"state"`
	EmittersHalted bool    `json:"emittersHalted"`
	Rejected       struct {
		LogFull       int64 `json:"logFull"`
		LogUnwritable int64 `json:"logUnwritable"`
	} `json:"rejected"`
	Detail string `json:"detail,omitempty"`
	// LastCompaction is nil before the first compaction.
	LastCompaction *LastCompaction `json:"lastCompaction,omitempty"`
}

// LastCompaction is queueLog.lastCompaction.
type LastCompaction struct {
	At            time.Time `json:"at"`
	Trigger       string    `json:"trigger"`
	BytesBefore   int64     `json:"bytesBefore"`
	BytesAfter    int64     `json:"bytesAfter"`
	RecordsBefore int       `json:"recordsBefore"`
	RecordsAfter  int       `json:"recordsAfter"`
	DurationMs    int64     `json:"durationMs"`
}

// ResolvedConfig is the small resolved-configuration snapshot of the last tick.
type ResolvedConfig struct {
	RepoRoot    string `json:"repoRoot"`
	BeadsPrefix string `json:"beadsPrefix"`
	// PollIntervalMs is absent in drain-and-exit mode.
	PollIntervalMs int `json:"pollIntervalMs,omitempty"`
	ActiveRoles    int `json:"activeRoles"`
	ActiveQueries  int `json:"activeQueries"`
	// PerParticipant is opaque and always an object.
	PerParticipant map[string]any `json:"perParticipant"`
}

// Counters are the delivery-side counters, keyed by event type.
type Counters struct {
	UnconsumedExpired   map[string]int64 `json:"unconsumedExpired"`
	UnknownTypeRejected map[string]int64 `json:"unknownTypeRejected"`
	Deduped             map[string]int64 `json:"deduped"`
}

// Level is one resource's level in force. Only Resource is always present; the
// rest is absent for a resource with no reading.
type Level struct {
	Resource   string         `json:"resource"`
	Level      string         `json:"level,omitempty"`
	Source     string         `json:"source,omitempty"` // sensor, operator or grace_fallback
	Owner      string         `json:"owner,omitempty"`
	ObservedAt time.Time      `json:"observed_at,omitzero"`
	ReceivedAt time.Time      `json:"received_at,omitzero"`
	ExpiresAt  time.Time      `json:"expires_at,omitzero"`
	AgeS       int64          `json:"age_s,omitempty"`
	Stale      bool           `json:"stale,omitempty"`
	NoSensor   bool           `json:"no_sensor,omitempty"`
	Since      time.Time      `json:"since,omitzero"`
	Reading    *LevelReading  `json:"reading,omitempty"`
	Override   *LevelOverride `json:"override,omitempty"`
}

// LevelReading is the numeric reading behind a level.
type LevelReading struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// LevelOverride marks an operator override; ExpiresAt is absent until cleared.
type LevelOverride struct {
	UntilCleared bool      `json:"until_cleared"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
}

// PausedRole is a role a level requirement blocks right now.
type PausedRole struct {
	Role    string          `json:"role"`
	Failing []FailingClause `json:"failing"`
}

// FailingClause is one failing requires clause; Kind is level, unknown or stale.
type FailingClause struct {
	Clause   string        `json:"clause"`
	Resource string        `json:"resource"`
	Level    string        `json:"level,omitempty"`
	Kind     string        `json:"kind"`
	Source   string        `json:"source,omitempty"`
	Owner    string        `json:"owner,omitempty"`
	Since    time.Time     `json:"since,omitzero"`
	Allowed  []string      `json:"allowed,omitempty"`
	Reading  *LevelReading `json:"reading,omitempty"`
}

// Registration is one self-reported participant.
type Registration struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Self  string `json:"self"`
	// Requires is absent for a registration without requires clauses.
	Requires []RequiresClause `json:"requires,omitempty"`
}

// RequiresClause is one requires clause with the current verdict (pass or fail).
type RequiresClause struct {
	Clause   string   `json:"clause"`
	Resource string   `json:"resource"`
	Allowed  []string `json:"allowed"`
	Verdict  string   `json:"verdict"`
	Level    string   `json:"level,omitempty"`
	Kind     string   `json:"kind,omitempty"`
}

// Backoff is a listener's backoff state; the wire value is null when the
// listener is not backing off.
type Backoff struct {
	Streak       int       `json:"streak"`
	NextEligible time.Time `json:"nextEligible"`
}

// Progress is the state behind the role-progress gauges.
type Progress struct {
	AgeSeconds   float64 `json:"ageSeconds"`
	Unsuccessful int64   `json:"unsuccessful"`
	Busy         int64   `json:"busy"`
}

// Listener is one configured role, including a selector-excluded one.
type Listener struct {
	Role      string   `json:"role"`
	Binds     []string `json:"binds"`
	Enabled   bool     `json:"enabled"`
	Excluded  bool     `json:"excluded"`
	Delivered int64    `json:"delivered"`
	Declined  int64    `json:"declined"`
	// LastDeliveredAtMs is Unix millis of the last delivery; 0 means never.
	LastDeliveredAtMs int64 `json:"lastDeliveredAtMs"`
	// DeclinedByReason is a second, additive breakdown of Declined.
	DeclinedByReason map[string]int64 `json:"declinedByReason"`
	HandlerFailures  int64            `json:"handlerFailures"`
	Backoff          *Backoff         `json:"backoff"`
	// SelfReportState is healthy, degraded or unavailable; empty when the role
	// never self-reported.
	SelfReportState   string `json:"selfReportState"`
	UnavailableReason string `json:"unavailableReason"`
	InFlight          bool   `json:"inFlight"`
	InFlightEventType string `json:"inFlightEventType"`
	Busy              bool   `json:"busy"`
	Description       string `json:"description"`
	// Progress is nil for a role with no progress series.
	Progress *Progress `json:"progress"`
}

// Failure is a source's failure-backoff state; the wire value is null when the
// source has none.
type Failure struct {
	Count        int       `json:"count"`
	NextEligible time.Time `json:"nextEligible"`
}

// CalendarLast is the last handled slot of a calendar source.
type CalendarLast struct {
	Slot    string `json:"slot"`
	Outcome string `json:"outcome"` // fired, catch_up or skipped
	Reason  string `json:"reason,omitempty"`
}

// Calendar is the schedule view of a calendar-triggered source.
type Calendar struct {
	Schedule string        `json:"schedule"`
	OnMissed string        `json:"onMissed"`
	Next     string        `json:"next"`
	Last     *CalendarLast `json:"last,omitempty"`
}

// Source is one configured source, including a selector-excluded one.
type Source struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Enabled  bool   `json:"enabled"`
	Excluded bool   `json:"excluded"`
	Mode     string `json:"mode"`
	// LastTick is absent for a source that never fired in this process.
	LastTick           time.Time `json:"lastTick,omitzero"`
	Failure            *Failure  `json:"failure"`
	ExpectedIntervalMs int64     `json:"expectedIntervalMs"`
	InFlight           bool      `json:"inFlight"`
	Busy               bool      `json:"busy"`
	Description        string    `json:"description"`
	// Calendar is set only for a calendar-triggered source.
	Calendar *Calendar `json:"calendar,omitempty"`
}

// Busy names the roles and the sources that are busy right now.
type Busy struct {
	Roles   []string `json:"roles"`
	Sources []string `json:"sources"`
}

// ActivityEntry is one entry of the activity ring, oldest first.
type ActivityEntry struct {
	Seq         uint64    `json:"seq"`
	StartedAt   time.Time `json:"startedAt"`
	Type        string    `json:"type"`
	Outcome     string    `json:"outcome"`
	Participant string    `json:"participant"`
}

// DeclinedBucketed buckets DeclinedByReason into (busy, unavailable, other).
// Any other key, including a detail override, folds into other.
func (l Listener) DeclinedBucketed() (busy, unavailable, other int64) {
	for reason, n := range l.DeclinedByReason {
		switch reason {
		case "busy":
			busy += n
		case "unavailable":
			unavailable += n
		default:
			other += n
		}
	}
	return busy, unavailable, other
}

// Encode marshals r as the wire body. Array and object properties that are
// always present on the wire are written as empty, never null, when nil.
func Encode(r Reply) ([]byte, error) {
	if r.SchemaVersion == "" {
		r.SchemaVersion = SchemaVersion
	}
	r.normalize()
	return json.Marshal(r)
}

// Decode strictly decodes a wire body: an unknown property is an error.
func Decode(data []byte) (Reply, error) {
	var r Reply
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Reply{}, fmt.Errorf("decode status reply: %w", err)
	}
	return r, nil
}

func (r *Reply) normalize() {
	if r.Deliveries == nil {
		r.Deliveries = []Delivery{}
	}
	if r.Queues == nil {
		r.Queues = []Queue{}
	}
	if r.Levels == nil {
		r.Levels = []Level{}
	}
	if r.PausedRoles == nil {
		r.PausedRoles = []PausedRole{}
	}
	for i := range r.PausedRoles {
		if r.PausedRoles[i].Failing == nil {
			r.PausedRoles[i].Failing = []FailingClause{}
		}
	}
	if r.Registry == nil {
		r.Registry = []Registration{}
	}
	if r.Listeners == nil {
		r.Listeners = []Listener{}
	}
	for i := range r.Listeners {
		l := &r.Listeners[i]
		if l.Binds == nil {
			l.Binds = []string{}
		}
		if l.DeclinedByReason == nil {
			l.DeclinedByReason = map[string]int64{}
		}
	}
	if r.Sources == nil {
		r.Sources = []Source{}
	}
	if r.Busy.Roles == nil {
		r.Busy.Roles = []string{}
	}
	if r.Busy.Sources == nil {
		r.Busy.Sources = []string{}
	}
	if r.UnmatchedBindings == nil {
		r.UnmatchedBindings = []string{}
	}
	if r.Activity == nil {
		r.Activity = []ActivityEntry{}
	}
	if rc := r.ResolvedConfig; rc != nil && rc.PerParticipant == nil {
		rc.PerParticipant = map[string]any{}
	}
	if c := r.Counters; c != nil {
		if c.UnconsumedExpired == nil {
			c.UnconsumedExpired = map[string]int64{}
		}
		if c.UnknownTypeRejected == nil {
			c.UnknownTypeRejected = map[string]int64{}
		}
		if c.Deduped == nil {
			c.Deduped = map[string]int64{}
		}
	}
}
