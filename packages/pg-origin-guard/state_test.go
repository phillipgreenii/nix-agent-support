package originguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T, clock Clock, maxBackoff time.Duration) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "state"), clock, maxBackoff)
}

func TestStoreModesAndRoundTrip(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	err := s.Update("zr", func(st *RealmState) error {
		st.Attempts = 2
		st.LastError = "boom"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Load("zr")
	if err != nil || st.Attempts != 2 || st.LastError != "boom" {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	for _, d := range []string{s.Dir(), s.realmsDir()} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v err=%v, want 0700", d, fi.Mode().Perm(), err)
		}
	}
	for _, f := range []string{"zr.json", "zr.state.lock"} {
		fi, err := os.Stat(filepath.Join(s.realmsDir(), f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v err=%v, want 0600", f, fi.Mode().Perm(), err)
		}
	}
	// no temp files left behind
	m, _ := filepath.Glob(filepath.Join(s.realmsDir(), ".*tmp-*"))
	if len(m) != 0 {
		t.Errorf("leftover temp files: %v", m)
	}
}

func TestStoreTightensExistingDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(dir, "realms"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir, newClock(), 0)
	if err := s.Update("zr", func(*RealmState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestStoreUpdateErrorWritesNothing(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	boom := errors.New("boom")
	if err := s.Update("zr", func(st *RealmState) error { st.Attempts = 9; return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if st, _ := s.Load("zr"); st.Attempts != 0 {
		t.Errorf("state written despite error: %+v", st)
	}
}

func TestStoreRejectsBadRealmName(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	for _, name := range []string{"", "../x", "a/b", "A", ".hidden"} {
		var ue *UsageError
		if _, err := s.Load(name); !errors.As(err, &ue) {
			t.Errorf("Load(%q) err = %v, want UsageError", name, err)
		}
	}
}

func TestStoreCorruptFileMovedAside(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	if err := s.Update("zr", func(st *RealmState) error { st.Attempts = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.realmsDir(), "zr.json")
	for i, junk := range []string{"{not json", "[1,2,3]"} {
		if err := os.WriteFile(p, []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := s.Load("zr")
		if err != nil || st.Attempts != 0 {
			t.Fatalf("round %d: st=%+v err=%v, want no state", i, st, err)
		}
		got, err := os.ReadFile(p + ".corrupt")
		if err != nil || string(got) != junk {
			t.Fatalf("round %d: .corrupt = %q err=%v (earlier one must be overwritten)", i, got, err)
		}
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("round %d: corrupt file still in place", i)
		}
	}
}

func TestStoreCapsNextAttemptAtWallClock(t *testing.T) {
	clock := newClock()
	s := newTestStore(t, clock, 30*time.Minute)
	if err := s.Update("zr", func(st *RealmState) error {
		st.NextAttemptAt = clock.Now().Add(48 * time.Hour) // clock was set far ahead once
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load("zr")
	if want := clock.Now().Add(30 * time.Minute); !st.NextAttemptAt.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want %v", st.NextAttemptAt, want)
	}
}

func TestStoreRacesBetweenGoroutinesOnRealFlock(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	const n, per = 8, 15
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			for range per {
				if err := s.Update("zr", func(st *RealmState) error {
					v := st.Attempts
					time.Sleep(time.Microsecond) // widen the read-modify-write window
					st.Attempts = v + 1
					return nil
				}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if st, _ := s.Load("zr"); st.Attempts != n*per {
		t.Errorf("Attempts = %d, want %d (lost updates)", st.Attempts, n*per)
	}
}

func TestLoginLockIsExclusive(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	lk, err := s.TryLogin("zr")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TryLogin("zr"); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second TryLogin err = %v, want ErrLockHeld", err)
	}
	if other, err := s.TryLogin("other"); err != nil {
		t.Fatalf("a different realm must not block: %v", err)
	} else {
		other.Unlock()
	}
	lk.Unlock()
	lk2, err := s.TryLogin("zr")
	if err != nil {
		t.Fatalf("after Unlock: %v", err)
	}
	lk2.Unlock()
}

func TestWaitLoginGetsLockOnRelease(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	lk, err := s.TryLogin("zr")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		lk.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := s.WaitLogin(ctx, "zr", 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	got.Unlock()
}

func TestWaitLoginTimesOut(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	lk, _ := s.TryLogin("zr")
	defer lk.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.WaitLogin(ctx, "zr", 5*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleAttemptWithFreeLockIsIgnored(t *testing.T) {
	clock := newClock()
	s := newTestStore(t, clock, 0)
	if err := s.Update("zr", func(st *RealmState) error {
		st.AttemptStartedAt = clock.Now().Add(-time.Hour)
		st.HolderPID = 4242
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.Attempting("zr"); err != nil || got {
		t.Fatalf("free lock: Attempting = %v err=%v, want false", got, err)
	}
	lk, _ := s.TryLogin("zr")
	defer lk.Unlock()
	if got, _, err := s.Attempting("zr"); err != nil || !got {
		t.Fatalf("held lock: Attempting = %v err=%v, want true", got, err)
	}
}

func TestHeldLockWithoutAttemptIsNotAttempting(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	lk, _ := s.TryLogin("zr")
	defer lk.Unlock()
	if got, _, _ := s.Attempting("zr"); got {
		t.Error("no attempt_started_at must not read as attempting")
	}
}

func TestIgnoreMarker(t *testing.T) {
	s := newTestStore(t, newClock(), 0)
	if s.Ignored("zr") {
		t.Fatal("ignored before set")
	}
	if err := s.SetIgnored("zr", true); err != nil {
		t.Fatal(err)
	}
	if !s.Ignored("zr") {
		t.Fatal("not ignored after set")
	}
	if err := s.SetIgnored("zr", false); err != nil || s.Ignored("zr") {
		t.Fatalf("err=%v ignored=%v", err, s.Ignored("zr"))
	}
	if err := s.SetIgnored("zr", false); err != nil {
		t.Errorf("clearing an absent marker: %v", err)
	}
}

func TestApplyTransitions(t *testing.T) {
	b := BreakerConfig{FailureThreshold: 2, OpenDuration: time.Minute}
	var st RealmState
	st.ApplyStartupFailure(t0, ReasonNetwork, "x", b)
	if st.BreakerOpen(t0) {
		t.Fatal("one failure must not open the breaker")
	}
	st.ApplyStartupFailure(t0, ReasonAuthRejected, "x", b)
	if st.ConsecutiveFailures != 1 {
		t.Fatalf("auth-rejected must not count: %d", st.ConsecutiveFailures)
	}
	st.ApplyStartupFailure(t0, ReasonTimeout, "x", b)
	if !st.BreakerOpen(t0.Add(59*time.Second)) || st.BreakerOpen(t0.Add(time.Minute)) {
		t.Fatalf("breaker window wrong: open_until=%v", st.OpenUntil)
	}
	st.ApplyOK(t0)
	if st.ConsecutiveFailures != 0 || st.BreakerOpen(t0) || !st.LastSuccessAt.Equal(t0) {
		t.Fatalf("ok must reset: %+v", st)
	}

	l := LoginConfig{Backoff: []time.Duration{5 * time.Minute, 10 * time.Minute, 30 * time.Minute}}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		st.AttemptStartedAt = t0
		st.ApplyLoginFailure(t0, l, "e")
		if !st.NextAttemptAt.Equal(t0.Add(w)) || st.Attempts != i+1 || !st.AttemptStartedAt.IsZero() {
			t.Fatalf("attempt %d: %+v, want next %v", i+1, st, w)
		}
	}
}
