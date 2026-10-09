package testgen

import (
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// checkLog checks what every generated log promises: each event is one the
// codec writes and reads back unchanged, ids are unique, Line is the 1-based
// position, and every batch ends with its batch.committed.
func checkLog(t interface {
	Helper()
	Fatalf(string, ...any)
}, log []event.Event,
) {
	t.Helper()
	ids := map[event.ID]bool{}
	open := map[event.ID]bool{}
	for i, e := range log {
		if e.Line != i+1 {
			t.Fatalf("event %d has Line %d", i+1, e.Line)
		}
		if ids[e.ID] {
			t.Fatalf("event id %s is used twice", e.ID)
		}
		ids[e.ID] = true
		line, err := event.Encode(e)
		if err != nil {
			t.Fatalf("event %d does not encode: %v", i+1, err)
		}
		back, err := event.Decode(line)
		if err != nil {
			t.Fatalf("event %d does not decode: %v", i+1, err)
		}
		if back.ID != e.ID || back.Payload.EventType() != e.Payload.EventType() {
			t.Fatalf("event %d does not round-trip", i+1)
		}
		if e.EffectiveAt.Time().Nanosecond()%int(time.Millisecond) != 0 {
			t.Fatalf("event %d has a sub-millisecond instant", i+1)
		}
		b := e.Payload.BatchID()
		switch {
		case e.Payload.EventType() == event.TypeBatchCommitted:
			if !open[b] {
				t.Fatalf("event %d commits batch %s, which has no members", i+1, b)
			}
			delete(open, b)
		case b != "":
			open[b] = true
		}
		if len(open) > 1 {
			t.Fatalf("batches %v interleave at event %d", open, i+1)
		}
	}
	if len(open) != 0 {
		t.Fatalf("batches %v are never committed", open)
	}
}

func TestLogDrawsCodecValidCommittedLogs(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		log := Log(80).Draw(t, "log")
		checkLog(t, log)
		types := map[event.Type]bool{}
		for _, e := range log {
			types[e.Payload.EventType()] = true
		}
		if !types[event.TypeProfileChanged] || !types[event.TypePeriodChanged] {
			t.Fatalf("the log does not begin with a profile and a day: %v", types)
		}
	})
}

func TestLogReachesInterruptsSwitchesAndBreaks(t *testing.T) {
	var interrupts, switches, breaks bool
	rapid.Check(t, func(t *rapid.T) {
		for _, e := range Log(80).Draw(t, "log") {
			switch p := e.Payload.(type) {
			case event.CycleStarted:
				interrupts = interrupts || p.Interrupts != ""
			case event.CyclePaused:
				// A break is back-filled, so its pause takes effect before it
				// is recorded; a switch takes effect when it is recorded.
				if p.Batch != "" && e.EffectiveAt.Time().Before(e.At.Time()) {
					breaks = true
				} else if p.Batch != "" {
					switches = true
				}
			}
		}
	})
	if !interrupts || !switches || !breaks {
		t.Errorf("over the checks: interrupts %v, switches %v, breaks %v; want all three", interrupts, switches, breaks)
	}
}

func TestDaysIsDeterministicWithFiftyEventsADay(t *testing.T) {
	a, b := Days(2), Days(2)
	if len(a) != 100 || !slices.EqualFunc(a, b, func(x, y event.Event) bool { return x.ID == y.ID && x.EffectiveAt == y.EffectiveAt }) {
		t.Fatalf("Days(2) has %d events or differs between calls", len(a))
	}
	checkLog(t, a)
	if Days(0) != nil {
		t.Error("Days(0) is not empty")
	}
}

func TestInstantAndZoneName(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		at := Instant().Draw(t, "at")
		if at.Location() != time.UTC || at.Nanosecond()%int(time.Millisecond) != 0 {
			t.Fatalf("Instant %v is not UTC with millisecond precision", at)
		}
		if _, err := time.LoadLocation(ZoneName().Draw(t, "zone")); err != nil {
			t.Fatalf("ZoneName: %v", err)
		}
	})
}
