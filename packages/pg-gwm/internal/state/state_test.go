package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
)

func newStore(t *testing.T) Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return Open(t.TempDir())
}

func TestLockBusyAfterWait(t *testing.T) {
	s := newStore(t)
	l, err := s.Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	start := time.Now()
	_, err = s.Lock(context.Background(), 100*time.Millisecond)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > time.Second || d < 90*time.Millisecond {
		t.Fatalf("waited %v", d)
	}
	l.Unlock()
	l.Unlock() // idempotent
	l2, err := s.Lock(context.Background(), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	l2.Unlock()
}

func TestCommitAtomic(t *testing.T) {
	s := newStore(t)
	l, err := s.Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l.State().Worktrees = []Worktree{{ID: "w-first", Status: Available}}
	if err := l.Commit(); err != nil {
		t.Fatal(err)
	}
	// Crash after the temp write: the rename never happens.
	s.rename = func(string, string) error { return errors.New("crash") }
	l.s = s
	l.State().Worktrees = []Worktree{{ID: "w-second", Status: Available}}
	if err := l.Commit(); err == nil {
		t.Fatal("commit should fail")
	}
	l.Unlock()
	got, err := s.ReadUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Worktrees) != 1 || got.Worktrees[0].ID != "w-first" {
		t.Fatalf("state = %+v", got)
	}
	ents, _ := os.ReadDir(s.Dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReadUnlockedMissing(t *testing.T) {
	s := newStore(t)
	st, err := s.ReadUnlocked()
	if err != nil || st.SchemaVersion != SchemaVersion || len(st.Worktrees) != 0 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}

func TestStateRoundTrip(t *testing.T) {
	s := newStore(t)
	exp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, ttl := range []config.Duration{{D: 30 * time.Minute}, config.NoDuration, {}} {
		l, _ := s.Lock(context.Background(), time.Second)
		l.State().Worktrees = []Worktree{{ID: "w-a", Status: Leased, Lease: &Lease{ID: "l-a", Name: "n", TTL: ttl, Expiry: &exp, Metadata: map[string]string{"k": "v"}}}}
		if err := l.Commit(); err != nil {
			t.Fatal(err)
		}
		l.Unlock()
		got, err := s.ReadUnlocked()
		if err != nil {
			t.Fatal(err)
		}
		g := got.Worktrees[0].Lease
		if g.TTL != ttl || g.Name != "n" || g.Metadata["k"] != "v" || !g.Expiry.Equal(exp) {
			t.Fatalf("lease = %+v", g)
		}
	}
}

func TestEffectiveStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	cases := []struct {
		w    Worktree
		want Status
	}{
		{Worktree{Status: Leased, Lease: &Lease{Expiry: &past}}, LeaseExpired},
		{Worktree{Status: Leased, Lease: &Lease{Expiry: &now}}, LeaseExpired},
		{Worktree{Status: Leased, Lease: &Lease{Expiry: &future}}, Leased},
		{Worktree{Status: Leased, Lease: &Lease{}}, Leased},
		{Worktree{Status: Preparing, Lease: &Lease{Expiry: &past}}, Preparing},
		{Worktree{Status: Available}, Available},
	}
	for i, c := range cases {
		if got := c.w.Effective(now); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
	if LeaseExpired != "lease-expired" || Unmanaged != "unmanaged" {
		t.Fatal("derived status strings")
	}
}

func TestFindNameCaseInsensitive(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	l := &Locked{st: State{
		Worktrees: []Worktree{
			{ID: "w-1", Status: Leased, Lease: &Lease{ID: "l-1", Name: "Potato"}},
			{ID: "w-2", Status: Leased, Lease: &Lease{ID: "l-2", Name: "gone", Expiry: &past}},
			{ID: "w-3", Status: Preparing, Lease: &Lease{ID: "l-3", Name: "prep"}},
			{ID: "w-4", Status: Available},
		},
		Tombstones: []Tombstone{
			{Kind: "released", LeaseID: "l-old", Name: "Salad", At: now.Add(-2 * time.Hour)},
			{Kind: "reclaimed", LeaseID: "l-new", Name: "salad", At: now.Add(-time.Hour)},
		},
	}}
	if w, tb := l.FindName("potato", now); w == nil || w.ID != "w-1" || tb != nil {
		t.Fatalf("potato: %v %v", w, tb)
	}
	if w, _ := l.FindName("GONE", now); w == nil || w.ID != "w-2" {
		t.Fatal("expired lease still holds its name")
	}
	if w, _ := l.FindName("PREP", now); w == nil || w.ID != "w-3" {
		t.Fatal("preparing lease holds its name")
	}
	// A released lease's name is free: only a tombstone answers.
	w, tb := l.FindName("SALAD", now)
	if w != nil || tb == nil || tb.LeaseID != "l-new" {
		t.Fatalf("salad: %v %v", w, tb)
	}
	if w, tb := l.FindName("", now); w != nil || tb != nil {
		t.Fatal("empty name matches nothing")
	}
	if w, tb := l.FindName("nope", now); w != nil || tb != nil {
		t.Fatal("unknown name")
	}
}

func TestTombstones(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	l := &Locked{st: State{
		Worktrees:  []Worktree{{ID: "w-1", Status: Leased, Lease: &Lease{ID: "l-2", Name: "second"}}},
		Tombstones: []Tombstone{{Kind: "released", LeaseID: "l-1", Name: "first", At: now.Add(-24 * time.Hour)}},
	}}
	// The worktree was re-leased; the old lease id still answers.
	if w, tb := l.FindLease("l-1"); w != nil || tb == nil || tb.Kind != "released" {
		t.Fatalf("l-1: %v %v", w, tb)
	}
	if w, _ := l.FindLease("l-2"); w == nil || w.ID != "w-1" {
		t.Fatal("live lease")
	}
	if w, tb := l.FindLease("l-9"); w != nil || tb != nil {
		t.Fatal("unknown lease")
	}
	// Edge: At + retention <= now is pruned; one second short is kept.
	l.PruneTombstones(25*time.Hour, now)
	if len(l.st.Tombstones) != 1 {
		t.Fatal("pruned too early")
	}
	l.PruneTombstones(24*time.Hour, now)
	if len(l.st.Tombstones) != 0 {
		t.Fatal("not pruned at retention")
	}
	if _, tb := l.FindLease("l-1"); tb != nil {
		t.Fatal("tombstone survived prune")
	}
}

func TestIDs(t *testing.T) {
	w, l := regexp.MustCompile(`^w-[a-z2-7]{10}$`), regexp.MustCompile(`^l-[a-z2-7]{10}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		a, b := NewWorktreeID(), NewLeaseID()
		if !w.MatchString(a) || !l.MatchString(b) || seen[a] || seen[b] {
			t.Fatalf("ids %q %q", a, b)
		}
		seen[a], seen[b] = true, true
	}
}

func TestOpHeld(t *testing.T) {
	s := newStore(t)
	if held, err := s.OpHeld("w-1"); err != nil || held {
		t.Fatalf("held=%v err=%v", held, err)
	}
	rel, err := s.HoldOp("w-1")
	if err != nil {
		t.Fatal(err)
	}
	if held, err := s.OpHeld("w-1"); err != nil || !held {
		t.Fatalf("held=%v err=%v", held, err)
	}
	if _, err := s.HoldOp("w-1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second hold: %v", err)
	}
	if held, _ := s.OpHeld("w-2"); held {
		t.Fatal("other op held")
	}
	rel()
	if held, err := s.OpHeld("w-1"); err != nil || held {
		t.Fatalf("after release held=%v err=%v", held, err)
	}
}

func TestCreateHoldSerializes(t *testing.T) {
	s := newStore(t)
	rel1, err := s.HoldCreate(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan time.Time, 1)
	go func() {
		rel2, err := s.HoldCreate(context.Background(), 5*time.Second)
		if err != nil {
			got <- time.Time{}
			return
		}
		got <- time.Now()
		rel2()
	}()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-got:
		t.Fatal("second creation did not wait")
	default:
	}
	released := time.Now()
	rel1()
	at := <-got
	if at.IsZero() || at.Before(released) {
		t.Fatalf("second hold at %v, first released %v", at, released)
	}
	if _, err := s.HoldCreate(context.Background(), 0); err != nil {
		t.Fatalf("free after release: %v", err)
	}
}

func TestEventLogDailyFiles(t *testing.T) {
	s := newStore(t)
	s.Retention = config.Duration{D: 48 * time.Hour}
	day := func(n int) time.Time { return time.Date(2026, 3, n, 15, 0, 0, 0, time.UTC) }
	cur := day(1)
	s.Clock = func() time.Time { return cur }
	files := func() []string {
		m, _ := filepath.Glob(filepath.Join(s.Dir, "events-*.jsonl"))
		for i := range m {
			m[i] = filepath.Base(m[i])
		}
		sort.Strings(m)
		return m
	}
	for n := 1; n <= 3; n++ {
		cur = day(n)
		for i := 0; i < 2; i++ {
			if err := s.Append(Event{Kind: "renew", LeaseID: "l-1", Outcome: "ok"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := []string{"events-20260301.jsonl", "events-20260302.jsonl", "events-20260303.jsonl"}
	if got := files(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v", got)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, want[0]))
	if strings.Count(string(b), "\n") != 2 {
		t.Fatalf("day 1 file = %q", b)
	}
	// Day 4: day 1 ends at day 2 00:00; + 48h = day 4 00:00 <= now, so it goes.
	cur = day(4)
	if err := s.Append(Event{Kind: "renew"}); err != nil {
		t.Fatal(err)
	}
	if got := files(); strings.Join(got, ",") != "events-20260302.jsonl,events-20260303.jsonl,events-20260304.jsonl" {
		t.Fatalf("after day 4: %v", got)
	}
	// A "none" retention keeps everything.
	s.Retention = config.NoDuration
	cur = day(20)
	_ = s.Append(Event{Kind: "renew"})
	if len(files()) != 4 {
		t.Fatalf("none retention: %v", files())
	}
}

func TestRegistry(t *testing.T) {
	newStore(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "repo")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(main, link); err != nil {
		t.Fatal(err)
	}
	r := Registry{Path: filepath.Join(t.TempDir(), "sub", "registry.json")}
	if es, err := r.List(); err != nil || len(es) != 0 {
		t.Fatalf("empty: %v %v", es, err)
	}
	for _, p := range []string{main, link, main} {
		if err := r.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	es, err := r.List()
	if err != nil || len(es) != 1 {
		t.Fatalf("entries = %+v %v", es, err)
	}
	want, _ := filepath.EvalSymlinks(main)
	if es[0].Main != want {
		t.Fatalf("main = %q, want %q", es[0].Main, want)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := r.MarkMissing(link, t0); err != nil {
		t.Fatal(err)
	}
	_ = r.MarkMissing(main, t0.Add(time.Hour)) // first mark stays
	es, _ = r.List()
	if es[0].MissingSince == nil || !es[0].MissingSince.Equal(t0) {
		t.Fatalf("missing = %v", es[0].MissingSince)
	}
	_ = r.Prune(48*time.Hour, t0.Add(47*time.Hour))
	if es, _ = r.List(); len(es) != 1 {
		t.Fatal("pruned too early")
	}
	// Re-adding clears the mark.
	_ = r.Add(main)
	if es, _ = r.List(); es[0].MissingSince != nil {
		t.Fatal("Add keeps the missing mark")
	}
	_ = r.MarkMissing(main, t0)
	_ = r.Prune(48*time.Hour, t0.Add(48*time.Hour))
	if es, _ = r.List(); len(es) != 0 {
		t.Fatalf("not pruned at retention: %+v", es)
	}
}

func TestCountersOutsideLock(t *testing.T) {
	s := newStore(t)
	c := Counters{Path: filepath.Join(UserDir(), "counters.json")}
	if snap, err := c.Read(); err != nil || len(snap.Repos) != 0 {
		t.Fatalf("empty: %+v %v", snap, err)
	}
	l, err := s.Lock(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	done := make(chan struct{})
	go func() {
		c.Refusal("app-abc123", "busy")
		c.Refusal("app-abc123", "busy")
		c.Refusal("app-abc123", "name-held")
		c.Refusal("other-111111", "internal")
		c.Reaped("app-abc123", 2, 1)
		c.Reaped("app-abc123", 1, 0)
		c.LeasePhase("app-abc123", "wait", 0.4)
		c.LeasePhase("app-abc123", "wait", 3)
		c.LeasePhase("app-abc123", "create", 5000)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("counting blocked on the repo lock")
	}
	snap, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	a := snap.Repos["app-abc123"]
	if a.Refusals["busy"] != 2 || a.Refusals["name-held"] != 1 || snap.Repos["other-111111"].Refusals["internal"] != 1 {
		t.Fatalf("refusals = %+v", snap)
	}
	if a.ReapRemoved != 3 || a.ReapKept != 1 {
		t.Fatalf("reap = %d/%d", a.ReapRemoved, a.ReapKept)
	}
	w := a.LeasePhases["wait"]
	if w.Count != 2 || w.Sum != 3.4 || w.Buckets[0] != 1 || w.Buckets[3] != 2 || w.Buckets[len(w.Buckets)-1] != 2 {
		t.Fatalf("wait histogram = %+v", w)
	}
	cr := a.LeasePhases["create"]
	if cr.Count != 1 || cr.Buckets[len(cr.Buckets)-1] != 0 {
		t.Fatalf("create histogram = %+v", cr)
	}
	if LeasePhaseBuckets[len(LeasePhaseBuckets)-1] != 1800 {
		t.Fatal("largest bucket bound")
	}
	// Best effort: an unwritable location is swallowed.
	Counters{Path: "/dev/null/x/counters.json"}.Refusal("x", "busy")
}

func TestReaperRecordMerges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reaper.json")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ok := t0.Add(time.Minute)
	if err := WriteReaperRecord(path, ReaperRecord{
		Started: t0, Ended: ok, Outcome: "ok", LastSuccess: &ok,
		Repos: map[string]RepoReap{"a-111111": {Label: "a-111111", At: ok, Outcome: "ok", Removed: 2}, "b-222222": {Label: "b-222222", At: ok, Outcome: "ok"}},
	}); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(time.Hour)
	if err := WriteReaperRecord(path, ReaperRecord{
		Started: t1, Ended: t1.Add(time.Second), Outcome: "error",
		Repos: map[string]RepoReap{"a-111111": {Label: "a-111111", At: t1, Outcome: "error", Kept: 3}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReaperRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "error" || !got.Started.Equal(t1) {
		t.Fatalf("run fields = %+v", got)
	}
	if got.LastSuccess == nil || !got.LastSuccess.Equal(ok) {
		t.Fatalf("last success lost: %v", got.LastSuccess)
	}
	if a := got.Repos["a-111111"]; a.Outcome != "error" || a.Kept != 3 || a.Removed != 0 {
		t.Fatalf("a = %+v", a)
	}
	if b, found := got.Repos["b-222222"]; !found || b.Outcome != "ok" {
		t.Fatalf("other repo dropped: %+v", got.Repos)
	}
	if empty, err := ReadReaperRecord(filepath.Join(t.TempDir(), "none.json")); err != nil || len(empty.Repos) != 0 {
		t.Fatal("missing file")
	}
}

func TestHoldReapSingleFlight(t *testing.T) {
	s := newStore(t)
	rel, err := s.HoldReap()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.HoldReap(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second hold: %v", err)
	}
	rel()
	rel2, err := s.HoldReap()
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	rel2()
}

func TestUpdateRepoReapLeavesRunFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reaper.json")
	ok := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	if err := WriteReaperRecord(path, ReaperRecord{
		Started: ok, Ended: ok, Outcome: "completed", LastSuccess: &ok,
		Repos: map[string]RepoReap{"a-1": {Label: "a-1", Outcome: "completed"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRepoReap(path, RepoReap{Label: "b-2", Outcome: "deadline", Removed: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReaperRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Started.Equal(ok) || got.Outcome != "completed" || got.LastSuccess == nil || !got.LastSuccess.Equal(ok) {
		t.Errorf("run fields changed: %+v", got)
	}
	if len(got.Repos) != 2 || got.Repos["b-2"].Removed != 3 || got.Repos["a-1"].Outcome != "completed" {
		t.Errorf("repos = %+v", got.Repos)
	}
}
