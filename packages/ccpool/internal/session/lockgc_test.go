package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/lock"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// lockGCNow is the fixed clock of the lock-GC tests; ids are stamped relative to
// it in the handler's "<yyyymmddThhmmss.nnnnnnnnn>" shape.
var lockGCNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func lockGCID(role string, age time.Duration) string {
	return "pg-router-" + role + "-zr-abc.2-" + lockGCNow.Add(-age).Format("20060102T150405.000000000")
}

// lockGCFixture builds a Service over a real flock Locker in a temp dir (never a
// real pool dir) and an in-memory store.
func lockGCFixture(t *testing.T) (*Service, *lock.Flock, string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	fl := lock.New(dir)
	st := newMemStore(t)
	s := New(Deps{
		Tmux: &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}, Trust: &fakeTrust{}, Store: st,
		Prefix: "cc-", Exister: fakeExister{ok: true}, Lock: fl,
		Now: func() time.Time { return lockGCNow },
	})
	return s, fl, dir, st
}

// mkLock creates <id>.lock through the real Lock path and backdates its mtime.
func mkLock(t *testing.T, fl *lock.Flock, dir, id string, mtimeAge time.Duration) string {
	t.Helper()
	u, err := fl.Lock(id)
	if err != nil {
		t.Fatal(err)
	}
	u()
	p := filepath.Join(dir, id+".lock")
	old := lockGCNow.Add(-mtimeAge)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestGCLocks_removesOrphanedAgedLock(t *testing.T) {
	s, fl, dir, _ := lockGCFixture(t)
	id := lockGCID("review", 48*time.Hour)
	p := mkLock(t, fl, dir, id, 48*time.Hour)
	if n := s.gcLocks(context.Background()); n != 1 {
		t.Fatalf("gcLocks removed %d, want 1", n)
	}
	if exists(p) {
		t.Fatal("aged orphan lock still present")
	}
}

func TestGCLocks_keepsLockWithLiveRow(t *testing.T) {
	s, fl, dir, st := lockGCFixture(t)
	id := lockGCID("review", 48*time.Hour)
	p := mkLock(t, fl, dir, id, 48*time.Hour)
	if err := st.Insert(context.Background(), store.Session{ExternalID: id, ClaudeSessionID: "c", State: store.Ready, TmuxSession: "cc-" + id}); err != nil {
		t.Fatal(err)
	}
	if n := s.gcLocks(context.Background()); n != 0 || !exists(p) {
		t.Fatalf("lock of a session with a row: removed=%d exists=%v; want kept", n, exists(p))
	}
}

func TestGCLocks_keepsLockWithClosedRow(t *testing.T) {
	// A closed row is still a row: the id has not been purged.
	s, fl, dir, st := lockGCFixture(t)
	id := lockGCID("review", 48*time.Hour)
	p := mkLock(t, fl, dir, id, 48*time.Hour)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: id, ClaudeSessionID: "c", State: store.Idle, TmuxSession: "cc-" + id}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCloseReason(ctx, id, "idle_ttl"); err != nil {
		t.Fatal(err)
	}
	if n := s.gcLocks(ctx); n != 0 || !exists(p) {
		t.Fatalf("lock of a closed-but-present row: removed=%d exists=%v; want kept", n, exists(p))
	}
}

func TestGCLocks_keepsHeldLock(t *testing.T) {
	s, fl, dir, _ := lockGCFixture(t)
	id := lockGCID("review", 48*time.Hour)
	p := mkLock(t, fl, dir, id, 48*time.Hour)
	unlock, err := fl.Lock(id) // a concurrent create/send/close holding the lock
	if err != nil {
		t.Fatal(err)
	}
	if n := s.gcLocks(context.Background()); n != 0 || !exists(p) {
		t.Fatalf("held lock: removed=%d exists=%v; want kept", n, exists(p))
	}
	unlock()
	// Once released it is collectable.
	if n := s.gcLocks(context.Background()); n != 1 || exists(p) {
		t.Fatalf("after release: removed=%d exists=%v; want removed", n, exists(p))
	}
}

