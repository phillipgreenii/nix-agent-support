package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

const (
	gateRepo     = "example/repo"
	gateOwning   = "owning-1"
	gateOther    = "beads-1"
	gateAt       = "2026-10-20T00:00:00Z"
	gateOrigin   = "pg-desk"
	gateIssueTyp = "issue"
)

// gateStore opens a cut-over store with the owning gate installed: backend
// "owning-1" is owning, and an id with the prefix "OWN-" resolves to it while
// its backend column is NULL.
func gateStore(t *testing.T) *Store {
	t.Helper()
	s := OpenNewSchemaForTest(t)
	s.SetGateScope(OwningGateScope(map[string]bool{gateOwning: true}, func(id string) (string, bool) {
		if len(id) >= 4 && id[:4] == "OWN-" {
			return gateOwning, true
		}
		return "", false
	}))
	return s
}

// gateSeed writes an issue entity (through a store handle WITHOUT the gate,
// so its first row never depends on it) with the given backend and watched
// columns, and returns its version.
func gateSeed(t *testing.T, s *Store, id, backend string, watched *bool) int64 {
	t.Helper()
	plain, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close() }()
	v, err := plain.WriteEntityStateWithLog(
		Entity{Repo: gateRepo, EntityType: gateIssueTyp, EntityID: id, Facts: `{}`, AsOf: gateAt},
		0, gateAt, true, []string{"created"}, gateOrigin, gateAt,
	)
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	var b *string
	if backend != "" {
		b = &backend
	}
	if err := plain.SetEntityColumns(gateRepo, gateIssueTyp, id, b, watched); err != nil {
		t.Fatal(err)
	}
	return v
}

func gateBool(b bool) *bool { return &b }

// gateRows counts the issue change_log rows of one entity.
func gateRows(t *testing.T, s *Store, id string) int {
	t.Helper()
	rows, err := s.ListChangesAfter(gateIssueTyp, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.EntityID == id {
			n++
		}
	}
	return n
}

func gateEntity(t *testing.T, s *Store, id string) Entity {
	t.Helper()
	e, found, err := s.GetEntity(gateRepo, gateIssueTyp, id)
	if err != nil || !found {
		t.Fatalf("GetEntity %s: found=%v err=%v", id, found, err)
	}
	return e
}

func gateWrite(t *testing.T, s *Store, id string, version int64, kinds []string, opts WriteOptions) int64 {
	t.Helper()
	v, err := s.WriteEntityStateWithLogOpts(
		Entity{Repo: gateRepo, EntityType: gateIssueTyp, EntityID: id, Facts: `{"n":1}`, AsOf: gateAt},
		version, gateAt, true, nil, kinds, gateOrigin, gateAt, opts,
	)
	if err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
	return v
}

// The three append paths on an UNWATCHED in-scope issue: the write succeeds
// and no row, no sequence appears.
func TestDeliveryGate_UnwatchedInScopeAppendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name, id, backend string
	}{
		{"backend column owning", "K-1", gateOwning},
		{"NULL backend resolving to owning", "OWN-2", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := gateStore(t)
			v := gateSeed(t, s, tc.id, tc.backend, nil)
			before := gateRows(t, s, tc.id) // the seed row, appended without a gate

			// A refresh-style write with kinds.
			nv := gateWrite(t, s, tc.id, v, []string{"fields_changed"}, WriteOptions{})
			if nv != v+1 {
				t.Errorf("version = %d, want %d (snapshot write proceeds)", nv, v+1)
			}
			if got := gateEntity(t, s, tc.id); got.Facts != `{"n":1}` || got.Version != nv {
				t.Errorf("snapshot not persisted: %+v", got)
			}

			// link_changed / work_changed propagation and a reset replay row.
			for _, kind := range []string{"link_changed", "work_changed", "added"} {
				origin := gateOrigin
				if kind == "added" {
					origin = "reset"
				}
				seq, err := s.AppendEntityChange(gateRepo, gateIssueTyp, tc.id, []string{kind}, origin, gateAt)
				if err != nil || seq != 0 {
					t.Errorf("AppendEntityChange(%s) = (%d, %v), want (0, nil)", kind, seq, err)
				}
			}

			// An annotation write is persisted, reads as changed, appends nothing.
			seq, changed, err := s.SetAnnotationSeq(KVAnnotation{
				Repo: gateRepo, EntityType: gateIssueTyp, EntityID: tc.id, Key: "wip", Value: "true",
				Origin: gateOrigin, SetBy: "op", SetAt: gateAt,
			})
			if err != nil || seq != 0 || !changed {
				t.Errorf("SetAnnotationSeq = (%d, %v, %v), want (0, true, nil)", seq, changed, err)
			}
			if _, found, _ := s.GetKVAnnotation(gateRepo, gateIssueTyp, tc.id, "wip"); !found {
				t.Error("annotation was not persisted")
			}
			if removed, err := s.DeleteAnnotation(gateRepo, gateIssueTyp, tc.id, "wip", gateOrigin, gateAt); err != nil || !removed {
				t.Errorf("DeleteAnnotation = (%v, %v)", removed, err)
			}

			if got := gateRows(t, s, tc.id); got != before {
				t.Errorf("rows = %d, want %d: the gate withheld nothing", got, before)
			}
		})
	}
}

