package parser_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// bulkCorpus adds files copies of the transcript fixture, each its own session,
// and returns the kit (journal only; nothing parsed yet).
func bulkCorpus(t *testing.T, files int) *testkit.Kit {
	t.Helper()
	k := testkit.New(t)
	for i := 0; i < files; i++ {
		name := fmt.Sprintf("example-session-%d.jsonl", i)
		body := strings.ReplaceAll(transcriptFixture, "example-session-1", fmt.Sprintf("example-session-%d", i))
		k.Add("example-proj", name, body)
	}
	return k
}

// indexDump is everything a query can see, independent of record ids and
// commit grouping: records, their fields and texts, and an FTS5 hit set.
func indexDump(t *testing.T, k *testkit.Kit) string {
	t.Helper()
	var b strings.Builder
	rows, err := k.Store.Read().Query(`
SELECT r.file_id, r.gen, r.off, r.item, r.type, r.session, r.time_ns,
  COALESCE((SELECT group_concat(fn.name||'='||COALESCE(f.s,'')||'/'||COALESCE(f.n,'')||'/'||COALESCE(f.f,''), ';')
    FROM fields f JOIN field_names fn ON fn.id=f.name_id WHERE f.rec_id=r.id ORDER BY fn.name), ''),
  COALESCE((SELECT group_concat(t.field||':'||t.kind, ';') FROM texts t WHERE t.rec_id=r.id ORDER BY t.id), '')
FROM records r ORDER BY r.file_id, r.gen, r.off, r.item`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var f, g, off, item, tm int64
		var typ, sess, flds, txts string
		if err := rows.Scan(&f, &g, &off, &item, &typ, &sess, &tm, &flds, &txts); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %d %d %d %s %s %d [%s] [%s]\n", f, g, off, item, typ, sess, tm, flds, txts)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM fts_word WHERE fts_word MATCH 'registry'`,
		`SELECT COUNT(*) FROM fts_word WHERE fts_word MATCH 'words'`,
		`SELECT COUNT(*) FROM fts_tri WHERE fts_tri MATCH 'done'`,
		`SELECT COUNT(*) FROM bad_lines`,
		`SELECT COUNT(*) FROM unknown_keys`,
		`SELECT COUNT(*) FROM unknown_keys_total`,
		`SELECT COALESCE(SUM(count),0) FROM unknown_keys_total`,
	} {
		fmt.Fprintf(&b, "%s -> %d\n", q, k.Count(q))
	}
	return b.String()
}

func TestBulkRebuildMatchesDefaultDurabilityBuild(t *testing.T) {
	const files = 25
	def := bulkCorpus(t, files)
	def.Parse()
	want := indexDump(t, def)
	if !strings.Contains(want, "fts_word MATCH 'registry' -> ") || strings.Contains(want, "'registry' -> 0\n") {
		t.Fatalf("the fixture does not exercise FTS:\n%s", want)
	}

	bulk := bulkCorpus(t, files)
	// Restart as an offline bulk rebuild, with a budget that spans many files.
	bulk.StoreOptions = store.Options{Rebuild: true, BulkRebuild: true, BulkRecords: 100}
	bulk.Reopen()
	bulk.Parse()
	if err := bulk.Store.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if got := indexDump(t, bulk); got != want {
		t.Fatalf("a bulk rebuild differs from a default build\n--- bulk\n%s\n--- default\n%s", got, want)
	}
	// The finished file opens normally and a second parse has nothing to do.
	bulk.StoreOptions = store.Options{}
	bulk.Reopen()
	seq := bulk.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`)
	bulk.Parse()
	if got := bulk.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`); got != seq {
		t.Fatalf("a finished bulk rebuild still had work (%d -> %d)", seq, got)
	}
}

