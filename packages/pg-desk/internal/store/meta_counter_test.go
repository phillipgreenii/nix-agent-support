package store

import (
	"strconv"
	"sync"
	"testing"
)

// TestIncrMetaLosesNoIncrementUnderConcurrentWriters runs writers on SEPARATE
// handles of one database file (as concurrent processes would): a read then a
// write loses increments under that load, the single-statement increment does
// not.
func TestIncrMetaLosesNoIncrementUnderConcurrentWriters(t *testing.T) {
	seed := OpenForTest(t)
	const writers, perWriter = 8, 40
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		h, err := Open(seed.path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Close() })
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := h.IncrMeta("ctr", 1); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("IncrMeta: %v", err)
	}
	got, found, err := seed.GetMeta("ctr")
	if err != nil || !found {
		t.Fatalf("GetMeta: %q %v %v", got, found, err)
	}
	if want := strconv.Itoa(writers * perWriter); got != want {
		t.Errorf("counter = %s, want %s: an increment was lost", got, want)
	}
}

func TestIncrMetaCreatesAddsAndKeepsTheValueAsText(t *testing.T) {
	s := OpenForTest(t)
	for _, d := range []int64{3, 4} {
		if err := s.IncrMeta("c", d); err != nil {
			t.Fatal(err)
		}
	}
	var typ string
	if err := s.sql.QueryRow(`SELECT typeof(value) FROM meta WHERE key = 'c'`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.GetMeta("c"); v != "7" || typ != "text" {
		t.Errorf("value %q typeof %q, want 7 and text", v, typ)
	}
	// A value that is not an integer casts to 0 and the increment still lands.
	if err := s.SetMeta("bad", "not a number"); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrMeta("bad", 2); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.GetMeta("bad"); v != "2" {
		t.Errorf("corrupt counter after +2 = %q, want 2", v)
	}
}

func TestSetMetaIfAbsentHasExactlyOneWinner(t *testing.T) {
	seed := OpenForTest(t)
	const writers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for w := 0; w < writers; w++ {
		h, err := Open(seed.path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Close() })
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			won, err := h.SetMetaIfAbsent("once", v)
			if err != nil {
				t.Error(err)
				return
			}
			if won {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(strconv.Itoa(w))
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d writers won a set-if-absent, want exactly 1", wins)
	}
}

func TestMarkEntityInactiveCountsOnlyARealChange(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.UpsertEntity(Entity{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Facts: `{}`, AsOf: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	changed, err := s.MarkEntityInactive("o/r", "pr", "o/r#1", "n")
	if err != nil || !changed {
		t.Fatalf("first mark: changed=%v err=%v", changed, err)
	}
	if changed, err = s.MarkEntityInactive("o/r", "pr", "o/r#1", "n"); err != nil || changed {
		t.Errorf("second mark: changed=%v err=%v, want unchanged", changed, err)
	}
	if changed, err = s.MarkEntityInactive("o/r", "pr", "o/r#404", "n"); err != nil || changed {
		t.Errorf("absent entity: changed=%v err=%v, want unchanged", changed, err)
	}
	if v, _, _ := s.GetMeta("n"); v != "1" {
		t.Errorf("counter = %q, want 1 (one real change)", v)
	}
	e, found, err := s.GetEntity("o/r", "pr", "o/r#1")
	if err != nil || !found || !e.Inactive {
		t.Errorf("entity after mark: %+v found=%v err=%v, want inactive", e, found, err)
	}
}

func TestMarkEntityInactiveRefusesAnOldSchemaStore(t *testing.T) {
	s := OpenForTest(t)
	if _, err := s.MarkEntityInactive("o/r", "pr", "o/r#1", "n"); err == nil {
		t.Error("marking on a schema-1 store must fail, never silently do nothing")
	}
}