// The same operations on a WATCHED in-scope issue append.
func TestDeliveryGate_WatchedAppends(t *testing.T) {
	s := gateStore(t)
	v := gateSeed(t, s, "K-1", gateOwning, gateBool(true))
	base := gateRows(t, s, "K-1")

	gateWrite(t, s, "K-1", v, []string{"fields_changed"}, WriteOptions{})
	if seq, err := s.AppendEntityChange(gateRepo, gateIssueTyp, "K-1", []string{"link_changed"}, gateOrigin, gateAt); err != nil || seq == 0 {
		t.Errorf("link_changed = (%d, %v), want a sequence", seq, err)
	}
	if seq, err := s.AppendEntityChange(gateRepo, gateIssueTyp, "K-1", []string{"added"}, "reset", gateAt); err != nil || seq == 0 {
		t.Errorf("reset replay = (%d, %v), want a sequence", seq, err)
	}
	seq, changed, err := s.SetAnnotationSeq(KVAnnotation{
		Repo: gateRepo, EntityType: gateIssueTyp, EntityID: "K-1", Key: "wip", Value: "true",
		Origin: gateOrigin, SetBy: "op", SetAt: gateAt,
	})
	if err != nil || seq == 0 || !changed {
		t.Errorf("SetAnnotationSeq = (%d, %v, %v), want (>0, true, nil)", seq, changed, err)
	}
	if got := gateRows(t, s, "K-1"); got != base+4 {
		t.Errorf("rows = %d, want %d", got, base+4)
	}
}

// A write that sets Watched=true passes its own first row (the entity row is
// created in the same statement).
func TestDeliveryGate_SettingWatchedPassesOwnRow(t *testing.T) {
	s := gateStore(t)
	backend := gateOwning
	v := gateWrite(t, s, "K-3", 0, []string{"added"}, WriteOptions{Backend: &backend, Watched: gateBool(true)})
	if v != 1 {
		t.Fatalf("version = %d", v)
	}
	if got := gateRows(t, s, "K-3"); got != 1 {
		t.Errorf("first-observation rows = %d, want 1", got)
	}
	e := gateEntity(t, s, "K-3")
	if e.Backend != gateOwning || e.Watched == nil || !*e.Watched {
		t.Errorf("columns = backend %q watched %v", e.Backend, e.Watched)
	}

	// Without Watched the same first write is withheld, though the entity is created.
	v = gateWrite(t, s, "K-4", 0, []string{"added"}, WriteOptions{Backend: &backend})
	if v != 1 || gateRows(t, s, "K-4") != 0 {
		t.Errorf("unwatched first write: version %d rows %d, want 1 and 0", v, gateRows(t, s, "K-4"))
	}
	if e := gateEntity(t, s, "K-4"); e.Backend != gateOwning || e.Watched != nil {
		t.Errorf("columns = backend %q watched %v", e.Backend, e.Watched)
	}
}

