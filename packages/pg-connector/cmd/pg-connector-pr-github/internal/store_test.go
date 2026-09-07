package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	got, err := DefaultStorePath()
	if err != nil {
		t.Fatalf("DefaultStorePath: %v", err)
	}
	want := filepath.Join("/xdg-state", "pg-connector-pr-github", "store.json")
	if got != want {
		t.Fatalf("DefaultStorePath() = %q, want %q", got, want)
	}
}

// TestDefaultStorePath_UnresolvableHomeFailsLoudly is finding A18's
// regression test: a missing/unresolvable $HOME with no XDG_STATE_HOME
// override MUST be reported as an error, not silently produce a
// cwd-relative path (the previous version discarded os.UserHomeDir's error
// via `home, _ := os.UserHomeDir()`).
func TestDefaultStorePath_UnresolvableHomeFailsLoudly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	// os.UserHomeDir on non-Windows consults $HOME only, so clearing it is
	// sufficient to force the "cannot resolve a home directory" error path
	// deterministically across darwin/Linux.
	_, err := DefaultStorePath()
	if err == nil {
		t.Fatal("DefaultStorePath() with no $HOME and no $XDG_STATE_HOME should have failed, not silently succeeded")
	}
}

// TestStore_ConcurrentFeedbackSetCallsDoNotDropWrites is the bead's required
// concurrency proof for finding A17: df-feedback issues one SetDisposition
// call per comment, and two concurrent writers must not silently drop one
// another's write. Each goroutine gets its OWN *Store instance (its own
// os.File, its own in-process mutex) pointed at the SAME on-disk path — the
// same "separate process" simulation TestStore_PersistsAcrossInstances
// above already establishes — so this exercises exactly the cross-process
// flock withLock added, not merely the in-process sync.Mutex that never
// spanned two instances even before this fix.
func TestStore_ConcurrentFeedbackSetCallsDoNotDropWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	const prID = "owner/repo#1"
	const n = 25

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := NewStore(path) // fresh instance per "process"
			commentID := fmt.Sprintf("c%d", i)
			if err := s.SetDisposition(prID, commentID, schema.DispositionWillFix); err != nil {
				t.Errorf("SetDisposition(%s): %v", commentID, err)
			}
		}(i)
	}
	wg.Wait()

	final := NewStore(path)
	st, err := final.Get(prID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(st.Dispositions) != n {
		missing := 0
		for i := 0; i < n; i++ {
			if _, ok := st.Dispositions[fmt.Sprintf("c%d", i)]; !ok {
				missing++
			}
		}
		t.Fatalf("Dispositions has %d entries, want %d — %d concurrent write(s) were silently dropped: %+v", len(st.Dispositions), n, missing, st.Dispositions)
	}
}

// TestStore_ConcurrentCategorizeAndFeedbackSetInterleaveSafely mixes the two
// write paths (SetCategory, SetDisposition) concurrently against the same
// PR to prove withLock serializes the whole load-modify-save cycle for both
// call shapes, not just same-method calls.
func TestStore_ConcurrentCategorizeAndFeedbackSetInterleaveSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	const prID = "owner/repo#2"
	const n = 20

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := NewStore(path)
			if err := s.SetDisposition(prID, fmt.Sprintf("c%d", i), schema.DispositionWontFix); err != nil {
				t.Errorf("SetDisposition: %v", err)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s := NewStore(path)
		if err := s.SetCategory(prID, "focus"); err != nil {
			t.Errorf("SetCategory: %v", err)
		}
	}()
	wg.Wait()

	final := NewStore(path)
	st, err := final.Get(prID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Category != "focus" {
		t.Fatalf("Category = %q, want focus (SetCategory's write must survive the interleaving)", st.Category)
	}
	if len(st.Dispositions) != n {
		t.Fatalf("Dispositions has %d entries, want %d — a concurrent write was dropped: %+v", len(st.Dispositions), n, st.Dispositions)
	}
}

// TestStore_CorruptedFileDoesNotPermanentlyBreakReads is finding A18's
// corruption-resilience proof: a single corrupted byte in the store file
// must not make every subsequent op — including a read-only Get, which is
// what backs the "show" wire op — fail permanently.
func TestStore_CorruptedFileDoesNotPermanentlyBreakReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s := NewStore(path)
	if err := s.SetCategory("owner/repo#1", "focus"); err != nil {
		t.Fatalf("SetCategory: %v", err)
	}

	// Corrupt one byte in the middle of the on-disk file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store file: %v", err)
	}
	if len(raw) < 2 {
		t.Fatalf("store file too short to corrupt meaningfully: %d bytes", len(raw))
	}
	raw[len(raw)/2] = '\x00'
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write corrupted store file: %v", err)
	}

	// A read-only Get must not error forever.
	if _, err := s.Get("owner/repo#1"); err != nil {
		t.Fatalf("Get after corruption = %v, want nil (corruption must not permanently break read-only ops)", err)
	}

	// The store must have quarantined the corrupted bytes rather than
	// silently discarding evidence of the corruption.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	quarantined := false
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt.") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Errorf("no quarantine file found alongside %s after corruption; entries=%v", path, entries)
	}

	// The store must also remain writable after corruption (self-healing,
	// not merely read-tolerant).
	if err := s.SetCategory("owner/repo#1", "later"); err != nil {
		t.Fatalf("SetCategory after corruption: %v", err)
	}
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get after recovery write: %v", err)
	}
	if st.Category != "later" {
		t.Fatalf("Category after recovery = %q, want later", st.Category)
	}
}

// TestStore_SaveStampsCurrentVersion proves every save() writes the
// currentStoreVersion into the on-disk file (finding A18's schema-version
// field), so a future migration has a real value to key off of instead of
// every existing file looking like "version 0, never migrated."
func TestStore_SaveStampsCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s := NewStore(path)
	if err := s.SetCategory("owner/repo#1", "focus"); err != nil {
		t.Fatalf("SetCategory: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store file: %v", err)
	}
	var onDisk storeFile
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("unmarshal store file: %v", err)
	}
	if onDisk.Version != currentStoreVersion {
		t.Fatalf("on-disk version = %d, want %d", onDisk.Version, currentStoreVersion)
	}
}

// TestStore_LoadUpgradesVersionZeroFile proves a pre-versioning on-disk file
// (no "version" key at all — exactly what every store.json written before
// this bead's fix looks like) loads cleanly and is treated as version 0,
// then upgraded to currentStoreVersion, rather than being rejected as
// unreadable.
func TestStore_LoadUpgradesVersionZeroFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	legacy := `{"prs":{"owner/repo#1":{"category":"focus"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy store file: %v", err)
	}
	s := NewStore(path)
	st, err := s.Get("owner/repo#1")
	if err != nil {
		t.Fatalf("Get on pre-versioning file: %v", err)
	}
	if st.Category != "focus" {
		t.Fatalf("Category = %q, want focus (pre-versioning data must survive the upgrade)", st.Category)
	}
}

// TestStore_LoadRejectsNewerVersion proves a store file written by a future,
// newer binary (a schema version this binary does not understand) is
// rejected with an error rather than silently misinterpreted — mirroring
// pg-pr's own migrate() precedent ("refuses rather than writing against a
// schema it doesn't understand").
func TestStore_LoadRejectsNewerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	future := fmt.Sprintf(`{"version":%d,"prs":{}}`, currentStoreVersion+1)
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatalf("write future store file: %v", err)
	}
	s := NewStore(path)
	if _, err := s.Get("owner/repo#1"); err == nil {
		t.Fatal("Get on a newer-version store file should have failed, not silently succeeded")
	}
}
