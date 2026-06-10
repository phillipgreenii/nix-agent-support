package store

import (
	"context"
	"testing"
)

func TestTransition_bumpsGenerationAndReturnsPrior(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "u-a") // state Starting, generation 1
	bumpClock(t, st, 5)

	prior, err := st.Transition(ctx, "a", Ready, "u-a", "/p/a.jsonl")
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if prior != Starting {
		t.Errorf("prior = %q, want starting", prior)
	}
	got, _, _ := st.GetByName(ctx, "a")
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
	if err := st.Insert(ctx, Session{Name: "a", UUID: "u-a", State: Ready, TranscriptPath: "/keep.jsonl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Transition(ctx, "a", Done, "", ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got, _, _ := st.GetByName(ctx, "a")
	if got.TranscriptPath != "/keep.jsonl" {
		t.Errorf("transcript_path = %q, want unchanged /keep.jsonl", got.TranscriptPath)
	}
}

func TestUpsert_insertsWhenAbsentNoopWhenPresent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.Upsert(ctx, "a", "u-a"); err != nil {
		t.Fatalf("Upsert(new): %v", err)
	}
	got, ok, _ := st.GetByName(ctx, "a")
	if !ok || got.State != Starting || got.UUID != "u-a" {
		t.Fatalf("after upsert-new: ok=%v %+v", ok, got)
	}

	// Second upsert with a different uuid must NOT clobber the existing row.
	if err := st.Upsert(ctx, "a", "u-OTHER"); err != nil {
		t.Fatalf("Upsert(existing): %v", err)
	}
	got2, _, _ := st.GetByName(ctx, "a")
	if got2.UUID != "u-a" {
		t.Errorf("upsert clobbered uuid: %q, want u-a", got2.UUID)
	}
}
