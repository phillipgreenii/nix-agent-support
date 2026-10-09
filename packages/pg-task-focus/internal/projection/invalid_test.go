package projection

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func TestInvalidImplementsErrorAndCodesListsTheCodesDeclaredSoFar(t *testing.T) {
	var err error = &Invalid{
		Code:     codeUnknownEvent,
		Message:  "The new event corrects an event that is not in the log.",
		Entity:   "day:2026-10-07:post-plan",
		Events:   []event.ID{eid(1)},
		Instants: []time.Time{at(0)},
	}
	if got, want := err.Error(), "The new event corrects an event that is not in the log."; got != want {
		t.Errorf("Error() = %q, want the message %q", got, want)
	}
	var inv *Invalid
	if !errors.As(err, &inv) || inv.Entity != "day:2026-10-07:post-plan" {
		t.Errorf("errors.As did not recover the *Invalid: %+v", inv)
	}

	want := []Code{"unknown_event", "invalid_correction"}
	if got := Codes(); !reflect.DeepEqual(got, want) {
		t.Errorf("Codes() = %v, want %v", got, want)
	}
	got := Codes()
	got[0] = "tampered"
	if Codes()[0] != "unknown_event" {
		t.Error("Codes() hands out its own backing array: a caller changed the list")
	}
}

func TestInvalidFieldsCarryTheCyclesOfAReplayFinding(t *testing.T) {
	inv := &Invalid{Code: codeInvalidCorrection, Cycles: []event.CycleID{cycleA}}
	if len(inv.Cycles) != 1 || inv.Cycles[0] != cycleA {
		t.Errorf("Cycles = %v, want [%s]", inv.Cycles, cycleA)
	}
}
