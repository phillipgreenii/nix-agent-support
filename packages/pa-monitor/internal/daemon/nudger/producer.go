package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// Producer reconciles its own per-session intents against the latest
// snapshot. Reconcile MUST be cancel-then-add: cancel its own keys that
// no longer apply, then add intents for newly-applicable conditions.
type Producer interface {
	Reconcile(ctx TickContext, store *PendingStore)
}

// TickContext carries the per-tick inputs producers read.
type TickContext struct {
	Now               time.Time
	Tree              *aggregate.Tree
	AutoResumeEnabled bool
	AutoResumeMessage string
	AutoResumeDelay   time.Duration
	DisruptGrace      time.Duration
	EscalationAfter   time.Duration

	// AutoSessionWrapUpEnabled / AutoSessionWrapUpIdleThreshold /
	// AutoSessionWrapUpDirectiveTemplate configure AutoSessionWrapUpProducer
	// (bead tc-m08w3). DirectiveTemplate is a fmt template with a single
	// indexed idle-minutes verb (%[1]d), formatted per-session at fire time
	// (see nudger.DefaultDirectiveTemplate for the shipped default).
	AutoSessionWrapUpEnabled           bool
	AutoSessionWrapUpIdleThreshold     time.Duration
	AutoSessionWrapUpDirectiveTemplate string

	// State the dispatcher has updated for past fires; producers read these
	// for cancellation/escalation decisions.
	Watermarks WatermarkView

	// HasSurface reports whether pid currently has a terminal surface a nudge
	// could be delivered to (a resolvable Signaler). It is the producer-side
	// no-surface gate (bead pg2-gjekd): a surfaceless "ghost" session — a dead
	// pid, or a live pid whose cmux pane closed and whose process detached from
	// the cmux server ancestry — has nowhere to deliver, so auto-resume
	// producers REAP it from the candidate set rather than enqueue an intent the
	// dispatcher can only per-tick-suppress ("no_surface"). This is the deeper
	// fix complementing pg2-2o0p7's dispatcher-side suppress-and-drop backstop.
	//
	// The daemon wires this from signal.ResolveSignaler over its full signaler
	// slice (Detect-only, never Send — the same predicate the D5 keep-awake
	// disjunct uses). Nil means "assume a surface is present" so tests and
	// early-startup paths that don't wire a resolver keep their prior behavior.
	HasSurface func(pid int) bool

	// HasUnsubmittedInput reports whether pid's terminal surface currently
	// shows composed-but-not-yet-submitted text (a half-typed prompt).
	// AutoSessionWrapUpProducer's unsubmitted-input guard (bead tc-m08w3,
	// safety-critical): injecting a directive via send-keys+Enter while the
	// user is mid-compose would splice into their half-typed line and
	// force-submit a corrupted message. A nil predicate defaults to false
	// (assume no unsubmitted input) so callers that do not wire a resolver —
	// tests, and any surface type without an inspector — are unaffected. The
	// daemon wires this from signal.HasUnsubmittedInput over its signaler set.
	HasUnsubmittedInput func(pid int) bool
}

// hasSurface reports whether pid has a deliverable surface per the HasSurface
// predicate. A nil predicate defaults to true (surface assumed present) so
// callers that do not wire a resolver are unaffected.
func (ctx TickContext) hasSurface(pid int) bool {
	if ctx.HasSurface == nil {
		return true
	}
	return ctx.HasSurface(pid)
}

// hasUnsubmittedInput reports whether pid's surface currently shows
// composed-but-not-yet-submitted input, per the HasUnsubmittedInput
// predicate. A nil predicate defaults to false (assume clear to send).
func (ctx TickContext) hasUnsubmittedInput(pid int) bool {
	if ctx.HasUnsubmittedInput == nil {
		return false
	}
	return ctx.HasUnsubmittedInput(pid)
}

// WatermarkView is the nudger state visible to producers. Producers may
// call SetDisruptEscalated to persist escalation transitions; all other
// watermark writes remain owned by the dispatcher.
type WatermarkView interface {
	WindowResetFiredFor() time.Time
	// LimitPauseFiredFor returns the account-global FiveHourResetsAt value the
	// limit-pause nudge last fired for. Used by LimitPauseProducer as its
	// once-per-window latch (mirrors WindowResetFiredFor).
	LimitPauseFiredFor() time.Time
	SessionWatermark(sid string) SessionWatermark
	// SetDisruptEscalated persists the escalation flag for sid. Called by
	// DisruptProducer when it detects the session has been stuck past
	// EscalationAfter. Also used to clear the flag (escalated=false) when a
	// fresh error arrives.
	SetDisruptEscalated(sid string, escalated bool)
	// SetAutoSessionWrapUpNudgedFor persists, for sid, the idle-episode-start
	// timestamp (session TranscriptMTime at the moment idleness began) that an
	// AutoSessionWrapUp nudge just fired FOR. Called by AutoSessionWrapUpProducer
	// itself at enqueue time (mirroring SetDisruptEscalated) — this is the
	// once-per-episode latch the no-runaway requirement depends on (bead
	// tc-m08w3): it MUST be a persisted write (survives a daemon restart),
	// never an in-memory-only map, or a restart mid-episode would look "new"
	// and re-fire.
	SetAutoSessionWrapUpNudgedFor(sid string, at time.Time)
}

type SessionWatermark struct {
	LastNudgedAt         time.Time
	LastNudgeSources     []string
	LastDisruptNudgeAt   time.Time
	LastDisruptNudgeFor  time.Time
	DisruptEscalated     bool
	LastDisruptAttemptAt time.Time
	// LastAutoSessionWrapUpNudgedFor is the idle-episode-start timestamp the
	// AutoSessionWrapUp nudge last fired for (bead tc-m08w3). Distinct from
	// LastNudgedAt (which records the wall-clock of ANY nudge) because it must
	// be compared against the CURRENT idle episode's start, not "was there ever
	// a nudge" — see AutoSessionWrapUpProducer.Reconcile.
	LastAutoSessionWrapUpNudgedFor time.Time
}

// WindowResetProducer, DisruptProducer, ManualProducer are concrete
// producers; their reconcile bodies live in their own files. Empty
// declarations here so the interface assertions compile.
type (
	WindowResetProducer struct{}
	LimitPauseProducer  struct{}
	DisruptProducer     struct {
		firstSeen map[string]time.Time // sid -> when this disrupt was first observed
	}
)
type ManualProducer struct{}

// AutoSessionWrapUpProducer nudges idle-but-live sessions to run
// session-wrapup:wrap-up-session before their prompt cache expires (bead
// tc-m08w3). Stateless: the once-per-episode latch lives in the persisted
// WatermarkView, not in-memory, so it survives a daemon restart.
type AutoSessionWrapUpProducer struct{}
