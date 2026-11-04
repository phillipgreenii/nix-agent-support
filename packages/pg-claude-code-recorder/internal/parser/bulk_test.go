package parser_test

import (
	"fmt"
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
