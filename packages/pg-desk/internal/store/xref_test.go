package store

import "testing"

// TestUpsertXrefAndGetXref proves the existing (Phase 9) writer/reader
// round-trips, and that a second UpsertXref for the same key overwrites
// evidence/last_confirmed while leaving first_seen untouched — the ON
// CONFLICT semantics docket pg2-2j5ac.40's own packets rely on.
func TestUpsertXrefAndGetXref(t *testing.T) {
	s := OpenForTest(t)

	if err := s.UpsertXref(Xref{
		Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7",
		ToType: "issue", ToID: "PROJ-1", Evidence: "branch",
		FirstSeen: "2026-09-17T00:00:00Z", LastConfirmed: "2026-09-17T00:00:00Z",
	}); err != nil {
		t.Fatalf("first UpsertXref: %v", err)
	}

	got, found, err := s.GetXref("acme/widgets", "pr", "acme/widgets#7", "issue", "PROJ-1")
	if err != nil || !found {
		t.Fatalf("GetXref: found=%v err=%v", found, err)
	}
	if got.Evidence != "branch" {
		t.Fatalf("Evidence = %q, want %q", got.Evidence, "branch")
	}

	if err := s.UpsertXref(Xref{
		Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7",
		ToType: "issue", ToID: "PROJ-1", Evidence: "title",
		FirstSeen: "2026-09-18T00:00:00Z", LastConfirmed: "2026-09-18T00:00:00Z",
	}); err != nil {
		t.Fatalf("second UpsertXref: %v", err)
	}

	got, found, err = s.GetXref("acme/widgets", "pr", "acme/widgets#7", "issue", "PROJ-1")
	if err != nil || !found {
		t.Fatalf("GetXref (after second upsert): found=%v err=%v", found, err)
	}
	if got.Evidence != "title" {
		t.Fatalf("Evidence after second upsert = %q, want %q (last-upserted wins)", got.Evidence, "title")
	}
	if got.FirstSeen != "2026-09-17T00:00:00Z" {
		t.Fatalf("FirstSeen = %q, want the original insert's value untouched", got.FirstSeen)
	}
	if got.LastConfirmed != "2026-09-18T00:00:00Z" {
		t.Fatalf("LastConfirmed = %q, want the second upsert's value", got.LastConfirmed)
	}
}

// TestGetXrefNotFound proves the not-found branch: no row, no error.
func TestGetXrefNotFound(t *testing.T) {
	s := OpenForTest(t)
	_, found, err := s.GetXref("acme/widgets", "pr", "acme/widgets#7", "issue", "PROJ-1")
	if err != nil {
		t.Fatalf("GetXref: unexpected error %v", err)
	}
	if found {
		t.Fatal("GetXref: found=true for a row that was never written")
	}
}

