package query

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

func at(day, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC) }

func seed(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := at(9, 0)
	for _, e := range []store.Entry{
		{ID: "a", ExternalID: "x1", SourceID: "backend-one", Type: "change", OccurredAt: at(1, 10), Summary: "a v1", Labels: []string{"x"}},
		{ID: "b", ExternalID: "x2", SourceID: "backend-two", Type: "issue", OccurredAt: at(2, 10), Summary: "b", Labels: []string{"x", "y"}},
		{ID: "c", ExternalID: "x3", SourceID: "backend-one", Type: "review", OccurredAt: at(3, 10), Summary: "c", Labels: []string{"z"}},
		// "a" re-observed with new content: the latest observation wins.
		{ID: "a", ExternalID: "x1", SourceID: "backend-one", Type: "change", OccurredAt: at(1, 10), Summary: "a v2", Labels: []string{"x"}},
	} {
		if _, err := st.Append(context.Background(), e, now); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func ids(es []store.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	sort.Strings(out)
	return out
}

func TestQueryRunComposesNarrowing(t *testing.T) {
	st := seed(t)
	cases := []struct {
		name string
		f    store.Filter
		want []string
	}{
		{"no narrowing, open range, one per id", store.Filter{OpenStart: true}, []string{"a", "b", "c"}},
		{"range excludes later days", store.Filter{Since: at(1, 0), Before: at(2, 0)}, []string{"a"}},
		{"id", store.Filter{OpenStart: true, ID: "b"}, []string{"b"}},
		{"types are OR within", store.Filter{OpenStart: true, Types: []string{"change", "issue"}}, []string{"a", "b"}},
		{"type AND label", store.Filter{OpenStart: true, Types: []string{"change", "issue"}, Labels: []string{"y"}}, []string{"b"}},
		{"labels are OR within", store.Filter{OpenStart: true, Labels: []string{"y", "z"}}, []string{"b", "c"}},
		{"sources", store.Filter{OpenStart: true, Sources: []string{"backend-one"}}, []string{"a", "c"}},
		{"source AND type", store.Filter{OpenStart: true, Sources: []string{"backend-one"}, Types: []string{"review"}}, []string{"c"}},
		{"nothing matches", store.Filter{OpenStart: true, Types: []string{"change"}, Labels: []string{"z"}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Run(context.Background(), st, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids(got), tc.want) {
				t.Errorf("ids = %v, want %v", ids(got), tc.want)
			}
		})
	}
}

func TestQueryRunReturnsLatestObservation(t *testing.T) {
	st := seed(t)
	got, err := Run(context.Background(), st, store.Filter{OpenStart: true, ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Summary != "a v2" {
		t.Fatalf("got %+v, want exactly the superseding observation (a v2)", got)
	}
}
