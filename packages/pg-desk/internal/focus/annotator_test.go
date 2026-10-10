package focus

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	testRepo = "acme/widgets"
	testType = "pr"
)

func seedEntities(t *testing.T, s *store.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		e := store.Entity{Repo: testRepo, EntityType: testType, EntityID: id, Facts: `{}`, AsOf: "2026-09-10T00:00:00Z", ContentHash: "h" + id}
		if _, err := s.WriteEntityWithLog(e, 0, []string{"created"}, "sync", "2026-09-10T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
}

func newAnnotator(t *testing.T, s *store.Store) *StoreAnnotator {
	t.Helper()
	return NewAnnotator(s, store.NewLocker(store.LockerOptions{LockDir: t.TempDir(), Timeout: 2 * time.Second}))
}

func TestStoreAnnotatorWritesTheFocusSelectedKey(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	seedEntities(t, s, "1")
	var a Annotator = newAnnotator(t, s)

	seq, changed, err := a.SetFocusSelected(testRepo, testType, "1", "2026-09-23", "pg-desk", "operator", "2026-09-23T08:00:00Z")
	if err != nil || !changed || seq == 0 {
		t.Fatalf("first write = %d, %v, %v", seq, changed, err)
	}
	got, found, err := s.GetKVAnnotation(testRepo, testType, "1", store.AnnotationFocusSelected)
	if err != nil || !found || got.Value != "2026-09-23" || got.SetBy != "operator" || got.Origin != "pg-desk" || got.SetAt != "2026-09-23T08:00:00Z" {
		t.Fatalf("annotation = %+v, %v, %v", got, found, err)
	}

	// A re-run with the same value is idempotent: no sequence, no record.
	seq2, changed2, err := a.SetFocusSelected(testRepo, testType, "1", "2026-09-23", "pg-desk", "agent", "2026-09-23T09:00:00Z")
	if err != nil || changed2 || seq2 != 0 {
		t.Fatalf("idempotent re-run = %d, %v, %v", seq2, changed2, err)
	}

	// An entity with no row errors.
	if _, _, err := a.SetFocusSelected(testRepo, testType, "ghost", "none", "pg-desk", "operator", "t"); !errors.Is(err, store.ErrNoEntity) {
		t.Fatalf("unknown entity: err = %v, want ErrNoEntity", err)
	}
}

// failingAnnotator is the substitution the seam exists for: it fails for one
// entity and delegates for the rest.
type failingAnnotator struct {
	inner  Annotator
	failID string
}

var errInjected = errors.New("injected annotation failure")

func (f failingAnnotator) SetFocusSelected(repo, entityType, entityID, value, origin, actor, at string) (int64, bool, error) {
	if entityID == f.failID {
		return 0, false, errInjected
	}
	return f.inner.SetFocusSelected(repo, entityType, entityID, value, origin, actor, at)
}

func TestAnnotatorCanBeReplacedByAFakeThatFailsForOneEntity(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	seedEntities(t, s, "1", "2")
	var a Annotator = failingAnnotator{inner: newAnnotator(t, s), failID: "2"}

	if _, _, err := a.SetFocusSelected(testRepo, testType, "1", "2026-09-23", "pg-desk", "op", "t"); err != nil {
		t.Fatalf("entity 1: %v", err)
	}
	if _, _, err := a.SetFocusSelected(testRepo, testType, "2", "2026-09-23", "pg-desk", "op", "t"); !errors.Is(err, errInjected) {
		t.Fatalf("entity 2: err = %v, want the injected failure", err)
	}
	if _, found, _ := s.GetKVAnnotation(testRepo, testType, "2", store.AnnotationFocusSelected); found {
		t.Fatalf("the failed entity was annotated")
	}
}

// TestAnnotatorSerializesPerEntityUnderTheLocker holds the per-entity lock
// from outside: an Annotator call for that entity waits (and then fails by
// timeout while it is held), a call for another entity does not wait, and
// once released the call goes through.
func TestAnnotatorSerializesPerEntityUnderTheLocker(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	seedEntities(t, s, "1", "2")
	locker := store.NewLocker(store.LockerOptions{LockDir: t.TempDir(), Timeout: 300 * time.Millisecond})
	a := NewAnnotator(s, locker)

	unlock, err := locker.Lock(testRepo, testType, "1")
	if err != nil {
		t.Fatal(err)
	}
	// Another entity is not blocked.
	if _, _, err := a.SetFocusSelected(testRepo, testType, "2", "2026-09-23", "pg-desk", "op", "t"); err != nil {
		t.Fatalf("a different entity was blocked: %v", err)
	}
	// The held entity is: the call times out on the lock and writes nothing.
	if _, _, err := a.SetFocusSelected(testRepo, testType, "1", "2026-09-23", "pg-desk", "op", "t"); !errors.Is(err, store.ErrLockTimeout) {
		t.Fatalf("held entity: err = %v, want ErrLockTimeout", err)
	}
	if _, found, _ := s.GetKVAnnotation(testRepo, testType, "1", store.AnnotationFocusSelected); found {
		t.Fatalf("a write happened without the lock")
	}
	unlock()
	if _, changed, err := a.SetFocusSelected(testRepo, testType, "1", "2026-09-23", "pg-desk", "op", "t"); err != nil || !changed {
		t.Fatalf("after release: %v, %v", changed, err)
	}
}

// TestTwoAnnotatorCallsOnOneEntitySerialize runs two writers for the same
// entity and value: exactly one appends a record (the second reads the first's
// write under the lock and finds the value held), so no redundant record is
// ever appended.
func TestTwoAnnotatorCallsOnOneEntitySerialize(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	seedEntities(t, s, "1")
	a := newAnnotator(t, s)

	var wg sync.WaitGroup
	var mu sync.Mutex
	changedCount := 0
	for _, actor := range []string{"operator", "agent"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := a.SetFocusSelected(testRepo, testType, "1", "2026-09-23", "pg-desk", actor, "t")
			if err != nil {
				t.Errorf("%s: %v", actor, err)
				return
			}
			if changed {
				mu.Lock()
				changedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if changedCount != 1 {
		t.Fatalf("%d of 2 concurrent writers appended a record, want exactly 1", changedCount)
	}
	hist, err := s.ListEntityHistory(testRepo, testType, "1", 0)
	if err != nil {
		t.Fatal(err)
	}
	annotated := 0
	for _, h := range hist {
		for _, k := range h.Kinds {
			if k == store.ChangeKindAnnotationChanged {
				annotated++
			}
		}
	}
	if annotated != 1 {
		t.Fatalf("change_log holds %d annotation_changed records, want 1", annotated)
	}
}
