package sqlite

import (
	"context"
	"testing"
)

func TestToggleStore_GetAfterSet(t *testing.T) {
	db := openTestDB(t)
	ts := NewToggleStore(db)
	ctx := context.Background()

	val, present, err := ts.Get(ctx, "caffeinate_on")
	if err != nil {
		t.Fatalf("Get pre-set: %v", err)
	}
	if present {
		t.Error("Get pre-set: present should be false")
	}

	if err := ts.Set(ctx, "caffeinate_on", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, present, err = ts.Get(ctx, "caffeinate_on")
	if err != nil {
		t.Fatalf("Get post-set: %v", err)
	}
	if !present || !val {
		t.Errorf("Get post-set = (%v, %v), want (true, true)", val, present)
	}

	// Flip
	if err := ts.Set(ctx, "caffeinate_on", false); err != nil {
		t.Fatalf("Set false: %v", err)
	}
	val, _, _ = ts.Get(ctx, "caffeinate_on")
	if val {
		t.Errorf("Get post-flip = true, want false")
	}
}

func TestToggleStore_All(t *testing.T) {
	db := openTestDB(t)
	ts := NewToggleStore(db)
	ctx := context.Background()
	_ = ts.Set(ctx, "caffeinate_on", true)
	_ = ts.Set(ctx, "auto_resume_enabled", false)

	m, err := ts.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(m) != 2 || !m["caffeinate_on"] || m["auto_resume_enabled"] {
		t.Errorf("All = %+v", m)
	}
}
