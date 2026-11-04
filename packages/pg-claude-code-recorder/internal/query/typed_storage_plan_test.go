package query

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// typedBuilder is a builder for the new layout over the compiled-in sources.
func typedBuilder() *builder {
	srcs := sources.All()
	b := newBuilder(sources.Catalog(srcs))
	b.typed = true
	b.storage = sources.Storage(srcs)
	return b
}

// The store has no sqlite_stat1, so every join the typed paths add must be a
// key seek: the lookup table by its id or its unique value, the extension table
// by rec_id. A scan of either would be read once per candidate row.
func TestTypedStoragePlansSeekTheLookupAndExtensionTables(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{ExtensionTables: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cases := []struct {
		name   string
		filter string
		cols   []string
		order  []string
		want   []string // plan fragments that must appear
	}{
		{
			"eq resolves the id first", `{"field":"cwd","eq":"/example/work"}`, nil, nil,
			[]string{"SEARCH lk_cwd USING"},
		},
		{
			"in resolves the ids first", `{"field":"git_branch","in":["a","b"]}`, nil, nil,
			[]string{"SEARCH lk_branch USING"},
		},
		{
			"select joins the lookup by id", ``,
			[]string{"cwd", "version"},
			nil,
			nil,
		},
		{
			"order by joins the lookup and sorts by value", ``,
			[]string{"type"},
			[]string{"cwd"},
			nil,
		},
		{
			"ext filter and select join on rec_id", `{"field":"usage.output_tokens","range":{"gte":10}}`,
			[]string{"usage.service_tier"},
			nil,
			nil,
		},
	}
	for _, c := range cases {
		b := typedBuilder()
		var f *Filter
		if c.filter != "" {
			f = new(Filter)
			if e := Decode([]byte(c.filter), f); e != nil {
				t.Fatal(e)
			}
		}
		var sel []string
		for _, n := range append(append([]string{}, c.cols...), c.order...) {
			col, e := b.col(n)
			if e != nil {
				t.Fatal(n, e)
			}
			sel = append(sel, col.expr)
		}
		cond, args, e := b.where(f)
		if e != nil {
			t.Fatal(e)
		}
		if len(sel) == 0 {
			sel = []string{"r.id"}
		}
		q := "SELECT " + strings.Join(sel, ",") + base(false) + " " + strings.Join(b.joins, " ") + " WHERE " + cond
		if len(c.order) > 0 {
			col, _ := b.col(c.order[0])
			q += " ORDER BY " + col.expr
		}
		p := plan(t, st, q, append(append([]any{}, b.joinArgs...), args...))
		// Only records is walked; every other table (the plan names a joined
		// table by its alias) is a key seek.
		for _, l := range strings.Split(p, "\n") {
			if strings.HasPrefix(l, "SCAN ") && !strings.HasPrefix(l, "SCAN r ") && l != "SCAN r" {
				t.Errorf("%s: a joined table is scanned (%q):\n%s\nSQL: %s", c.name, l, p, q)
			}
		}
		for _, w := range c.want {
			if !strings.Contains(p, w) {
				t.Errorf("%s: plan lacks %q:\n%s\nSQL: %s", c.name, w, p, q)
			}
		}
	}
}
