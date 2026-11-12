package eventqueue

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"
)

// This file is the queue's level-based admission (the replacement of the removed
// gate mechanism). Levels govern ADMISSION only: a level change never stops,
// signals, pauses or deprioritises running or accepted work.
//
// The evaluation (levels.Decide over a level snapshot, the registry and one
// requirement) is called from exactly four sites: snapshotLocked and
// chainNextLocked in queue.go (both under q.mu), and CheckPull and
// EmitterBlockedBy below (lock-free readers of the injected level source).
//
// The queue is given what it needs by injection: WithLevelSource (the live
// snapshot), WithRegistry (the scales, static configuration), WithStartStagger,
// WithLevelObserver, and per-participant requirements through RegisterListener
// and RegisterEmitter. With no level source the queue blocks nothing.
//
// Exported names: LevelObserver, LevelBlockedError, ErrLevelBlocked,
// ReasonStartStagger, WithLevelSource, WithRegistry, WithStartStagger,
// WithLevelObserver, Queue.RegisterListener, Queue.RegisterEmitter,
// Queue.CheckPull, Queue.EmitterBlockedBy, Queue.Explain, Queue.Levels, Queue.Now,
// PausedRole, Queue.PausedRoles, Queue.IdleExceptPaused.

// ReasonStartStagger is the explanation reason of a listener held back by the
// start stagger (ADM-2). It is not a requirement block.
const ReasonStartStagger = "start_stagger"

// LevelObserver receives the queue's admission signals for metric and log
// emission. It is separate from Observer; install one with WithLevelObserver.
// Every hook fires AFTER q.mu is released.
type LevelObserver interface {
	// OnRequirementBlocked fires each time a requirement stops a participant
	// from doing work it had: work is "dispatch" (a blocked listener with
	// deliverable work was skipped), "poll" (a blocked emitter was not polled)
	// or "pull" (a blocked listener's pull was rejected); resource is the first
	// failing resource and kind says why its clause failed: "level" (a measured
	// level), "unknown" (no level held) or "stale" (a held level past its lease).
	OnRequirementBlocked(participant, work, resource, kind string)
	// OnRequirementDrop fires when an event goes away for a blocked listener at
	// its final attempt (INV-EVT-4, ADM-7).
	OnRequirementDrop(evtType, role, resource string)
	// OnRolePaused fires when a listener turns from admitted to blocked, naming
	// the first failing resource and the level it is at; OnRoleResumed fires when
	// it turns back, naming the resource that had blocked it and the level that
	// resource is at now.
	OnRolePaused(role, resource, level string)
	OnRoleResumed(role, resource, level string)
	// OnLegacyGateDropped fires once per ACTIVE legacy gate a compaction pass
	// drops: the gate's type, owner and expiry (zero: no expiry).
	OnLegacyGateDropped(gateType, owner string, expiresAt time.Time)
}

type noopLevelObserver struct{}

func (noopLevelObserver) OnRequirementBlocked(string, string, string, string) {}
func (noopLevelObserver) OnRequirementDrop(string, string, string)            {}
func (noopLevelObserver) OnRolePaused(string, string, string)                 {}
func (noopLevelObserver) OnRoleResumed(string, string, string)                {}
func (noopLevelObserver) OnLegacyGateDropped(string, string, time.Time)       {}

// admission is the queue's level admission state.
type admission struct {
	src       func() levels.Snapshot
	registry  levels.Registry
	obs       LevelObserver
	interval  time.Duration
	startedAt time.Time
	// reqs and emitters are copy-on-write maps replaced under q.mu and read
	// lock-free (the pull and poll paths never contend on the queue lock).
	reqs     atomic.Pointer[map[string]levels.Requirement]
	emitters atomic.Pointer[map[string][]levels.Consumer]
}

func newAdmission() admission { return admission{obs: noopLevelObserver{}} }

// WithLevelSource installs the live level snapshot accessor.
func WithLevelSource(src func() levels.Snapshot) Option {
	return func(q *Queue) { q.adm.src = src }
}

// WithRegistry installs the resource scales.
func WithRegistry(reg levels.Registry) Option {
	return func(q *Queue) { q.adm.registry = reg }
}

