package core

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/schemas"
)

// The in-memory level projection and the level ingest rules.
//
// Exported names: LevelOwnerOperator, LevelOutcome (LevelAccepted,
// LevelIdempotent, LevelRejected), the rejection reason constants
// (ReasonUnknownResource, ReasonUnknownLevel, ReasonMalformed,
// ReasonNotRegisteredSensor, ReasonFutureObservedAt, ReasonOlderThanHeld,
// ReasonConflict), LevelAnswer, LevelReading, LevelRecord, LevelAppender,
// LevelResource, LevelProjectionConfig, LevelProjection (NewLevelProjection,
// Ingest, IngestJSON, ApplyRecord, SetOverride, ClearOverride, Snapshot,
// Statuses), LevelStatus.
//
// The projection is updated independently of the durable append: Ingest
// appends through the consumer-side LevelAppender seam and takes the receipt
// sequence from the returned log offset (no second counter); replay calls
// ApplyRecord with the stored offset. Level ingest is never gated by a level,
// pause, soft-halt or backoff: nothing in this file consults any of them.

// LevelOwnerOperator is the reserved owner of operator overrides. Level ingest
// rejects it; the operator verbs use SetOverride.
const LevelOwnerOperator = "operator"

// LevelOutcome is the result of ingesting one level answer.
type LevelOutcome string

// The ingest outcomes.
const (
	LevelAccepted   LevelOutcome = "accepted"
	LevelIdempotent LevelOutcome = "idempotent"
	LevelRejected   LevelOutcome = "rejected"
)

// The rejection reasons (the reason label of pg_router_level_events_rejected).
const (
	ReasonUnknownResource     = "unknown_resource"
	ReasonUnknownLevel        = "unknown_level"
	ReasonMalformed           = "malformed"
	ReasonNotRegisteredSensor = "not_registered_sensor"
	ReasonFutureObservedAt    = "future_observed_at"
	ReasonOlderThanHeld       = "older_than_held"
	ReasonConflict            = "conflict"
)

// LevelReading is the optional numeric reading behind a level.
type LevelReading struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// LevelAnswer is one decoded, schema-valid level answer.
type LevelAnswer struct {
	Resource   string        `json:"resource"`
	Level      string        `json:"level"`
	Reading    *LevelReading `json:"reading,omitempty"`
	ObservedAt time.Time     `json:"-"`
	ValidForS  int           `json:"valid_for_s"`
	Owner      string        `json:"owner"`
}

// LevelRecord is the durable form of an accepted level (level_set) or a
// cleared override (level_cleared). ExpiresAt is absolute; zero means no
// expiry (an until-cleared override).
type LevelRecord struct {
	Cleared    bool
	Resource   string
	Level      string
	Owner      string
	Reading    *LevelReading
	ObservedAt time.Time
	ReceivedAt time.Time
	ExpiresAt  time.Time
	ValidForS  int
}

// LevelAppender is the consumer-side seam to the durable log: it appends one
// level record WITHOUT passing the queue admission limit and returns the log
// offset, which is the receipt sequence. The durable-records layer implements it.
type LevelAppender interface {
	Append(rec LevelRecord) (offset int64, err error)
}

// LevelResource is the per-resource ingest configuration: the registered
// sensor (the only owner besides the operator that may report the resource)
// and the grace factor, a multiple of valid_for_s.
type LevelResource struct {
	Sensor      string
	GraceFactor float64
	// NoSensor is true when no sensor of this resource is configured on this
	// machine, so its silence is not a failing sensor: status prints "no sensor
	// configured" for it.
	NoSensor bool
}

// LevelProjectionConfig configures NewLevelProjection. Skew is the bound on
// observed_at ahead of now ([levels] observed_at_skew); Now is the clock seam.
// OnReject, OnBoundary and OnOverrideExpired are optional hooks (metric and
// log sites).
type LevelProjectionConfig struct {
	Registry  levels.Registry
	Resources map[string]LevelResource
	Skew      time.Duration
	Now       func() time.Time
	Appender  LevelAppender

	OnReject          func(reason string)
	OnBoundary        func(resource, from, to string)
	OnOverrideExpired func(resource, owner string)
	// OnOverrideSet is called once per operator override set (level-set, pause).
	OnOverrideSet func(resource, level, owner string, expiresAt time.Time)
}

