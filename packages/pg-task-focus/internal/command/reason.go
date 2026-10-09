package command

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// Reason is the code of a refusal. The set is closed: one specific code per
// condition and no catch-all, and every code replay can find
// (projection.Codes) is one of them, with the same spelling.
type Reason string

// The reasons, by status. 400: the request is malformed or incomplete
// (including a cycle verb that does not say which of several cycles it means).
const (
	ReasonInvalidRequest   Reason = "invalid_request"
	ReasonInvalidZone      Reason = "invalid_zone"
	ReasonUnknownProfile   Reason = "unknown_profile"
	ReasonUnknownCycleType Reason = "unknown_cycle_type"
	ReasonReservedKey      Reason = "reserved_key"
	ReasonCycleAmbiguous   Reason = "cycle_ambiguous"
)

// 404: the thing named does not exist.
const (
	ReasonUnknownTask  Reason = "unknown_task"
	ReasonUnknownCycle Reason = "unknown_cycle"
	ReasonUnknownEvent Reason = "unknown_event"
)

// 409: the request conflicts with the current state or with other events.
const (
	ReasonNoRunningCycle             Reason = "no_running_cycle"
	ReasonCycleStopped               Reason = "cycle_stopped"
	ReasonAnotherCycleRunning        Reason = "another_cycle_running"
	ReasonInterruptedCycleNotRunning Reason = "interrupted_cycle_not_running"
	ReasonCycleActive                Reason = "cycle_active"
	ReasonCycleSegmentsOverlap       Reason = "cycle_segments_overlap"
	ReasonBreakEndsAtStop            Reason = "break_ends_at_stop"
	ReasonClockBehindLog             Reason = "clock_behind_log"
	ReasonTaskAlreadyResolved        Reason = "task_already_resolved"
	ReasonTaskWithdrawn              Reason = "task_withdrawn"
	ReasonTaskNotWithdrawn           Reason = "task_not_withdrawn"
	ReasonTaskMaterializedTwice      Reason = "task_materialized_twice"
	ReasonTaskWithoutPeriod          Reason = "task_without_period"
	ReasonTaskBeforeProfile          Reason = "task_before_profile"
	ReasonPeriodUnchanged            Reason = "period_unchanged"
	ReasonIDConflict                 Reason = "id_conflict"
	ReasonStalePreview               Reason = "stale_preview"
	ReasonBatchHasDependents         Reason = "batch_has_dependents"
	ReasonCycleHasDependents         Reason = "cycle_has_dependents"
)

// 422: the request is well formed, but its instant or content is
// unacceptable for its own target.
const (
	ReasonInvalidCorrection               Reason = "invalid_correction"
	ReasonFutureEffectiveAt               Reason = "future_effective_at"
	ReasonStopNotAfterStart               Reason = "stop_not_after_start"
	ReasonEmptyRunningSegment             Reason = "empty_running_segment"
	ReasonCycleEventBeforeStart           Reason = "cycle_event_before_start"
	ReasonResolutionBeforeMaterialization Reason = "resolution_before_materialization"
	ReasonPeriodOutOfOrder                Reason = "period_out_of_order"
)

// 503: the store cannot take the request. not_ready belongs to the daemon,
// which answers it before replay has finished; this library never returns it.
const (
	ReasonStoreUnavailable Reason = "store_unavailable"
	ReasonNotReady         Reason = "not_ready"
)

// statuses is the one error table: each reason and its status.
var statuses = map[Reason]int{
	ReasonInvalidRequest: 400, ReasonInvalidZone: 400, ReasonUnknownProfile: 400,
	ReasonUnknownCycleType: 400, ReasonReservedKey: 400, ReasonCycleAmbiguous: 400,

	ReasonUnknownTask: 404, ReasonUnknownCycle: 404, ReasonUnknownEvent: 404,

	ReasonNoRunningCycle: 409, ReasonCycleStopped: 409, ReasonAnotherCycleRunning: 409,
	ReasonInterruptedCycleNotRunning: 409, ReasonCycleActive: 409, ReasonCycleSegmentsOverlap: 409,
	ReasonBreakEndsAtStop: 409, ReasonClockBehindLog: 409, ReasonTaskAlreadyResolved: 409,
	ReasonTaskWithdrawn: 409, ReasonTaskNotWithdrawn: 409, ReasonTaskMaterializedTwice: 409,
	ReasonTaskWithoutPeriod: 409, ReasonTaskBeforeProfile: 409, ReasonPeriodUnchanged: 409,
	ReasonIDConflict: 409, ReasonStalePreview: 409, ReasonBatchHasDependents: 409,
	ReasonCycleHasDependents: 409,

	ReasonInvalidCorrection: 422, ReasonFutureEffectiveAt: 422, ReasonStopNotAfterStart: 422,
	ReasonEmptyRunningSegment: 422, ReasonCycleEventBeforeStart: 422,
	ReasonResolutionBeforeMaterialization: 422, ReasonPeriodOutOfOrder: 422,

	ReasonStoreUnavailable: 503, ReasonNotReady: 503,
}

// Status is the HTTP status of the reason, as a number: 400, 404, 409, 422 or
// 503. It is 0 for a string that is not one of the reasons.
func (r Reason) Status() int { return statuses[r] }

// Reasons lists every reason, in no particular order. The caller owns the
// slice.
func Reasons() []Reason {
	out := make([]Reason, 0, len(statuses))
	for r := range statuses {
		out = append(out, r)
	}
	return out
}

// CycleRef names a cycle in a rejection: its id and title, its status at the
// end of the log and the instant it started. A cycle whose start is retracted
// now has no status and no start instant.
type CycleRef struct {
	ID        event.CycleID
	Title     string
	Status    projection.CycleStatus
	StartedAt time.Time
}

// Rejection is a refusal: the reason, one plain sentence saying what is wrong,
// and the same facts in structured form. Entity is the stored task or cycle id
// (or period kind) the refusal is about, empty when there is none. Events
// lists only stored events, never the one the request would add (the message
// calls it "the new event"), and Instants always includes the new event's
// effective_at once the request has one. Cycles names the candidates of a
// cycle_ambiguous, the blockers of a cycle_active and the cycles of an
// another_cycle_running, each with its title.
type Rejection struct {
	Reason   Reason
	Message  string
	Entity   string
	Events   []event.ID
	Instants []time.Time
	Cycles   []CycleRef
}

// Error implements error with the message.
func (r *Rejection) Error() string { return r.Message }
