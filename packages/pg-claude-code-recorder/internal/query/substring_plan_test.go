package query

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// A substring scan reads candidates newest first and stops at its budget, so its
// statement must start from records and stream in time order. The planner used
// to start from every opaque text of the store and sort them all before the
// first candidate, so a time bound or an unscoped request outran the query
// deadline on a large store.
func TestSubstringPlanStreamsRecordsNewestFirst(t *testing.T) {
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
	cases := map[string]string{
		"since":   `{"all":[{"field":"source","eq":"claude-transcript"},{"field":"time","range":{"gte":"2026-11-04T00:00:00Z"}}]}`,
		"open":    `{"all":[{"field":"source","eq":"claude-transcript"}]}`,
		"session": `{"all":[{"field":"source","eq":"claude-transcript"},{"field":"session","eq":"abc"}]}`,
	}
	for name, where := range cases {
		t.Run(name, func(t *testing.T) {
			var f Filter
			if werr := Decode([]byte(where), &f); werr != nil {
				t.Fatal(werr)
			}
			q, args, fields, werr := e.substringQuery(SearchRequest{Mode: "substring", Q: "needle", Where: &f})
			if werr != nil || len(fields) == 0 {
				t.Fatalf("query: %v fields=%v", werr, fields)
			}
			p := plan(t, st, q, args)
			lines := strings.Split(p, "\n")
			if !strings.HasPrefix(lines[0], "SEARCH r USING INDEX records_") && !strings.HasPrefix(lines[0], "SCAN r USING INDEX records_") {
				t.Errorf("records is not the outer table:\n%s", p)
			}
			if name != "session" && !strings.Contains(lines[0], "records_time") {
				t.Errorf("the scan does not walk records_time:\n%s", p)
			}
			for _, l := range lines {
				if strings.HasPrefix(l, "SCAN t") {
					t.Errorf("texts is scanned whole:\n%s", p)
				}
				if name != "session" && strings.Contains(l, "USE TEMP B-TREE FOR ORDER BY") {
					t.Errorf("every candidate is sorted before the first is read:\n%s", p)
				}
			}
		})
	}
}
