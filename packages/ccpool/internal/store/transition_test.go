package store

import (
	"context"
	"testing"
)

func TestTransition_bumpsGenerationAndReturnsPrior(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a") // state Starting, generation 1
	bumpClock(t, st, 5)

	prior, err := st.Transition(ctx, "a", Ready, "csid-a", "/p/a.jsonl")
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if prior != Starting {
		t.Errorf("prior = %q, want starting", prior)
	}
	got, _, _ := st.GetByExternalID(ctx, "a")
	if got.State != Ready {
		t.Errorf("state = %q, want ready", got.State)
	}
	if got.Generation != 2 {
		t.Errorf("generation = %d, want 2", got.Generation)
	}
	if got.LastActivityAt != 1005 {
		t.Errorf("last_activity_at = %d, want 1005", got.LastActivityAt)
	}
	if got.TranscriptPath != "/p/a.jsonl" {
		t.Errorf("transcript_path = %q, want /p/a.jsonl", got.TranscriptPath)
	}
}

func TestTransition_emptyTranscriptLeavesItUnchanged(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, Session{ExternalID: "a", ClaudeSessionID: "csid-a", State: Ready, TranscriptPath: "/keep.jsonl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Transition(ctx, "a", Idle, "", ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "a")
	if got.TranscriptPath != "/keep.jsonl" {
		t.Errorf("transcript_path = %q, want unchanged /keep.jsonl", got.TranscriptPath)
	}
}

// TestSetPendingQuestion_roundTrips proves the hook-set question text persists on
// the row and is readable back via GetByExternalID (pg2-7a5b).
func TestSetPendingQuestion_roundTrips(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a")

	if err := st.SetPendingQuestion(ctx, "a", "Which path? Alpha or Bravo"); err != nil {
		t.Fatalf("SetPendingQuestion: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "a")
	if got.PendingQuestion != "Which path? Alpha or Bravo" {
		t.Errorf("PendingQuestion = %q, want round-tripped text", got.PendingQuestion)
	}
}

// TestTransition_clearsPendingQuestionExceptNeedsInput proves Transition wipes a
// stale pending_question whenever it moves to a state OTHER than NeedsInput, but
// leaves it intact when moving INTO NeedsInput (so the ask handler's subsequent
// SetPendingQuestion survives) (pg2-7a5b).
func TestTransition_clearsPendingQuestionExceptNeedsInput(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		to        State
		wantClear bool // true => question must be wiped to ""
	}{
		{"to_working_clears", Working, true},
		{"to_idle_clears", Idle, true},
		{"to_ready_clears", Ready, true},
		{"to_errored_clears", Errored, true},
		{"to_needs_input_preserves", NeedsInput, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			mustInsert(t, st, "a", "csid-a")
			if err := st.SetPendingQuestion(ctx, "a", "stale question"); err != nil {
				t.Fatalf("SetPendingQuestion: %v", err)
			}
			if _, err := st.Transition(ctx, "a", tc.to, "csid-a", ""); err != nil {
				t.Fatalf("Transition: %v", err)
			}
			got, _, _ := st.GetByExternalID(ctx, "a")
			if tc.wantClear && got.PendingQuestion != "" {
				t.Errorf("PendingQuestion = %q, want cleared on transition to %s", got.PendingQuestion, tc.to)
			}
			if !tc.wantClear && got.PendingQuestion != "stale question" {
				t.Errorf("PendingQuestion = %q, want preserved on transition to %s", got.PendingQuestion, tc.to)
			}
		})
	}
}

func TestDelete_removesRow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a")
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "a"); ok {
		t.Error("row still present after Delete")
	}
	// Deleting a missing row is not an error.
	if err := st.Delete(ctx, "missing"); err != nil {
		t.Errorf("Delete(missing) = %v, want nil", err)
	}
}
