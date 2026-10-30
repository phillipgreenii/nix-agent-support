package pending

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/sidecar"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite3"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seed gives each id an entity with a pending row and a detail row.
func seed(t *testing.T, st *store.Store, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		if err := st.PutEntity(ctx, store.EntityRow{Kind: entity.KindPR, ID: id, Version: 1, LastAccess: t0}); err != nil {
			t.Fatal(err)
		}
		for _, g := range []string{groups.Pending, groups.Detail, groups.Summary} {
			row := store.GroupRow{Kind: entity.KindPR, ID: id, Group: g, Content: []byte(g + "-of-" + id), FetchedAt: t0, Generation: 1}
			if err := st.PutGroup(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// groupsOf lists the group names stored for id.
func groupsOf(t *testing.T, st *store.Store, id string) []string {
	t.Helper()
	rows, err := st.ListGroups(context.Background(), entity.KindPR, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Group)
	}
	return out
}

func TestPendingGroupsFlushDeletesOnlyThePendingRows(t *testing.T) {
	st := openStore(t)
	seed(t, st, "PR_1", "PR_2", "PR_3")
	if err := FlushPendingGroupsFunc(st)(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"PR_1", "PR_2", "PR_3"} {
		if got := fmt.Sprint(groupsOf(t, st, id)); got != "[detail summary]" {
			t.Errorf("%s groups = %s, want only the pending row gone", id, got)
		}
	}
	if es, _ := st.ListEntities(context.Background(), entity.KindPR); len(es) != 3 {
		t.Errorf("entities = %d, want all 3 kept", len(es))
	}
}

type failingStore struct{ FlushStore }

func (failingStore) ListEntities(context.Context, string) ([]store.EntityRow, error) {
	return nil, errors.New("closed")
}

func TestPendingGroupsFlushReportsAStoreError(t *testing.T) {
	if err := FlushPendingGroupsFunc(failingStore{})(context.Background()); err == nil {
		t.Fatal("want the list error")
	}
}

// ---- the tracker-driven tests ----

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// viewerGH answers the tracker's viewer query.
type viewerGH struct {
	mu    sync.Mutex
	login string
}

func (g *viewerGH) Run(context.Context, ...string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return []byte(fmt.Sprintf(`{"data":{"viewer":{"login":%q}}}`, g.login)), nil
}

func (g *viewerGH) set(login string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.login = login
}

// openSidecar returns the posted-review sidecar over st.
func openSidecar(t *testing.T, st *store.Store) *sidecar.Store {
	t.Helper()
	s, err := sidecar.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sidecarRows returns every posted_review row of st as "owner/repo#n state at".
func sidecarRows(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT owner, repo, number, state, updated_at FROM posted_review ORDER BY owner, repo, number`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var owner, repo, state string
		var n, at int64
		if err := rows.Scan(&owner, &repo, &n, &state, &at); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s/%s#%d %s %d", owner, repo, n, state, at))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAViewerChangeRunsBothFlushesOnce(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seed(t, st, "PR_1", "PR_2")

	sidecars := openSidecar(t, st)
	before := pr.LastAppend{At: t0.Add(-time.Hour).Format(time.RFC3339), Added: 2, Head: "head0"}
	if err := sidecars.Save(ctx, "foo", "bar", 1, posted.State{LastAppend: &posted.LastAppend{At: before.At, Added: before.Added, Head: before.Head}}); err != nil {
		t.Fatal(err)
	}
	after := pr.LastAppend{At: t0.Add(5 * time.Minute).Format(time.RFC3339), Added: 3, Head: "head1"}
	if err := sidecars.Save(ctx, "foo", "bar", 2, posted.State{LastAppend: &posted.LastAppend{At: after.At, Added: after.Added, Head: after.Head}}); err != nil {
		t.Fatal(err)
	}
	rowsBefore := sidecarRows(t, st)
	if len(rowsBefore) != 2 {
		t.Fatalf("seeded rows = %v", rowsBefore)
	}

	clock := &fakeClock{now: t0}
	gh := &viewerGH{login: "octo-one"}
	tr := identity.New(identity.Options{Host: "github.example.test", GH: gh, Clock: clock})
	view := &SidecarView{Store: sidecars}
	RegisterFlushes(tr, st, view, clock)

	// The first resolution flushes nothing; both entries are visible.
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := view.Last("foo/bar", 1); got == nil || *got != before {
		t.Fatalf("before any change: last_append = %+v, want %+v", got, before)
	}
	if got := fmt.Sprint(groupsOf(t, st, "PR_1")); got != "[detail pending summary]" {
		t.Fatalf("the first resolution flushed: %s", got)
	}

	// The viewer changes: the pending rows go and the earlier append is hidden.
	gh.set("octo-two")
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"PR_1", "PR_2"} {
		if got := fmt.Sprint(groupsOf(t, st, id)); got != "[detail summary]" {
			t.Errorf("%s groups = %s, want the pending row flushed", id, got)
		}
	}
	if got := view.Last("foo/bar", 1); got != nil {
		t.Errorf("an append from before the change is still reported: %+v", got)
	}
	if got := view.Last("foo/bar", 2); got == nil || *got != after {
		t.Errorf("an append recorded after the change = %+v, want %+v", got, after)
	}

	// A flush runs once per change: a pending row written later survives the next refresh.
	seed(t, st, "PR_1")
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(groupsOf(t, st, "PR_1")); got != "[detail pending summary]" {
		t.Errorf("a refresh without a change flushed again: %s", got)
	}

	// The sidecar rows are identical: a flush never touches them.
	if got := sidecarRows(t, st); !slices.Equal(got, rowsBefore) {
		t.Errorf("sidecar rows changed:\n%v\nwas:\n%v", got, rowsBefore)
	}
}

// flakyStore fails its first DeleteGroup.
type flakyStore struct {
	FlushStore
	mu    sync.Mutex
	fails int
}

func (f *flakyStore) DeleteGroup(ctx context.Context, kind, id, group string) error {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return errors.New("database is locked")
	}
	f.mu.Unlock()
	return f.FlushStore.DeleteGroup(ctx, kind, id, group)
}

func TestAFailingFlushStaysPendingAndTheNextRefreshRetriesIt(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seed(t, st, "PR_1")
	clock := &fakeClock{now: t0}
	gh := &viewerGH{login: "octo-one"}
	tr := identity.New(identity.Options{GH: gh, Clock: clock})
	view := &SidecarView{Store: openSidecar(t, st)}
	RegisterFlushes(tr, &flakyStore{FlushStore: st, fails: 1}, view, clock)

	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	gh.set("octo-two")
	if err := tr.Refresh(ctx); err != nil { // the pending-groups flush fails here
		t.Fatal(err)
	}
	if got := fmt.Sprint(groupsOf(t, st, "PR_1")); got != "[detail pending summary]" {
		t.Fatalf("the failed flush deleted something: %s", got)
	}
	if err := tr.Refresh(ctx); err != nil { // same viewer: only the pending flush is retried
		t.Fatal(err)
	}
	if got := fmt.Sprint(groupsOf(t, st, "PR_1")); got != "[detail summary]" {
		t.Errorf("the retry did not flush: %s", got)
	}
}

func TestSidecarViewDegradesToNoLastAppend(t *testing.T) {
	var nilView *SidecarView
	if nilView.Last("foo/bar", 1) != nil || (&SidecarView{}).Last("foo/bar", 1) != nil {
		t.Error("an unconfigured view reported a last_append")
	}
	st := openStore(t)
	v := &SidecarView{Store: openSidecar(t, st)}
	if v.Last("foo/bar", 1) != nil || v.Last("nope", 1) != nil || v.Last("foo/bar", 0) != nil {
		t.Error("a missing sidecar, a bad repo or a bad number reported a last_append")
	}
	if _, err := st.DB().Exec(
		`INSERT INTO posted_review (owner, repo, number, state, updated_at) VALUES ('foo', 'bar', 1, 'not json', 1)`,
	); err != nil {
		t.Fatal(err)
	}
	if v.Last("foo/bar", 1) != nil {
		t.Error("a corrupt sidecar reported a last_append")
	}
}

func TestSidecarViewHidesAnAppendWithAnUnreadableTimeOnceTheIdentityChanged(t *testing.T) {
	s := openSidecar(t, openStore(t))
	if err := s.Save(context.Background(), "foo", "bar", 1, posted.State{LastAppend: &posted.LastAppend{At: "yesterday", Added: 1, Head: "h"}}); err != nil {
		t.Fatal(err)
	}
	v := &SidecarView{Store: s}
	if v.Last("foo/bar", 1) == nil {
		t.Fatal("before any change the append is reported")
	}
	v.MarkIdentityChanged(t0)
	if v.Last("foo/bar", 1) != nil {
		t.Error("an append that cannot be shown to postdate the change is reported")
	}
}
