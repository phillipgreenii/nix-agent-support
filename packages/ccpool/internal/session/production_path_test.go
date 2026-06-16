package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// writeTranscriptAt writes a real transcript file at path (the hook-recorded
// transcript path ccpool's resume probe stats, ADR 0015).
func writeTranscriptAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProductionExister_statsRecordedTranscriptPath is the regression that would
// have caught CRITICAL #1: it wires the REAL production SessionExister
// (NewFSSessionExister) and proves it reports resumability by stat-ing the
// hook-recorded transcript path directly. A nil-wired Exister makes Exists return
// false even though the session is resumable.
func TestProductionExister_statsRecordedTranscriptPath(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "session-redesign.jsonl")

	ex := NewFSSessionExister() // production constructor, not a fake

	if ex.Exists(transcript) {
		t.Fatal("must be false before any transcript exists")
	}
	writeTranscriptAt(t, transcript)
	if !ex.Exists(transcript) {
		t.Errorf("production Exister must find the hook-recorded transcript at %q "+
			"(a nil Exister would miss it)", transcript)
	}
}

// TestReap_realWiring_keepsResumableRow drives the REAL prune path with the REAL
// production Exister. A dead row (no tmux) whose hook-recorded transcript exists
// on disk MUST be kept (resume later). With a nil Exister claudeSessionResumable
// returns false and reap would prune this row.
func TestReap_realWiring_keepsResumableRow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)

	transcript := filepath.Join(t.TempDir(), "resumable.jsonl")
	if err := st.Insert(ctx, store.Session{
		ExternalID: "resumable", ClaudeSessionID: "csid-resumable-0001",
		CWD: "/Users/x/proj", TranscriptPath: transcript, State: store.Idle,
		TmuxSession: "cc-resumable", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	}); err != nil {
		t.Fatal(err)
	}
	writeTranscriptAt(t, transcript) // transcript exists on disk → resumable

	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: NewFSSessionExister(), // REAL production wiring
		Now:     func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "resumable"); !ok {
		t.Error("a dead row whose hook-recorded transcript exists on disk must be KEPT (resumable), not pruned")
	}
}

// TestReap_realWiring_prunesGoneRow is the negative control: the same real wiring
// prunes a dead row whose recorded transcript is absent (genuinely gone).
// Together with the keep test this proves the prune decision flows through the
// real path-stat Exister, not a fake.
func TestReap_realWiring_prunesGoneRow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)

	// A recorded transcript path that does NOT exist on disk → not resumable.
	transcript := filepath.Join(t.TempDir(), "gone.jsonl")
	if err := st.Insert(ctx, store.Session{
		ExternalID: "gone", ClaudeSessionID: "csid-gone-0001",
		CWD: "/Users/x/gone", TranscriptPath: transcript, State: store.Idle,
		TmuxSession: "cc-gone", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	}); err != nil {
		t.Fatal(err)
	}
	// no transcript written → not resumable

	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: NewFSSessionExister(),
		Now:     func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "gone"); ok {
		t.Error("a dead row whose hook-recorded transcript is absent must be pruned")
	}
}
