package identity

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
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

func keys(t *testing.T, st *store.Store) map[string]bool {
	t.Helper()
	rows, err := st.ListQueries(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Key] = true
	}
	return out
}

func TestUsesMe(t *testing.T) {
	resolve := func(key string) []string {
		if key == "mine" {
			return []string{"is:open author:@me"}
		}
		return nil
	}
	cases := []struct {
		key     string
		resolve ExprResolver
		want    bool
	}{
		{"mine", resolve, true},                     // configured, expression uses @me
		{"team", resolve, false},                    // configured, expression does not
		{"is:open review-requested:@me", nil, true}, // ad-hoc key carries its expression
		{"is:open label:bug", resolve, false},
		{"mine", nil, false}, // no resolver: only the key text is inspected
	}
	for _, c := range cases {
		if got := UsesMe(c.key, c.resolve); got != c.want {
			t.Errorf("UsesMe(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestViewerChangeFlushesStoredMeQueriesAndLeavesTheOthers(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	for _, q := range []store.QueryRow{
		{Key: "mine", Configured: true, IDs: []string{"n1"}},       // configured, uses @me
		{Key: "team", Configured: true, IDs: []string{"n2"}},       // configured, does not
		{Key: "is:open review-requested:@me", IDs: []string{"n3"}}, // ad-hoc, uses @me
		{Key: "is:open label:bug", IDs: []string{"n4"}},            // ad-hoc, does not
	} {
		if err := st.PutQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exprs := map[string][]string{"mine": {"is:open author:@me"}, "team": {"is:open team-review-requested:org/team"}}

	gh := &fakeGH{login: "octo-one"}
	log := &logRecorder{}
	tr := newTracker(gh, newFakeClock(), log)
	tr.RegisterFlush("queries", FlushMeQueries(st, func(k string) []string { return exprs[k] }))

	if err := tr.Refresh(ctx); err != nil { // first resolution: no flush
		t.Fatal(err)
	}
	if got := keys(t, st); len(got) != 4 {
		t.Fatalf("the first resolution flushed: %v", got)
	}

	gh.set("octo-two", nil)
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	got := keys(t, st)
	if got["mine"] || got["is:open review-requested:@me"] {
		t.Errorf("@me results survived the change: %v", got)
	}
	if !got["team"] || !got["is:open label:bug"] || len(got) != 2 {
		t.Errorf("results that do not use @me were touched: %v", got)
	}
}

type failingStore struct{ QueryStore }

func (failingStore) ListQueries(context.Context, bool) ([]store.QueryRow, error) {
	return nil, errors.New("closed")
}

func TestFlushMeQueriesReportsAStoreError(t *testing.T) {
	if err := FlushMeQueries(failingStore{}, nil)(context.Background()); err == nil {
		t.Fatal("want the list error")
	}
}
