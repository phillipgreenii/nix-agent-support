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

	// State the dispatcher has updated for past fires; producers read these
	// for cancellation/escalation decisions.
	Watermarks WatermarkView
}

// WatermarkView is the read-only slice of nudger state visible to
// producers. The dispatcher owns writes.
type WatermarkView interface {
	WindowResetFiredFor() time.Time
	SessionWatermark(sid string) SessionWatermark
}

type SessionWatermark struct {
	LastNudgedAt        time.Time
	LastDisruptNudgeAt  time.Time
	LastDisruptNudgeFor time.Time
	DisruptEscalated    bool
}

// WindowResetProducer, DisruptProducer, ManualProducer are concrete
// producers; their reconcile bodies live in their own files. Empty
// declarations here so the interface assertions compile.
type WindowResetProducer struct{}
type DisruptProducer struct {
	firstSeen map[string]time.Time // sid -> when this disrupt was first observed
}
type ManualProducer struct{}

func (*DisruptProducer) Reconcile(TickContext, *PendingStore) {}
func (*ManualProducer) Reconcile(TickContext, *PendingStore)  {}
