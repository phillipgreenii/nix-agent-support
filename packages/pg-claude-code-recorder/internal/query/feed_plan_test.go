package query

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The snapshot page of a subscriber that filters on source and item and projects
// many fields must walk the record id range and stop at its limit. The test
// database is unanalyzed (the live store is analyzed daily), and the planner then
// prefers records_source_type for the
// source test, which reads every record of the source and sorts them by id
// before the limit applies: the first page costs the whole table and outruns the
// query deadline on a large store. Once sqlite_stat1 exists on a skewed store
// the planner already avoids that index without the unary plus, so a plan test
// over an analyzed store cannot fail for this regression and is not kept.
func TestSelectPlanWalksTheIDRangeForASourceAndItemFilter(t *testing.T) {
	st, err := openPrepared(t, t.TempDir(), store.Options{ExtensionTables: true})
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

// skewedStore is the shared read-only store of the two source-threshold tests:
// 1000 records of which 8 are 'other' (ids 125, 250, ...) and the rest
// 'claude-transcript', plus 150 'big' records (ids 5001..5150, three types).
// The rare-source test reads up to id 1000 only, so the 'big' rows do not
// reach it.
func skewedStore(t *testing.T) *store.Store {
	t.Helper()
	return sharedStore(t, "skewed", store.Options{ExtensionTables: true}, func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'claude-transcript','append',1,0)`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1000)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT i,1,0,i,-1,1,CASE WHEN i%125=0 THEN 'other' ELSE 'claude-transcript' END,CASE WHEN i%2=0 THEN 'user' ELSE 'assistant' END,1,i,i%400+1 FROM n`,
			`WITH RECURSIVE n(i) AS (SELECT 5001 UNION ALL SELECT i+1 FROM n WHERE i<5150)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT i,1,0,i,-1,1,'big',CASE i%3 WHEN 0 THEN 'a' WHEN 1 THEN 'b' ELSE 'c' END,1,i,i%400+1 FROM n`,
		} {
			if _, err := tx.ExecContext(context.Background(), q); err != nil {
				return err
			}
		}
		return nil
	})
}

// setRareSourceMax lowers the rare-source threshold for a test whose seeded
// table is small.
func setRareSourceMax(t *testing.T, n int) {
	t.Helper()
	old := rareSourceMax
	rareSourceMax = n
	t.Cleanup(func() { rareSourceMax = old })
}

// An empty-cursor feed filtered on a source that is rare in the id range (the
// session registry) must read it through records_source_type instead of walking
// every retained record, and must answer exactly what the walk answers.
func TestSelectRareSourceUsesTheSourceIndexAndMatchesTheWalk(t *testing.T) {
	st := skewedStore(t) // 8 'other' records (ids 125, 250, ...) among 1000
	srcs := sources.All()
	e := &Engine{
		DB:           st.Read(),
		Catalog:      sources.Catalog(srcs),
		Storage:      sources.Storage(srcs),
		TypedStorage: func() bool { return true },
		SessionRef:   func() bool { return true },
	}
	var f Filter
	if werr := Decode([]byte(`{"field":"source","eq":"other"}`), &f); werr != nil {
		t.Fatal(werr)
	}
	sel := Selection{Where: &f, UpToID: 1000, Limit: 5}

	setRareSourceMax(t, 100)
	if !e.rareSource(st.Read(), "other", sel) {
		t.Fatal("a source with 8 records is not rare under a threshold of 100")
	}
	idx := sel
	idx.sourceIdx = true
	q, args, _, _, werr := e.selectQuery(idx)
	if werr != nil {
		t.Fatal(werr)
	}
	if p := plan(t, st, q, args); !strings.Contains(p, "INDEX records_source_type") {
		t.Errorf("the rare source is not read through its index:\n%s", p)
	}
	got, werr := e.Select(st.Read(), sel)
	if werr != nil {
		t.Fatal(werr)
	}

	setRareSourceMax(t, 2) // too many records now: the id walk
	if e.rareSource(st.Read(), "other", sel) {
		t.Fatal("a source with 8 records is rare under a threshold of 2")
	}
	want, werr := e.Select(st.Read(), sel)
	if werr != nil {
		t.Fatal(werr)
	}
	if len(got) != 6 || len(want) != 6 { // limit+1 rows
		t.Fatalf("rows: index %d, walk %d, want 6 each", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID || got[i].ID != int64(125*(i+1)) {
			t.Errorf("row %d: index id %d, walk id %d, want %d", i, got[i].ID, want[i].ID, 125*(i+1))
		}
	}
}

// A pinned source with more records than rareSourceMax (the session registry
// holds tens of thousands) must not walk the id range or sort its rows: the
// empty-cursor feed page and the snapshot page of limit 1 read each type through
// records_source_type in id order, and answer exactly what the id walk answers.
func TestSelectSourceAboveThresholdReadsByTypeAndMatchesTheWalk(t *testing.T) {
	st := skewedStore(t) // 150 'big' records, above a threshold of 100
	srcs := sources.All()
	e := &Engine{
		DB:           st.Read(),
		Catalog:      sources.Catalog(srcs),
		Storage:      sources.Storage(srcs),
		TypedStorage: func() bool { return true },
		SessionRef:   func() bool { return true },
	}
	var f Filter
	if werr := Decode([]byte(`{"field":"source","eq":"big"}`), &f); werr != nil {
		t.Fatal(werr)
	}
	setRareSourceMax(t, 100)

	for _, tc := range []struct {
		name  string
		sel   Selection
		after int64
	}{
		{"feed empty cursor", Selection{Where: &f, UpToID: 1 << 40, Limit: 5}, 0},
		{"snapshot limit 1", Selection{Where: &f, UpToID: 1 << 40, Limit: 1, CurGenOnly: true}, 0},
		{"snapshot next page", Selection{Where: &f, AfterID: 5030, UpToID: 1 << 40, Limit: 7, CurGenOnly: true}, 0},
	} {
		sel := tc.sel
		if e.rareSource(st.Read(), "big", sel) {
			t.Fatalf("%s: a source of 150 records is rare under a threshold of 100", tc.name)
		}
		got, werr := e.Select(st.Read(), sel)
		if werr != nil {
			t.Fatal(werr)
		}
		want, werr := e.selectRows(st.Read(), sel) // the id walk
		if werr != nil {
			t.Fatal(werr)
		}
		if len(got) != limitOf(sel.Limit)+1 || len(got) != len(want) {
			t.Fatalf("%s: rows by type %d, walk %d, want %d", tc.name, len(got), len(want), limitOf(sel.Limit)+1)
		}
		for i := range got {
			if got[i].ID != want[i].ID {
				t.Errorf("%s: row %d: by type id %d, walk id %d", tc.name, i, got[i].ID, want[i].ID)
			}
		}
	}

	typ := "b"
	sel := Selection{Where: &f, UpToID: 1 << 40, Limit: 1, CurGenOnly: true, typePin: &typ, srcPin: "big"}
	q, args, _, _, werr := e.selectQuery(sel)
	if werr != nil {
		t.Fatal(werr)
	}
	p := plan(t, st, q, args)
	if !strings.Contains(p, "INDEX records_source_type") || strings.Contains(p, "USE TEMP B-TREE FOR ORDER BY") {
		t.Errorf("a type's page is not read in id order through its index:\n%s", p)
	}
}
