package settle

import (
	"reflect"
	"testing"
	"time"
)

var dueT0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestDueHeapEmpty(t *testing.T) {
	h := NewDueHeap()
	if _, ok := h.Next(); ok {
		t.Fatal("Next on empty heap reported ok")
	}
	if got := h.Pop(dueT0); len(got) != 0 {
		t.Fatalf("Pop on empty = %v", got)
	}
}

func TestDueHeapOrdering(t *testing.T) {
	h := NewDueHeap()
	h.Push("c", dueT0.Add(3*time.Second))
	h.Push("a", dueT0.Add(1*time.Second))
	h.Push("b", dueT0.Add(2*time.Second))
	if at, ok := h.Next(); !ok || !at.Equal(dueT0.Add(time.Second)) {
		t.Fatalf("Next = %v %v", at, ok)
	}
	got := h.Pop(dueT0.Add(10 * time.Second))
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pop = %v want %v", got, want)
	}
}

func TestDueHeapPopBoundaryInclusive(t *testing.T) {
	h := NewDueHeap()
	h.Push("a", dueT0)
	if got := h.Pop(dueT0.Add(-time.Nanosecond)); len(got) != 0 {
		t.Fatalf("popped before due: %v", got)
	}
	if got := h.Pop(dueT0); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("entry due exactly at now not popped: %v", got)
	}
}

func TestDueHeapPopAllDueLeavesRest(t *testing.T) {
	h := NewDueHeap()
	h.Push("a", dueT0.Add(1*time.Second))
	h.Push("b", dueT0.Add(2*time.Second))
	h.Push("c", dueT0.Add(5*time.Second))
	got := h.Pop(dueT0.Add(2 * time.Second))
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pop = %v want %v", got, want)
	}
	if at, ok := h.Next(); !ok || !at.Equal(dueT0.Add(5*time.Second)) {
		t.Fatalf("Next after pop = %v %v", at, ok)
	}
	if h.Len() != 1 {
		t.Fatalf("Len = %d", h.Len())
	}
}

func TestDueHeapReplaceOnPush(t *testing.T) {
	h := NewDueHeap()
	h.Push("a", dueT0.Add(1*time.Second))
	h.Push("a", dueT0.Add(9*time.Second)) // later replaces earlier
	if h.Len() != 1 {
		t.Fatalf("Len = %d, want 1", h.Len())
	}
	if got := h.Pop(dueT0.Add(2 * time.Second)); len(got) != 0 {
		t.Fatalf("stale earlier deadline survived: %v", got)
	}
	h.Push("a", dueT0.Add(time.Second)) // earlier replaces later
	if got := h.Pop(dueT0.Add(2 * time.Second)); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("Pop = %v", got)
	}
}

func TestDueHeapDuplicatesCollapse(t *testing.T) {
	h := NewDueHeap()
	for i := 0; i < 5; i++ {
		h.Push("a", dueT0)
	}
	if got := h.Pop(dueT0); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("Pop = %v", got)
	}
	if h.Len() != 0 {
		t.Fatalf("Len = %d", h.Len())
	}
}

func TestDueHeapKindsAreIndependent(t *testing.T) {
	h := NewDueHeap()
	h.PushKind("a", DueQuiet, dueT0.Add(1*time.Second))
	h.PushKind("a", DueRemoval, dueT0.Add(5*time.Second))
	if h.Len() != 2 {
		t.Fatalf("Len = %d, want 2", h.Len())
	}
	if got := h.Pop(dueT0.Add(2 * time.Second)); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("Pop = %v", got)
	}
	if h.Len() != 1 {
		t.Fatalf("removal entry lost: Len = %d", h.Len())
	}
	// Replace within the removal kind only.
	h.PushKind("a", DueRemoval, dueT0.Add(7*time.Second))
	if h.Len() != 1 {
		t.Fatalf("Len = %d", h.Len())
	}
	// Both kinds due at once: session returned once.
	h.PushKind("a", DueQuiet, dueT0.Add(6*time.Second))
	if got := h.Pop(dueT0.Add(time.Minute)); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("Pop = %v", got)
	}
}
