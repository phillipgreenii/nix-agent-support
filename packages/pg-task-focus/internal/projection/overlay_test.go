package projection

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// The tests build their logs from payload structs and fixed ids, so a fixture
// cannot drift from the codec and a failure names an id that can be found.

var epoch = time.Date(2026, 10, 7, 13, 30, 0, 0, time.UTC)

const (
	taskA       = event.TaskID("day:2026-10-07:post-plan")
	cycleA      = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZA")
	cycleB      = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZB")
	exampleLink = "https://example.test/plan"
)

// newID is the id of the nth event of a kind; ids of different kinds or
// numbers never collide.
func newID(kind byte, n int) event.ID {
	var entropy [10]byte
	entropy[0] = kind
	binary.BigEndian.PutUint32(entropy[6:], uint32(n))
	return event.NewID(epoch, bytes.NewReader(entropy[:]))
}

func eid(n int) event.ID { return newID('e', n) }
func bid(n int) event.ID { return newID('b', n) }

// at is the instant the given number of minutes after the fixture epoch.
func at(min int) time.Time { return epoch.Add(time.Duration(min) * time.Minute) }

// ev is the nth event of the log, effective min minutes after the epoch.
func ev(n, min int, p event.Payload) event.Event {
	return event.Event{
		Envelope: event.Envelope{V: event.SchemaVersion, ID: eid(n), At: event.At(at(min)), EffectiveAt: event.At(at(min)), Type: p.EventType()},
		Payload:  p,
	}
}

// fieldsOf builds the fields of a correction from alternating keys and values.
func fieldsOf(kv ...any) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i < len(kv); i += 2 {
		raw, err := json.Marshal(kv[i+1])
		if err != nil {
			panic(err)
		}
		m[kv[i].(string)] = raw
	}
	return m
}

func correct(n, min int, target event.ID, kv ...any) event.Event {
	return ev(n, min, event.EventCorrected{Target: target, Fields: fieldsOf(kv...)})
}

func retract(n, min int, target event.ID) event.Event {
	return ev(n, min, event.EventRetracted{Target: target})
}

func retractBatch(n, min int, batch event.ID) event.Event {
	return ev(n, min, event.EventRetracted{TargetBatch: batch})
}

func boost(n, min, minutes int) event.Event {
	return ev(n, min, event.CycleBoosted{CycleID: cycleA, Minutes: minutes})
}

func completed(n, min int) event.Event {
	return ev(n, min, event.TaskCompleted{TaskID: taskA})
}

func missed(n, min int, batch event.ID) event.Event {
	return ev(n, min, event.TaskMissed{TaskID: taskA, Batch: batch})
}

func committed(n, min int, batch event.ID) event.Event {
	return ev(n, min, event.BatchCommitted{Batch: batch})
}

func newZone(t *testing.T, name string) zone.Zone {
	t.Helper()
	z, err := zone.Load(name)
	if err != nil {
		t.Fatalf("zone.Load(%q): %v", name, err)
	}
	return z
}

func materialized(t *testing.T, n, min int, batch event.ID) event.Event {
	t.Helper()
	z := newZone(t, "America/New_York")
	return ev(n, min, event.TaskMaterialized{
		TaskID: taskA, Definition: "post-plan", Cadence: due.Daily,
		Period: civil.Date{Year: 2026, Month: time.October, Day: 7},
		Title:  "Post the plan", Link: exampleLink, Due: event.At(at(60)),
		DueRule: due.Rule{At: civil.TimeOfDay{Hour: 9, Minute: 30}, TZ: z},
		Batch:   batch,
	})
}

func periodChanged(t *testing.T, n, min int, batch event.ID) event.Event {
	t.Helper()
	return ev(n, min, event.PeriodChanged{
		Kind: "day", Start: civil.Date{Year: 2026, Month: time.October, Day: 7},
		TZ: "America/New_York", Batch: batch,
	})
}

