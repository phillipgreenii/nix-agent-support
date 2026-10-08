package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// Type is an event type: one of the closed set of seventeen.
type Type string

// The event types.
const (
	TypePeriodChanged    Type = "period.changed"
	TypeProfileChanged   Type = "profile.changed"
	TypeTaskMaterialized Type = "task.materialized"
	TypeTaskCompleted    Type = "task.completed"
	TypeTaskSkipped      Type = "task.skipped"
	TypeTaskMissed       Type = "task.missed"
	TypeTaskWithdrawn    Type = "task.withdrawn"
	TypeTaskReinstated   Type = "task.reinstated"
	TypeCycleStarted     Type = "cycle.started"
	TypeCyclePaused      Type = "cycle.paused"
	TypeCycleResumed     Type = "cycle.resumed"
	TypeCycleBoosted     Type = "cycle.boosted"
	TypeCycleStopped     Type = "cycle.stopped"
	TypeCycleAnnotated   Type = "cycle.annotated"
	TypeEventCorrected   Type = "event.corrected"
	TypeEventRetracted   Type = "event.retracted"
	TypeBatchCommitted   Type = "batch.committed"
)

// Types returns the seventeen event types, in the order the log's
// documentation lists them. The caller owns the slice.
func Types() []Type {
	return []Type{
		TypePeriodChanged, TypeProfileChanged, TypeTaskMaterialized, TypeTaskCompleted,
		TypeTaskSkipped, TypeTaskMissed, TypeTaskWithdrawn, TypeTaskReinstated,
		TypeCycleStarted, TypeCyclePaused, TypeCycleResumed, TypeCycleBoosted,
		TypeCycleStopped, TypeCycleAnnotated, TypeEventCorrected, TypeEventRetracted,
		TypeBatchCommitted,
	}
}

// TaskID names a task instance: <prefix>:<period start>:<definition>.
type TaskID string

// CycleID names a work cycle.
type CycleID string

// NewTaskID derives the id of the task instance a definition becomes in the
// period that starts on periodStart. The prefix is day, week or sprint for
// the daily, weekly and sprint cadences, so the same period and definition
// always name the same task. A cadence outside the closed set is used as its
// own prefix; the callers that take cadences from the configuration have
// already refused it.
func NewTaskID(c due.Cadence, periodStart civil.Date, definition string) TaskID {
	prefix := string(c)
	switch c {
	case due.Daily:
		prefix = "day"
	case due.Weekly:
		prefix = "week"
	case due.Sprint:
		prefix = "sprint"
	}
	return TaskID(prefix + ":" + periodStart.String() + ":" + definition)
}

// Payload is the data of one event. BatchID is the id of the batch group the
// event is a member of: empty for an event that is not a member, and always
// empty for EventRetracted, which names a batch only as TargetBatch. A
// BatchCommitted returns the batch it closes.
type Payload interface {
	EventType() Type
	BatchID() ID
}

// PeriodChanged records that a day, week or sprint began. End is set for a
// week or a sprint and absent for a day. TZ is the zone in force.
type PeriodChanged struct {
	Kind  string      `json:"kind"`
	Start civil.Date  `json:"start"`
	End   *civil.Date `json:"end,omitempty"`
	TZ    string      `json:"tz"`
	Label string      `json:"label,omitempty"`
	Batch ID          `json:"batch,omitempty"`
}

// ProfileChanged records the active profile.
type ProfileChanged struct {
	Profile string `json:"profile"`
	Batch   ID     `json:"batch,omitempty"`
}

// TaskMaterialized records a task definition becoming an instance for a
// period, with the snapshot of everything replay needs: the config is never
// consulted again.
type TaskMaterialized struct {
	TaskID     TaskID      `json:"task_id"`
	Definition string      `json:"definition"`
	Cadence    due.Cadence `json:"cadence"`
	Period     civil.Date  `json:"period"`
	Title      string      `json:"title"`
	Group      string      `json:"group,omitempty"`
	Link       string      `json:"link,omitempty"`
	Due        Instant     `json:"due"`
	DueRule    due.Rule    `json:"due_rule"`
	Batch      ID          `json:"batch,omitempty"`
}