// The mark that stood before the statement counts even when the write sets
// it false; a clearing write appends its row, then clears.
func TestDeliveryGate_ClearAfterAppend(t *testing.T) {
	s := gateStore(t)
	v := gateSeed(t, s, "K-1", gateOwning, gateBool(true))
	base := gateRows(t, s, "K-1")

	nv := gateWrite(t, s, "K-1", v, []string{"closed"}, WriteOptions{ClearWatchedAfterAppend: true})
	if got := gateRows(t, s, "K-1"); got != base+1 {
		t.Errorf("closed row not appended: rows %d, want %d", got, base+1)
	}
	if e := gateEntity(t, s, "K-1"); e.Watched == nil || *e.Watched {
		t.Errorf("watched = %v, want false after the clear", e.Watched)
	}

	// Reads stay unfiltered: the row appended while watched is still delivered.
	rows, err := s.ListChangesAfter(gateIssueTyp, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.EntityID == "K-1" && len(r.Kinds) == 1 && r.Kinds[0] == "closed" {
			found = true
		}
	}
	if !found {
		t.Error("closed row hidden after the mark was cleared")
	}
	if _, err := s.ReadChanges("c1", gateIssueTyp, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Once cleared, the next write is withheld.
	gateWrite(t, s, "K-1", nv, []string{"fields_changed"}, WriteOptions{})
	if got := gateRows(t, s, "K-1"); got != base+1 {
		t.Errorf("row appended after the clear: rows %d", got)
	}
}

// A clear with no kinds appends nothing but still clears, in the same write.
func TestDeliveryGate_ClearWithoutKinds(t *testing.T) {
	s := gateStore(t)
	v := gateSeed(t, s, "K-1", gateOwning, gateBool(true))
	base := gateRows(t, s, "K-1")
	gateWrite(t, s, "K-1", v, nil, WriteOptions{ClearWatchedAfterAppend: true})
	if gateRows(t, s, "K-1") != base {
		t.Error("a row was appended for empty kinds")
	}
	if e := gateEntity(t, s, "K-1"); e.Watched == nil || *e.Watched {
		t.Errorf("watched = %v, want false", e.Watched)
	}
}

// An out-of-scope (beads) issue and a non-issue entity still append as before.
func TestDeliveryGate_OutOfScopeStillAppends(t *testing.T) {
	s := gateStore(t)
	v := gateSeed(t, s, "B-1", gateOther, nil)
	base := gateRows(t, s, "B-1")
	gateWrite(t, s, "B-1", v, []string{"fields_changed"}, WriteOptions{})
	if seq, err := s.AppendEntityChange(gateRepo, gateIssueTyp, "B-1", []string{"link_changed"}, gateOrigin, gateAt); err != nil || seq == 0 {
		t.Errorf("beads append = (%d, %v)", seq, err)
	}
	if _, changed, err := s.SetAnnotationSeq(KVAnnotation{
		Repo: gateRepo, EntityType: gateIssueTyp, EntityID: "B-1", Key: "wip", Value: "true",
		Origin: gateOrigin, SetBy: "op", SetAt: gateAt,
	}); err != nil || !changed {
		t.Errorf("annotation = (%v, %v)", changed, err)
	}
	if got := gateRows(t, s, "B-1"); got != base+3 {
		t.Errorf("rows = %d, want %d", got, base+3)
	}

	// A NULL-backend id that resolves to nothing owning is out of scope too.
	v = gateSeed(t, s, "PLAIN-1", "", nil)
	base = gateRows(t, s, "PLAIN-1")
	gateWrite(t, s, "PLAIN-1", v, []string{"fields_changed"}, WriteOptions{})
	if gateRows(t, s, "PLAIN-1") != base+1 {
		t.Error("an unresolved NULL-backend issue was gated")
	}

	// A PR entity is not an issue.
	if _, err := s.WriteEntityWithLog(
		Entity{Repo: gateRepo, EntityType: "pr", EntityID: "example/repo#1", Facts: `{}`, AsOf: gateAt},
		0, []string{"created"}, "sync", gateAt,
	); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListChangesAfter("pr", 0, 10)
	if err != nil || len(rows) != 1 {
		t.Errorf("pr rows = %d, %v", len(rows), err)
	}
}

// Without an installed scope (the default) nothing is gated.
func TestDeliveryGate_NoScopeInstalledGatesNothing(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	gateWrite(t, s, "K-9", 0, []string{"added"}, WriteOptions{})
	if gateRows(t, s, "K-9") != 1 {
		t.Error("a row was withheld with no scope installed")
	}
	s.SetGateScope(OwningGateScope(map[string]bool{gateOwning: true}, nil))
	s.SetGateScope(nil)
	gateWrite(t, s, "K-9", 1, []string{"fields_changed"}, WriteOptions{})
	if gateRows(t, s, "K-9") != 2 {
		t.Error("SetGateScope(nil) did not remove the gate")
	}
}

// A scope error aborts the append and surfaces.
func TestDeliveryGate_ScopeErrorPropagates(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	boom := errors.New("boom")
	s.SetGateScope(func(_ *sql.Tx, _, _, _, _ string) (bool, error) { return false, boom })
	_, err := s.WriteEntityStateWithLogOpts(
		Entity{Repo: gateRepo, EntityType: gateIssueTyp, EntityID: "K-1", Facts: `{}`, AsOf: gateAt},
		0, gateAt, true, nil, []string{"added"}, gateOrigin, gateAt, WriteOptions{},
	)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}