func mustOverlay(t *testing.T, events []event.Event) ([]liveEvent, map[event.ID]EventView) {
	t.Helper()
	live, views, err := overlay(events)
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	return live, views
}

func liveIDs(live []liveEvent) []event.ID {
	ids := make([]event.ID, 0, len(live))
	for _, l := range live {
		ids = append(ids, l.ID)
	}
	return ids
}

func equalIDs(a, b []event.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func minutesOf(t *testing.T, e event.Event) int {
	t.Helper()
	p, ok := e.Payload.(event.CycleBoosted)
	if !ok {
		t.Fatalf("event %s is %T, want event.CycleBoosted", e.ID, e.Payload)
	}
	return p.Minutes
}

func TestLastCorrectionWins(t *testing.T) {
	tests := []struct {
		name         string
		corrections  []event.Event
		wantMinutes  int
		wantBy       []event.ID
		wantAnnotate string
	}{
		{"no correction keeps the original", nil, 10, nil, ""},
		{"one correction replaces the value", []event.Event{
			correct(2, 1, eid(1), "minutes", 20),
		}, 20, []event.ID{eid(2)}, ""},
		{"the last of two corrections of one key wins", []event.Event{
			correct(2, 1, eid(1), "minutes", 20),
			correct(3, 2, eid(1), "minutes", 30),
		}, 30, []event.ID{eid(2), eid(3)}, ""},
		{"a later correction of the same value back to the first still wins", []event.Event{
			correct(2, 1, eid(1), "minutes", 20),
			correct(3, 2, eid(1), "minutes", 30),
			correct(4, 3, eid(1), "minutes", 20),
		}, 20, []event.ID{eid(2), eid(3), eid(4)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := append([]event.Event{boost(1, 0, 10)}, tt.corrections...)
			live, views := mustOverlay(t, log)
			if len(live) != 1 {
				t.Fatalf("live = %v, want only the corrected boost", liveIDs(live))
			}
			if got := minutesOf(t, live[0].Event); got != tt.wantMinutes {
				t.Errorf("live minutes = %d, want %d", got, tt.wantMinutes)
			}
			v := views[eid(1)]
			if got := minutesOf(t, v.Corrected); got != tt.wantMinutes {
				t.Errorf("corrected view minutes = %d, want %d", got, tt.wantMinutes)
			}
			if got := minutesOf(t, v.Original); got != 10 {
				t.Errorf("original view minutes = %d, want 10", got)
			}
			if !equalIDs(v.CorrectedBy, tt.wantBy) {
				t.Errorf("CorrectedBy = %v, want %v", v.CorrectedBy, tt.wantBy)
			}
		})
	}
}

func TestCorrectionsOfDifferentKeysBothApply(t *testing.T) {
	annotated := ev(1, 0, event.CycleAnnotated{CycleID: cycleA, Note: "first"})
	log := []event.Event{
		annotated,
		correct(2, 1, eid(1), "note", "second"),
		correct(3, 2, eid(1), "kv", []event.KV{{Key: "ticket", Value: "T-1"}}),
	}
	live, _ := mustOverlay(t, log)
	got := live[0].Payload.(event.CycleAnnotated)
	if got.Note != "second" || len(got.KV) != 1 || got.KV[0] != (event.KV{Key: "ticket", Value: "T-1"}) {
		t.Errorf("corrected annotation = %+v, want note second and the ticket pair", got)
	}
}

func TestCycleTypeIsCorrectable(t *testing.T) {
	started := ev(1, 0, event.CycleStarted{CycleID: cycleA, Type: "focus", Title: "Focus", PlannedMinutes: 25})
	log := []event.Event{started, correct(2, 1, eid(1), "type", "review", "title", "Review")}
	live, _ := mustOverlay(t, log)
	got := live[0].Payload.(event.CycleStarted)
	if got.Type != "review" || got.Title != "Review" || got.CycleID != cycleA {
		t.Errorf("corrected cycle start = %+v, want type review, title Review and the same cycle", got)
	}
	if live[0].Envelope.Type != event.TypeCycleStarted {
		t.Errorf("envelope type = %q, want it unchanged", live[0].Envelope.Type)
	}
}

func TestCorrectionDoesNotChangeAtOrReqHash(t *testing.T) {
	orig := boost(1, 0, 10)
	orig.ReqHash = strings.Repeat("ab", 32)
	orig.At = event.At(at(5))
	log := []event.Event{
		orig,
		correct(2, 9, eid(1), "minutes", 20, "effective_at", event.At(at(3))),
	}
	live, views := mustOverlay(t, log)
	for name, e := range map[string]event.Event{"live": live[0].Event, "corrected view": views[eid(1)].Corrected} {
		if e.At != orig.At {
			t.Errorf("%s: at = %v, want the original %v", name, e.At.Time(), orig.At.Time())
		}
		if e.ReqHash != orig.ReqHash {
			t.Errorf("%s: req_hash = %q, want the original %q", name, e.ReqHash, orig.ReqHash)
		}
		if e.ID != orig.ID || e.Type != event.TypeCycleBoosted || e.V != orig.V {
			t.Errorf("%s: id, type or v changed: %+v", name, e.Envelope)
		}
	}
}

func TestEffectiveAtCorrection(t *testing.T) {
	tests := []struct {
		name          string
		fields        []any
		wantEffective time.Time
		wantMinutes   int
	}{
		{"effective_at alone", []any{"effective_at", event.At(at(3))}, at(3), 10},
		{"effective_at with a data key", []any{"effective_at", event.At(at(3)), "minutes", 15}, at(3), 15},
		{"data key alone leaves effective_at", []any{"minutes", 15}, at(0), 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := []event.Event{boost(1, 0, 10), correct(2, 1, eid(1), tt.fields...)}
			live, views := mustOverlay(t, log)
			if got := live[0].EffectiveAt.Time(); !got.Equal(tt.wantEffective) {
				t.Errorf("live effective_at = %v, want %v", got, tt.wantEffective)
			}
			if got := minutesOf(t, live[0].Event); got != tt.wantMinutes {
				t.Errorf("live minutes = %d, want %d", got, tt.wantMinutes)
			}
			if got := views[eid(1)].Original.EffectiveAt.Time(); !got.Equal(at(0)) {
				t.Errorf("original effective_at = %v, want %v untouched", got, at(0))
			}
		})
	}
}

