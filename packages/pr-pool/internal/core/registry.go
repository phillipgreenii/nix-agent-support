package core

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
)

// Kind is the interface a registered participant speaks (interfaces.md's five
// boundaries, minus INTF-CLI which is the operator, not a registered peer).
type Kind string

// The participant kinds the core registers.
const (
	KindSource  Kind = "source"  // INTF-SOURCE
	KindHandler Kind = "handler" // INTF-HANDLER
	KindMonitor Kind = "monitor" // INTF-MON
	KindStorage Kind = "storage" // INTF-STORE
)

// SelfStatus is a participant's report on ITSELF — the common contract's
// self-status channel (interfaces.md): "Any participant MAY push its own status —
// healthy / degraded / unavailable — over its callback channel, independent of any
// per-item outcome."
//
// This is NOT a handler SESSION's state. The two live in the same doc section and
// are routinely confused, so to be explicit: the per-session channel
// (`session-status`: running / paused / completed / failed) was DROPPED
// 2026-07-28 — pr-pool has no consumer for a post-accept outcome. Self-status
// SURVIVES and pr-pool needs it, because an `unavailable` self-report is a
// PRE-ACCEPT decline that drives the core's re-offer (INV-FAIL-1 / INV-CONC-1).
type SelfStatus string

// The self-status values (interfaces.md "Self-status").
const (
	SelfHealthy     SelfStatus = "healthy"
	SelfDegraded    SelfStatus = "degraded"    // serving, but impaired — still routable
	SelfUnavailable SelfStatus = "unavailable" // cannot take work now — pre-accept decline
)

// ErrUnknownParticipant is returned for an operation on an id that is not in the
// registry (never registered, or already deregistered).
var ErrUnknownParticipant = errors.New("core: unknown participant")

// ErrInvalidRegistration is returned for a structurally unusable registration.
var ErrInvalidRegistration = errors.New("core: invalid registration")

// ParseSelfStatus reads a wire self-status value, rejecting anything outside the
// declared set rather than guessing (INV-INTF-1: report, do not guess).
func ParseSelfStatus(s string) (SelfStatus, error) {
	switch SelfStatus(s) {
	case SelfHealthy, SelfDegraded, SelfUnavailable:
		return SelfStatus(s), nil
	}
	return "", fmt.Errorf("%w: unknown self-status %q (want healthy|degraded|unavailable)", ErrInvalidRegistration, s)
}

// ParseKind reads a wire participant kind, rejecting anything outside the
// declared set.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindSource, KindHandler, KindMonitor, KindStorage:
		return Kind(s), nil
	}
	return "", fmt.Errorf("%w: unknown participant kind %q (want source|handler|monitor|storage)", ErrInvalidRegistration, s)
}

