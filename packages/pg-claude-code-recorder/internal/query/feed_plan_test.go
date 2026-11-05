package query

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The snapshot page of a subscriber that filters on source and item and projects
// many fields must walk the record id range and stop at its limit. The store has
// no sqlite_stat1, and the planner then prefers records_source_type for the
// source test, which reads every record of the source and sorts them by id
// before the limit applies: the first page costs the whole table and outruns the
// query deadline on a large store.
func TestSelectPlanWalksTheIDRangeForASourceAndItemFilter(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{ExtensionTables: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srcs := sources.All()
	e := &Engine{
		DB:           st.Read(),
		Catalog:      sources.Catalog(srcs),
		Storage:      sources.Storage(srcs),
		TypedStorage: func() bool { return true },
		SessionRef:   func() bool { return true },
	}
	var f Filter
	if werr := Decode([]byte(`{"all":[{"field":"source","eq":"claude-transcript"},{"field":"item","eq":-1}]}`), &f); werr != nil {
		t.Fatal(werr)
	}
	sel := Selection{
		Where: &f,
		Project: []string{
			"type", "session", "time", "uuid", "parent_uuid", "is_sidechain", "cwd",
			"git_branch", "permission_mode", "duration_ms", "user_type", "source_tool_assistant_uuid",
			"prompt_id", "entrypoint",
		},
		UpToID:     1 << 40,
		CurGenOnly: true,
		Limit:      1000,
	}
	q, args, _, _, werr := e.selectQuery(sel)
	if werr != nil {
		t.Fatal(werr)
	}
	p := plan(t, st, q, args)
	lines := strings.Split(p, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "SEARCH r USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)") {
		t.Errorf("records is not walked by id range first:\n%s", p)
	}
	for _, l := range lines {
		if strings.Contains(l, "USE TEMP B-TREE FOR ORDER BY") {
			t.Errorf("the page is sorted, so every matching row is read before the limit applies:\n%s", p)
		}
		if strings.Contains(l, "INDEX records_") {
			t.Errorf("a records index serves the page (%q):\n%s", l, p)
		}
	}
	if _, werr := e.Select(st.Read(), sel); werr != nil {
		t.Fatal(werr)
	}
}