func TestRetractionRemovesEventFromLiveSet(t *testing.T) {
	tests := []struct {
		name     string
		log      []event.Event
		wantLive []event.ID
	}{
		{"retract the first of two", []event.Event{boost(1, 0, 10), boost(2, 1, 20), retract(3, 2, eid(1))}, []event.ID{eid(2)}},
		{"retract the last of two", []event.Event{boost(1, 0, 10), boost(2, 1, 20), retract(3, 2, eid(2))}, []event.ID{eid(1)}},
		{"retract the only event", []event.Event{boost(1, 0, 10), retract(2, 1, eid(1))}, []event.ID{}},
		{"a retraction of a corrected event removes it with its corrections", []event.Event{
			boost(1, 0, 10), correct(2, 1, eid(1), "minutes", 20), retract(3, 2, eid(1)),
		}, []event.ID{}},
		{"no retraction keeps every event", []event.Event{boost(1, 0, 10), boost(2, 1, 20)}, []event.ID{eid(1), eid(2)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live, views := mustOverlay(t, tt.log)
			if got := liveIDs(live); !equalIDs(got, tt.wantLive) {
				t.Errorf("live = %v, want %v", got, tt.wantLive)
			}
			for _, e := range tt.log {
				if e.Payload.EventType() != event.TypeCycleBoosted {
					continue
				}
				wantRetracted := true
				for _, id := range tt.wantLive {
					if id == e.ID {
						wantRetracted = false
					}
				}
				if views[e.ID].Retracted != wantRetracted {
					t.Errorf("view of %s: Retracted = %v, want %v", e.ID, views[e.ID].Retracted, wantRetracted)
				}
			}
		})
	}
}