type levelEntry struct {
	rec LevelRecord
	seq int64
	// since is when the resource entered this entry's level: the previous
	// entry's since when the level did not change, else the record's receipt time.
	since time.Time
}

type levelSlot struct {
	sensor   *levelEntry
	override *levelEntry
	state    string // last reported boundary state: held, stale, unknown
	expired  bool   // override expiry already reported
}

// LevelProjection is the in-memory level state. Safe for concurrent use.
type LevelProjection struct {
	cfg     LevelProjectionConfig
	mu      sync.Mutex
	slots   map[string]*levelSlot
	lastSeq int64
}

// NewLevelProjection returns an empty projection. A nil Now means time.Now.
func NewLevelProjection(cfg LevelProjectionConfig) *LevelProjection {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LevelProjection{cfg: cfg, slots: map[string]*levelSlot{}}
}

// IngestJSON validates the raw JSON of one level answer against the shared
// schema and ingests it; anything that does not validate is rejected as
// malformed and counted.
func (p *LevelProjection) IngestJSON(raw []byte) (LevelOutcome, string, error) {
	if err := schemas.ValidateLevelAnswer(raw); err != nil {
		return p.reject(ReasonMalformed), ReasonMalformed, nil
	}
	var wire struct {
		LevelAnswer
		ObservedAt string `json:"observed_at"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return p.reject(ReasonMalformed), ReasonMalformed, nil
	}
	t, err := time.Parse(time.RFC3339Nano, wire.ObservedAt)
	if err != nil {
		return p.reject(ReasonMalformed), ReasonMalformed, nil
	}
	a := wire.LevelAnswer
	a.ObservedAt = t
	return p.Ingest(a)
}

func (p *LevelProjection) reject(reason string) LevelOutcome {
	if p.cfg.OnReject != nil {
		p.cfg.OnReject(reason)
	}
	return LevelRejected
}

// Ingest applies LVL-2 to a decoded answer. It returns the outcome and, when
// rejected, the reason label. A non-nil error means the durable append failed;
// the projection is still updated (it does not depend on the append).
func (p *LevelProjection) Ingest(a LevelAnswer) (LevelOutcome, string, error) {
	now := p.cfg.Now()
	p.mu.Lock()
	reason := p.checkLocked(a, now)
	if reason == "idempotent" {
		p.mu.Unlock()
		return LevelIdempotent, "", nil
	}
	if reason != "" {
		p.mu.Unlock()
		return p.reject(reason), reason, nil
	}
	rec := LevelRecord{
		Resource: a.Resource, Level: a.Level, Owner: a.Owner, Reading: a.Reading,
		ObservedAt: a.ObservedAt.UTC(), ReceivedAt: now.UTC(),
		ExpiresAt: now.UTC().Add(time.Duration(a.ValidForS) * time.Second), ValidForS: a.ValidForS,
	}
	var appendErr error
	seq := p.lastSeq + 1
	if p.cfg.Appender != nil {
		// Append outside no lock-free path: holding the lock keeps the log
		// order and the projection order the same.
		off, err := p.cfg.Appender.Append(rec)
		if err != nil {
			appendErr = err
		} else {
			seq = off
		}
	}
	p.applyLocked(rec, seq)
	p.mu.Unlock()
	return LevelAccepted, "", appendErr
}

// checkLocked runs the rejection checks; "" means accept, "idempotent" a duplicate.
func (p *LevelProjection) checkLocked(a LevelAnswer, now time.Time) string {
	if a.Owner == LevelOwnerOperator {
		return ReasonNotRegisteredSensor
	}
	res, ok := p.cfg.Resources[a.Resource]
	scale, sok := p.cfg.Registry[a.Resource]
	if !ok || !sok {
		return ReasonUnknownResource
	}
	if a.ValidForS < 1 || a.Owner == "" || a.ObservedAt.IsZero() {
		return ReasonMalformed
	}
	found := false
	for _, l := range scale.Levels {
		if l == a.Level {
			found = true
		}
	}
	if !found {
		return ReasonUnknownLevel
	}
	if res.Sensor == "" || a.Owner != res.Sensor {
		return ReasonNotRegisteredSensor
	}
	if a.ObservedAt.After(now.Add(p.cfg.Skew)) {
		return ReasonFutureObservedAt
	}
	if s := p.slots[a.Resource]; s != nil && s.sensor != nil && s.sensor.rec.Owner == a.Owner {
		held := s.sensor.rec
		switch {
		case a.ObservedAt.Equal(held.ObservedAt):
			if a.Level == held.Level {
				return "idempotent"
			}
			return ReasonConflict
		case a.ObservedAt.Before(held.ObservedAt):
			return ReasonOlderThanHeld
		}
	}
	return ""
}

func (p *LevelProjection) slotLocked(resource string) *levelSlot {
	s := p.slots[resource]
	if s == nil {
		s = &levelSlot{}
		p.slots[resource] = s
	}
	return s
}

func (p *LevelProjection) applyLocked(rec LevelRecord, seq int64) {
	if seq > p.lastSeq {
		p.lastSeq = seq
	}
	s := p.slotLocked(rec.Resource)
	e := &levelEntry{rec: rec, seq: seq, since: rec.ReceivedAt}
	if rec.Owner == LevelOwnerOperator || p.isOperatorOwner(rec) {
		if rec.Cleared {
			s.override = nil
			s.expired = false
			return
		}
		if s.override != nil && seq <= s.override.seq {
			return
		}
		s.override = e
		s.expired = false
		return
	}
	if s.sensor != nil && (seq <= s.sensor.seq || rec.ObservedAt.Before(s.sensor.rec.ObservedAt)) {
		return
	}
	if s.sensor != nil && s.sensor.rec.Level == rec.Level && !s.sensor.since.IsZero() {
		e.since = s.sensor.since
	}
	s.sensor = e
}

// isOperatorOwner reports whether a record's owner is not the registered
// sensor of its resource: any custom owner given to level-set counts as an
// operator source.
func (p *LevelProjection) isOperatorOwner(rec LevelRecord) bool {
	res, ok := p.cfg.Resources[rec.Resource]
	return !ok || res.Sensor == "" || rec.Owner != res.Sensor
}

// ApplyRecord applies one durable record during replay, in log order, with the
// stored offset as the receipt sequence. A lapsed override (expires_at strictly
// before now) is dropped. Records for resources not in the registry are ignored.
func (p *LevelProjection) ApplyRecord(rec LevelRecord, offset int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.cfg.Registry[rec.Resource]; !ok {
		return
	}
	if !rec.Cleared && p.isOperatorOwner(rec) && !rec.ExpiresAt.IsZero() && rec.ExpiresAt.Before(p.cfg.Now()) {
		if offset > p.lastSeq {
			p.lastSeq = offset
		}
		return
	}
	p.applyLocked(rec, offset)
}

// SetOverride stores an operator override (until-cleared when expiresAt is
// zero) and appends it. The owner is the caller's string (default operator).
func (p *LevelProjection) SetOverride(resource, level, owner string, expiresAt time.Time) error {
	if owner == "" {
		owner = LevelOwnerOperator
	}
	now := p.cfg.Now().UTC()
	rec := LevelRecord{Resource: resource, Level: level, Owner: owner, ReceivedAt: now, ObservedAt: now, ExpiresAt: expiresAt.UTC()}
	if expiresAt.IsZero() {
		rec.ExpiresAt = time.Time{}
	}
	if err := p.appendAndApply(rec); err != nil {
		return err
	}
	if p.cfg.OnOverrideSet != nil {
		p.cfg.OnOverrideSet(resource, level, owner, rec.ExpiresAt)
	}
	return nil
}

// ClearOverride removes the operator override of a resource.
func (p *LevelProjection) ClearOverride(resource string) error {
	now := p.cfg.Now().UTC()
	return p.appendAndApply(LevelRecord{Cleared: true, Resource: resource, Owner: LevelOwnerOperator, ReceivedAt: now})
}

// ValidateLevel reports whether resource is in the registry and level is a
// member of its scale (the router checks this; the wire only checks the pattern).
func (p *LevelProjection) ValidateLevel(resource, level string) error {
	scale, ok := p.cfg.Registry[resource]
	if !ok {
		return errors.New("unknown resource " + resource)
	}
	for _, l := range scale.Levels {
		if l == level {
			return nil
		}
	}
	return errors.New("unknown level " + level + " for resource " + resource)
}

// OverrideResources returns, sorted, the resources that hold an operator
// override record (fresh or lapsed but not yet dropped).
func (p *LevelProjection) OverrideResources() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for n, s := range p.slots {
		if s.override != nil {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ClearOverrides removes the operator override of each named resource that holds
// one and returns those resources, in input order. Resources with no override
// are skipped (clearing is idempotent).
func (p *LevelProjection) ClearOverrides(resources []string) ([]string, error) {
	var cleared []string
	for _, r := range resources {
		p.mu.Lock()
		s := p.slots[r]
		has := s != nil && s.override != nil
		p.mu.Unlock()
		if !has {
			continue
		}
		if err := p.ClearOverride(r); err != nil {
			return cleared, err
		}
		cleared = append(cleared, r)
	}
	return cleared, nil
}

func (p *LevelProjection) appendAndApply(rec LevelRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.cfg.Registry[rec.Resource]; !ok {
		return errors.New("core: unknown resource " + rec.Resource)
	}
	seq := p.lastSeq + 1
	var err error
	if p.cfg.Appender != nil {
		off, aerr := p.cfg.Appender.Append(rec)
		if aerr != nil {
			err = aerr
		} else {
			seq = off
		}
	}
	p.forceApplyLocked(rec, seq)
	return err
}

// forceApplyLocked applies an operator record that is live (a clear always
// applies; a set replaces the held override).
func (p *LevelProjection) forceApplyLocked(rec LevelRecord, seq int64) {
	if seq > p.lastSeq {
		p.lastSeq = seq
	}
	s := p.slotLocked(rec.Resource)
	if rec.Cleared {
		s.override = nil
		s.expired = false
		return
	}
	s.override = &levelEntry{rec: rec, seq: seq, since: rec.ReceivedAt}
	s.expired = false
}

// LevelStatus is one resource's current reading for the list surface.
type LevelStatus struct {
	Resource string
	Level    levels.Level
	// Flag is "held", "stale" or "unknown" (no level is in force: the
	// resource has not answered since start, or grace ran out).
	Flag string
}

// effectiveLocked decides the level in force for one resource at now.
func (p *LevelProjection) effectiveLocked(resource string, now time.Time) (levels.Level, string) {
	s := p.slots[resource]
	if s == nil {
		return levels.Level{}, "unknown"
	}
	if o := s.override; o != nil {
		if o.rec.ExpiresAt.IsZero() || !now.After(o.rec.ExpiresAt) {
			return levels.Level{
				Value: o.rec.Level, State: levels.StateHeld, Source: "operator",
				Owner: o.rec.Owner, Since: o.since, ExpireAt: o.rec.ExpiresAt,
			}, "held"
		}
		if !s.expired {
			s.expired = true
			if p.cfg.OnOverrideExpired != nil {
				p.cfg.OnOverrideExpired(resource, o.rec.Owner)
			}
		}
	}
	if s.sensor == nil {
		return levels.Level{}, "unknown"
	}
	r := s.sensor.rec
	valid := time.Duration(r.ValidForS) * time.Second
	grace := time.Duration(float64(valid) * p.cfg.Resources[resource].GraceFactor)
	lv := levels.Level{Value: r.Level, State: levels.StateHeld, Source: "sensor", Owner: r.Owner, Since: s.sensor.since, ExpireAt: r.ExpiresAt}
	stale := now.After(r.ExpiresAt) || r.ReceivedAt.Sub(r.ObservedAt) > valid
	if !stale {
		return lv, "held"
	}
	if now.After(r.ExpiresAt.Add(grace)) {
		return levels.Level{}, "unknown"
	}
	lv.State = levels.StateStale
	return lv, "stale"
}

// evaluateLocked computes every resource at now and reports boundary changes.
func (p *LevelProjection) evaluateLocked(now time.Time) []LevelStatus {
	names := make([]string, 0, len(p.slots))
	for n := range p.slots {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]LevelStatus, 0, len(names))
	for _, n := range names {
		lv, flag := p.effectiveLocked(n, now)
		s := p.slots[n]
		if s.state != flag {
			if s.state != "" && p.cfg.OnBoundary != nil {
				p.cfg.OnBoundary(n, s.state, flag)
			}
			s.state = flag
		}
		out = append(out, LevelStatus{Resource: n, Level: lv, Flag: flag})
	}
	return out
}

// Snapshot is the read accessor handed to the queue (WithLevelSource): the
// levels in force at the current time. A resource with no level in force
// (never answered, or past grace) has NO entry, so the decision function reads
// its unknown value: credentials worst, every other resource best.
func (p *LevelProjection) Snapshot() levels.Snapshot {
	now := p.cfg.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := levels.Snapshot{Now: now, Levels: map[string]levels.Level{}}
	for _, st := range p.evaluateLocked(now) {
		if st.Flag != "unknown" {
			snap.Levels[st.Resource] = st.Level
		}
	}
	return snap
}

// Statuses returns every resource that has ever held a record, with its flag.
func (p *LevelProjection) Statuses() []LevelStatus {
	now := p.cfg.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.evaluateLocked(now)
}

// LevelOverride describes the operator override in force on a resource.
type LevelOverride struct {
	UntilCleared bool
	ExpiresAt    time.Time
}

// LevelDetail is one resource's full row for level list and status. HasLevel is
// false when no level is in force (never answered, or past grace): it prints as
// "no reading".
type LevelDetail struct {
	Resource   string
	HasLevel   bool
	Level      string
	Source     string // sensor, operator or grace_fallback
	Owner      string
	ObservedAt time.Time
	ReceivedAt time.Time
	ExpiresAt  time.Time
	Age        time.Duration
	Stale      bool
	Since      time.Time
	Reading    *LevelReading
	Override   *LevelOverride
	// NoSensor is true when no sensor is configured for the resource.
	NoSensor bool
}

// Details returns one row per registry resource, sorted by name, at the current
// time. It evaluates the projection, so boundary and override-expiry hooks fire
// as for Snapshot.
func (p *LevelProjection) Details() []LevelDetail {
	now := p.cfg.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evaluateLocked(now)
	names := make([]string, 0, len(p.cfg.Registry))
	for n := range p.cfg.Registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]LevelDetail, 0, len(names))
	for _, n := range names {
		d := LevelDetail{Resource: n, NoSensor: p.cfg.Resources[n].NoSensor}
		lv, flag := p.effectiveLocked(n, now)
		if flag != "unknown" {
			s := p.slots[n]
			d.HasLevel, d.Level, d.Owner, d.Since = true, lv.Value, lv.Owner, lv.Since
			if lv.Source == "operator" {
				r := s.override.rec
				d.Source, d.ObservedAt, d.ReceivedAt, d.ExpiresAt = "operator", r.ObservedAt, r.ReceivedAt, r.ExpiresAt
				d.Override = &LevelOverride{UntilCleared: r.ExpiresAt.IsZero(), ExpiresAt: r.ExpiresAt}
			} else {
				r := s.sensor.rec
				d.Source, d.ObservedAt, d.ReceivedAt, d.ExpiresAt, d.Reading = "sensor", r.ObservedAt, r.ReceivedAt, r.ExpiresAt, r.Reading
				if flag == "stale" {
					d.Source, d.Stale = "grace_fallback", true
				}
			}
			d.Age = now.Sub(d.ObservedAt)
			if d.Age < 0 {
				d.Age = 0
			}
		}
		out = append(out, d)
	}
	return out
}
