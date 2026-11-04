package parser_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const historyFixture = `{"project=":"fix the example bug","timestamp":1761768000000,"project":"/example/work","sessionId":"example-session-1"}` + "\n" +
	`{"project=":"second prompt","timestamp":1761768001000,"project":"/example/work"}` + "\n"

// TestFieldNameLookupsStopAfterWarmup shows that, once the first file has
// interned its field names, parsing more files of the same shape sends no
// field_names statement at all, where it used to send one per name per line.
func TestFieldNameLookupsStopAfterWarmup(t *testing.T) {
	c := countdriver.Register("fieldnames-counting")
	k := testkit.New(t)
	k.StoreOptions = store.Options{DriverName: "fieldnames-counting"}
	k.Reopen()
	const files = 20
	for i := 0; i < files; i++ {
		body := strings.ReplaceAll(transcriptFixture, "example-session-1", fmt.Sprintf("example-session-%d", i))
		k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", i), body)
	}
	fs := k.Count(`SELECT COUNT(*) FROM generations`)
	if fs != files {
		t.Fatalf("fixture has %d files", fs)
	}
	ids, err := k.Store.AllFiles()
	if err != nil {
		t.Fatal(err)
	}
	// Warm up on the first file.
	if _, err := k.Parser.ParseFile(ids[0].ID); err != nil {
		t.Fatal(err)
	}
	warm := k.Count(`SELECT COUNT(*) FROM records`)
	c.Reset()
	for _, f := range ids[1:] {
		if _, err := k.Parser.ParseFile(f.ID); err != nil {
			t.Fatal(err)
		}
	}
	recs := k.Count(`SELECT COUNT(*) FROM records`) - warm
	if recs < 10*(files-1) {
		t.Fatalf("only %d records parsed", recs)
	}
	lookups := c.Matching("field_names")
	t.Logf("field_names statements: %d over %d records (%.3f per record); all statements %d (%.2f per record)",
		lookups, recs, float64(lookups)/float64(recs), c.Queries(), float64(c.Queries())/float64(recs))
	if lookups != 0 {
		t.Fatalf("%d field_names statements after warmup, want 0", lookups)
	}
}

// fieldsDump lists every stored field as record identity, name and value,
// sorted by name, independent of the ids the names were given.
func fieldsDump(t *testing.T, k *testkit.Kit) string {
	t.Helper()
	rows, err := k.Store.Read().Query(`
SELECT r.file_id, r.gen, r.off, r.item, fn.name, COALESCE(f.s,''), COALESCE(f.n,''), COALESCE(f.f,'')
FROM fields f JOIN records r ON r.id=f.rec_id JOIN field_names fn ON fn.id=f.name_id
ORDER BY r.file_id, r.gen, r.off, r.item, fn.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var f, g, off, item int64
		var name, s, n, fl string
		if err := rows.Scan(&f, &g, &off, &item, &name, &s, &n, &fl); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %d %d %d %s=%s/%s/%s\n", f, g, off, item, name, s, n, fl)
	}
	return b.String()
}

// failParseState makes the next parse_state upsert abort, so a parse fails
// AFTER its inserts interned new field names.
func failParseState(t *testing.T, k *testkit.Kit, on bool) {
	t.Helper()
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		q := `DROP TRIGGER IF EXISTS fail_parse_state`
		if on {
			q = `CREATE TRIGGER fail_parse_state BEFORE INSERT ON parse_state BEGIN SELECT RAISE(ABORT, 'injected'); END`
		}
		_, err := tx.Exec(q)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFieldNameCacheSurvivesRolledBackWrites: a write that fails after
// interning field names is undone (the whole transaction, or in a bulk rebuild
// just its savepoint) and field_names ids are reused, so the cache must not
// keep the undone ids. The result must equal a build that never failed.
func TestFieldNameCacheSurvivesRolledBackWrites(t *testing.T) {
	build := func(t *testing.T, opt store.Options, fail bool) string {
		k := testkit.New(t)
		k.StoreOptions = opt
		k.Reopen()
		tr := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
		k.Add("example-proj", "history.jsonl", historyFixture)
		files, err := k.Store.AllFiles()
		if err != nil {
			t.Fatal(err)
		}
		if fail {
			failParseState(t, k, true)
			if _, err := k.Parser.ParseFile(tr); err == nil {
				t.Fatal("the injected failure did not fail the parse")
			}
			if n := k.Count(`SELECT COUNT(*) FROM field_names`); n != 0 {
				t.Fatalf("%d field_names rows survived the rolled-back write", n)
			}
			failParseState(t, k, false)
			// A different shape of file takes the freed ids first.
			for _, f := range files {
				if f.ID != tr {
					if _, err := k.Parser.ParseFile(f.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := k.Parser.ParseFile(tr); err != nil {
				t.Fatal(err)
			}
		} else {
			k.Parse()
		}
		if opt.BulkRebuild {
			if err := k.Store.FinishRebuild(); err != nil {
				t.Fatal(err)
			}
		}
		return fieldsDump(t, k)
	}
	for name, opt := range map[string]store.Options{
		"transaction": {},
		"savepoint":   {Rebuild: true, BulkRebuild: true, BulkRecords: 1000},
	} {
		t.Run(name, func(t *testing.T) {
			want := build(t, opt, false)
			if !strings.Contains(want, "message.usage.input_tokens") || !strings.Contains(want, "project=") {
				t.Fatalf("fixture stores no fields:\n%s", want)
			}
			if got := build(t, opt, true); got != want {
				t.Fatalf("a rolled-back write left a stale field name id\n--- got\n%s\n--- want\n%s", got, want)
			}
		})
	}
}