func TestRetractionNamesItself(t *testing.T) {
	log := []event.Event{boost(1, 0, 10), retract(2, 1, eid(1))}
	_, views := mustOverlay(t, log)
	if got := views[eid(1)].RetractedBy; got != eid(2) {
		t.Errorf("RetractedBy = %q, want the retraction %q", got, eid(2))
	}
	if views[eid(2)].Retracted {
		t.Error("the retraction itself is flagged retracted")
	}
}

func TestRetractTargetBatchRemovesEveryMember(t *testing.T) {
	b := bid(1)
	log := []event.Event{
		boost(1, 0, 10),
		missed(2, 1, b), ev(3, 1, event.TaskWithdrawn{TaskID: taskA, Batch: b}), committed(4, 1, b),
		boost(5, 2, 20),
		retractBatch(6, 3, b),
	}
	live, views := mustOverlay(t, log)
	if got, want := liveIDs(live), []event.ID{eid(1), eid(5)}; !equalIDs(got, want) {
		t.Errorf("live = %v, want %v", got, want)
	}
	for _, n := range []int{2, 3} {
		v := views[eid(n)]
		if !v.Retracted || v.RetractedBy != eid(6) {
			t.Errorf("view of member %d = Retracted %v by %q, want retracted by %q", n, v.Retracted, v.RetractedBy, eid(6))
		}
	}
}

func TestRetractionOfCorrectionRestoresPriorValue(t *testing.T) {
	log := []event.Event{
		boost(1, 0, 10),
		correct(2, 1, eid(1), "minutes", 20),
		retract(3, 2, eid(2)),
	}
	live, views := mustOverlay(t, log)
	if got := minutesOf(t, live[0].Event); got != 10 {
		t.Errorf("live minutes = %d, want the original 10", got)
	}
	if len(views[eid(1)].CorrectedBy) != 0 {
		t.Errorf("CorrectedBy = %v, want none once the correction is retracted", views[eid(1)].CorrectedBy)
	}
	if !views[eid(2)].Retracted {
		t.Error("the retracted correction is not flagged retracted")
	}
}