// WithStartStagger sets the router start stagger interval (ADM-2); zero or
// negative disables it.
func WithStartStagger(d time.Duration) Option {
	return func(q *Queue) { q.adm.interval = d }
}

// WithLevelObserver installs a LevelObserver. Install it before the queue is
// built: the startup compaction reports dropped legacy gates through it.
func WithLevelObserver(o LevelObserver) Option {
	return func(q *Queue) { q.adm.obs = o }
}

// RegisterListener records the requirement of the listener id. A listener
// without one admits always.
func (q *Queue) RegisterListener(id string, req levels.Requirement) {
	q.mu.Lock()
	defer q.mu.Unlock()
	next := map[string]levels.Requirement{}
	if cur := q.adm.reqs.Load(); cur != nil {
		for k, v := range *cur {
			next[k] = v
		}
	}
	next[id] = req
	q.adm.reqs.Store(&next)
}

// RegisterEmitter records the consumers of the emitter id.
func (q *Queue) RegisterEmitter(id string, consumers []levels.Consumer) {
	q.mu.Lock()
	defer q.mu.Unlock()
	next := map[string][]levels.Consumer{}
	if cur := q.adm.emitters.Load(); cur != nil {
		for k, v := range *cur {
			next[k] = v
		}
	}
	next[id] = append([]levels.Consumer(nil), consumers...)
	q.adm.emitters.Store(&next)
}

// Now returns the queue's own clock reading, so a caller computing a time
// relative to the queue agrees with it under an injected test clock.
func (q *Queue) Now() time.Time { return q.now() }

// LevelBlockedError is returned when a requirement stops a participant's
// request (a listener's pull): it names the failing resource and level.
type LevelBlockedError struct {
	Participant string
	Result      levels.Result
}

func (e *LevelBlockedError) Error() string {
	if len(e.Result.Failing) == 0 {
		return fmt.Sprintf("participant %q is blocked by its requirements", e.Participant)
	}
	f := e.Result.Failing[0]
	return fmt.Sprintf("participant %q is blocked by %s at level %s (%s)", e.Participant, f.Resource, f.Level, f.ClauseText)
}

// ErrLevelBlocked lets a caller test errors.Is(err, ErrLevelBlocked).
var ErrLevelBlocked = errors.New("blocked by levels")

// Is makes *LevelBlockedError match ErrLevelBlocked.
func (e *LevelBlockedError) Is(target error) bool { return target == ErrLevelBlocked }

// admissionView is the snapshot one pass judges every listener against.
type admissionView struct {
	snap levels.Snapshot
	has  bool
}

func (q *Queue) loadView() admissionView {
	if q.adm.src == nil {
		return admissionView{}
	}
	return admissionView{snap: q.adm.src(), has: true}
}

// admissionViewLocked reads the level source once for a whole pass.
func (q *Queue) admissionViewLocked() admissionView { return q.loadView() }

func (q *Queue) requirement(id string) levels.Requirement {
	if cur := q.adm.reqs.Load(); cur != nil {
		return (*cur)[id]
	}
	return levels.Requirement{}
}

func firstResource(res levels.Result) string {
	if len(res.Failing) == 0 {
		return ""
	}
	return res.Failing[0].Resource
}

// firstKind is why the first failing clause failed (level, unknown or stale).
func firstKind(res levels.Result) string {
	if len(res.Failing) == 0 {
		return ""
	}
	return string(res.Failing[0].Kind)
}

// firstLevel is the level the first failing clause was judged against.
func firstLevel(res levels.Result) string {
	if len(res.Failing) == 0 {
		return ""
	}
	return res.Failing[0].Level
}

// levelOfResource is the level resource reads at in view: its held level, else
// the scale's unknown value.
func (q *Queue) levelOfResource(view admissionView, resource string) string {
	if lv, ok := view.snap.Levels[resource]; ok {
		return lv.Value
	}
	return q.adm.registry[resource].Unknown
}

// admissionBlock is one blocked dispatch observed while snapshotting a pass.
type admissionBlock struct{ participant, resource, kind string }

// admissionDrop is one event lost to a blocked listener at its final attempt.
type admissionDrop struct{ evtType, role, resource string }

type admissionPause struct{ role, resource, level string }

type admissionResume struct{ role, resource, level string }