// TaskCompleted records a task being done.
type TaskCompleted struct {
	TaskID TaskID `json:"task_id"`
}

// TaskSkipped records a task deliberately not done. Reason is non-blank.
type TaskSkipped struct {
	TaskID TaskID `json:"task_id"`
	Reason string `json:"reason"`
	Batch  ID     `json:"batch,omitempty"`
}

// TaskMissed records an open task left behind at a rollover.
type TaskMissed struct {
	TaskID TaskID `json:"task_id"`
	Batch  ID     `json:"batch,omitempty"`
}

// TaskWithdrawn records a task leaving the active profile.
type TaskWithdrawn struct {
	TaskID TaskID `json:"task_id"`
	Batch  ID     `json:"batch,omitempty"`
}

// TaskReinstated records a withdrawn task returning to the active profile.
type TaskReinstated struct {
	TaskID TaskID `json:"task_id"`
	Batch  ID     `json:"batch,omitempty"`
}

// CycleStarted records a cycle beginning, with the snapshot of its type's
// title and planned minutes. Interrupts names the cycle it paused.
type CycleStarted struct {
	CycleID        CycleID `json:"cycle_id"`
	Type           string  `json:"type"`
	Title          string  `json:"title"`
	PlannedMinutes int     `json:"planned_minutes"`
	Interrupts     CycleID `json:"interrupts,omitempty"`
}

// CyclePaused records a cycle ceasing to run.
type CyclePaused struct {
	CycleID CycleID `json:"cycle_id"`
	Batch   ID      `json:"batch,omitempty"`
}

// CycleResumed records a paused cycle running again.
type CycleResumed struct {
	CycleID CycleID `json:"cycle_id"`
	Batch   ID      `json:"batch,omitempty"`
}

// CycleBoosted records minutes added to a cycle.
type CycleBoosted struct {
	CycleID CycleID `json:"cycle_id"`
	Minutes int     `json:"minutes"`
}

// CycleStopped records a cycle ending.
type CycleStopped struct {
	CycleID CycleID `json:"cycle_id"`
}

// KV is one key and value of a cycle's annotation. Keys may repeat.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CycleAnnotated records the cycle's whole note and key/value list; the latest
// annotation replaces the earlier ones.
type CycleAnnotated struct {
	CycleID CycleID `json:"cycle_id"`
	Note    string  `json:"note,omitempty"`
	KV      []KV    `json:"kv,omitempty"`
}

// EventCorrected records replacement values for an earlier event. Fields holds
// whole replacement values by key: effective_at and the target's non-identity
// data keys. Which keys may be corrected is the correction rules' concern, not
// the codec's.
type EventCorrected struct {
	Target ID                         `json:"target"`
	Fields map[string]json.RawMessage `json:"fields"`
	Reason string                     `json:"reason,omitempty"`
}

