package gather

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

type fakeStackStore struct {
	rows []store.Entity
	err  error
}

func (f fakeStackStore) UpsertXref(store.Xref) error { return nil }
func (f fakeStackStore) ListXrefsByFrom(_, _, _, _ string) ([]store.Xref, error) {
	return nil, nil
}
func (f fakeStackStore) ListEntities() ([]store.Entity, error) { return f.rows, f.err }

func stackRow(repo, id, state, branch, base, mss string) store.Entity {
	return store.Entity{
		Repo: repo, EntityType: "pr", EntityID: id,
		Facts: fmt.Sprintf(`{"pr_show":{"state":%q,"branch":%q,"base":%q,"merge_state_status":%q}}`, state, branch, base, mss),
	}
}

func TestGatherStackParents(t *testing.T) {
	const repo = "acme/api"
	tests := []struct {
		name string
		rows []store.Entity
		base string
		want []string
	}{
		{"no base", []store.Entity{stackRow(repo, "acme/api#2", "open", "b", "main", "DIRTY")}, "", nil},
		{"direct parent", []store.Entity{stackRow(repo, "acme/api#2", "open", "b", "main", "DIRTY")}, "b", []string{"acme/api#2"}},
		{"two-level chain", []store.Entity{
			stackRow(repo, "acme/api#2", "open", "b", "a", "CLEAN"),
			stackRow(repo, "acme/api#1", "open", "a", "main", "DIRTY"),
		}, "b", []string{"acme/api#2", "acme/api#1"}},
		{"merged parent is not a parent", []store.Entity{stackRow(repo, "acme/api#2", "merged", "b", "main", "DIRTY")}, "b", nil},
		{"closed parent is not a parent", []store.Entity{stackRow(repo, "acme/api#2", "closed", "b", "main", "DIRTY")}, "b", nil},
		{"other repo ignored", []store.Entity{stackRow("acme/web", "acme/web#2", "open", "b", "main", "DIRTY")}, "b", nil},
		{"cycle terminates", []store.Entity{
			stackRow(repo, "acme/api#2", "open", "b", "a", "CLEAN"),
			stackRow(repo, "acme/api#1", "open", "a", "b", "CLEAN"),
		}, "b", []string{"acme/api#2", "acme/api#1"}},
		{"head equal to its own base is no parent", []store.Entity{stackRow(repo, "acme/api#2", "open", "main", "main", "DIRTY")}, "main", nil},
		{"unreadable facts skipped", []store.Entity{{Repo: repo, EntityType: "pr", EntityID: "acme/api#2", Facts: "not json"}}, "b", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGatherer(nil, fakeStackStore{rows: tt.rows})
			var f Facts
			g.gatherStackParents(repo, "acme/api#3", tt.base, &f)
			var got []string
			for _, p := range f.StackParents {
				got = append(got, p.ID)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parents = %v; want %v", got, tt.want)
			}
			if f.Degraded != "" {
				t.Errorf("Degraded = %q; want empty", f.Degraded)
			}
		})
	}
}

func TestGatherStackParents_StoreErrorDegrades(t *testing.T) {
	g := NewGatherer(nil, fakeStackStore{err: errors.New("boom")})
	var f Facts
	g.gatherStackParents("acme/api", "acme/api#3", "b", &f)
	if f.Degraded != "stack parents" || len(f.StackParents) != 0 {
		t.Errorf("Degraded = %q, parents = %v; want degraded mark and none", f.Degraded, f.StackParents)
	}
}

func TestGatherStackParents_NilStoreIsNoop(t *testing.T) {
	g := NewGatherer(nil, nil)
	var f Facts
	g.gatherStackParents("acme/api", "acme/api#3", "b", &f)
	if f.Degraded != "" || len(f.StackParents) != 0 {
		t.Errorf("want untouched facts, got %+v", f)
	}
}