func TestBulkRebuildCommitsFarFewerTimesPerRecord(t *testing.T) {
	const files = 60 // 11 records each
	def := bulkCorpus(t, files)
	def.Parse()
	records := def.Count(`SELECT COUNT(*) FROM records`)
	defCommits := def.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`)

	bulk := bulkCorpus(t, files)
	bulk.StoreOptions = store.Options{Rebuild: true, BulkRebuild: true, BulkRecords: 200}
	bulk.Reopen()
	bulk.Parse()
	bulkCommits := bulk.Store.BulkCommits()
	if err := bulk.Store.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if bulk.Count(`SELECT COUNT(*) FROM records`) != records {
		t.Fatal("record counts differ")
	}
	// 60 files commit 60 times by default; a 200-record budget (about 18
	// files) commits 4 times: 3 budget commits and the final flush.
	if defCommits != files || bulkCommits < 1 || bulkCommits*10 > defCommits {
		t.Fatalf("default build %d commits, bulk rebuild %d commits for %d records", defCommits, bulkCommits, records)
	}
	t.Logf("commits per record: default %.4f, bulk %.4f (%.0fx fewer)",
		float64(defCommits)/float64(records), float64(bulkCommits)/float64(records), float64(defCommits)/float64(bulkCommits))
}

// schemaAndPlans is the sorted sqlite_master (type, name, tbl_name, sql) and the
// query plan of one query per secondary index, so two builds compare equal only
// when they have the same objects and the planner picks the same indexes.
func schemaAndPlans(t *testing.T, k *testkit.Kit) string {
	t.Helper()
	db := k.Store.Read()
	var out []string
	rows, err := db.Query(`SELECT type||' '||name||' '||tbl_name||' '||COALESCE(sql,'') FROM sqlite_master`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	_ = rows.Close()
	sort.Strings(out)
	for _, q := range []string{
		`SELECT id FROM records WHERE session='s'`,
		`SELECT id FROM records WHERE time_ns>5`,
		`SELECT id FROM records WHERE type='text'`,
		`SELECT id FROM records WHERE file_id=1 AND gen=1 AND off=0 AND item=0`,
		`SELECT rec_id FROM fields WHERE name_id=1 AND n>3`,
		`SELECT id FROM texts WHERE rec_id=1`,
	} {
		prows, err := db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		for prows.Next() {
			var id, parent, notused int
			var detail string
			if err := prows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			out = append(out, "plan "+q+" -> "+detail)
		}
		_ = prows.Close()
	}
	return strings.Join(out, "\n")
}

func TestDeferredIndexRebuildHasTheSchemaAndPlansOfAnInlineBuild(t *testing.T) {
	def := bulkCorpus(t, 10)
	def.Parse()
	want := schemaAndPlans(t, def)
	for _, n := range []string{"records_ident", "records_session", "records_time", "records_type", "fields_name_n", "texts_rec"} {
		if !strings.Contains(want, "index "+n+" ") {
			t.Fatalf("the inline build lacks %s:\n%s", n, want)
		}
	}

	bulk := bulkCorpus(t, 10)
	bulk.StoreOptions = store.Options{Rebuild: true, BulkRebuild: true, BulkRecords: 50}
	bulk.Reopen()
	bulk.Parse()
	if n := bulk.Count(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('records_ident','fields_name_n','texts_rec')`); n != 0 {
		t.Fatalf("%d secondary indexes exist before the rebuild finishes; they must be deferred", n)
	}
	if err := bulk.Store.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if got := schemaAndPlans(t, bulk); got != want {
		t.Fatalf("a deferred-index rebuild differs from an inline build\n--- deferred\n%s\n--- inline\n%s", got, want)
	}
}

// A bulk rebuild fills the per-key aggregate, equal to the GROUP BY over its rows.
func TestBulkRebuildFillsUnknownKeysTotal(t *testing.T) {
	bulk := bulkCorpus(t, 5)
	bulk.StoreOptions = store.Options{Rebuild: true, BulkRebuild: true, BulkRecords: 100}
	bulk.Reopen()
	bulk.Parse()
	if err := bulk.Store.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if got := requireSummaryMatchesGroupBy(t, bulk); len(got) == 0 {
		t.Fatal("the fixture has no unknown keys, so the aggregate is untested")
	}
}