// Registration is one participant's entry in the registry: who it is, what it
// speaks, where it is in the lifecycle, what it says about its own health, and
// the single callback command the core handed it.
type Registration struct {
	ID    string
	Kind  Kind
	State conformance.Lifecycle
	Self  SelfStatus
	// Callback is the event-delivery command string the core handed this
	// participant, with the socket and token already baked in (interfaces.md
	// "Callback"). Empty when the participant's kind has no callback target for
	// this purpose — today only KindSource's `ingest-event` (core.go's
	// ingestCallbackFor).
	Callback string
	// SelfStatusCallback is the `self-status` push callback the core hands EVERY
	// participant, regardless of kind — interfaces.md "Self-status": "Any
	// participant MAY push its own status … over a callback the core hands it."
	// Unlike Callback it is never empty for a valid registration.
	SelfStatusCallback string
	// Subset is the metric catalog subset (by INTF-MON name) a kind=monitor
	// participant may read via `mon.read` (Task 3.6-prereq), resolved from
	// config at registration time by Service.Register and recorded here via
	// SetSubset. Always empty for every OTHER kind.
	Subset       []string
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

// Registry is the core's participant registry (interfaces.md "Registry &
// lifecycle"): a participant registers to receive lifecycle signals and to make
// its callback reachable, and deregisters on exit. Safe for concurrent use — the
// socket accept loop touches it from many goroutines.
//
// The registry deliberately holds NO transport handle. The core reaches a
// participant by running its configured command; the registry records only the
// facts the core reasons about.
type Registry struct {
	mu   sync.Mutex
	now  func() time.Time
	byID map[string]*Registration
}

// NewRegistry returns an empty registry. now is the clock seam (nil ⇒ time.Now).
func NewRegistry(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{now: now, byID: map[string]*Registration{}}
}

// Register adds (or REPLACES) a participant's entry and returns it. A new
// registration starts in `starting` and self-reports `healthy` — the core does
// not route to it until it reaches `started` (INV-INTF-1).
//
// Re-registering an existing id REPLACES the entry rather than failing: a
// participant that crashed and came back presents the same id, the core cannot
// distinguish that from a duplicate, and refusing would lock a restarted
// participant out of the registry forever. The stale entry carries no resources,
// so replacing it is lossless.
//
// callback is the participant's kind-specific event-delivery callback (empty for
// a kind with none). selfStatusCallback is the self-status push callback every
// kind gets (interfaces.md "Self-status") — the caller (Service.Register)
// computes both once from the same socket/token and hands them in together.
func (r *Registry) Register(id string, kind Kind, callback string, selfStatusCallback string) (Registration, error) {
	if id == "" {
		return Registration{}, fmt.Errorf("%w: empty participant id", ErrInvalidRegistration)
	}
	if _, err := ParseKind(string(kind)); err != nil {
		return Registration{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	reg := &Registration{
		ID:                 id,
		Kind:               kind,
		State:              conformance.Starting,
		Self:               SelfHealthy,
		Callback:           callback,
		SelfStatusCallback: selfStatusCallback,
		RegisteredAt:       now,
		UpdatedAt:          now,
	}
	r.byID[id] = reg
	return *reg, nil
}

// Deregister removes a participant (its orderly exit). It reports whether an
// entry was present, so a double-deregister is a no-op rather than an error —
// `stopped` and `crashing` can both reach here for the same participant.
func (r *Registry) Deregister(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.byID[id]
	delete(r.byID, id)
	return ok
}

// SetLifecycle records a participant's lifecycle transition (the signals
// interfaces.md's state diagram declares).
func (r *Registry) SetLifecycle(id string, state conformance.Lifecycle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownParticipant, id)
	}
	reg.State = state
	reg.UpdatedAt = r.now()
	return nil
}

// SetSubset records a kind=monitor participant's resolved metric catalog
// subset (Task 3.6-prereq) — a plain field update, the same shape as
// SetLifecycle/SetSelfStatus, called by Service.Register right after
// Register itself so the caller never observes a registration missing its
// subset.
func (r *Registry) SetSubset(id string, subset []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownParticipant, id)
	}
	reg.Subset = subset
	reg.UpdatedAt = r.now()
	return nil
}

// SetSelfStatus records a participant's report about ITSELF (healthy / degraded /
// unavailable), independent of any per-item outcome.
func (r *Registry) SetSelfStatus(id string, self SelfStatus) error {
	if _, err := ParseSelfStatus(string(self)); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownParticipant, id)
	}
	reg.Self = self
	reg.UpdatedAt = r.now()
	return nil
}

// Get returns a copy of one participant's entry.
func (r *Registry) Get(id string) (Registration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.byID[id]
	if !ok {
		return Registration{}, false
	}
	return *reg, true
}

// List returns every registration, sorted by id so any view built on it (a status
// report, a log line) is deterministic.
func (r *Registry) List() []Registration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Registration, 0, len(r.byID))
	for _, reg := range r.byID {
		out = append(out, *reg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len reports how many participants are registered.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// Available reports whether the core may route to this participant RIGHT NOW:
// registered, `started` (messages cross only there, INV-INTF-1), and not
// self-reporting `unavailable`.
//
// A false here is a PRE-ACCEPT decline, not a delivery failure: the core keeps the
// event and re-offers it while it is unexpired (INV-CONC-1 / INV-FAIL-1, bounded by
// INV-EVT-4), exactly as it
// does for a `busy` exit code. `degraded` stays routable — it is a warning about
// quality, not a refusal of work.
func (r *Registry) Available(id string) bool {
	reg, ok := r.Get(id)
	return ok && reg.State == conformance.Started && reg.Self != SelfUnavailable
}
