package store_test

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestSetBetweenBumpAndAppendHook_CallableFromAnotherPackage proves the
// exported fault-injection seam works from outside package store, which is
// how the pipeline package forces a failure between the bump and the append.
func TestSetBetweenBumpAndAppendHook_CallableFromAnotherPackage(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	e := store.Entity{Repo: "acme/widgets", EntityType: "pr", EntityID: "acme/widgets#7", Facts: "a"}
	if _, err := s.WriteEntityStateWithLog(e, 0, "2026-09-11T00:00:00Z", true, []string{"created"}, "sync", "t1"); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("injected failure")
	s.SetBetweenBumpAndAppendHook(func() error { return boom })
	e.Facts = "b"
	_, err := s.WriteEntityStateWithLog(e, 1, "2026-09-12T00:00:00Z", false, []string{"x"}, "sync", "t2")
	s.SetBetweenBumpAndAppendHook(nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	got, _, _ := s.GetEntity(e.Repo, e.EntityType, e.EntityID)
	if got.Version != 1 || got.Facts != "a" || got.HydratedAt != "2026-09-11T00:00:00Z" || got.Inactive {
		t.Fatalf("entity after failed write = %+v, want rolled back", got)
	}
	recs, err := s.ListEntityHistory(e.Repo, e.EntityType, e.EntityID, 0)
	if err != nil || len(recs) != 1 {
		t.Fatalf("history = (%+v, %v), want 1 record", recs, err)
	}
}
