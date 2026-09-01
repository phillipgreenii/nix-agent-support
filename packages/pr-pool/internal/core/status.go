package core

import (
	"sync"
	"time"
)

// RunMode is the daemon's two operator-facing run modes — interfaces.md's own
// vocabulary for them (INV-LIFE-1), not a third pair of synonyms
// [design: Task 3.5 Contract].
const (
	RunModeLongRunning  = "long-running"
	RunModeDrainAndExit = "drain-and-exit"
)

// TickSnapshot is the published state of one drive-loop pass — either
// long-running `run`'s periodic tick body or drain-and-exit `run-until-idle`'s
// per-pass body (Binding Decision 1) — published by PublishTick and read live
// by Task 3.8's status verb via CurrentTick(). It is the daemon's own state,
// never mutated once published: a caller that wants the NEXT pass's view must
// call CurrentTick() again [design: Task 3.5 Interfaces].
type TickSnapshot struct {
	// Sources is one SourceReport per active [[query]] this run (the
	// post-selector subset --only/--disable already narrowed cfg.Queries to).
	//
	// Freedom-boundary note (Task 3.5, curation flag 2026-09-01): the
	// design's own field comment for this slot reads "from ProduceReport,
	// incl. Rejected", but no packet in this docket defines a
	// ProduceReport/SourceReport type, and Orchestrator.ProduceTick returns
	// only an error today — there is no live per-source rejected count to
	// report yet. SourceReport.Rejected therefore always reads 0 until a
	// later task wires a real per-source producer report through; the field
	// stays on SourceReport now so that later task's consumer does not need
	// to change shape when it lands.
	Sources []SourceReport
	// Config is this pass's small resolved-configuration snapshot. Its field
	// list is this task's own choice (see ResolvedConfig's doc) — the design
	// fixes only that SOME resolved-config snapshot belongs here.
	Config ResolvedConfig
	// RunMode is RunModeLongRunning or RunModeDrainAndExit.
	RunMode string
	// Version is the running binary's stamped version string.
	Version string
	// LastTickAt is when this pass's dispatch/expire work ran.
	LastTickAt time.Time
	// SnapshotAt is when this TickSnapshot value was assembled — distinct
	// from LastTickAt only in principle (today the two callers stamp both
	// from the same `now`), kept separate because a future caller composing
	// a snapshot from cached per-source data may not.
	SnapshotAt time.Time
}

// SourceReport is one active source's per-tick summary (see TickSnapshot.Sources
// for the Rejected freedom-boundary note).
type SourceReport struct {
	// Name is the source's configured name (query.Source.Name).
	Name string
	// Rejected is always 0 today; see TickSnapshot.Sources.
	Rejected int
}

// ResolvedConfig is the small resolved-configuration snapshot embedded in a
// TickSnapshot, read back by Task 3.8's status verb ("config in the reply").
//
// Freedom-boundary note (Task 3.5): ResolvedConfig's field list is this
// task's own choice — it appears exactly once in the design text and nowhere
// in the live worktree as pre-existing code, and no Phase 3 task's Produces
// block defines its fields. The design fixes only that a resolved-config
// snapshot belongs on TickSnapshot.
//
// PollInterval is a pointer so a "drain-and-exit" pass can leave it nil —
// omitted, not merely zero — per Step 7's run-mode gating rule: a one-shot
// drain-to-idle pass has no polling cadence to report, and Task 3.8's status
// IA suppresses tick-derived staleness signals in that mode using this same
// signal [design: Task 3.5 Step 7]. cmd/pr-pool's resolvedConfigFor is what
// sets or omits it based on RunMode.
type ResolvedConfig struct {
	RepoRoot      string
	BeadsPrefix   string
	PollInterval  *time.Duration
	ActiveRoles   int
	ActiveQueries int
}

// GateInfo is one named gate's last-observed state.
type GateInfo struct {
	Set   bool
	Mtime time.Time
	Owner string
}

// gateState is the daemon's per-gate observation cache — its OWN small
// mutex, never the Service's own mu (Task 3.5 Contract): gate state has
// nothing to do with queue dispatch or socket accept, so serializing it
// against those would be pure contention with no correctness benefit.
type gateState struct {
	mu              sync.Mutex
	perGate         map[string]GateInfo
	gatesObservedAt time.Time
}

// PublishTick publishes next as the Service's current tick snapshot. Sole
// callers: cmd/pr-pool's runRun (long-running `run`'s per-tick body, after its
// Dispatch()/Expire() pair) and runRunUntilIdle (`run-until-idle`'s per-pass
// body, Binding Decision 1) [design: Task 3.5 Interfaces].
func (s *Service) PublishTick(next TickSnapshot) {
	s.tick.Store(&next)
}

// CurrentTick returns the Service's most recently published tick snapshot, or
// nil before the first PublishTick call (the boot window). Callers MUST
// nil-check — a nil tick means boot-only state, never a crash
// [design: Task 3.5 Step 4].
func (s *Service) CurrentTick() *TickSnapshot {
	return s.tick.Load()
}

// ObserveGateFromTick records the drive loop's own periodic gate-file read
// (Orchestrator.Gated()'s underlying per-gate file state) at now. This is a
// DRIVE-LOOP write: if a socket pause/resume verb
// (ObserveGateFromSocketVerb) already recorded a STRICTLY NEWER
// gatesObservedAt, this call drops instead of overwriting it — a concurrent
// tick-stat write with an older observation MUST NOT overwrite a socket
// verb's newer one [design: Task 3.5 Step 1].
func (s *Service) ObserveGateFromTick(now time.Time, gates map[string]GateInfo) {
	s.gates.mu.Lock()
	defer s.gates.mu.Unlock()
	if now.Before(s.gates.gatesObservedAt) {
		return
	}
	s.gates.perGate = gates
	s.gates.gatesObservedAt = now
}

// ObserveGateFromSocketVerb records a socket pause/resume verb's write
// (Task 3.9, same package) for the ONE gate it names — it always wins for
// that gate, regardless of gatesObservedAt ordering, and advances
// gatesObservedAt to now if now is newer, so a later drive-loop tick stamped
// with an OLDER now cannot immediately clobber it via
// ObserveGateFromTick's compare rule above.
func (s *Service) ObserveGateFromSocketVerb(now time.Time, gate string, info GateInfo) {
	s.gates.mu.Lock()
	defer s.gates.mu.Unlock()
	if s.gates.perGate == nil {
		s.gates.perGate = make(map[string]GateInfo)
	}
	s.gates.perGate[gate] = info
	if now.After(s.gates.gatesObservedAt) {
		s.gates.gatesObservedAt = now
	}
}

// GateSnapshot returns a live, independent copy of the daemon's per-gate
// observation cache plus the time it was last updated by either writer.
func (s *Service) GateSnapshot() (map[string]GateInfo, time.Time) {
	s.gates.mu.Lock()
	defer s.gates.mu.Unlock()
	out := make(map[string]GateInfo, len(s.gates.perGate))
	for k, v := range s.gates.perGate {
		out[k] = v
	}
	return out, s.gates.gatesObservedAt
}
