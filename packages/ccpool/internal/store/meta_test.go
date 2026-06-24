package store

import (
	"context"
	"reflect"
	"testing"
)

func TestSetMeta_setGetRoundTrips(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, ok, err := st.GetMeta(ctx, "ext-a", "role")
	if err != nil || !ok {
		t.Fatalf("GetMeta: ok=%v err=%v", ok, err)
	}
	if got != "worker" {
		t.Errorf("GetMeta = %q, want %q", got, "worker")
	}
}

func TestSetMeta_replacesExistingValue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "bead", "zr-old")
	if err := st.SetMeta(ctx, "ext-a", "bead", "zr-new"); err != nil {
		t.Fatalf("SetMeta replace: %v", err)
	}
	got, _, _ := st.GetMeta(ctx, "ext-a", "bead")
	if got != "zr-new" {
		t.Errorf("after replace GetMeta = %q, want zr-new", got)
	}
}

func TestSetMeta_emptyKeyErrors(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetMeta(context.Background(), "ext-a", "", "v"); err == nil {
		t.Fatal("SetMeta with empty key must error")
	}
}

func TestSetMeta_allowsEmptyValueAsBareTag(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "pinned", ""); err != nil {
		t.Fatalf("SetMeta bare tag: %v", err)
	}
	got, ok, _ := st.GetMeta(ctx, "ext-a", "pinned")
	if !ok || got != "" {
		t.Errorf("bare tag GetMeta = (%q,%v), want (\"\",true)", got, ok)
	}
}

func TestGetMeta_missingKeyOkFalse(t *testing.T) {
	st := newTestStore(t)
	_, ok, err := st.GetMeta(context.Background(), "ext-a", "nope")
	if err != nil || ok {
		t.Fatalf("GetMeta(missing): ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestMeta_returnsAllAsMap(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	_ = st.SetMeta(ctx, "ext-a", "bead", "zr-1")
	_ = st.SetMeta(ctx, "ext-b", "role", "feedback") // other session, must not leak in
	got, err := st.Meta(ctx, "ext-a")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	want := map[string]string{"role": "worker", "bead": "zr-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Meta = %v, want %v", got, want)
	}
}

func TestMeta_emptyIsNonNilEmptyMap(t *testing.T) {
	st := newTestStore(t)
	got, err := st.Meta(context.Background(), "ext-none")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("Meta(empty) = %v, want non-nil empty map", got)
	}
}

func TestDeleteMeta_removesKeyIdempotently(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	if err := st.DeleteMeta(ctx, "ext-a", "role"); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	if _, ok, _ := st.GetMeta(ctx, "ext-a", "role"); ok {
		t.Error("key still present after DeleteMeta")
	}
	if err := st.DeleteMeta(ctx, "ext-a", "role"); err != nil {
		t.Fatalf("DeleteMeta(absent) must be nil, got %v", err)
	}
}
