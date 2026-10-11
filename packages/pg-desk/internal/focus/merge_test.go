package focus

import (
	"errors"
	"testing"
)

func TestParseMerges(t *testing.T) {
	f := newFixture(t)
	f.cfg.TicketPatterns = []string{"PROJ-[0-9]+"}
	f.cfg.Repos = nil
	if _, err := ParseMerges([]string{"PROJ-1=#4"}, f.cfg); err == nil {
		t.Fatalf("a PR key with no repository configured must fail")
	}
	got, err := ParseMerges([]string{"PROJ-1=PROJ-2, PROJ-3=PROJ-4", "PROJ-5=PROJ-6"}, f.cfg)
	if err != nil || len(got) != 3 || got[0] != (Merge{Kept: Key{"issue", "PROJ-1"}, Absorbed: Key{"issue", "PROJ-2"}}) || got[2].Absorbed.ID != "PROJ-6" {
		t.Fatalf("ParseMerges = %v, %v", got, err)
	}
	for _, bad := range []string{"PROJ-1", "=PROJ-2", "PROJ-1=", "PROJ-1=PROJ-2=PROJ-3", "nonsense=PROJ-2"} {
		if _, err := ParseMerges([]string{bad}, f.cfg); !errors.Is(err, ErrUsage) {
			t.Errorf("ParseMerges(%q) = %v, want a usage error", bad, err)
		}
	}
	if got, err := ParseMerges(nil, f.cfg); err != nil || got != nil {
		t.Errorf("no flags = %v, %v", got, err)
	}
}

func TestMergeLinkRoundTrip(t *testing.T) {
	f := newFixture(t)
	kept, gone := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"})
	if err := RecordMerges(f.st, testRepo, []Merge{{Kept: kept, Absorbed: gone}}, "operator", fixtureAt); err != nil {
		t.Fatal(err)
	}
	// Recording again is idempotent.
	if err := RecordMerges(f.st, testRepo, []Merge{{Kept: kept, Absorbed: gone}}, "operator", fixtureAt); err != nil {
		t.Fatal(err)
	}
	in := f.inputs()
	pm := PersistedMerges(in)
	if len(pm) != 1 || pm[0] != (Merge{Kept: kept, Absorbed: gone}) {
		t.Fatalf("PersistedMerges = %v", pm)
	}
	if in.Absorbed[gone] != kept {
		t.Errorf("Inputs.Absorbed = %v, want the absorbed key mapped to the kept key", in.Absorbed)
	}
	// Another actor's removal still removes the merge (it belongs to the plan).
	if err := RemoveMerges(f.st, testRepo, pm); err != nil {
		t.Fatal(err)
	}
	if got := PersistedMerges(f.inputs()); len(got) != 0 {
		t.Errorf("merge survived removal: %v", got)
	}
	if err := RecordMerges(f.st, testRepo, []Merge{{Kept: kept, Absorbed: gone}}, "", fixtureAt); err == nil {
		t.Errorf("recording with no actor must fail")
	}
}

func TestOnlyFocusMergeLinksAreMerges(t *testing.T) {
	f := newFixture(t)
	a, b := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"})
	f.external(a, b, relationReferences, "see also")
	if got := PersistedMerges(f.inputs()); len(got) != 0 {
		t.Errorf("a plain references link read as a merge: %v", got)
	}
}
