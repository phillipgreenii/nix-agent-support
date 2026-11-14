package settle

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// stageFacts are the inputs to decideStage. The plain fields are cheap values
// the caller already holds; the func fields are facts that cost a read (file
// mtimes, the settled manifest, a lock), so decideStage asks for them only on
// the paths that need them and at most once.
type stageFacts struct {
	Live   bool
	HasRow bool
	Row    store.StageRow
	Force  bool
	Now    time.Time

	After   time.Duration // settle.after
	Recheck time.Duration // held.recheck

	// ChangedSinceSettle reports bytes the settled manifest does not describe.
	ChangedSinceSettle func() bool
	// Newest is the newest write time over the Conversation's files.
	Newest func() time.Time
	// CheckedAt is the time of the last settle attempt (zero when none).
	CheckedAt func() time.Time
	// LowSpace reports whether the settle.after gate is waived.
	LowSpace func() bool
}

// stageDecision is what evaluate must do. Clear, when non-empty, is the reason
// of the stage row to clear before anything else. Proceed false means evaluate
// returns Out as is; true means it runs the settle (WasHeld says the row it
// started from was Held) and Out is only the outcome to return if no settle
// is attempted.
type stageDecision struct {
	Clear   string
	Proceed bool
	WasHeld bool
	Out     Outcome
}

// decideStage is the whole transition table of Settler.evaluate over plain
// facts. It performs no effect: the caller does the reads it hands in and the
// store writes the decision asks for.
func decideStage(session string, f stageFacts) stageDecision {
	out := Outcome{Session: session, Stage: StageQuiet}
	if f.Live {
		// Settled to Live and Held to Live: a Process holds it again.
		out.Stage, out.Skipped = StageLive, "live process"
		if f.HasRow {
			return stageDecision{Clear: "process", Out: out}
		}
		return stageDecision{Out: out}
	}
	d := stageDecision{}
	hasRow := f.HasRow
	switch {
	case hasRow && f.Row.Stage == store.StageRemoved:
		// Removed to Quiet: a removed path reappeared.
		d.Clear = "path_reappeared"
		hasRow = false
	case hasRow && (f.Row.Stage == store.StageRemovalPending || f.Row.Stage == store.StageExtracting):
		out.Stage, out.Skipped = f.Row.Stage, "removal or extract in progress"
		d.Out = out
		return d
	}
	if hasRow && f.Row.Stage == StageSettled {
		if !f.ChangedSinceSettle() {
			out.Stage, out.Skipped = StageSettled, "settled"
			d.Out = out
			return d
		}
		// Settled to Quiet: new writes.
		d.Clear = "new_writes"
		hasRow = false
	}
	newest := f.Newest()
	if hasRow && f.Row.Stage == StageHeld {
		if heldHasNewWrites(newest, f.Row.SinceNS) {
			// Held to Quiet: a file was written after the hold.
			d.Clear = "new_writes"
			hasRow = false
		} else if heldRecheckNotDue(f.Force, f.CheckedAt(), f.Now, f.Recheck) {
			out.Stage, out.Skipped, out.Reason = StageHeld, "held, recheck not due", f.Row.Reason
			d.Out = out
			return d
		}
	}
	if !quietAgeReached(f.Now, newest, f.After) && !f.LowSpace() {
		out.Skipped = "quiet age below settle.after"
		d.Out = out
		return d
	}
	d.Out = out
	d.Proceed = true
	d.WasHeld = hasRow && f.Row.Stage == StageHeld
	return d
}
