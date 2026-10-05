package changes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

type fakeLister struct {
	res gather.ListChangesResult
	err error
}

func (f fakeLister) ListChanges(context.Context, string, string, string) (gather.ListChangesResult, error) {
	return f.res, f.err
}

type fakeHydrator struct{ calls []string }

func (f *fakeHydrator) RunEntityChange(_ context.Context, _ string, id string, _ gather.ChangeKind, _ pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	f.calls = append(f.calls, id)
	return pipeline.EntityChangeResult{}, nil
}

func seededEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	st := store.OpenNewSchemaForTest(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, id := range []string{"bd-1", "bd-2"} {
		if _, err := st.WriteEntityStateWithLog(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, "2026-10-01T11:00:00Z", true, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open"}
	return &Engine{
		Cfg: cfg, Store: st, Hydrator: &fakeHydrator{},
		Lister: fakeLister{res: gather.ListChangesResult{Changes: []gather.ListedChange{{Change: gather.ChangeRemoved, EntityID: "bd-9"}}}},
		Now:    func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
	}, st
}

func cursorOf(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	cs, err := st.ListConsumers()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name {
			return c.Cursor
		}
	}
	return -1
}

// A flush that fails MUST leave the cursor where it was: the same records are
// re-delivered by the next call.
func TestRunDoesNotAdvanceCursorWhenTheFlushFails(t *testing.T) {
	e, st := seededEngine(t)
	opts := Options{EntityType: "issue", Consumer: "router"}
	_, err := e.Run(context.Background(), opts, func(Envelope) error { return errors.New("broken pipe") })
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("err = %v", err)
	}
	if got := cursorOf(t, st, "router"); got != 0 {
		t.Fatalf("cursor = %d after a failed flush, want 0", got)
	}

	var env Envelope
	if _, err := e.Run(context.Background(), opts, func(v Envelope) error { env = v; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 2 || cursorOf(t, st, "router") != env.Cursor.To {
		t.Errorf("redelivery = %+v, cursor %d", env, cursorOf(t, st, "router"))
	}
}

// Connector `removed` entries are never hydrated; one for an entity the store
// does not hold (bd-9) writes nothing. The seeded entities were hydrated
// within sweep.max_age, so the sweep leaves them alone too.
func TestRunIgnoresConnectorRemovedEntries(t *testing.T) {
	e, _ := seededEngine(t)
	h := e.Hydrator.(*fakeHydrator)
	if _, err := e.Run(context.Background(), Options{EntityType: "issue", Consumer: "router"}, func(Envelope) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 {
		t.Errorf("hydrated %v", h.calls)
	}
}

func TestSelectQueries(t *testing.T) {
	cfg := &config.Config{}
	cfg.Watch.PR.Queries = []string{"mine", "team"}
	if got, err := SelectQueries(cfg, "pr", ""); err != nil || len(got) != 2 {
		t.Errorf("all = %v, %v", got, err)
	}
	if got, err := SelectQueries(cfg, "pr", "team"); err != nil || len(got) != 1 || got[0] != "team" {
		t.Errorf("one = %v, %v", got, err)
	}
	if _, err := SelectQueries(cfg, "pr", "nope"); err == nil {
		t.Error("an unknown query was accepted")
	}
	if _, err := SelectQueries(cfg, "issue", ""); err == nil || !strings.Contains(err.Error(), "watch.issue.queries") {
		t.Errorf("no queries: %v", err)
	}
}

// A watched query that fails to list contributes no membership and no
// removal: only a successful, non-degraded listing may drop an entity.
func TestRunRemovalIgnoresAFailedQuery(t *testing.T) {
	e, st := seededEngine(t)
	e.Cfg.Watch.Issue.Queries = []string{"open", "mine"}
	e.Lister = queryLister{
		"open": {res: gather.ListChangesResult{Changes: []gather.ListedChange{{Change: gather.ChangeRemoved, EntityID: "bd-1"}}}},
		"mine": {err: errors.New("boom")},
	}
	if _, err := e.Run(context.Background(), Options{EntityType: "issue", Consumer: "router"}, func(Envelope) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// "open" listed bd-1 removed and nothing else holds it: removed.
	ent, _, _ := st.GetEntity("o/r", "issue", "bd-1")
	if !ent.Inactive {
		t.Error("bd-1 should be removed: the only successful query dropped it")
	}
}

// An entity another configured query still holds survives one query's removal.
func TestRunRemovalNeedsEveryQueryToDropTheEntity(t *testing.T) {
	e, st := seededEngine(t)
	e.Cfg.Watch.Issue.Queries = []string{"open", "mine"}
	e.Lister = queryLister{
		"open": {res: gather.ListChangesResult{Changes: []gather.ListedChange{{Change: gather.ChangeChanged, EntityID: "bd-1"}}}},
		"mine": {res: gather.ListChangesResult{Changes: []gather.ListedChange{{Change: gather.ChangeChanged, EntityID: "bd-1"}}}},
	}
	run := func() {
		t.Helper()
		if _, err := e.Run(context.Background(), Options{EntityType: "issue", Consumer: "router"}, func(Envelope) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	run()
	e.Lister = queryLister{
		"open": {res: gather.ListChangesResult{Changes: []gather.ListedChange{{Change: gather.ChangeRemoved, EntityID: "bd-1"}}}},
		"mine": {},
	}
	run()
	if ent, _, _ := st.GetEntity("o/r", "issue", "bd-1"); ent.Inactive {
		t.Error("bd-1 is still held by query mine")
	}
	e.Lister = queryLister{
		"open": {},
		"mine": {res: gather.ListChangesResult{Changes: []gather.ListedChange{{Change: gather.ChangeRemoved, EntityID: "bd-1"}}}},
	}
	run()
	if ent, _, _ := st.GetEntity("o/r", "issue", "bd-1"); !ent.Inactive {
		t.Error("bd-1 should be removed once both queries dropped it")
	}
}

type queryLister map[string]fakeLister

func (q queryLister) ListChanges(_ context.Context, _, query, _ string) (gather.ListChangesResult, error) {
	return q[query].res, q[query].err
}
