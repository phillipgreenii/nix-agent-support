package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// writeTranscript writes a real transcript at the path the REAL encoder produces
// for cwd: <home>/.claude/projects/<encodeProjectDir(cwd)>/<csid>.jsonl. This is
// the on-disk shape ccpool's resume probe looks for (ADR 0015).
func writeTranscript(t *testing.T, home, cwd, csid string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", encodeProjectDir(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, csid+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProductionExister_findsTranscriptViaRealEncoder is the regression that
// would have caught BOTH critical bugs: it wires the REAL production
// SessionExister (NewHomeSessionExister) over a temp HOME and uses the REAL
// encoder to lay down the transcript. A nil-wired Exister (#1) or a wrong
// encoder (#2) makes Exists return false even though the session is resumable.
//
// The cwd intentionally contains '_' and a '/.' run so a separator-only /
// run-collapsing encoder would look under the wrong directory and miss it.
func TestProductionExister_findsTranscriptViaRealEncoder(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/x/phillipg_mbp/.worktrees/session-redesign"
	csid := "11111111-2222-3333-4444-555555555555"

	ex := NewHomeSessionExister(home) // production constructor, not a fake

	if ex.Exists(cwd, csid) {
		t.Fatal("must be false before any transcript exists")
	}
	writeTranscript(t, home, cwd, csid)
	if !ex.Exists(cwd, csid) {
		t.Errorf("production Exister must find the transcript the real encoder names "+
			"under %q (nil Exister or wrong encoding would miss it)", cwd)
	}
}

// TestReap_realWiring_keepsResumableRow drives the REAL prune path with the REAL
// production Exister over a temp HOME. A dead row (no tmux) whose Claude session
// transcript exists on disk MUST be kept (resume later). With a nil Exister (#1)
// claudeSessionResumable returns false and reap would prune this row; with a
// wrong encoder (#2) the transcript would not be found and the row pruned too.
func TestReap_realWiring_keepsResumableRow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	home := t.TempDir()
	st := newMemStore(t)

	cwd := "/Users/x/phillipg_mbp/.worktrees/session-redesign"
	csid := "csid-resumable-0001"
	if err := st.Insert(ctx, store.Session{
		ExternalID: "resumable", ClaudeSessionID: csid, CWD: cwd, State: store.Idle,
		TmuxSession: "cc-resumable", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	}); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, home, cwd, csid) // session exists on disk → resumable

	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: NewHomeSessionExister(home), // REAL production wiring + REAL encoder
		Now:     func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "resumable"); !ok {
		t.Error("a dead row whose Claude transcript exists on disk must be KEPT (resumable), not pruned")
	}
}

// TestReap_realWiring_prunesGoneRow is the negative control: the same real
// wiring prunes a dead row whose transcript is absent (genuinely gone). Together
// with the keep test this proves the prune decision flows through the real
// encoder + real Exister, not a fake.
func TestReap_realWiring_prunesGoneRow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	home := t.TempDir()
	st := newMemStore(t)

	cwd := "/Users/x/phillipg_mbp/.worktrees/gone"
	if err := st.Insert(ctx, store.Session{
		ExternalID: "gone", ClaudeSessionID: "csid-gone-0001", CWD: cwd, State: store.Idle,
		TmuxSession: "cc-gone", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	}); err != nil {
		t.Fatal(err)
	}
	// no transcript written → not resumable

	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: NewHomeSessionExister(home),
		Now:     func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "gone"); ok {
		t.Error("a dead row whose Claude transcript is absent must be pruned")
	}
}
