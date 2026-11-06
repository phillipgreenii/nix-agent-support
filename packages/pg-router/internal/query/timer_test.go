package query

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTimerQuery_EmitsOneTickOfItsDeclaredType(t *testing.T) {
	q := TimerQuery{Meta: Meta{EmitTypes: []string{"timer.5m"}, Trig: PeriodTrigger{Every: 5 * time.Minute}}}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	if q.BackingCommand() != "" {
		t.Errorf("a timer needs no executable; BackingCommand = %q", q.BackingCommand())
	}
	evts, err := q.Run(context.Background(), Env{})
	if err != nil || len(evts) != 1 {
		t.Fatalf("Run = (%v, %v), want exactly one tick event", evts, err)
	}
	if evts[0].Type != "timer.5m" || !strings.HasPrefix(evts[0].ID, "timer:timer.5m:") {
		t.Errorf("tick event = %+v", evts[0])
	}
	// Two firings must not collide on id (the dedup key).
	again, _ := q.Run(context.Background(), Env{})
	if again[0].ID == evts[0].ID {
		t.Errorf("two firings produced the same id %q", evts[0].ID)
	}
}

func TestTimerQuery_WithoutAnEmitTypeFailsLoudly(t *testing.T) {
	if _, err := (TimerQuery{}).Run(context.Background(), Env{}); err == nil {
		t.Fatal("a timer with no declared emit type must error, not emit an untyped event")
	}
}

// IsTimer is true for the timer emitter alone: gates never block it, and a
// command source is gated like any other emitter.
func TestIsTimer(t *testing.T) {
	if !IsTimer(TimerQuery{}) {
		t.Error("TimerQuery must be the timer emitter")
	}
	if IsTimer(CommandQuery{}) {
		t.Error("CommandQuery must not be the timer emitter")
	}
	if IsTimer(nil) {
		t.Error("nil is not the timer emitter")
	}
}

func TestTimerQuery_NoMetadataEmitsTheOriginalItem(t *testing.T) {
	for _, md := range []map[string]string{nil, {}} {
		q := TimerQuery{Meta: Meta{EmitTypes: []string{"timer.5m"}}, Metadata: md}
		evts, err := q.Run(context.Background(), Env{})
		if err != nil || len(evts) != 1 {
			t.Fatalf("Run = (%v, %v)", evts, err)
		}
		it := evts[0].Item
		if it.Metadata != nil || it.Type != "timer" || it.Title != "timer tick" {
			t.Errorf("item with metadata %#v = %+v, want the pre-metadata item (nil Metadata)", md, it)
		}
	}
}

func TestTimerQuery_MetadataIsCopiedPerTickAndNeverAliased(t *testing.T) {
	cfg := map[string]string{"lens": "security"}
	q := TimerQuery{Meta: Meta{EmitTypes: []string{"scan.due"}}, Metadata: cfg}
	first, _ := q.Run(context.Background(), Env{})
	if first[0].Item.Metadata["lens"] != "security" {
		t.Fatalf("metadata = %v", first[0].Item.Metadata)
	}
	// Mutating a tick's map must not leak into the query or later ticks.
	first[0].Item.Metadata["lens"] = "tampered"
	first[0].Item.Metadata["extra"] = "x"
	// Mutating the config map after building the query shows through only as the
	// query's own state (a copy is taken at Run, so the previous tick is stable).
	second, _ := q.Run(context.Background(), Env{})
	if second[0].Item.Metadata["lens"] != "security" || len(second[0].Item.Metadata) != 1 {
		t.Errorf("next tick = %v, want an unchanged {lens: security}", second[0].Item.Metadata)
	}
	if cfg["lens"] != "security" || len(cfg) != 1 {
		t.Errorf("query map was mutated through a tick: %v", cfg)
	}
}
