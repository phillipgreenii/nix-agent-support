package snapshot

import (
	"testing"
	"time"
)

func TestStoreEmpty(t *testing.T) {
	s := NewStore()
	got, ok := s.Get()
	if ok {
		t.Errorf("expected !ok on empty store, got snap=%+v", got)
	}
}

func TestStoreSetGet(t *testing.T) {
	s := NewStore()
	want := &Snapshot{GeneratedAt: time.Unix(1700000000, 0).UTC()}
	s.Set(want)
	got, ok := s.Get()
	if !ok {
		t.Fatal("expected ok=true after Set")
	}
	if !got.GeneratedAt.Equal(want.GeneratedAt) {
		t.Errorf("got %v want %v", got.GeneratedAt, want.GeneratedAt)
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := NewStore()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.Set(&Snapshot{GeneratedAt: time.Now()})
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_, _ = s.Get()
	}
	<-done
}
