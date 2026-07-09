package main

import (
	"reflect"
	"testing"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/tui"
)

// TestNudgeQueueResultMsg_FieldMapping pins the NudgeQueue response → TUI msg
// wiring: Queued ← queued_session_ids, Already ← already_queued_session_ids,
// Cancel stays false. Distinct, non-overlapping values are used on each side so
// a field swap (Queued ↔ Already) would fail rather than pass silently.
func TestNudgeQueueResultMsg_FieldMapping(t *testing.T) {
	resp := &pb.NudgeQueueResponse{
		QueuedSessionIds:        []string{"q-1", "q-2"},
		AlreadyQueuedSessionIds: []string{"a-1"},
	}
	got := nudgeQueueResultMsg(resp)

	want := tui.NudgeResultMsg{
		Queued:  []string{"q-1", "q-2"},
		Already: []string{"a-1"},
	}
	if got.Cancel {
		t.Errorf("NudgeQueue mapping must not set Cancel, got Cancel=true")
	}
	if !reflect.DeepEqual(got.Queued, want.Queued) {
		t.Errorf("Queued = %v, want %v (field swap?)", got.Queued, want.Queued)
	}
	if !reflect.DeepEqual(got.Already, want.Already) {
		t.Errorf("Already = %v, want %v (field swap?)", got.Already, want.Already)
	}
	if len(got.Cancelled) != 0 {
		t.Errorf("Cancelled should be empty for a queue response, got %v", got.Cancelled)
	}
}

// TestNudgeCancelResultMsg_FieldMapping pins the NudgeCancel response → TUI msg
// wiring: Cancel=true and Cancelled ← cancelled_session_ids.
func TestNudgeCancelResultMsg_FieldMapping(t *testing.T) {
	resp := &pb.NudgeCancelResponse{
		CancelledSessionIds: []string{"c-1", "c-2"},
	}
	got := nudgeCancelResultMsg(resp)

	if !got.Cancel {
		t.Errorf("NudgeCancel mapping must set Cancel=true, got false")
	}
	if !reflect.DeepEqual(got.Cancelled, []string{"c-1", "c-2"}) {
		t.Errorf("Cancelled = %v, want [c-1 c-2]", got.Cancelled)
	}
	if len(got.Queued) != 0 || len(got.Already) != 0 {
		t.Errorf("Queued/Already should be empty for a cancel response, got Queued=%v Already=%v", got.Queued, got.Already)
	}
}
