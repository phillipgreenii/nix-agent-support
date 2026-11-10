package settle

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func TestDirtyOnAKnownFileAddsExactlyItsSessionWithoutAStoreQuery(t *testing.T) {
	e := newGrpEnv(t)
	a := e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	e.add("append", "/c/projects/p/"+gSessB+".jsonl")
	e.same("placed")

	e.cnt.Reset()
	e.s.Dirty(a, 123)
	if q := e.cnt.Queries(); q != 0 {
		t.Fatalf("Dirty on a known file issued %d store queries", q)
	}
	got := e.s.TakeDirty()
	if len(got) != 1 || got[gSessA] != 123 {
		t.Fatalf("dirty set %v, want only %s at 123", got, gSessA)
	}
	if q := e.cnt.Queries(); q != 0 {
		t.Fatalf("TakeDirty with nothing unresolved issued %d store queries", q)
	}
	if again := e.s.TakeDirty(); len(again) != 0 {
		t.Fatalf("set not cleared: %v", again)
	}
}

func TestDirtyOnAnUnknownFileRebuildsOnceThenResolves(t *testing.T) {
	e := newGrpEnv(t)
	e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	e.same("start")
	b := e.add("append", "/c/projects/p/"+gSessB+".jsonl") // not placed yet

	e.cnt.Reset()
	e.s.Dirty(b, 7)
	e.s.Dirty(b, 9)
	if q := e.cnt.Queries(); q != 0 {
		t.Fatalf("Dirty itself queried the store (%d)", q)
	}
	got := e.s.TakeDirty()
	if len(got) != 1 || got[gSessB] != 9 {
		t.Fatalf("dirty set %v, want %s at 9", got, gSessB)
	}
	if e.cnt.Queries() == 0 {
		t.Fatal("the unknown file did not trigger a groups() read")
	}
	// Now known: no more queries.
	e.cnt.Reset()
	e.s.Dirty(b, 11)
	if q := e.cnt.Queries(); q != 0 {
		t.Fatalf("a resolved file still queried (%d)", q)
	}
	if got := e.s.TakeDirty(); got[gSessB] != 11 {
		t.Fatalf("resolved file: %v", got)
	}
}

func TestDirtyDropsAFileThatBelongsToNoConversation(t *testing.T) {
	e := newGrpEnv(t)
	f := e.add("append", "/c/history.jsonl")
	e.s.Dirty(f, 1)
	if got := e.s.TakeDirty(); len(got) != 0 {
		t.Fatalf("a file outside every Conversation was kept: %v", got)
	}
}

func TestTenDirtyCallsForOneSessionLeaveOneEntry(t *testing.T) {
	e := newGrpEnv(t)
	a1 := e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	a2 := e.add("snapshot", "/c/projects/p/"+gSessA+"/subagents/x.jsonl")
	e.same("placed")
	for i := 0; i < 5; i++ {
		e.s.Dirty(a1, int64(i))
		e.s.Dirty(a2, int64(100+i))
	}
	got := e.s.TakeDirty()
	if len(got) != 1 {
		t.Fatalf("%d entries, want 1: %v", len(got), got)
	}
	select {
	case <-e.s.DirtyWake():
	default:
		t.Fatal("no wake signalled")
	}
	select {
	case <-e.s.DirtyWake():
		t.Fatal("the wake channel held more than one signal")
	default:
	}
}

func TestConcurrentDirtyCallsAreSafe(t *testing.T) {
	e := newGrpEnv(t)
	var ids []int64
	for i, s := range []string{gSessA, gSessB, gSessC} {
		ids = append(ids, e.add("append", "/c/projects/p/"+s+".jsonl"))
		_ = i
	}
	e.same("placed")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				e.s.Dirty(ids[i%len(ids)], int64(i))
				if i%50 == 0 {
					e.s.LiveChanged()
					_ = e.s.TakeDirty()
				}
			}
		}()
	}
	wg.Wait()
	e.s.Dirty(ids[0], 1)
	if got := e.s.TakeDirty(); got[gSessA] != 1 {
		t.Fatalf("after the burst: %v", got)
	}
}

func TestFileSessionFollowsAFileThatMovesBetweenConversations(t *testing.T) {
	e := newGrpEnv(t)
	f := e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	e.same("placed")
	e.s.Dirty(f, 1)
	if got := e.s.TakeDirty(); got[gSessA] != 1 {
		t.Fatalf("before the move: %v", got)
	}
	e.rename(f, "/c/projects/p/"+gSessA+".jsonl", "/c/projects/p/"+gSessB+".jsonl")
	e.same("moved")
	e.s.Dirty(f, 2)
	if got := e.s.TakeDirty(); len(got) != 1 || got[gSessB] != 2 {
		t.Fatalf("after the move: %v", got)
	}
}

func TestLiveChangedIsRememberedOnceAndSignals(t *testing.T) {
	e := newGrpEnv(t)
	if e.s.TakeLiveChanged() {
		t.Fatal("live change reported before any")
	}
	e.s.LiveChanged()
	e.s.LiveChanged()
	select {
	case <-e.s.DirtyWake():
	default:
		t.Fatal("LiveChanged did not signal")
	}
	if !e.s.TakeLiveChanged() || e.s.TakeLiveChanged() {
		t.Fatal("LiveChanged must be taken exactly once")
	}
}