func TestRetractionOfRetractionRestoresEvent(t *testing.T) {
	tests := []struct {
		name     string
		log      []event.Event
		wantLive []event.ID
	}{
		{"undo of undo", []event.Event{boost(1, 0, 10), retract(2, 1, eid(1)), retract(3, 2, eid(2))}, []event.ID{eid(1)}},
		{"undo of undo of undo", []event.Event{boost(1, 0, 10), retract(2, 1, eid(1)), retract(3, 2, eid(2)), retract(4, 3, eid(3))}, []event.ID{}},
		{"undo of undo of undo of undo", []event.Event{
			boost(1, 0, 10), retract(2, 1, eid(1)), retract(3, 2, eid(2)), retract(4, 3, eid(3)), retract(5, 4, eid(4)),
		}, []event.ID{eid(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live, _ := mustOverlay(t, tt.log)
			if got := liveIDs(live); !equalIDs(got, tt.wantLive) {
				t.Errorf("live = %v, want %v", got, tt.wantLive)
			}
		})
	}
}

func TestRetractionOfABatchRetractionRestoresTheBatch(t *testing.T) {
	b := bid(1)
	log := []event.Event{
		missed(1, 0, b), committed(2, 0, b),
		retractBatch(3, 1, b), // the batch retraction names the batch; it is no member
		retract(4, 2, eid(3)), // undo of the batch retraction, by target
	}
	live, views := mustOverlay(t, log)
	if got, want := liveIDs(live), []event.ID{eid(1)}; !equalIDs(got, want) {
		t.Errorf("live = %v, want the restored batch member %v", got, want)
	}
	if views[eid(1)].Retracted {
		t.Error("the batch member is still flagged retracted")
	}
	if !views[eid(3)].Retracted {
		t.Error("the batch retraction is not flagged retracted by the undo")
	}
}

func TestCorrectedTwiceThenFirstCorrectionRetracted(t *testing.T) {
	log := []event.Event{
		boost(1, 0, 10),
		correct(2, 1, eid(1), "minutes", 20),
		correct(3, 2, eid(1), "minutes", 30),
		retract(4, 3, eid(2)),
	}
	live, views := mustOverlay(t, log)
	if got := minutesOf(t, live[0].Event); got != 30 {
		t.Errorf("live minutes = %d, want the second correction's 30 applied on the original", got)
	}
	if got, want := views[eid(1)].CorrectedBy, []event.ID{eid(3)}; !equalIDs(got, want) {
		t.Errorf("CorrectedBy = %v, want %v", got, want)
	}
}

func TestSecondCorrectionRetractedFallsBackToTheFirst(t *testing.T) {
	log := []event.Event{
		boost(1, 0, 10),
		correct(2, 1, eid(1), "minutes", 20),
		correct(3, 2, eid(1), "minutes", 30),
		retract(4, 3, eid(3)),
	}
	live, _ := mustOverlay(t, log)
	if got := minutesOf(t, live[0].Event); got != 20 {
		t.Errorf("live minutes = %d, want the first correction's 20", got)
	}
}

func TestCorrectionOfARetractedEventIsKeptForTheView(t *testing.T) {
	log := []event.Event{
		boost(1, 0, 10),
		retract(2, 1, eid(1)),
		correct(3, 2, eid(1), "minutes", 20),
	}
	live, views := mustOverlay(t, log)
	if len(live) != 0 {
		t.Errorf("live = %v, want none", liveIDs(live))
	}
	v := views[eid(1)]
	if !v.Retracted || minutesOf(t, v.Corrected) != 20 {
		t.Errorf("view = retracted %v with minutes %d, want retracted with the correction's 20", v.Retracted, minutesOf(t, v.Corrected))
	}
}

func TestCorrectionOfABatchMemberCorrectsIt(t *testing.T) {
	b := bid(1)
	log := []event.Event{
		missed(1, 0, b), committed(2, 0, b),
		correct(3, 1, eid(1), "effective_at", event.At(at(-5))),
	}
	live, _ := mustOverlay(t, log)
	if got := live[0].EffectiveAt.Time(); !got.Equal(at(-5)) {
		t.Errorf("effective_at = %v, want %v", got, at(-5))
	}
	if got := live[0].Payload.BatchID(); got != b {
		t.Errorf("batch = %q, want %q kept", got, b)
	}
}

func TestOverlayRejectsIdentityCorrectionInStoredLog(t *testing.T) {
	start := ev(1, 0, event.CycleStarted{CycleID: cycleA, Type: "focus", Title: "Focus", PlannedMinutes: 25})
	task := func(t *testing.T) event.Event { return materialized(t, 1, 0, "") }
	period := func(t *testing.T) event.Event { return periodChanged(t, 1, 0, "") }
	tests := []struct {
		name   string
		target func(t *testing.T) event.Event
		fields []any
		want   string // the field the message must name
	}{
		{"cycle_id", func(*testing.T) event.Event { return start }, []any{"cycle_id", string(cycleB)}, "cycle_id"},
		{"interrupts", func(*testing.T) event.Event { return start }, []any{"interrupts", string(cycleB)}, "interrupts"},
		{"task_id", task, []any{"task_id", "day:2026-10-08:post-plan"}, "task_id"},
		{"definition", task, []any{"definition", "other"}, "definition"},
		{"cadence", task, []any{"cadence", "weekly"}, "cadence"},
		{"period", task, []any{"period", civil.Date{Year: 2026, Month: time.October, Day: 8}}, "period"},
		{"kind", period, []any{"kind", "week"}, "kind"},
		{"start", period, []any{"start", civil.Date{Year: 2026, Month: time.October, Day: 8}}, "start"},
		{"batch", func(*testing.T) event.Event { return missed(1, 0, bid(1)) }, []any{"batch", string(bid(2))}, "batch"},
		{"profile", func(*testing.T) event.Event { return ev(1, 0, event.ProfileChanged{Profile: "work"}) }, []any{"profile", "home"}, "profile"},
		{"target", func(*testing.T) event.Event { return start }, []any{"target", string(eid(9))}, "target"},
		{"target_batch", func(*testing.T) event.Event { return start }, []any{"target_batch", string(bid(9))}, "target_batch"},
		{"identity key beside a correctable one", func(*testing.T) event.Event { return start }, []any{"title", "Other", "cycle_id", string(cycleB)}, "cycle_id"},
		{"envelope type of a task event", func(*testing.T) event.Event { return completed(1, 0) }, []any{"type", "task.skipped"}, "type"},
		{"envelope id", func(*testing.T) event.Event { return completed(1, 0) }, []any{"id", string(eid(9))}, "id"},
		{"envelope req_hash", func(*testing.T) event.Event { return completed(1, 0) }, []any{"req_hash", strings.Repeat("ab", 32)}, "req_hash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := tt.target(t)
			log := []event.Event{target, correct(2, 1, target.ID, tt.fields...)}
			_, _, err := overlay(log)
			inv := asInvalid(t, err)
			if inv.Code != codeInvalidCorrection {
				t.Errorf("code = %q, want %q (message: %s)", inv.Code, codeInvalidCorrection, inv.Message)
			}
			if !strings.Contains(inv.Message, tt.want) {
				t.Errorf("message %q does not name the field %q", inv.Message, tt.want)
			}
			if !strings.Contains(inv.Message, string(target.ID)) {
				t.Errorf("message %q does not name the stored target %s", inv.Message, target.ID)
			}
			if len(inv.Events) == 0 || inv.Events[len(inv.Events)-1] != target.ID {
				t.Errorf("Events = %v, want the stored target %s last", inv.Events, target.ID)
			}
		})
	}
}