// TestListXrefsByTo proves the REVERSE (to-id-keyed, multi-row) lookup
// docket pg2-2j5ac.40's `run issue`/`run thread` need: every row for
// (repo, to_type, to_id) regardless of from_id/from_type, and none for a
// mismatched repo or to_type/to_id.
func TestListXrefsByTo(t *testing.T) {
	s := OpenForTest(t)

	// Three DISTINCT PRs (from_id) all xref'd to the same PROJ-1, a fourth
	// row for a different to_id (PROJ-2, must be excluded), and a fifth row
	// in a different repo (must be excluded) — each row's from_id is
	// unique so none collide on the (repo,from_type,from_id,to_type,to_id)
	// primary key.
	seed := []Xref{
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7", ToType: "issue", ToID: "PROJ-1", Evidence: "branch", FirstSeen: "t1", LastConfirmed: "t1"},
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#8", ToType: "issue", ToID: "PROJ-1", Evidence: "title", FirstSeen: "t1", LastConfirmed: "t1"},
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#9", ToType: "issue", ToID: "PROJ-1", Evidence: "body", FirstSeen: "t1", LastConfirmed: "t1"},
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7", ToType: "issue", ToID: "PROJ-2", Evidence: "branch", FirstSeen: "t1", LastConfirmed: "t1"},
		{Repo: "other/repo", FromType: "pr", FromID: "other/repo#1", ToType: "issue", ToID: "PROJ-1", Evidence: "branch", FirstSeen: "t1", LastConfirmed: "t1"},
	}
	for _, x := range seed {
		if err := s.UpsertXref(x); err != nil {
			t.Fatalf("seed UpsertXref %+v: %v", x, err)
		}
	}

	got, err := s.ListXrefsByTo("acme/widgets", "issue", "PROJ-1")
	if err != nil {
		t.Fatalf("ListXrefsByTo: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListXrefsByTo returned %d rows, want 3: %+v", len(got), got)
	}
	fromIDs := map[string]bool{}
	for _, x := range got {
		if x.Repo != "acme/widgets" || x.ToType != "issue" || x.ToID != "PROJ-1" {
			t.Fatalf("unexpected row in ListXrefsByTo result: %+v", x)
		}
		fromIDs[x.FromID] = true
	}
	for _, want := range []string{"acme/widgets#7", "acme/widgets#8", "acme/widgets#9"} {
		if !fromIDs[want] {
			t.Fatalf("ListXrefsByTo missing from_id %q; got %+v", want, got)
		}
	}

	none, err := s.ListXrefsByTo("acme/widgets", "issue", "PROJ-999")
	if err != nil {
		t.Fatalf("ListXrefsByTo (no match): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListXrefsByTo (no match) = %+v, want empty", none)
	}
}

// TestListXrefsByFrom proves the FORWARD lookup (this docket's Slack-half
// sibling packet's own need): every row for (repo, from_type, from_id,
// to_type), regardless of to_id.
func TestListXrefsByFrom(t *testing.T) {
	s := OpenForTest(t)

	seed := []Xref{
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7", ToType: "issue", ToID: "PROJ-1", Evidence: "branch", FirstSeen: "t1", LastConfirmed: "t1"},
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7", ToType: "issue", ToID: "PROJ-2", Evidence: "body", FirstSeen: "t1", LastConfirmed: "t1"},
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#7", ToType: "thread", ToID: "T1", Evidence: "permalink", FirstSeen: "t1", LastConfirmed: "t1"},
		{Repo: "acme/widgets", FromType: "pr", FromID: "acme/widgets#8", ToType: "issue", ToID: "PROJ-3", Evidence: "branch", FirstSeen: "t1", LastConfirmed: "t1"},
	}
	for _, x := range seed {
		if err := s.UpsertXref(x); err != nil {
			t.Fatalf("seed UpsertXref %+v: %v", x, err)
		}
	}

	got, err := s.ListXrefsByFrom("acme/widgets", "pr", "acme/widgets#7", "issue")
	if err != nil {
		t.Fatalf("ListXrefsByFrom: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListXrefsByFrom returned %d rows, want 2 (issue-typed only, thread excluded): %+v", len(got), got)
	}
	toIDs := map[string]bool{}
	for _, x := range got {
		if x.Repo != "acme/widgets" || x.FromType != "pr" || x.FromID != "acme/widgets#7" || x.ToType != "issue" {
			t.Fatalf("unexpected row in ListXrefsByFrom result: %+v", x)
		}
		toIDs[x.ToID] = true
	}
	for _, want := range []string{"PROJ-1", "PROJ-2"} {
		if !toIDs[want] {
			t.Fatalf("ListXrefsByFrom missing to_id %q; got %+v", want, got)
		}
	}

	none, err := s.ListXrefsByFrom("acme/widgets", "pr", "acme/widgets#999", "issue")
	if err != nil {
		t.Fatalf("ListXrefsByFrom (no match): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListXrefsByFrom (no match) = %+v, want empty", none)
	}
}
