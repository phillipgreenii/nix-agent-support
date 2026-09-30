package store

import "testing"

func derivedLink(toID, relation, origin, seen string) XrefLink {
	return XrefLink{
		Repo: kvRepo, FromType: "pr", FromID: kvID, ToType: "issue", ToID: toID,
		Relation: relation, Origin: origin, Evidence: "body", FirstSeen: seen, LastConfirmed: seen,
	}
}

func externalLink(toID, relation, actor string) XrefLink {
	return XrefLink{
		Repo: kvRepo, FromType: "pr", FromID: kvID, ToType: "issue", ToID: toID,
		Relation: relation, Actor: actor, ActedAt: "2026-09-15T00:00:00Z", Reason: "because",
		FirstSeen: "2026-09-15T00:00:00Z", LastConfirmed: "2026-09-15T00:00:00Z",
	}
}

func TestReplaceDerivedXrefsReplacesPerHydration(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{
		derivedLink("A-1", "fixes", "derived:body", "2026-09-01T00:00:00Z"),
		derivedLink("A-2", "references", "derived:body", "2026-09-01T00:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddExternalXref(externalLink("A-9", "blocks", "alice")); err != nil {
		t.Fatal(err)
	}
	// Second hydration: A-1 stays (first_seen kept, last_confirmed moves), A-2 goes, A-3 arrives.
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{
		derivedLink("A-1", "fixes", "derived:body", "2026-09-05T00:00:00Z"),
		derivedLink("A-3", "fixes", "derived:body", "2026-09-05T00:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	links, err := s.ListXrefLinksFrom(kvRepo, "pr", kvID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]XrefLink{}
	for _, l := range links {
		got[l.ToID] = l
	}
	if len(got) != 3 || got["A-2"].ToID != "" {
		t.Fatalf("links = %+v", links)
	}
	a1 := got["A-1"]
	if a1.FirstSeen != "2026-09-01T00:00:00Z" || a1.LastConfirmed != "2026-09-05T00:00:00Z" || a1.Relation != "fixes" || a1.Origin != "derived:body" {
		t.Fatalf("A-1 = %+v", a1)
	}
	ext := got["A-9"]
	if ext.Origin != "external:alice" || ext.Actor != "alice" || ext.Reason != "because" || ext.ActedAt != "2026-09-15T00:00:00Z" {
		t.Fatalf("external row did not persist: %+v", ext)
	}
	// Reverse lookup returns origin/relation too.
	to, err := s.ListXrefLinksTo(kvRepo, "issue", "A-3")
	if err != nil || len(to) != 1 || to[0].Origin != "derived:body" || to[0].Relation != "fixes" {
		t.Fatalf("ListXrefLinksTo = %+v, %v", to, err)
	}
	// Empty replacement clears derived rows only.
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, nil); err != nil {
		t.Fatal(err)
	}
	links, _ = s.ListXrefLinksFrom(kvRepo, "pr", kvID)
	if len(links) != 1 || links[0].ToID != "A-9" {
		t.Fatalf("after clear = %+v", links)
	}
}

func TestReplaceDerivedXrefsValidatesLinks(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	bad := []XrefLink{
		derivedLink("A-1", "fixes", "external:bob", "t"),
		derivedLink("A-1", "fixes", xrefLegacyOrigin, "t"),
		derivedLink("A-1", "", "derived:body", "t"),
	}
	for _, l := range bad {
		if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{l}); err == nil {
			t.Errorf("link %+v accepted", l)
		}
	}
	other := derivedLink("A-1", "fixes", "derived:body", "t")
	other.FromID = "acme/widgets#8"
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{other}); err == nil {
		t.Error("link from another entity accepted")
	}
}

func TestExternalXrefCannotOverrideDerived(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{derivedLink("A-1", "fixes", "derived:body", "t1")}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddExternalXref(externalLink("A-1", "fixes", "alice")); err != nil {
		t.Fatalf("add over derived must be a silent no-op, got %v", err)
	}
	links, _ := s.ListXrefLinksFrom(kvRepo, "pr", kvID)
	if len(links) != 1 || links[0].Origin != "derived:body" {
		t.Fatalf("links = %+v, want only the derived row", links)
	}
	// A different relation to the same target is a distinct link and is allowed.
	if err := s.AddExternalXref(externalLink("A-1", "blocks", "alice")); err != nil {
		t.Fatal(err)
	}
	if links, _ := s.ListXrefLinksFrom(kvRepo, "pr", kvID); len(links) != 2 {
		t.Fatalf("links = %+v, want 2", links)
	}
}

func TestRemoveExternalXrefOnlyRemovesThatActorsExternalRow(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{derivedLink("A-1", "fixes", "derived:body", "t1")}); err != nil {
		t.Fatal(err)
	}
	for _, x := range []XrefLink{externalLink("A-2", "blocks", "alice"), externalLink("A-2", "blocks", "bob")} {
		if err := s.AddExternalXref(x); err != nil {
			t.Fatal(err)
		}
	}
	// Never a derived row, even naming its relation.
	if removed, err := s.RemoveExternalXref(kvRepo, "pr", kvID, "issue", "A-1", "fixes", "alice"); err != nil || removed {
		t.Fatalf("remove derived = (%v, %v)", removed, err)
	}
	// Removes only alice's row, not bob's.
	if removed, err := s.RemoveExternalXref(kvRepo, "pr", kvID, "issue", "A-2", "blocks", "alice"); err != nil || !removed {
		t.Fatalf("remove alice = (%v, %v)", removed, err)
	}
	links, _ := s.ListXrefLinksFrom(kvRepo, "pr", kvID)
	if len(links) != 2 {
		t.Fatalf("links = %+v, want derived + bob", links)
	}
	for _, l := range links {
		if l.Origin != "derived:body" && l.Origin != "external:bob" {
			t.Fatalf("unexpected surviving link %+v", l)
		}
	}
}

func TestLegacyXrefRowsAreUntouchedByLinkAPI(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.UpsertXref(Xref{Repo: kvRepo, FromType: "pr", FromID: kvID, ToType: "issue", ToID: "L-1", Evidence: "e", FirstSeen: "t", LastConfirmed: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, nil); err != nil {
		t.Fatal(err)
	}
	links, _ := s.ListXrefLinksFrom(kvRepo, "pr", kvID)
	if len(links) != 1 || links[0].Origin != xrefLegacyOrigin || links[0].Relation != xrefLegacyRelation {
		t.Fatalf("links = %+v, want the legacy row intact", links)
	}
}