func TestOverlayRejectsCorrectionOfAnUncorrectableTarget(t *testing.T) {
	b := bid(1)
	tests := []struct {
		name string
		log  []event.Event
		id   event.ID // the target
	}{
		{"an event.corrected", []event.Event{boost(1, 0, 10), correct(2, 1, eid(1), "minutes", 20), correct(3, 2, eid(2), "reason", "x")}, eid(2)},
		{"an event.retracted", []event.Event{boost(1, 0, 10), retract(2, 1, eid(1)), correct(3, 2, eid(2), "reason", "x")}, eid(2)},
		{"a batch.committed", []event.Event{missed(1, 0, b), committed(2, 0, b), correct(3, 1, eid(2), "batch", string(bid(2)))}, eid(2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := overlay(tt.log)
			inv := asInvalid(t, err)
			if inv.Code != codeInvalidCorrection {
				t.Errorf("code = %q, want %q (message: %s)", inv.Code, codeInvalidCorrection, inv.Message)
			}
			if !strings.Contains(inv.Message, string(tt.id)) {
				t.Errorf("message %q does not name the stored target %s", inv.Message, tt.id)
			}
		})
	}
}

func TestOverlayRejectsAReplacementThatBreaksTheEvent(t *testing.T) {
	tests := []struct {
		name   string
		fields []any
		want   string
	}{
		{"a key the event does not have", []any{"note", "x"}, "note"},
		{"a value of the wrong type", []any{"minutes", "ten"}, "minutes"},
		{"a value out of range", []any{"minutes", 0}, "minutes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := []event.Event{boost(1, 0, 10), correct(2, 1, eid(1), tt.fields...)}
			_, _, err := overlay(log)
			inv := asInvalid(t, err)
			if inv.Code != codeInvalidCorrection {
				t.Errorf("code = %q, want %q (message: %s)", inv.Code, codeInvalidCorrection, inv.Message)
			}
			if !strings.Contains(inv.Message, tt.want) || !strings.Contains(inv.Message, string(eid(1))) {
				t.Errorf("message %q does not name field %q and target %s", inv.Message, tt.want, eid(1))
			}
		})
	}
}

