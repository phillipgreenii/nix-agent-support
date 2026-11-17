//go:build integration

package query

import (
	"reflect"
	"testing"
)

func TestPartialIndexResultsMatchFullIndexBaseline(t *testing.T) {
	part, full := partialStore(t, false), partialStore(t, true)
	cases := map[string][2]string{}
	for name, where := range numericFilters {
		cases[name] = [2]string{where, ""}
		cases[name+" sorted"] = [2]string{where, "tokens"}
	}
	cases["sort only"] = [2]string{`{"field":"label","eq":"l3"}`, "tokens"}
	cases["exists"] = [2]string{`{"field":"tokens","exists":true}`, ""}
	cases["absent"] = [2]string{`{"field":"tokens","exists":false}`, ""}
	for name, c := range cases {
		q, args := sqlFor(t, c[0], c[1])
		want, got := ids(t, full, q, args), ids(t, part, q, args)
		if len(want) == 0 {
			t.Errorf("%s: baseline is empty, the case proves nothing", name)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: partial index gave %v, full index %v", name, got, want)
		}
	}
}
