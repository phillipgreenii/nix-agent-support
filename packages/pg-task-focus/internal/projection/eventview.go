package projection

import (
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// EventView is one logged event as the editor shows it. Original is the event
// as it was appended; Corrected is the same event with the live corrections
// applied (equal to Original when none applies). CorrectedBy lists the live
// correcting events in log order, so a retracted correction is absent from it.
// Retracted is set while a live retraction names the event or its batch, and
// RetractedBy is the earliest such retraction.
type EventView struct {
	Original    event.Event
	Corrected   event.Event
	CorrectedBy []event.ID
	Retracted   bool
	RetractedBy event.ID
}

// clone copies the view's CorrectedBy; the events are not copied.
func (v EventView) clone() EventView {
	v.CorrectedBy = slices.Clone(v.CorrectedBy)
	return v
}

// The two views of an EventQuery: the instants as corrected, which is the
// default, and the raw instants as first logged.
const (
	ViewCorrected = "corrected"
	ViewOriginal  = "original"
)

// EventQuery selects events by the effective instant they have in the chosen
// view: From is inclusive, To is exclusive, and a nil bound is open. Types
// keeps only the listed event types (all when empty). View is ViewOriginal for
// the raw instants; any other value, including the empty one, is the corrected
// view (ViewCorrected).
type EventQuery struct {
	From, To *time.Time
	Types    []event.Type
	View     string
}

func (q EventQuery) matches(v EventView) bool {
	e := v.Corrected
	if q.View == ViewOriginal {
		e = v.Original
	}
	t := e.EffectiveAt.Time()
	if q.From != nil && t.Before(*q.From) {
		return false
	}
	if q.To != nil && !t.Before(*q.To) {
		return false
	}
	return len(q.Types) == 0 || slices.Contains(q.Types, v.Original.Payload.EventType())
}