func TestGCLocks_ageGates(t *testing.T) {
	s, fl, dir, _ := lockGCFixture(t)
	young := lockGCID("review", 2*time.Hour)
	stampOld := lockGCID("worker", 48*time.Hour)
	mtimeOld := lockGCID("worker", 2*time.Hour)
	noStamp := "hand-named-session"
	badStamp := "pg-router-review-zr-1-20269999T999999"
	futureStamp := lockGCID("review", -48*time.Hour)
	paths := map[string]string{
		"young":       mkLock(t, fl, dir, young, 2*time.Hour),
		"mtimeFresh":  mkLock(t, fl, dir, stampOld, 2*time.Hour),
		"stampFresh":  mkLock(t, fl, dir, mtimeOld, 48*time.Hour),
		"noStamp":     mkLock(t, fl, dir, noStamp, 48*time.Hour),
		"badStamp":    mkLock(t, fl, dir, badStamp, 48*time.Hour),
		"futureStamp": mkLock(t, fl, dir, futureStamp, 48*time.Hour),
	}
	if n := s.gcLocks(context.Background()); n != 0 {
		t.Fatalf("gcLocks removed %d, want 0 (every file fails one age gate)", n)
	}
	for name, p := range paths {
		if !exists(p) {
			t.Errorf("%s: lock removed but should have been kept", name)
		}
	}
}

func TestGCLocks_ignoresNonLockFilesAndDirs(t *testing.T) {
	s, fl, dir, _ := lockGCFixture(t)
	id := lockGCID("review", 48*time.Hour)
	mkLock(t, fl, dir, id, 48*time.Hour)
	old := lockGCNow.Add(-72 * time.Hour)
	var kept []string
	for _, n := range []string{id + ".txt", id + ".lock.bak", "README"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
		kept = append(kept, p)
	}
	sub := filepath.Join(dir, lockGCID("review", 49*time.Hour)+".lock")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	kept = append(kept, sub)
	if n := s.gcLocks(context.Background()); n != 1 {
		t.Fatalf("gcLocks removed %d, want exactly the one .lock file", n)
	}
	for _, p := range kept {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
}

// A row appearing after the unlocked listing (a creator that won the race) must
// win: the row is re-read under the flock, immediately before the unlink.
func TestGCLocks_rowCreatedAfterListingIsRespected(t *testing.T) {
	s, fl, dir, st := lockGCFixture(t)
	id := lockGCID("review", 48*time.Hour)
	p := mkLock(t, fl, dir, id, 48*time.Hour)
	s.d.Lock = insertRowBeforeRemove{Flock: fl, insert: func() {
		_ = st.Insert(context.Background(), store.Session{ExternalID: id, ClaudeSessionID: "c", State: store.Starting, TmuxSession: "cc-" + id})
	}}
	if n := s.gcLocks(context.Background()); n != 0 || !exists(p) {
		t.Fatalf("row inserted between list and remove: removed=%d exists=%v; want kept", n, exists(p))
	}
}

// insertRowBeforeRemove runs a hook after List and before RemoveIfFree, to model
// a creator inserting a row in the gap between the unlocked scan and the unlink.
type insertRowBeforeRemove struct {
	*lock.Flock
	insert func()
}

func (i insertRowBeforeRemove) RemoveIfFree(name string, c func(time.Time) (bool, error)) (bool, error) {
	i.insert()
	return i.Flock.RemoveIfFree(name, c)
}

func TestGCLocks_noopWithoutSweepingLocker(t *testing.T) {
	s, _, _, _ := lockGCFixture(t)
	s.d.Lock = nil
	if n := s.gcLocks(context.Background()); n != 0 {
		t.Fatalf("nil Locker: removed %d", n)
	}
	s.d.Lock = lockHook{before: func() {}}
	if n := s.gcLocks(context.Background()); n != 0 {
		t.Fatalf("non-sweeping Locker: removed %d", n)
	}
}

// Reap runs the sweep as its last step, and it never fails the reap.
func TestReap_collectsOrphanedLocks(t *testing.T) {
	s, fl, dir, _ := lockGCFixture(t)
	id := lockGCID("review", 48*time.Hour)
	p := mkLock(t, fl, dir, id, 48*time.Hour)
	if err := s.Reap(context.Background(), 5, time.Hour); err != nil {
		t.Fatal(err)
	}
	if exists(p) {
		t.Fatal("Reap left an orphaned aged lock file")
	}
}

func TestIDStamp(t *testing.T) {
	cases := []struct {
		id string
		ok bool
	}{
		{"pg-router-review-zr-o51j3.2-20261006T230345.307679000", true},
		{"pg-router-worker-zr-1-20260616T010203", true},
		{"worktree--zr-13y4j.2", false},
		{"nostamp", false},
		{"x-20261006T230345.", false},
		{"x-20261306T230345", false},
	}
	for _, c := range cases {
		if _, ok := idStamp(c.id); ok != c.ok {
			t.Errorf("idStamp(%q) ok=%v, want %v", c.id, ok, c.ok)
		}
	}
}
