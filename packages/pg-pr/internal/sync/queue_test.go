package sync

import "testing"

func TestRefreshQueue_DedupAndFIFO(t *testing.T) {
	q := newRefreshQueue()
	q.enqueue(prKey{Repo: "o/r", Number: 1})
	q.enqueue(prKey{Repo: "o/r", Number: 2})
	q.enqueue(prKey{Repo: "o/r", Number: 1}) // dup → ignored, keeps position
	if got := q.depth(); got != 2 {
		t.Fatalf("depth = %d, want 2", got)
	}
	k1, ok := q.dequeue()
	if !ok || k1 != (prKey{Repo: "o/r", Number: 1}) {
		t.Fatalf("first dequeue = %+v ok=%v, want o/r#1", k1, ok)
	}
	k2, _ := q.dequeue()
	if k2 != (prKey{Repo: "o/r", Number: 2}) {
		t.Fatalf("second dequeue = %+v, want o/r#2", k2)
	}
	if _, ok := q.dequeue(); ok {
		t.Fatal("empty queue should return ok=false")
	}
	q.enqueue(prKey{Repo: "o/r", Number: 1})
	if q.depth() != 1 {
		t.Fatal("re-enqueue after drain should add")
	}
}