// admissionSignals collects the observer notifications a locked phase produces,
// to be fired after q.mu is released.
type admissionSignals struct {
	drops   []admissionDrop
	blocks  []admissionBlock
	paused  []admissionPause
	resumed []admissionResume
}

func (q *Queue) fireAdmissionSignals(s admissionSignals) {
	for _, p := range s.paused {
		q.adm.obs.OnRolePaused(p.role, p.resource, p.level)
	}
	for _, r := range s.resumed {
		q.adm.obs.OnRoleResumed(r.role, r.resource, r.level)
	}
	for _, b := range s.blocks {
		q.adm.obs.OnRequirementBlocked(b.participant, "dispatch", b.resource, b.kind)
	}
	for _, d := range s.drops {
		q.adm.obs.OnRequirementDrop(d.evtType, d.role, d.resource)
	}
}

// levelAdmitLocked is the evaluation at the two locked sites. It reports the
// decision and, when sig is non-nil, records the paused / resumed transition of
// ls. Caller holds q.mu.
func (q *Queue) levelAdmitLocked(ls *listenerState, view admissionView, _ time.Time, sig *admissionSignals) (levels.Result, bool) {
	if !view.has {
		return levels.Result{Admit: true}, false
	}
	res := levels.Decide(view.snap, q.adm.registry, q.requirement(ls.l.ID()))
	blocked := !res.Admit
	if sig != nil {
		switch {
		case blocked && ls.blockedBy == "":
			ls.blockedBy = firstResource(res)
			sig.paused = append(sig.paused, admissionPause{role: ls.l.ID(), resource: ls.blockedBy, level: firstLevel(res)})
		case !blocked && ls.blockedBy != "":
			sig.resumed = append(sig.resumed, admissionResume{role: ls.l.ID(), resource: ls.blockedBy, level: q.levelOfResource(view, ls.blockedBy)})
			ls.blockedBy = ""
		}
	}
	return res, blocked
}

// dropBlockedLocked settles, for the blocked listener ls, every event it is
// owed a FINAL attempt on (the event is already expired, INV-EVT-4) but cannot
// be offered because a requirement blocks it: the event "goes away for it"
// (ADM-7). Events not yet expired stay queued. It returns whether ls still has
// deliverable work (a blocked DISPATCH, for the metric). Caller holds q.mu.
func (q *Queue) dropBlockedLocked(ls *listenerState, res levels.Result, now time.Time, sig *admissionSignals) (blockedWork bool) {
	lid := ls.l.ID()
	resource := firstResource(res)
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok || e.settled[lid] || !ls.l.Matches(e.evt) {
			continue
		}
		if e.evt.Expired(now) {
			e.settled[lid] = true
			if ls.declineEventID == e.evt.ID {
				ls.resetBackoff()
			}
			sig.drops = append(sig.drops, admissionDrop{evtType: e.evt.Type, role: lid, resource: resource})
			continue
		}
		blockedWork = true
	}
	return blockedWork
}

// staggeredLocked reports whether the start stagger (ADM-2) holds ls back at
// now. It is stateful and not part of the pure decision: a registration is
// staggered only while it has a clause on a resource that is not at its best
// level, only until it has been offered once, never on a final-attempt offer,
// and only inside the stagger window. Registration i of n (in registration
// order, rotated by the start time in Unix seconds modulo n) may start
// rank x interval after the queue started. Nothing is persisted. Caller holds
// q.mu.
func (q *Queue) staggeredLocked(ls *listenerState, view admissionView, now time.Time, final bool) bool {
	if final || ls.staggerDone || q.adm.interval <= 0 || !view.has {
		return false
	}
	if !q.hasNonBestClause(q.requirement(ls.l.ID()), view) {
		return false
	}
	n := len(q.listeners)
	idx := -1
	for i, o := range q.listeners {
		if o == ls {
			idx = i
			break
		}
	}
	if idx < 0 || n == 0 {
		return false
	}
	offset := int(q.adm.startedAt.Unix() % int64(n))
	rank := ((idx-offset)%n + n) % n
	return now.Before(q.adm.startedAt.Add(time.Duration(rank) * q.adm.interval))
}