func TestOverlayRejectsRetractionsTheLogRulesForbid(t *testing.T) {
	b := bid(1)
	tests := []struct {
		name string
		log  func(t *testing.T) []event.Event
		id   event.ID
	}{
		{"a batch.committed", func(*testing.T) []event.Event {
			return []event.Event{missed(1, 0, b), committed(2, 0, b), retract(3, 1, eid(2))}
		}, eid(2)},
		{"a lone task.materialized", func(t *testing.T) []event.Event {
			return []event.Event{materialized(t, 1, 0, ""), retract(2, 1, eid(1))}
		}, eid(1)},
		{"a lone period.changed", func(t *testing.T) []event.Event {
			return []event.Event{periodChanged(t, 1, 0, ""), retract(2, 1, eid(1))}
		}, eid(1)},
		{"a batch member alone", func(*testing.T) []event.Event {
			return []event.Event{missed(1, 0, b), committed(2, 0, b), retract(3, 1, eid(1))}
		}, eid(1)},
		{"a batched task.materialized alone", func(t *testing.T) []event.Event {
			return []event.Event{materialized(t, 1, 0, b), committed(2, 0, b), retract(3, 1, eid(1))}
		}, eid(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := overlay(tt.log(t))
			inv := asInvalid(t, err)
			if inv.Code != codeInvalidCorrection {
				t.Errorf("code = %q, want %q (message: %s)", inv.Code, codeInvalidCorrection, inv.Message)
			}
			if !strings.Contains(inv.Message, string(tt.id)) {
				t.Errorf("message %q does not name the stored target %s", inv.Message, tt.id)
			}
		})
	}
}

func TestRetractionOfABatchedPeriodThroughItsBatchIsAllowed(t *testing.T) {
	b := bid(1)
	log := []event.Event{periodChanged(t, 1, 0, b), committed(2, 0, b), retractBatch(3, 1, b)}
	live, _ := mustOverlay(t, log)
	if len(live) != 0 {
		t.Errorf("live = %v, want none", liveIDs(live))
	}
}

func TestTargetMustPrecede(t *testing.T) {
	tests := []struct {
		name    string
		log     []event.Event
		offends event.ID
		phrase  string
	}{
		{"a correction of a later event", []event.Event{correct(1, 0, eid(2), "minutes", 20), boost(2, 1, 10)}, eid(1), "later"},
		{"a retraction of a later event", []event.Event{retract(1, 0, eid(2)), boost(2, 1, 10)}, eid(1), "later"},
		{"a correction of itself", []event.Event{boost(1, 0, 10), correct(2, 1, eid(2), "minutes", 20)}, eid(2), ""},
		{"a retraction of itself", []event.Event{boost(1, 0, 10), retract(2, 1, eid(2))}, eid(2), ""},
		{"a batch retraction before the batch", []event.Event{retractBatch(1, 0, bid(1)), missed(2, 1, bid(1)), committed(3, 1, bid(1))}, eid(1), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := overlay(tt.log)
			inv := asInvalid(t, err)
			if inv.Code != codeUnknownEvent {
				t.Errorf("code = %q, want %q (message: %s)", inv.Code, codeUnknownEvent, inv.Message)
			}
			if !equalIDs(inv.Events, []event.ID{tt.offends}) {
				t.Errorf("Events = %v, want only the offender %s", inv.Events, tt.offends)
			}
			if !strings.Contains(inv.Message, string(tt.offends)) {
				t.Errorf("message %q does not name the offender %s", inv.Message, tt.offends)
			}
			if tt.phrase != "" && !strings.Contains(inv.Message, tt.phrase) {
				t.Errorf("message %q does not say the target is %s in the log", inv.Message, tt.phrase)
			}
		})
	}
}

