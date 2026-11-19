package wire

import (
	"slices"
	"testing"
)

func TestRegistryDerivesBothListsFromOneSlice(t *testing.T) {
	want := []string{"conversation.list", "conversation.show", "coverage", "doctor", "extract", "feed", "files.inventory", "held", "hello", "process.list", "process.show", "query", "raw", "remove", "search", "sessions.facts", "snapshot", "status", "sweep", "where"}
	if got := OpNames(); !slices.Equal(got, want) {
		t.Fatalf("OpNames %v, want %v", got, want)
	}
	bodies, exempt := CancelBodies(), CancelExempt()
	for _, o := range Registry {
		_, b := bodies[o.Name]
		_, e := exempt[o.Name]
		if b == e {
			t.Errorf("%s must be in exactly one of the cancel bodies and the cancel exempt set", o.Name)
		}
	}
	if len(bodies)+len(exempt) != len(Registry) {
		t.Errorf("cancel table covers %d of %d operations (duplicate name?)", len(bodies)+len(exempt), len(Registry))
	}
}
