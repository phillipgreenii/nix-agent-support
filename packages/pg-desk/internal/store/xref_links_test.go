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

// linksByOrigin lists the links leaving the test entity to (issue, toID)
// with relation, keyed by origin.
func linksByOrigin(t *testing.T, s *Store, toID, relation string) map[string]XrefLink {
	t.Helper()
	links, err := s.ListXrefLinksFrom(kvRepo, "pr", kvID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]XrefLink{}
	for _, l := range links {
		if l.ToType == "issue" && l.ToID == toID && l.Relation == relation {
			out[l.Origin] = l
		}
	}
	return out
}

// An external claim on an already-derived link is recorded as its own
// external-origin row, beside the derived row, and never changes that row.
func TestExternalXrefCannotOverrideDerived(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	derived := derivedLink("A-1", "fixes", "derived:body", "2026-09-01T00:00:00Z")
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{derived}); err != nil {
		t.Fatal(err)
	}
	ext := externalLink("A-1", "fixes", "alice")
	ext.Evidence = "external evidence"
	if err := s.AddExternalXref(ext); err != nil {
		t.Fatal(err)
	}
	got := linksByOrigin(t, s, "A-1", "fixes")
	if len(got) != 2 {
		t.Fatalf("links = %+v, want the derived row and alice's external row", got)
	}
	if d := got["derived:body"]; d != derived {
		t.Fatalf("derived row changed by the external claim: got %+v, want %+v", d, derived)
	}
	e := got["external:alice"]
	if e.Actor != "alice" || e.ActedAt != "2026-09-15T00:00:00Z" || e.Reason != "because" || e.Evidence != "external evidence" {
		t.Fatalf("external claim on a derived link = %+v", e)
	}
	// Re-adding refreshes only alice's row; the derived row is still untouched.
	again := externalLink("A-1", "fixes", "alice")
	again.LastConfirmed, again.ActedAt, again.Reason = "2026-09-20T00:00:00Z", "2026-09-20T00:00:00Z", "still"
	if err := s.AddExternalXref(again); err != nil {
		t.Fatal(err)
	}
	got = linksByOrigin(t, s, "A-1", "fixes")
	if d := got["derived:body"]; len(got) != 2 || d != derived {
		t.Fatalf("after re-add = %+v, want the derived row unchanged", got)
	}
	if e := got["external:alice"]; e.FirstSeen != "2026-09-15T00:00:00Z" || e.LastConfirmed != "2026-09-20T00:00:00Z" || e.Reason != "still" {
		t.Fatalf("re-added external row = %+v", e)
	}
	// A different relation to the same target is a distinct link and is allowed.
	if err := s.AddExternalXref(externalLink("A-1", "blocks", "alice")); err != nil {
		t.Fatal(err)
	}
	if links, _ := s.ListXrefLinksFrom(kvRepo, "pr", kvID); len(links) != 3 {
		t.Fatalf("links = %+v, want 3", links)
	}
}

// A link that is both derived and externally claimed survives, through its
// external origin, a re-hydration that no longer finds it.
func TestExternalClaimKeepsLinkWhenDerivedSourceDisappears(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{derivedLink("A-1", "fixes", "derived:body", "2026-09-01T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddExternalXref(externalLink("A-1", "fixes", "alice")); err != nil {
		t.Fatal(err)
	}
	// A hydration that still finds the link keeps both claims.
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{derivedLink("A-1", "fixes", "derived:body", "2026-09-05T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if got := linksByOrigin(t, s, "A-1", "fixes"); len(got) != 2 {
		t.Fatalf("after confirming hydration = %+v, want both claims", got)
	}
	// The source entity changes: re-hydration no longer derives the link.
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, nil); err != nil {
		t.Fatal(err)
	}
	got := linksByOrigin(t, s, "A-1", "fixes")
	e, ok := got["external:alice"]
	if len(got) != 1 || !ok {
		t.Fatalf("after the derived row was dropped = %+v, want only alice's external claim", got)
	}
	if e.Actor != "alice" || e.Reason != "because" {
		t.Fatalf("surviving external claim = %+v", e)
	}
	// The reverse lookup still sees the link too.
	to, err := s.ListXrefLinksTo(kvRepo, "issue", "A-1")
	if err != nil || len(to) != 1 || to[0].Origin != "external:alice" {
		t.Fatalf("ListXrefLinksTo = %+v, %v", to, err)
	}
}

// Removing an external claim from a link that is also derived removes only
// that claim; the derived link stays intact.
func TestRemoveExternalClaimLeavesDerivedLink(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	derived := derivedLink("A-1", "fixes", "derived:body", "2026-09-01T00:00:00Z")
	if err := s.ReplaceDerivedXrefs(kvRepo, "pr", kvID, []XrefLink{derived}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddExternalXref(externalLink("A-1", "fixes", "alice")); err != nil {
		t.Fatal(err)
	}
	if removed, err := s.RemoveExternalXref(kvRepo, "pr", kvID, "issue", "A-1", "fixes", "alice"); err != nil || !removed {
		t.Fatalf("remove alice's claim = (%v, %v)", removed, err)
	}
	got := linksByOrigin(t, s, "A-1", "fixes")
	if d := got["derived:body"]; len(got) != 1 || d != derived {
		t.Fatalf("after removing the external claim = %+v, want only the derived row, unchanged", got)
	}
	// With no external claim left, a second remove removes nothing.
	if removed, err := s.RemoveExternalXref(kvRepo, "pr", kvID, "issue", "A-1", "fixes", "alice"); err != nil || removed {
		t.Fatalf("second remove = (%v, %v)", removed, err)
	}
	if got := linksByOrigin(t, s, "A-1", "fixes"); len(got) != 1 {
		t.Fatalf("after second remove = %+v", got)
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
