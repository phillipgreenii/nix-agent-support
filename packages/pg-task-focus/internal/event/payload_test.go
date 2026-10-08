package event_test

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// wantTypes is the closed set of event types, in the order of the log's
// documentation.
var wantTypes = []event.Type{
	"period.changed", "profile.changed", "task.materialized", "task.completed",
	"task.skipped", "task.missed", "task.withdrawn", "task.reinstated",
	"cycle.started", "cycle.paused", "cycle.resumed", "cycle.boosted",
	"cycle.stopped", "cycle.annotated", "event.corrected", "event.retracted",
	"batch.committed",
}

func TestTypesListsTheSeventeenEventTypes(t *testing.T) {
	got := event.Types()
	if !reflect.DeepEqual(got, wantTypes) {
		t.Errorf("Types() = %v, want %v", got, wantTypes)
	}
	if len(got) != 17 {
		t.Errorf("len(Types()) = %d, want 17", len(got))
	}
	got[0] = "tampered"
	if event.Types()[0] != "period.changed" {
		t.Error("Types() hands out its own backing array: a caller changed the list")
	}
}

func TestEveryPayloadReportsItsOwnType(t *testing.T) {
	payloads := map[event.Type]event.Payload{
		"period.changed":    event.PeriodChanged{},
		"profile.changed":   event.ProfileChanged{},
		"task.materialized": event.TaskMaterialized{},
		"task.completed":    event.TaskCompleted{},
		"task.skipped":      event.TaskSkipped{},
		"task.missed":       event.TaskMissed{},
		"task.withdrawn":    event.TaskWithdrawn{},
		"task.reinstated":   event.TaskReinstated{},
		"cycle.started":     event.CycleStarted{},
		"cycle.paused":      event.CyclePaused{},
		"cycle.resumed":     event.CycleResumed{},
		"cycle.boosted":     event.CycleBoosted{},
		"cycle.stopped":     event.CycleStopped{},
		"cycle.annotated":   event.CycleAnnotated{},
		"event.corrected":   event.EventCorrected{},
		"event.retracted":   event.EventRetracted{},
		"batch.committed":   event.BatchCommitted{},
	}
	for _, typ := range wantTypes {
		p, ok := payloads[typ]
		if !ok {
			t.Errorf("no payload struct for %s", typ)
			continue
		}
		if got := p.EventType(); got != typ {
			t.Errorf("%T.EventType() = %q, want %q", p, got, typ)
		}
	}
}

func TestTaskIDShape(t *testing.T) {
	cases := []struct {
		cadence    due.Cadence
		start      civil.Date
		definition string
		want       event.TaskID
	}{
		{due.Daily, civil.Date{Year: 2026, Month: 10, Day: 7}, "post-plan", "day:2026-10-07:post-plan"},
		{due.Weekly, civil.Date{Year: 2026, Month: 10, Day: 5}, "weekly-update", "week:2026-10-05:weekly-update"},
		{due.Sprint, civil.Date{Year: 2026, Month: 9, Day: 28}, "capacity-check", "sprint:2026-09-28:capacity-check"},
	}
	for _, c := range cases {
		if got := event.NewTaskID(c.cadence, c.start, c.definition); got != c.want {
			t.Errorf("NewTaskID(%s, %v, %q) = %q, want %q", c.cadence, c.start, c.definition, got, c.want)
		}
	}
}

func TestBatchIDOfEveryPayload(t *testing.T) {
	const b = event.ID("01J9Z3K8M2B000000000000001")
	other := event.ID("01J9Z3K8M2B000000000000002")
	members := []event.Payload{
		event.PeriodChanged{Batch: b},
		event.ProfileChanged{Batch: b},
		event.TaskMaterialized{Batch: b},
		event.TaskSkipped{Batch: b},
		event.TaskMissed{Batch: b},
		event.TaskWithdrawn{Batch: b},
		event.TaskReinstated{Batch: b},
		event.CyclePaused{Batch: b},
		event.CycleResumed{Batch: b},
		event.BatchCommitted{Batch: b},
	}
	for _, p := range members {
		if got := p.BatchID(); got != b {
			t.Errorf("%T{Batch: %q}.BatchID() = %q, want the batch", p, b, got)
		}
	}
	optional := []event.Payload{event.TaskSkipped{}, event.CyclePaused{}, event.CycleResumed{}}
	for _, p := range optional {
		if got := p.BatchID(); got != "" {
			t.Errorf("%T with no batch: BatchID() = %q, want empty", p, got)
		}
	}
	nonMembers := []event.Payload{
		event.TaskCompleted{},
		event.CycleStarted{},
		event.CycleBoosted{},
		event.CycleStopped{},
		event.CycleAnnotated{},
		event.EventCorrected{Target: other},
		event.EventRetracted{Target: other},
		event.EventRetracted{TargetBatch: other},
		event.EventRetracted{Target: other, TargetBatch: other},
	}
	for _, p := range nonMembers {
		if got := p.BatchID(); got != "" {
			t.Errorf("%T: BatchID() = %q, want empty (not a member of a batch)", p, got)
		}
	}
}

func TestEventRetractedHasNoBatchField(t *testing.T) {
	typ := reflect.TypeOf(event.EventRetracted{})
	if _, ok := typ.FieldByName("Batch"); ok {
		t.Error("EventRetracted has a Batch field: batch is membership only, and a retraction names a batch as TargetBatch")
	}
	for _, name := range []string{"Target", "TargetBatch", "Reason"} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("EventRetracted has no %s field", name)
		}
	}
}