// EventRetracted records that an earlier event or a whole batch no longer
// counts. Exactly one of Target and TargetBatch is set. It has no Batch: a
// batch is only ever its target, so a retraction is never a member of one and
// can itself be retracted.
type EventRetracted struct {
	Target      ID     `json:"target,omitempty"`
	TargetBatch ID     `json:"target_batch,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// BatchCommitted closes the batch Batch.
type BatchCommitted struct {
	Batch ID `json:"batch"`
}

// EventType implements Payload.
func (PeriodChanged) EventType() Type { return TypePeriodChanged }

// EventType implements Payload.
func (ProfileChanged) EventType() Type { return TypeProfileChanged }

// EventType implements Payload.
func (TaskMaterialized) EventType() Type { return TypeTaskMaterialized }

// EventType implements Payload.
func (TaskCompleted) EventType() Type { return TypeTaskCompleted }

// EventType implements Payload.
func (TaskSkipped) EventType() Type { return TypeTaskSkipped }

// EventType implements Payload.
func (TaskMissed) EventType() Type { return TypeTaskMissed }

// EventType implements Payload.
func (TaskWithdrawn) EventType() Type { return TypeTaskWithdrawn }

// EventType implements Payload.
func (TaskReinstated) EventType() Type { return TypeTaskReinstated }

// EventType implements Payload.
func (CycleStarted) EventType() Type { return TypeCycleStarted }

// EventType implements Payload.
func (CyclePaused) EventType() Type { return TypeCyclePaused }

// EventType implements Payload.
func (CycleResumed) EventType() Type { return TypeCycleResumed }

// EventType implements Payload.
func (CycleBoosted) EventType() Type { return TypeCycleBoosted }

// EventType implements Payload.
func (CycleStopped) EventType() Type { return TypeCycleStopped }

// EventType implements Payload.
func (CycleAnnotated) EventType() Type { return TypeCycleAnnotated }

// EventType implements Payload.
func (EventCorrected) EventType() Type { return TypeEventCorrected }

// EventType implements Payload.
func (EventRetracted) EventType() Type { return TypeEventRetracted }

// EventType implements Payload.
func (BatchCommitted) EventType() Type { return TypeBatchCommitted }

// BatchID implements Payload.
func (p PeriodChanged) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (p ProfileChanged) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (p TaskMaterialized) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (TaskCompleted) BatchID() ID { return "" }

// BatchID implements Payload.
func (p TaskSkipped) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (p TaskMissed) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (p TaskWithdrawn) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (p TaskReinstated) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (CycleStarted) BatchID() ID { return "" }

// BatchID implements Payload.
func (p CyclePaused) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (p CycleResumed) BatchID() ID { return p.Batch }

// BatchID implements Payload.
func (CycleBoosted) BatchID() ID { return "" }

// BatchID implements Payload.
func (CycleStopped) BatchID() ID { return "" }

// BatchID implements Payload.
func (CycleAnnotated) BatchID() ID { return "" }

// BatchID implements Payload.
func (EventCorrected) BatchID() ID { return "" }

// BatchID implements Payload; a retraction is never a member of a batch.
func (EventRetracted) BatchID() ID { return "" }

// BatchID implements Payload; it is the batch the event closes.
func (p BatchCommitted) BatchID() ID { return p.Batch }

// maxMinutes bounds every minutes value, which keeps duration arithmetic far
// from overflow.
const maxMinutes = 525600

// The Go semantic checks. Decode runs them on every line it reads and Encode
// on every event it writes, so a line the library writes is a line it reads.
// Which fields are required is checked here; the rules that need other events
// (a known target, a legal timeline) belong to the projection.

func requireText(field, s string) error {
	if s == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func requireID(field string, id ID) error {
	if id == "" {
		return fmt.Errorf("%s is required", field)
	}
	return optionalID(field, id)
}

func optionalID(field string, id ID) error {
	if id == "" {
		return nil
	}
	if _, err := ParseID(string(id)); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}

func checkMinutes(field string, n int) error {
	if n < 1 || n > maxMinutes {
		return fmt.Errorf("%s %d is outside 1 to %d", field, n, maxMinutes)
	}
	return nil
}

func (p PeriodChanged) validate() error {
	switch p.Kind {
	case "day":
		if p.End != nil {
			return errors.New("a day period takes no end")
		}
	case "week", "sprint":
		if p.End == nil {
			return fmt.Errorf("a %s period requires end", p.Kind)
		}
	default:
		return fmt.Errorf("period kind %q is not one of day, week, sprint", p.Kind)
	}
	if p.Start == (civil.Date{}) {
		return errors.New("start is required")
	}
	if _, err := zone.Load(p.TZ); err != nil {
		return fmt.Errorf("tz: %w", err)
	}
	return optionalID("batch", p.Batch)
}

func (p ProfileChanged) validate() error {
	if err := requireText("profile", p.Profile); err != nil {
		return err
	}
	return optionalID("batch", p.Batch)
}

func (p TaskMaterialized) validate() error {
	if err := requireText("task_id", string(p.TaskID)); err != nil {
		return err
	}
	if err := requireText("definition", p.Definition); err != nil {
		return err
	}
	switch p.Cadence {
	case due.Daily, due.Weekly, due.Sprint:
	default:
		return fmt.Errorf("cadence %q is not one of daily, weekly, sprint", string(p.Cadence))
	}
	if p.Period == (civil.Date{}) {
		return errors.New("period is required")
	}
	if time.Time(p.Due).IsZero() {
		return errors.New("due is required")
	}
	if p.DueRule.TZ.Name() == "" {
		return errors.New("due_rule is required")
	}
	if err := p.DueRule.Validate(p.Cadence); err != nil {
		return fmt.Errorf("due_rule: %w", err)
	}
	return optionalID("batch", p.Batch)
}

func (p TaskCompleted) validate() error {
	return requireText("task_id", string(p.TaskID))
}

func (p TaskSkipped) validate() error {
	if err := requireText("task_id", string(p.TaskID)); err != nil {
		return err
	}
	if _, err := ValidReason(p.Reason); err != nil {
		return fmt.Errorf("reason: %w", err)
	}
	return optionalID("batch", p.Batch)
}

func (p TaskMissed) validate() error {
	if err := requireText("task_id", string(p.TaskID)); err != nil {
		return err
	}
	return optionalID("batch", p.Batch)
}

func (p TaskWithdrawn) validate() error {
	if err := requireText("task_id", string(p.TaskID)); err != nil {
		return err
	}
	return optionalID("batch", p.Batch)
}

func (p TaskReinstated) validate() error {
	if err := requireText("task_id", string(p.TaskID)); err != nil {
		return err
	}
	return optionalID("batch", p.Batch)
}

func (p CycleStarted) validate() error {
	if err := requireText("cycle_id", string(p.CycleID)); err != nil {
		return err
	}
	if err := requireText("type", p.Type); err != nil {
		return err
	}
	return checkMinutes("planned_minutes", p.PlannedMinutes)
}

func (p CyclePaused) validate() error {
	if err := requireText("cycle_id", string(p.CycleID)); err != nil {
		return err
	}
	return optionalID("batch", p.Batch)
}

func (p CycleResumed) validate() error {
	if err := requireText("cycle_id", string(p.CycleID)); err != nil {
		return err
	}
	return optionalID("batch", p.Batch)
}

func (p CycleBoosted) validate() error {
	if err := requireText("cycle_id", string(p.CycleID)); err != nil {
		return err
	}
	return checkMinutes("minutes", p.Minutes)
}

func (p CycleStopped) validate() error {
	return requireText("cycle_id", string(p.CycleID))
}

func (p CycleAnnotated) validate() error {
	if err := requireText("cycle_id", string(p.CycleID)); err != nil {
		return err
	}
	for i, kv := range p.KV {
		if kv.Key == "" {
			return fmt.Errorf("kv[%d].key is required", i)
		}
	}
	return nil
}

func (p EventCorrected) validate() error {
	if err := requireID("target", p.Target); err != nil {
		return err
	}
	if p.Fields == nil {
		return errors.New("fields is required")
	}
	return nil
}

func (p EventRetracted) validate() error {
	switch {
	case p.Target != "" && p.TargetBatch != "":
		return errors.New("a retraction names exactly one of target and target_batch, not both")
	case p.Target == "" && p.TargetBatch == "":
		return errors.New("a retraction names exactly one of target and target_batch")
	}
	if err := optionalID("target", p.Target); err != nil {
		return err
	}
	return optionalID("target_batch", p.TargetBatch)
}

func (p BatchCommitted) validate() error {
	return requireID("batch", p.Batch)
}
