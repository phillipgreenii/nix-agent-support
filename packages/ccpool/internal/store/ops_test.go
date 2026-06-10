package store

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
)

func TestInsertAndGet(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	in := Session{Name: "alpha", UUID: "u-alpha", CWD: "/tmp/a", State: Starting, TmuxSession: "cc-alpha"}
	if err := st.Insert(ctx, in); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, ok, err := st.GetByName(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("GetByName: ok=%v err=%v", ok, err)
	}
	if got.UUID != "u-alpha" || got.State != Starting {
		t.Errorf("got %+v", got)
	}
	if got.Generation != 1 {
		t.Errorf("Generation = %d, want 1 (set on insert)", got.Generation)
	}
	if got.CreatedAt != 1000 || got.LastActivityAt != 1000 {
		t.Errorf("timestamps from fake clock not applied: %+v", got)
	}

	byUUID, ok, err := st.GetByUUID(ctx, "u-alpha")
	if err != nil || !ok || byUUID.Name != "alpha" {
		t.Fatalf("GetByUUID: ok=%v err=%v name=%q", ok, err, byUUID.Name)
	}

	_, ok, err = st.GetByName(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("GetByName(missing): ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestList_orderedByLastActivityDesc(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "u-a")
	bumpClock(t, st, 10)
	mustInsert(t, st, "b", "u-b")

	got, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "b" || got[1].Name != "a" {
		t.Fatalf("List order = %v, want [b a]", names(got))
	}
}

// test helpers
func mustInsert(t *testing.T, st *Store, name, uuid string) {
	t.Helper()
	if err := st.Insert(context.Background(), Session{Name: name, UUID: uuid, State: Starting}); err != nil {
		t.Fatalf("Insert %s: %v", name, err)
	}
}
func names(ss []Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}
func bumpClock(t *testing.T, st *Store, secs int) {
	t.Helper()
	f, ok := st.clock.(*clock.Fake)
	if !ok {
		t.Fatal("test store must use *clock.Fake")
	}
	f.Advance(time.Duration(secs) * time.Second)
}
