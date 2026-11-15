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

	// HasSurface reports whether a nudge can currently be delivered to pid:
	// the pid is alive and the control mod keeps an inbox for it. It is the
	// producer-side gate: a session the mod cannot reach (dead pid, or started
	// without the mod) is REAPED from the candidate set rather than enqueued
	// as an intent the dispatcher can only suppress.
	//
	// The daemon wires this from the tick's tree (recorder control_inbox fact,
	// or a direct inbox-FIFO check when the recorder is unavailable). Nil means
	// "assume deliverable" so tests and early-startup paths that do not wire a
	// resolver keep their prior behavior.
	HasSurface func(pid int) bool
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
