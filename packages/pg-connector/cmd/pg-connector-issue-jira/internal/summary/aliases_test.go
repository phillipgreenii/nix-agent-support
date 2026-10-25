package summary

import (
	"context"
	"testing"
)

func TestAliasesResolveWithoutTheOrigin(t *testing.T) {
	st := openStore(t)
	a := NewAliases(st)
	ctx := context.Background()
	got, err := a.Resolve(ctx, []string{"abc-1", "XYZ-22", "Mixed-3"})
	if err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string][2]string{
		"abc-1":   {"ABC-1", "ABC-1"},
		"XYZ-22":  {"XYZ-22", "XYZ-22"},
		"Mixed-3": {"MIXED-3", "MIXED-3"},
	} {
		if g := got[ref]; g.ID != want[0] || g.CanonicalRef != want[1] {
			t.Errorf("Resolve(%q) = %+v, want %v", ref, g, want)
		}
	}
	if len(got) != 3 {
		t.Errorf("Resolve answered %d references, want 3", len(got))
	}
}

func TestAliasesPutAndRefUseTheStoreAliasTable(t *testing.T) {
	st := openStore(t)
	a := NewAliases(st)
	ctx := context.Background()
	if _, ok, err := a.Ref(ctx, "ABC-1"); err != nil || ok {
		t.Fatalf("Ref before Put = %v, %v", ok, err)
	}
	if err := a.Put(ctx, "ABC-1", "ABC-1"); err != nil {
		t.Fatal(err)
	}
	ref, ok, err := a.Ref(ctx, "abc-1")
	if err != nil || !ok || ref != "ABC-1" {
		t.Errorf("Ref = %q, %v, %v", ref, ok, err)
	}
	row, ok, err := st.AliasByNodeID(ctx, "ABC-1")
	if err != nil || !ok || row.Ref != "ABC-1" || row.SeenAt.IsZero() {
		t.Errorf("alias row = %+v, %v, %v", row, ok, err)
	}
	// Resolve does not read the alias table: an unrecorded reference still resolves.
	got, err := a.Resolve(ctx, []string{"NEW-7"})
	if err != nil || got["NEW-7"].ID != "NEW-7" {
		t.Errorf("Resolve of an unrecorded key = %+v, %v", got, err)
	}
}