// ---- every call site of a file-state change feeds the dirty set ----

// drain empties the dirty set and returns what it held.
func (e *env) drain() map[string]int64 { return e.sett.TakeDirty() }

func (e *env) wantDirty(what string) map[string]int64 {
	e.t.Helper()
	got := e.drain()
	if _, ok := got[sess]; !ok {
		e.t.Fatalf("%s: the Conversation is not in the dirty set %v", what, got)
	}
	return got
}

func TestEveryFileStateWriteFeedsTheDirtySet(t *testing.T) {
	t.Run("copy commit carries the new offset", func(t *testing.T) {
		e := newEnv(t, false)
		content := e.seed(3, time.Hour)
		if got := e.wantDirty("first copy"); got[sess] != int64(len(content)) {
			t.Fatalf("offset %d, want %d", got[sess], len(content))
		}
		more := line(9)
		e.write(e.transcript(), more, e.clk.Now(), true)
		e.ingest(e.transcript())
		if got := e.wantDirty("append"); got[sess] != int64(len(content+more)) {
			t.Fatalf("offset %d, want %d", got[sess], len(content+more))
		}
	})
	t.Run("backfill batch", func(t *testing.T) {
		e := newEnv(t, false)
		e.write(e.transcript(), line(1), e.clk.Now(), false)
		for _, r := range e.cp.IngestBatch([]string{e.path(e.transcript())}) {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
		if got := e.wantDirty("batch"); got[sess] != int64(len(line(1))) {
			t.Fatalf("offset %d", got[sess])
		}
	})
	t.Run("tombstone through a sweep", func(t *testing.T) {
		e := newEnv(t, false)
		e.seed(2, time.Hour)
		e.drain()
		if err := os.Remove(e.path(e.transcript())); err != nil {
			t.Fatal(err)
		}
		if err := e.cp.SweepMissing(map[copier.Identity]bool{}); err != nil {
			t.Fatal(err)
		}
		e.wantDirty("tombstone")
	})
	t.Run("doctor generation", func(t *testing.T) {
		e := newEnv(t, false)
		e.seed(5, time.Hour)
		file := e.fileID()
		f, _ := e.st.File(file)
		cp := e.st.CopyPath(file, f.CurGen)
		b, _ := os.ReadFile(cp)
		b[10] ^= 0xff
		if err := os.WriteFile(cp, b, 0o600); err != nil {
			t.Fatal(err)
		}
		e.drain()
		if _, err := e.sett.Doctor(); err != nil {
			t.Fatal(err)
		}
		e.wantDirty("doctor new generation")
	})
	t.Run("settler generation (torn tail)", func(t *testing.T) {
		e := newEnv(t, false)
		e.seed(2, 48*time.Hour)
		partial := `{"type":"user","uuid":"t2","sess`
		e.write(e.transcript(), partial, e.clk.Now().Add(-48*time.Hour), true)
		e.ingest(e.transcript())
		e.drain()
		e.tick()
		if facts, _ := e.st.TornTails(); len(facts) != 1 {
			t.Fatalf("no torn-tail fact written: %+v", facts)
		}
		e.wantDirty("torn-tail fact")
	})
	t.Run("removal path close", func(t *testing.T) {
		e := newEnv(t, false)
		e.remove_(true)
		e.oldSettled(6, removeAge+time.Hour)
		e.drain()
		if rep := e.remove(sess); len(rep.Removed) != 1 {
			t.Fatalf("%+v", rep)
		}
		e.wantDirty("removal closes the paths")
	})
	t.Run("extract path write", func(t *testing.T) {
		e := newEnv(t, false)
		e.removedConversation()
		e.drain()
		if _, err := e.sett.Extract(sess, ""); err != nil {
			t.Fatal(err)
		}
		e.wantDirty("extract registers the restored path")
	})
}

// The group rebuild TakeDirty does for a file it has not placed queues for the
// run lock through the same accounting as the other lock takers.
func TestTakeDirtyRebuildCountsItsRunLockWait(t *testing.T) {
	e := newGrpEnv(t)
	e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	e.same("start")
	b := e.add("append", "/c/projects/p/"+gSessB+".jsonl") // not placed yet
	e.s.Dirty(b, 1)

	e.s.run.Lock()
	done := make(chan time.Duration)
	go func() {
		_, w := e.s.takeDirty()
		done <- w
	}()
	time.Sleep(20 * time.Millisecond)
	e.s.run.Unlock()
	if w := <-done; w < 10*time.Millisecond {
		t.Fatalf("reported wait %v, want at least the time the lock was held", w)
	}
	lbl := map[string]string{"op": "take_dirty"}
	if n, _ := e.s.cfg.Obs.Reg.Counter(obs.MSettleRunLockWaits, lbl); n != 1 {
		t.Fatalf("take_dirty waits counted %v, want 1", n)
	}
	if sec, _ := e.s.cfg.Obs.Reg.Counter(obs.MSettleRunLockWaitSeconds, lbl); sec <= 0 {
		t.Fatalf("take_dirty wait seconds %v, want > 0", sec)
	}
}
