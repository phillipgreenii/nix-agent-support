package internal

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// newTestStore returns a Store backed by a file in a fresh t.TempDir() —
// isolated per this repo's Unit Tests convention (a test that touches files
// must generate its scenario in a temp directory).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "store.json"))
}

func TestStore_GetOnUnwrittenPRReturnsZeroValue(t *testing.T) {
	s := newTestStore(t)
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Category != "" || st.Dispositions != nil {
		t.Fatalf("unwritten PR should be zero-value, got %+v", st)
	}
}

func TestStore_SetCategoryThenGetRoundTrips(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetCategory("owner/repo#1", "focus"); err != nil {
		t.Fatalf("SetCategory: %v", err)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Category != "focus" {
		t.Fatalf("Category = %q, want focus", st.Category)
	}
}

func TestStore_SetCategoryOverwrites(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetCategory("owner/repo#1", "focus"); err != nil {
		t.Fatalf("SetCategory: %v", err)
	}
	if err := s.SetCategory("owner/repo#1", "later"); err != nil {
		t.Fatalf("SetCategory: %v", err)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Category != "later" {
		t.Fatalf("Category = %q, want later (plain set/overwrite)", st.Category)
	}
}

func TestStore_SetDispositionThenGetRoundTrips(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetDisposition("owner/repo#1", "c1", schema.DispositionWillFix); err != nil {
		t.Fatalf("SetDisposition: %v", err)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Dispositions["c1"] != schema.DispositionWillFix {
		t.Fatalf("Dispositions[c1] = %q, want will-fix", st.Dispositions["c1"])
	}
}

func TestStore_MultipleCommentsOnSamePRAreIndependent(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetDisposition("owner/repo#1", "c1", schema.DispositionWillFix); err != nil {
		t.Fatalf("SetDisposition c1: %v", err)
	}
	if err := s.SetDisposition("owner/repo#1", "c2", schema.DispositionWontFix); err != nil {
		t.Fatalf("SetDisposition c2: %v", err)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Dispositions["c1"] != schema.DispositionWillFix || st.Dispositions["c2"] != schema.DispositionWontFix {
		t.Fatalf("Dispositions = %+v", st.Dispositions)
	}
}

func TestStore_DifferentPRsAreIndependent(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetCategory("owner/repo#1", "focus"); err != nil {
		t.Fatalf("SetCategory #1: %v", err)
	}
	if err := s.SetCategory("owner/repo#2", "later"); err != nil {
		t.Fatalf("SetCategory #2: %v", err)
	}
	st1, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	st2, err := s.Get("owner/repo#2")
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if st1.Category != "focus" || st2.Category != "later" {
		t.Fatalf("got #1=%q #2=%q", st1.Category, st2.Category)
	}
}

func TestStore_PersistsAcrossInstances(t *testing.T) {
	// Simulates the scriptout protocol's one-process-per-call reality: a
	// fresh Store value opened against the same path must see an earlier
	// process's writes.
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	first := NewStore(path)
	if err := first.SetCategory("owner/repo#1", "focus"); err != nil {
		t.Fatalf("SetCategory: %v", err)
	}
	if err := first.SetDisposition("owner/repo#1", "c1", schema.DispositionOpen); err != nil {
		t.Fatalf("SetDisposition: %v", err)
	}

	second := NewStore(path)
	st, err := second.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get (second instance): %v", err)
	}
	if st.Category != "focus" || st.Dispositions["c1"] != schema.DispositionOpen {
		t.Fatalf("state did not survive across Store instances: %+v", st)
	}
}

func TestDefaultStorePath_HonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg-state")
	got := DefaultStorePath()
	want := filepath.Join("/xdg-state", "pg-connector-pr-github", "store.json")
	if got != want {
		t.Fatalf("DefaultStorePath() = %q, want %q", got, want)
	}
}
