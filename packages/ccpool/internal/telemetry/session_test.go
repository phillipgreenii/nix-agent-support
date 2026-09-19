package telemetry

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// newTestStore opens an in-memory store for one test, wires it as the
// package-level session labeler, and unwires it on cleanup so tests never
// leak state into one another regardless of run order.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:", &clock.Fake{T: time.Unix(1000, 0).UTC()})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	SetSessionLabeler(st)
	t.Cleanup(func() { SetSessionLabeler(nil) })
	return st
}

// markLabel is a small test helper: SetMeta then MarkAsLabel in one call,
// matching the store's own two-step label-marking flow.
func markLabel(t *testing.T, st *store.Store, externalID, key, value string) {
	t.Helper()
	if err := st.SetMeta(context.Background(), externalID, key, value); err != nil {
		t.Fatalf("SetMeta(%q, %q, %q): %v", externalID, key, value, err)
	}
	if err := st.MarkAsLabel(externalID, key); err != nil {
		t.Fatalf("MarkAsLabel(%q, %q): %v", externalID, key, err)
	}
}

func TestSessionAttrs_singleSessionCallSite(t *testing.T) {
	st := newTestStore(t)
	markLabel(t, st, "ext-a", "role", "worker")
	markLabel(t, st, "ext-a", "pool", "batch")
	// bead is metadata but never marked as a label, so it must not appear.
	if err := st.SetMeta(context.Background(), "ext-a", "bead", "zr-1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}

	got := SessionAttrs("ext-a")

	want := []attribute.KeyValue{
		attribute.String("pool", "batch"),
		attribute.String("role", "worker"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SessionAttrs(ext-a) = %v, want %v", got, want)
	}
}

// TestSessionAttrs_multiSessionLoop is the AC7 SessionAttrs-half scenario:
// two sessions resolved inside one simulated reap-all loop, each with a
// different marked role value, must each get their own distinct, correct
// attributes rather than one fixed value leaking across the loop.
func TestSessionAttrs_multiSessionLoop(t *testing.T) {
	st := newTestStore(t)
	markLabel(t, st, "ext-a", "role", "worker")
	markLabel(t, st, "ext-b", "role", "feedback")

	sessions := []string{"ext-a", "ext-b"}
	results := make(map[string][]attribute.KeyValue, len(sessions))
	for _, externalID := range sessions {
		// Resolved fresh per iteration, per D8.2 — never cached/reused
		// across sessions in the loop.
		results[externalID] = SessionAttrs(externalID)
	}

	wantA := []attribute.KeyValue{attribute.String("role", "worker")}
	wantB := []attribute.KeyValue{attribute.String("role", "feedback")}
	if !reflect.DeepEqual(results["ext-a"], wantA) {
		t.Errorf("SessionAttrs(ext-a) = %v, want %v", results["ext-a"], wantA)
	}
	if !reflect.DeepEqual(results["ext-b"], wantB) {
		t.Errorf("SessionAttrs(ext-b) = %v, want %v", results["ext-b"], wantB)
	}
	if reflect.DeepEqual(results["ext-a"], results["ext-b"]) {
		t.Fatalf("ext-a and ext-b resolved to identical attrs %v; role values must be distinct per session", results["ext-a"])
	}
}

func TestSessionAttrs_emptySliceWhenNoLabelsMarked(t *testing.T) {
	st := newTestStore(t)
	// Metadata exists but nothing is marked as a label.
	if err := st.SetMeta(context.Background(), "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}

	got := SessionAttrs("ext-a")

	if got == nil || len(got) != 0 {
		t.Errorf("SessionAttrs(no labels marked) = %v, want empty (non-nil) slice", got)
	}
}

func TestSessionAttrs_emptySliceWhenNoLabelerWired(t *testing.T) {
	SetSessionLabeler(nil)
	t.Cleanup(func() { SetSessionLabeler(nil) })

	got := SessionAttrs("ext-a")

	if got == nil || len(got) != 0 {
		t.Errorf("SessionAttrs(no labeler wired) = %v, want empty (non-nil) slice", got)
	}
}

// fakeErroringLabeler simulates an unreachable store: every Labels call
// fails.
type fakeErroringLabeler struct{}

func (fakeErroringLabeler) Labels(externalID string) (map[string]string, error) {
	return nil, fmt.Errorf("store unreachable")
}

func TestSessionAttrs_emptySliceNeverErrorWhenStoreUnreachable(t *testing.T) {
	SetSessionLabeler(fakeErroringLabeler{})
	t.Cleanup(func() { SetSessionLabeler(nil) })

	got := SessionAttrs("ext-a")

	if got == nil || len(got) != 0 {
		t.Errorf("SessionAttrs(store unreachable) = %v, want empty (non-nil) slice", got)
	}
}

func TestSessionAttrs_doesNotLeakAcrossSessions(t *testing.T) {
	st := newTestStore(t)
	markLabel(t, st, "ext-a", "role", "worker")
	// ext-b has no labels marked at all.

	got := SessionAttrs("ext-b")

	if got == nil || len(got) != 0 {
		t.Errorf("SessionAttrs(ext-b) = %v, want empty (ext-a's label must not leak)", got)
	}
}
