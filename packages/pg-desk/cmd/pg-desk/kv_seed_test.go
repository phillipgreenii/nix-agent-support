package main

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// seedKVAnnotation writes a key/value annotation on a pr entity the way the
// verbs do, seeding the entity row first when it is not stored (an annotation
// needs one).
func seedKVAnnotation(t *testing.T, st *store.Store, repo, id, key, value string) {
	t.Helper()
	if _, found, err := st.GetEntity(repo, entityTypePR, id); err != nil {
		t.Fatalf("seed annotation: %v", err)
	} else if !found {
		seedBareEntity(t, st, repo, id)
	}
	if err := st.SetAnnotation(store.KVAnnotation{
		Repo: repo, EntityType: entityTypePR, EntityID: id,
		Key: key, Value: value, Origin: "pg-desk", SetBy: "operator", SetAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed annotation %s: %v", key, err)
	}
}

// wantKV asserts one key/value annotation of a pr entity.
func wantKV(t *testing.T, st *store.Store, repo, id, key, value string) store.KVAnnotation {
	t.Helper()
	a, found, err := st.GetKVAnnotation(repo, entityTypePR, id, key)
	if err != nil || !found {
		t.Fatalf("annotation %q of %s: found=%v err=%v", key, id, found, err)
	}
	if a.Value != value {
		t.Fatalf("annotation %q of %s = %q, want %q", key, id, a.Value, value)
	}
	return a
}
