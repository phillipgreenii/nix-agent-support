package settle

import "time"

// schedAction says what schedule does with a Conversation's deadline.
type schedAction int

const (
	// schedNone: no deadline (removal disabled for a settled Conversation, or an
	// unknown stage).
	schedNone schedAction = iota
	// schedPush: push at into the heap as an ordinary evaluation deadline.
	schedPush
	// schedPushRemoval: push at into the heap as a removal deadline.
	schedPushRemoval
	// schedRemovalNow: the removal deadline is already due and the caller runs a
	// removal pass: mark the session for removal, push nothing.
	schedRemovalNow
)

// schedInput holds the values the deadline of one Conversation depends on.
// newest and checkedAt are functions because the shell reads them lazily:
// newest stats files and checkedAt takes a lock, and each is only needed by
// some stages.
type schedInput struct {
	errored      bool
	stage        string // the persisted stage, "" for none
	removeEnable bool
	removeAfter  time.Duration
	recheck      time.Duration
	after        time.Duration
	now          time.Time
	removalPass  bool // the caller runs a removal pass (removal map non-nil)
	newest       func() time.Time
	checkedAt    func() time.Time
}

// scheduleDecision derives the next deadline from a stage after an evaluation.
func scheduleDecision(in schedInput) (schedAction, time.Time) {
	switch {
	case in.errored:
		return schedPush, in.now
	case in.stage == StageSettled:
		if !in.removeEnable {
			return schedNone, time.Time{}
		}
		at := in.newest().Add(in.removeAfter)
		if in.removalPass && !at.After(in.now) {
			return schedRemovalNow, at
		}
		return schedPushRemoval, at
	case in.stage == StageHeld:
		at := in.checkedAt()
		if at.IsZero() {
			at = in.now
		}
		return schedPush, at.Add(in.recheck)
	case in.stage == "" || in.stage == StageQuiet:
		return schedPush, in.newest().Add(in.after)
	}
	return schedNone, time.Time{}
}
