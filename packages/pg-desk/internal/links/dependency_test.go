package links

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// putStackPR stores a PR with a snapshot URL, state and branches.
func putStackPR(t *testing.T, st *store.Store, n int, state, branch, base string, merged bool) string {
	t.Helper()
	id := fmt.Sprintf("acme/api#%d", n)
	facts := fmt.Sprintf(`{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/%d","state":%q,"branch":%q,"base":%q,"merged":%v,"title":"PR %d"}}`,
		n, state, branch, base, merged, n)
	putEntity(t, st, "pr", id, "2026-10-01T10:00:00Z", facts)
	return id
}

func dependsOnLinks(item Item) []Link {
	var out []Link
	for _, l := range item.Links {
		if l.Relation == "depends_on" {
			out = append(out, l)
		}
	}
	return out
}

// `pg-desk links` lists the PRs a PR depends on, with their state, on both
// store schema versions (the stack source needs only stored branches).
func TestPRLinks_DependsOnStack(t *testing.T) {
	for name, open := range map[string]func(testing.TB) *store.Store{
		"migrated store":   func(tb testing.TB) *store.Store { return store.OpenNewSchemaForTest(tb) },
		"unmigrated store": func(tb testing.TB) *store.Store { return store.OpenForTest(tb) },
	} {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			base := putStackPR(t, st, 1, "open", "feat-a", "main", false)
			top := putStackPR(t, st, 2, "open", "feat-b", "feat-a", false)

			r := resolve(t, deps(st), "pr:"+top, "pr:"+base)
			want := []Link{{Kind: KindPR, Relation: "depends_on", Label: "PR #1", URL: "https://scm.example.invalid/acme/api/pull/1", State: "open"}}
			if got := dependsOnLinks(r.Items["pr:"+top]); !reflect.DeepEqual(got, want) {
				t.Errorf("depends_on links of the stacked PR =\n%+v\nwant\n%+v", got, want)
			}
			if got := dependsOnLinks(r.Items["pr:"+base]); len(got) != 0 {
				t.Errorf("depends_on links of the stack root = %+v; want none", got)
			}
			// Both stored PRs resolve under the same Result, sharing one read.
			if !r.Items["pr:"+top].Known || !r.Items["pr:"+base].Known {
				t.Errorf("items = %+v", r.Items)
			}
		})
	}
}

// Once the base PR has merged the stack edge is gone; an external depends_on
// link to a merged PR stays and reports state merged.
func TestPRLinks_DependsOnExternalAndMerged(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	base := putStackPR(t, st, 1, "closed", "feat-a", "main", true)
	top := putStackPR(t, st, 2, "open", "feat-b", "feat-a", false)
	if got := dependsOnLinks(resolve(t, deps(st), "pr:"+top).Items["pr:"+top]); len(got) != 0 {
		t.Fatalf("depends_on links with a merged base = %+v; want none (stack needs the base open)", got)
	}
	err := st.AddExternalXref(store.XrefLink{
		Repo: testRepo, FromType: "pr", FromID: top, ToType: "pr", ToID: base, Relation: "depends_on",
		Actor: "operator", ActedAt: "2026-10-01T12:00:00Z", FirstSeen: "2026-10-01T12:00:00Z", LastConfirmed: "2026-10-01T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := dependsOnLinks(resolve(t, deps(st), "pr:"+top).Items["pr:"+top])
	want := []Link{{Kind: KindPR, Relation: "depends_on", Label: "PR #1", URL: "https://scm.example.invalid/acme/api/pull/1", State: "merged"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("depends_on links =\n%+v\nwant\n%+v", got, want)
	}
}

// `show`'s links[] carries the derived stack dependency with its origin and
// the dependency's stored state, and an external claim on the same edge
// merges into the same entry without repeating it.
func TestReadDetailed_DependsOn(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	base := putStackPR(t, st, 1, "open", "feat-a", "main", false)
	top := putStackPR(t, st, 2, "open", "feat-b", "feat-a", false)
	d := deps(st)

	got, err := ReadDetailed(d, "pr", top)
	if err != nil || len(got.Links) != 1 {
		t.Fatalf("ReadDetailed = %+v, %v", got, err)
	}
	l := got.Links[0]
	if l.Type != "pr" || l.ID != base || l.Relation != "depends_on" || l.State != "open" || !l.Stored ||
		l.URL != "https://scm.example.invalid/acme/api/pull/1" || l.Title != "PR 1" ||
		!reflect.DeepEqual(l.Origins, []Origin{{Origin: "derived:stack"}}) {
		t.Errorf("link = %+v", l)
	}

	err = st.AddExternalXref(store.XrefLink{
		Repo: testRepo, FromType: "pr", FromID: top, ToType: "pr", ToID: base, Relation: "depends_on",
		Actor: "operator", ActedAt: "2026-10-01T12:00:00Z", Reason: "stacked by hand",
		FirstSeen: "2026-10-01T12:00:00Z", LastConfirmed: "2026-10-01T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = ReadDetailed(d, "pr", top)
	if err != nil || len(got.Links) != 1 {
		t.Fatalf("ReadDetailed with an external claim = %+v, %v", got, err)
	}
	wantOrigins := []Origin{{Origin: "derived:stack"}, {Origin: "external:operator", At: "2026-10-01T12:00:00Z", Reason: "stacked by hand"}}
	if !reflect.DeepEqual(got.Links[0].Origins, wantOrigins) {
		t.Errorf("origins = %+v; want %+v", got.Links[0].Origins, wantOrigins)
	}
}

// On the unmigrated store `show` still lists the stack dependency.
func TestReadDetailed_DependsOnUnmigrated(t *testing.T) {
	st := store.OpenForTest(t)
	putStackPR(t, st, 1, "open", "feat-a", "main", false)
	top := putStackPR(t, st, 2, "open", "feat-b", "feat-a", false)
	got, err := ReadDetailed(deps(st), "pr", top)
	if err != nil || !got.Degraded || len(got.Links) != 1 || got.Links[0].Relation != "depends_on" {
		t.Fatalf("ReadDetailed = %+v, %v", got, err)
	}
}