func TestUnknownTarget(t *testing.T) {
	tests := []struct {
		name string
		last event.Event
	}{
		{"correction", correct(2, 1, eid(77), "minutes", 20)},
		{"retraction", retract(2, 1, eid(77))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := overlay([]event.Event{boost(1, 0, 10), tt.last})
			inv := asInvalid(t, err)
			if inv.Code != codeUnknownEvent {
				t.Errorf("code = %q, want %q", inv.Code, codeUnknownEvent)
			}
			if !equalIDs(inv.Events, []event.ID{eid(2)}) {
				t.Errorf("Events = %v, want the offending event %s", inv.Events, eid(2))
			}
			if !strings.Contains(inv.Message, string(eid(77))) || !strings.Contains(inv.Message, string(eid(2))) {
				t.Errorf("message %q does not name the offender %s and the unknown target %s", inv.Message, eid(2), eid(77))
			}
			if len(inv.Instants) != 1 || !inv.Instants[0].Equal(at(1)) {
				t.Errorf("Instants = %v, want the offender's effective_at %v", inv.Instants, at(1))
			}
		})
	}
}

func TestUnknownTargetBatch(t *testing.T) {
	b := bid(1)
	tests := []struct {
		name string
		log  []event.Event
	}{
		{"a batch id no event carries", []event.Event{boost(1, 0, 10), retractBatch(2, 1, bid(9))}},
		{"an event id given as a batch", []event.Event{boost(1, 0, 10), retractBatch(2, 1, eid(1))}},
		{"a batch that has only a commit marker", []event.Event{committed(1, 0, b), retractBatch(2, 1, b)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := overlay(tt.log)
			inv := asInvalid(t, err)
			if inv.Code != codeUnknownEvent {
				t.Errorf("code = %q, want %q (message: %s)", inv.Code, codeUnknownEvent, inv.Message)
			}
			if !strings.Contains(inv.Message, "batch") {
				t.Errorf("message %q does not say the batch is unknown", inv.Message)
			}
		})
	}
}

func TestNewEventIsNeverCitedByIDOrListed(t *testing.T) {
	stored := []event.Event{boost(1, 0, 10)}
	add := retract(2, 1, eid(77))
	_, _, err := overlayFrom(append(stored, add), len(stored))
	inv := asInvalid(t, err)
	if len(inv.Events) != 0 {
		t.Errorf("Events = %v, want none: the offender is the new event", inv.Events)
	}
	if !strings.HasPrefix(inv.Message, "The new event") || strings.Contains(inv.Message, string(add.ID)) {
		t.Errorf("message %q does not call the offender the new event without its id", inv.Message)
	}
	if len(inv.Instants) != 1 || !inv.Instants[0].Equal(at(1)) {
		t.Errorf("Instants = %v, want the new event's effective_at %v", inv.Instants, at(1))
	}

	add = correct(2, 1, eid(1), "cycle_id", string(cycleB))
	_, _, err = overlayFrom(append(stored, add), len(stored))
	inv = asInvalid(t, err)
	if !equalIDs(inv.Events, []event.ID{eid(1)}) {
		t.Errorf("Events = %v, want only the stored target %s", inv.Events, eid(1))
	}
	if inv.Entity != string(cycleA) {
		t.Errorf("Entity = %q, want the stored target's cycle %q", inv.Entity, cycleA)
	}
}

func TestOverlayDoesNotMutateItsInput(t *testing.T) {
	log := []event.Event{boost(1, 0, 10), correct(2, 1, eid(1), "minutes", 20)}
	mustOverlay(t, log)
	if got := minutesOf(t, log[0]); got != 10 {
		t.Errorf("input minutes = %d, want the original 10", got)
	}
}

func asInvalid(t *testing.T, err error) *Invalid {
	t.Helper()
	var inv *Invalid
	if !errors.As(err, &inv) {
		t.Fatalf("error = %v (%T), want *Invalid", err, err)
	}
	return inv
}