// hasNonBestClause reports whether req has a clause on a resource whose
// current level is not the best of its scale.
func (q *Queue) hasNonBestClause(req levels.Requirement, view admissionView) bool {
	for _, c := range req.Clauses {
		sc, ok := q.adm.registry[c.Resource]
		if !ok || len(sc.Levels) == 0 {
			continue
		}
		cur := sc.Unknown
		if lv, held := view.snap.Levels[c.Resource]; held {
			cur = lv.Value
		}
		if cur != sc.Levels[0] {
			return true
		}
	}
	return false
}

// Levels returns the current level snapshot from the injected level source (the
// zero snapshot when there is none). It is a READER for the operator surface (the
// pause notice, status); the admission decision is never made from it.
func (q *Queue) Levels() levels.Snapshot {
	if q.adm.src == nil {
		return levels.Snapshot{Now: q.now()}
	}
	return q.adm.src()
}

// Explain returns the admission explanation of the listener id as of now: the
// decision and its failing clauses, with Reason set to ReasonStartStagger when
// the only thing holding the listener back is the start stagger.
func (q *Queue) Explain(listenerID string) levels.Result {
	q.mu.Lock()
	defer q.mu.Unlock()
	view := q.loadView()
	for _, ls := range q.listeners {
		if ls.l.ID() != listenerID {
			continue
		}
		res, blocked := q.levelAdmitLocked(ls, view, q.now(), nil)
		if !blocked && q.staggeredLocked(ls, view, q.now(), false) {
			res.Reason = ReasonStartStagger
		}
		return res
	}
	return levels.Result{Admit: true} // not a registered listener
}

// CheckPull is the admission check for a listener's PULL request (site 3 of 4):
// it returns a *LevelBlockedError naming the failing resource when listenerID
// is blocked, nil otherwise. A rejection is counted via OnRequirementBlocked.
func (q *Queue) CheckPull(listenerID string) error {
	view := q.loadView()
	if !view.has {
		return nil
	}
	res := levels.Decide(view.snap, q.adm.registry, q.requirement(listenerID))
	if res.Admit {
		return nil
	}
	q.adm.obs.OnRequirementBlocked(listenerID, "pull", firstResource(res), firstKind(res))
	return &LevelBlockedError{Participant: listenerID, Result: res}
}

// EmitterBlockedBy reports whether levels stop the named emitter from being
// polled, and the first failing resource (site 4 of 4). The emitter is polled
// when ANY enabled consumer could run; an emitter with no registered or no
// enabled consumer is not blocked here (it can over-poll, never under-poll).
// The caller never asks about the timer emitter or a level-source query.
func (q *Queue) EmitterBlockedBy(emitterID string) (string, bool) {
	view := q.loadView()
	if !view.has {
		return "", false
	}
	cur := q.adm.emitters.Load()
	if cur == nil {
		return "", false
	}
	var first, firstWhy string
	enabled := 0
	for _, c := range (*cur)[emitterID] {
		if !c.Enabled {
			continue
		}
		enabled++
		res := levels.Decide(view.snap, q.adm.registry, c.Req)
		if res.Admit {
			return "", false
		}
		if first == "" {
			first, firstWhy = firstResource(res), firstKind(res)
		}
	}
	if enabled == 0 {
		return "", false
	}
	q.adm.obs.OnRequirementBlocked(emitterID, "poll", first, firstWhy)
	return first, true
}

// logLegacyGateDropped reports one dropped active legacy gate to the level
// observer, once per gate, from the compaction pass itself. The observer
// formats the log line; the queue writes none.
func (q *Queue) logLegacyGateDropped(g DroppedGate) {
	q.adm.obs.OnLegacyGateDropped(g.Type, g.Owner, g.ExpiresAt)
}

// PausedRole is one listener a requirement holds back right now, with the
// failing clauses that explain it.
type PausedRole struct {
	Role   string
	Result levels.Result
}

// PausedRoles returns every registered listener the level requirements block as
// of now, in registration order. It is a read-only evaluation for the operator
// surfaces (status, explain, run-until-idle): it goes through Explain, so it
// records no paused / resumed transition and fires no observer hook, and the
// evaluation stays at its admission sites. The start stagger is not a block.
func (q *Queue) PausedRoles() []PausedRole {
	q.mu.Lock()
	ids := make([]string, 0, len(q.listeners))
	for _, ls := range q.listeners {
		ids = append(ids, ls.l.ID())
	}
	q.mu.Unlock()
	var out []PausedRole
	for _, id := range ids {
		if res := q.Explain(id); !res.Admit {
			out = append(out, PausedRole{Role: id, Result: res})
		}
	}
	return out
}

