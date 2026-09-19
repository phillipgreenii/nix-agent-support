package store

import (
	"context"
	"reflect"
	"testing"
)

func TestMarkAsLabel_errorsOnNeverSetKey(t *testing.T) {
	st := newTestStore(t)
	if err := st.MarkAsLabel("ext-a", "role"); err == nil {
		t.Fatal("MarkAsLabel on a key never written via SetMeta must error")
	}
}

func TestMarkAsLabel_thenLabelsRoundTrips(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := st.SetMeta(ctx, "ext-a", "bead", "zr-1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := st.MarkAsLabel("ext-a", "role"); err != nil {
		t.Fatalf("MarkAsLabel: %v", err)
	}
	got, err := st.Labels("ext-a")
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	want := map[string]string{"role": "worker"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Labels = %v, want %v (only role marked; bead untouched)", got, want)
	}
}

func TestLabels_emptyWhenNoneMarked(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, err := st.Labels("ext-a")
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("Labels(none marked) = %v, want non-nil empty map", got)
	}
}

func TestMarkAsLabel_survivesSetMetaReplace(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := st.MarkAsLabel("ext-a", "role"); err != nil {
		t.Fatalf("MarkAsLabel: %v", err)
	}
	if err := st.SetMeta(ctx, "ext-a", "role", "feedback"); err != nil {
		t.Fatalf("SetMeta replace: %v", err)
	}
	got, err := st.Labels("ext-a")
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	want := map[string]string{"role": "feedback"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Labels after SetMeta replace = %v, want %v (label flag survives a value replace)", got, want)
	}
}

// TestMarkAsLabel_thenDeleteMetaRemovesLabel covers the AC's "interaction with
// a later metadata delete": DeleteMeta removes the whole row (value AND the
// is_label flag together), and re-marking the now-absent key errors again
// exactly like a never-SetMeta'd key — the label flag has no life independent
// of the metadata row it rides on.
func TestMarkAsLabel_thenDeleteMetaRemovesLabel(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := st.MarkAsLabel("ext-a", "role"); err != nil {
		t.Fatalf("MarkAsLabel: %v", err)
	}
	if err := st.DeleteMeta(ctx, "ext-a", "role"); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	got, err := st.Labels("ext-a")
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Labels after DeleteMeta = %v, want empty (label row deleted with the metadata)", got)
	}
	if err := st.MarkAsLabel("ext-a", "role"); err == nil {
		t.Error("MarkAsLabel after DeleteMeta must error again (key not found)")
	}
}

func TestLabels_doesNotLeakAcrossSessions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := st.SetMeta(ctx, "ext-b", "role", "feedback"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := st.MarkAsLabel("ext-a", "role"); err != nil {
		t.Fatalf("MarkAsLabel: %v", err)
	}
	got, err := st.Labels("ext-b")
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Labels(ext-b) = %v, want empty (ext-a's label must not leak)", got)
	}
}
