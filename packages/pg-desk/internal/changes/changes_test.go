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
		if _, err := st.WriteEntityWithLog(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z"); err != nil {
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

// Connector `removed` entries are never hydrated and never become records in
// this flow.
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