// IdleExceptPaused is Idle with the work held back by level requirements set
// aside: a listener a requirement blocks is not waited on, and an unexpired event
// that only a blocked listener could still take does not hold the queue open.
// run-until-idle exits on it and reports the blocked listeners as "paused by
// levels". Caller must NOT hold q.mu.
func (q *Queue) IdleExceptPaused() bool {
	blocked := map[string]bool{}
	for _, p := range q.PausedRoles() {
		blocked[p.Role] = true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for _, ls := range q.listeners {
		if !blocked[ls.l.ID()] && q.headFor(ls.l) != nil {
			return false
		}
	}
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok || e.evt.Expired(now) || len(e.accepted) > 0 {
			continue
		}
		heldByBlocked := false
		for _, ls := range q.listeners {
			if blocked[ls.l.ID()] && !e.settled[ls.l.ID()] && ls.l.Matches(e.evt) {
				heldByBlocked = true
				break
			}
		}
		if !heldByBlocked {
			return false
		}
	}
	return true
}

// BlockedParticipant is one registered listener or emitter a level requirement
// holds back right now, with how far each unmet resource is from clearing.
type BlockedParticipant struct {
	Participant string
	Shortfalls  []levels.Shortfall
}

// BlockedRequirements returns every registered listener and emitter a level
// requirement blocks as of now, listeners in registration order, then emitters
// in name order. It is the read-only feed of the requirement_blocked_seconds
// metric: like PausedRoles it goes through the admission decision's own inputs
// (levels.Shortfalls reads the same unmet clauses levels.Decide does), records no
// paused / resumed transition, fires no observer hook and takes q.mu only to copy
// the listener ids. The start stagger is not a block. An emitter is blocked only
// when every enabled consumer is (EmitterBlockedBy); its shortfalls are those of
// the consumer closest to admitting (fewest unmet resources, then smallest total
// steps with an unknown or none distance counting as stepsPenalty, then the
// first registered).
func (q *Queue) BlockedRequirements() []BlockedParticipant {
	view := q.loadView()
	if !view.has {
		return nil
	}
	q.mu.Lock()
	ids := make([]string, 0, len(q.listeners))
	for _, ls := range q.listeners {
		ids = append(ids, ls.l.ID())
	}
	q.mu.Unlock()
	var out []BlockedParticipant
	for _, id := range ids {
		if sf := levels.Shortfalls(view.snap, q.adm.registry, q.requirement(id)); len(sf) > 0 {
			out = append(out, BlockedParticipant{Participant: id, Shortfalls: sf})
		}
	}
	cur := q.adm.emitters.Load()
	if cur == nil {
		return out
	}
	names := make([]string, 0, len(*cur))
	for name := range *cur {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		var best []levels.Shortfall
		enabled, blocked := 0, true
		for _, c := range (*cur)[name] {
			if !c.Enabled {
				continue
			}
			enabled++
			sf := levels.Shortfalls(view.snap, q.adm.registry, c.Req)
			if len(sf) == 0 {
				blocked = false
				break
			}
			if best == nil || closer(sf, best) {
				best = sf
			}
		}
		if enabled > 0 && blocked {
			out = append(out, BlockedParticipant{Participant: name, Shortfalls: best})
		}
	}
	return out
}

// stepsPenalty stands in for an unknown or none distance when comparing
// consumers, so a measured distance always beats an unmeasured one.
const stepsPenalty = 1 << 20

func totalSteps(sf []levels.Shortfall) int {
	t := 0
	for _, s := range sf {
		if s.Steps < 0 {
			t += stepsPenalty
		} else {
			t += s.Steps
		}
	}
	return t
}

// closer reports whether a is strictly closer to admitting than b: fewer unmet
// resources, else a smaller total distance.
func closer(a, b []levels.Shortfall) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return totalSteps(a) < totalSteps(b)
}
