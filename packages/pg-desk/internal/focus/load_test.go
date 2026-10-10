package focus

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Load and Candidates write nothing: the whole computation runs over a store
// opened read-only (query_only), where any write would fail.
func TestLoadAndCandidatesWriteNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg-desk.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Cutover(); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, st: st, cfg: fixtureConfig(), derived: map[Key][]store.XrefLink{}}
	pr := f.pr("7", prSpec{ownership: "mine"})
	bead := f.labelledBead("bd-1")
	f.link(pr, bead, relationWork)
	f.hide(f.pr("8", prSpec{ownership: "mine"}))
	_ = f.inputs() // flush the derived links
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	in, err := Load(ro, fixtureConfig(), testRepo, fixtureNow)
	if err != nil {
		t.Fatalf("Load over a read-only store: %v", err)
	}
	wantCandidates(t, Candidates(in), pr, bead)
}

func TestLoadReadsEveryInput(t *testing.T) {
	f := newFixture(t)
	pr := f.pr("7", prSpec{ownership: "mine"})
	other := f.pr("8", prSpec{ownership: "team"})
	f.hide(other)
	f.link(pr, other, relationDependsOn)
	f.external(pr, Key{"pr", "9"}, relationReferences, MergeReason)
	f.pr("9", prSpec{})
	// Plan rows of two periods (read-only input).
	err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		for _, p := range []struct{ key, id string }{{"2026-10-08", "7"}, {"2026-10-09", "9"}} {
			per, err := tx.GetOrCreatePeriod("day", p.key)
			if err != nil {
				return err
			}
			if _, err := tx.InsertSelection(store.FocusSelection{FocusPeriodID: per.ID, Repo: testRepo, EntityType: "pr", EntityID: p.id, SelectedAt: fixtureAt, RankPosition: 1}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	in := f.inputs()
	if in.Repo != testRepo || !in.Now.Equal(fixtureNow) || in.Config == nil {
		t.Errorf("header = %q %v %v", in.Repo, in.Now, in.Config)
	}
	if len(in.Entities) != 3 {
		t.Errorf("entities = %d, want 3", len(in.Entities))
	}
	if in.Interps[pr].Ownership != "mine" {
		t.Errorf("interpretation of %v = %+v", pr, in.Interps[pr])
	}
	if _, ok := in.Annotations[other][store.AnnotationHidden]; !ok {
		t.Errorf("annotations of %v = %v, want hidden", other, in.Annotations[other])
	}
	if len(in.Links) != 2 {
		t.Errorf("links = %d, want 2", len(in.Links))
	}
	if !in.PlanKeys[pr] || !in.PlanKeys[Key{"pr", "9"}] || len(in.PlanKeys) != 2 {
		t.Errorf("plan keys = %v, want pr 7 and pr 9 across both periods", in.PlanKeys)
	}
	if in.Absorbed[Key{"pr", "9"}] != pr || len(in.Absorbed) != 1 {
		t.Errorf("absorbed = %v, want 9 -> 7", in.Absorbed)
	}
	// The plan rows are an input that is NOT read for entry: an entity in the
	// plan is still judged by the predicates (9 is absorbed, 7 is a seed).
	wantCandidates(t, Candidates(in), pr)
}

func TestLoadScopesToTheRepo(t *testing.T) {
	f := newFixture(t)
	f.pr("7", prSpec{ownership: "mine"})
	e := store.Entity{Repo: "other/repo", EntityType: "pr", EntityID: "1", Facts: `{"pr_show":{"state":"open"}}`, AsOf: fixtureAt, ContentHash: "x"}
	if _, err := f.st.WriteEntityStateWithLog(e, 0, fixtureAt, true, []string{"created"}, "sync", fixtureAt); err != nil {
		t.Fatal(err)
	}
	in := f.inputs()
	if len(in.Entities) != 1 {
		t.Errorf("entities = %v, want only the configured repo", in.Entities)
	}
}

func TestLoadRejectsAnOldSchemaStore(t *testing.T) {
	st := store.OpenForTest(t)
	if _, err := Load(st, fixtureConfig(), testRepo, fixtureNow); err == nil {
		t.Fatal("Load over an old-schema store: want an error, never an empty set")
	}
}

func TestLoadRejectsAnUndecodableHiddenAnnotation(t *testing.T) {
	f := newFixture(t)
	k := f.pr("7", prSpec{ownership: "mine"})
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: "pr", EntityID: "7", Key: store.AnnotationHidden,
		Value: "not json", Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f.st, f.cfg, testRepo, fixtureNow); err == nil {
		t.Errorf("Load with an undecodable hidden annotation on %v: want an error", k)
	}
}

func TestLoadRejectsNoStore(t *testing.T) {
	if _, err := Load(nil, fixtureConfig(), testRepo, fixtureNow); err == nil {
		t.Fatal("Load(nil): want an error")
	}
}
