package projection

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func mustModel(t *testing.T, events []event.Event) *Model {
	t.Helper()
	m, err := newModel(events)
	if err != nil {
		t.Fatalf("newModel: %v", err)
	}
	return m
}

func viewIDs(views []EventView) []event.ID {
	ids := make([]event.ID, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.Original.ID)
	}
	return ids
}

func TestNewModelRefusesAnInvalidLog(t *testing.T) {
	_, err := newModel([]event.Event{retract(1, 0, eid(5))})
	if inv := asInvalid(t, err); inv.Code != codeUnknownEvent {
		t.Errorf("code = %q, want %q", inv.Code, codeUnknownEvent)
	}
}

func TestEventsQueryCorrectedAndOriginalViews(t *testing.T) {
	log := []event.Event{
		boost(1, 0, 10),
		boost(2, 1, 20),
		correct(3, 2, eid(1), "minutes", 15, "effective_at", event.At(at(30))),
		retract(4, 3, eid(2)),
	}
	m := mustModel(t, log)

	corrected := m.Events(EventQuery{View: "corrected"})
	if got, want := viewIDs(corrected), []event.ID{eid(1), eid(2), eid(3), eid(4)}; !equalIDs(got, want) {
		t.Fatalf("corrected view ids = %v, want every event of the log in log order %v", got, want)
	}
	first := corrected[0]
	if first.Original.ID != eid(1) || minutesOf(t, first.Original) != 10 {
		t.Errorf("original of the corrected event = %s with minutes %d, want %s with 10", first.Original.ID, minutesOf(t, first.Original), eid(1))
	}
	if minutesOf(t, first.Corrected) != 15 || !first.Corrected.EffectiveAt.Time().Equal(at(30)) {
		t.Errorf("corrected = minutes %d at %v, want 15 at %v", minutesOf(t, first.Corrected), first.Corrected.EffectiveAt.Time(), at(30))
	}
	if !equalIDs(first.CorrectedBy, []event.ID{eid(3)}) {
		t.Errorf("CorrectedBy = %v, want [%s]", first.CorrectedBy, eid(3))
	}
	if first.Retracted {
		t.Error("the corrected live event is flagged retracted")
	}
	second := corrected[1]
	if !second.Retracted || second.RetractedBy != eid(4) {
		t.Errorf("retracted event view = Retracted %v by %q, want retracted by %s", second.Retracted, second.RetractedBy, eid(4))
	}
	if len(second.CorrectedBy) != 0 || minutesOf(t, second.Corrected) != 20 {
		t.Errorf("an uncorrected event shows CorrectedBy %v and minutes %d, want none and its own 20", second.CorrectedBy, minutesOf(t, second.Corrected))
	}

	// The original view flags retraction too and keeps the raw event in both fields' reach.
	original := m.Events(EventQuery{View: "original"})
	if got := viewIDs(original); !equalIDs(got, viewIDs(corrected)) {
		t.Errorf("original view ids = %v, want the same events as the corrected view %v", got, viewIDs(corrected))
	}
	if !original[1].Retracted {
		t.Error("the original view does not flag the retracted event")
	}
	if minutesOf(t, original[0].Original) != 10 {
		t.Errorf("original view shows minutes %d, want the raw 10", minutesOf(t, original[0].Original))
	}
}

func TestEventsQueryFilters(t *testing.T) {
	log := []event.Event{
		boost(1, 0, 10),
		completed(2, 10),
		boost(3, 20, 30),
		correct(4, 25, eid(1), "effective_at", event.At(at(40))),
	}
	m := mustModel(t, log)
	from, to := at(10), at(20)
	tests := []struct {
		name string
		q    EventQuery
		want []event.ID
	}{
		{"no filter lists everything", EventQuery{}, []event.ID{eid(1), eid(2), eid(3), eid(4)}},
		{"from is inclusive, to is exclusive", EventQuery{From: &from, To: &to}, []event.ID{eid(2)}},
		{"from alone", EventQuery{From: &to}, []event.ID{eid(1), eid(3), eid(4)}},
		{"to alone", EventQuery{To: &from}, []event.ID{}},
		{"a type", EventQuery{Types: []event.Type{event.TypeTaskCompleted}}, []event.ID{eid(2)}},
		{"two types", EventQuery{Types: []event.Type{event.TypeTaskCompleted, event.TypeEventCorrected}}, []event.ID{eid(2), eid(4)}},
		{"the corrected view filters on the corrected instant", EventQuery{View: "corrected", From: &to, To: ptr(at(50))}, []event.ID{eid(1), eid(3), eid(4)}},
		{"the original view filters on the raw instant", EventQuery{View: "original", From: &from, To: &to}, []event.ID{eid(2)}},
		{"the original view keeps a moved event where it was", EventQuery{View: "original", To: &from}, []event.ID{eid(1)}},
		{"the corrected view moves it", EventQuery{View: "corrected", To: &from}, []event.ID{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := viewIDs(m.Events(tt.q)); !equalIDs(got, tt.want) {
				t.Errorf("Events(%+v) = %v, want %v", tt.q, got, tt.want)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }

func TestEventLooksUpOneView(t *testing.T) {
	b := bid(1)
	log := []event.Event{boost(1, 0, 10), missed(2, 1, b), committed(3, 1, b), retract(4, 2, eid(1))}
	m := mustModel(t, log)

	v, ok := m.Event(eid(1))
	if !ok || !v.Retracted || v.RetractedBy != eid(4) {
		t.Errorf("Event(%s) = %+v, %v, want the retracted view", eid(1), v, ok)
	}
	if v, ok := m.Event(eid(4)); !ok || v.Original.ID != eid(4) {
		t.Errorf("Event(%s) = %+v, %v, want the retraction's own view so it can be undone", eid(4), v, ok)
	}
	if _, ok := m.Event(eid(3)); ok {
		t.Errorf("Event(%s) found the batch.committed marker, which is never listed", eid(3))
	}
	if _, ok := m.Event(eid(99)); ok {
		t.Error("Event found an id that is not in the log")
	}
}

func TestBatchEventsReadMembershipThroughBatchID(t *testing.T) {
	b1, b2 := bid(1), bid(2)
	log := []event.Event{
		missed(1, 0, b1),
		ev(2, 0, event.TaskWithdrawn{TaskID: taskA, Batch: b1}),
		committed(3, 0, b1),
		missed(4, 1, b2),
		committed(5, 1, b2),
		boost(6, 2, 10),
		retractBatch(7, 3, b1), // names a batch, belongs to none
		retract(8, 4, eid(7)),
	}
	m := mustModel(t, log)
	tests := []struct {
		name  string
		batch event.ID
		want  []event.ID
	}{
		{"a batch lists its members in log order, not its commit marker", b1, []event.ID{eid(1), eid(2)}},
		{"another batch lists only its own member", b2, []event.ID{eid(4)}},
		{"a batch that does not exist lists nothing", bid(3), []event.ID{}},
		{"an event id is not a batch", eid(6), []event.ID{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.BatchEvents(tt.batch); !equalIDs(got, tt.want) {
				t.Errorf("BatchEvents(%s) = %v, want %v", tt.batch, got, tt.want)
			}
		})
	}
	got := m.BatchEvents(b1)
	got[0] = eid(99)
	if m.BatchEvents(b1)[0] != eid(1) {
		t.Error("BatchEvents hands out its own backing array: a caller changed the batch")
	}
}
