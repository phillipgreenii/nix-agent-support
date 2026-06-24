package sessionmeta_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/phillipgreenii/ccpool/sessionmeta"
)

func TestOpenSetGet_roundTripsOnDisk(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, err := sessionmeta.Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Set(ctx, "zr-abc", "role", "worker"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get(ctx, "zr-abc", "role")
	if err != nil || !ok || got != "worker" {
		t.Fatalf("Get = (%q,%v,%v), want worker,true,nil", got, ok, err)
	}
}

func TestListByMeta_andFilter(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, _ := sessionmeta.Open(db)
	defer s.Close()
	ctx := context.Background()
	_ = s.Set(ctx, "ext-a", "role", "worker")
	_ = s.Set(ctx, "ext-a", "pool", "pr-pool")
	_ = s.Set(ctx, "ext-b", "role", "worker")
	got, err := s.ListByMeta(ctx, map[string]string{"role": "worker", "pool": "pr-pool"})
	if err != nil {
		t.Fatalf("ListByMeta: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"ext-a"}) {
		t.Errorf("ListByMeta = %v, want [ext-a]", got)
	}
}

func TestMeta_nonNilEmpty(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, _ := sessionmeta.Open(db)
	defer s.Close()
	m, err := s.Meta(context.Background(), "none")
	if err != nil || m == nil || len(m) != 0 {
		t.Fatalf("Meta = (%v,%v), want non-nil empty map, nil err", m, err)
	}
}

func TestDelete_removesKey(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, _ := sessionmeta.Open(db)
	defer s.Close()
	ctx := context.Background()
	_ = s.Set(ctx, "ext-a", "role", "worker")
	if err := s.Delete(ctx, "ext-a", "role"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "ext-a", "role"); ok {
		t.Error("key still present after Delete")
	}
	if err := s.Delete(ctx, "ext-a", "role"); err != nil {
		t.Fatalf("Delete(absent) must be nil, got %v", err)
	}
}

// TestConcurrentWriters_twoHandlesSameDB simulates ccpool + pr-pool both holding
// a handle to the SAME on-disk pool DB and writing metadata concurrently. With
// WAL + busy_timeout and single-statement autocommit writes, no write errors and
// the final reads are consistent (last-writer-wins per key, no lost rows).
func TestConcurrentWriters_twoHandlesSameDB(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	a, err := sessionmeta.Open(db) // "ccpool" writer
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	defer a.Close()
	b, err := sessionmeta.Open(db) // "pr-pool" writer
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	defer b.Close()

	ctx := context.Background()
	const n = 50
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := a.Set(ctx, "ext-a", "k", "from-a"); err != nil {
				t.Errorf("a.Set #%d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := b.Set(ctx, "ext-b", "k", "from-b"); err != nil {
				t.Errorf("b.Set #%d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	// Disjoint keys: both sessions present with their writer's value.
	va, oka, _ := a.Get(ctx, "ext-a", "k")
	vb, okb, _ := b.Get(ctx, "ext-b", "k")
	if !oka || va != "from-a" {
		t.Errorf("ext-a/k = (%q,%v), want from-a,true", va, oka)
	}
	if !okb || vb != "from-b" {
		t.Errorf("ext-b/k = (%q,%v), want from-b,true", vb, okb)
	}
}
