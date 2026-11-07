package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyIndex writes a minimal schema_version 1 index.db (fields keyed by name
// text, as before field names were interned) holding one field row.
func legacyIndex(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, DBName)
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','1')`,
		`CREATE TABLE fields(rec_id INTEGER NOT NULL, name TEXT NOT NULL, s TEXT, n INTEGER, f REAL)`,
		`CREATE INDEX fields_rec ON fields(rec_id, name)`,
		`INSERT INTO fields(rec_id,name,s) VALUES(1,'cwd','/example/work')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Fold the WAL into the file so a byte comparison sees every page.
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func sum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestOpenReplacesFullFieldsIndexWithPartialOne(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	for _, q := range []string{`DROP INDEX fields_name_n`, `CREATE INDEX fields_name_n ON fields(name_id, n)`} {
		if _, err := s.wdb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	defer func() { _ = s.Close() }()
	var def string
	if err := s.Read().QueryRow(`SELECT sql FROM sqlite_master WHERE name='fields_name_n'`).Scan(&def); err != nil || !strings.Contains(def, "WHERE n IS NOT NULL") {
		t.Fatalf("a full fields_name_n survived open: %q, %v", def, err)
	}
	if s.SchemaVersion() != SchemaVersion {
		t.Fatalf("schema_version %d", s.SchemaVersion())
	}
}

// A v2 store written before records_source_type existed gets the index at the
// next open, and an open of a store that has it changes nothing.
func TestOpenCreatesTheSourceTypeIndexOnAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := s.wdb.Exec(`DROP INDEX records_source_type`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s = open(t, dir)
		var def string
		if err := s.Read().QueryRow(`SELECT sql FROM sqlite_master WHERE name='records_source_type'`).Scan(&def); err != nil || !strings.Contains(def, "records(source, type)") {
			t.Fatalf("open %d: records_source_type is %q, %v", i, def, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOlderSchemaIsRefusedUntouchedAndRebuildKeepsTheOldFile(t *testing.T) {
	dir := t.TempDir()
	path := legacyIndex(t, dir)
	before := sum(t, path)

	_, err := Open(dir, Options{})
	var older *SchemaOlderError
	if !errors.As(err, &older) || older.Found != 1 || older.Known != SchemaVersion {
		t.Fatalf("Open of a version 1 index: %v, want a SchemaOlderError", err)
	}
	if sum(t, path) != before {
		t.Fatal("refusing the older schema changed index.db")
	}
	if _, err := os.Stat(path + ".corrupt"); err == nil {
		t.Fatal("an older schema was treated as corruption")
	}

	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion() != SchemaVersion {
		t.Fatalf("rebuilt schema_version %d, want %d", s.SchemaVersion(), SchemaVersion)
	}
	var n int
	if err := s.Read().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('fields') WHERE name='name_id'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rebuilt fields has no name_id column (%d, %v)", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if sum(t, path+".pre-rebuild") != before {
		t.Fatal("the old index was not kept intact as index.db.pre-rebuild")
	}
}

// An index.db written before unknown_keys_total existed has the aggregate
// backfilled once, correctly, at the next open; later opens leave it alone.
func TestOpenBackfillsUnknownKeysTotalOnceForAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	for _, q := range []string{
		`INSERT INTO unknown_keys(file_id,gen,source,rtype,path,count) VALUES(1,1,'example','user','a',2),(2,1,'example','user','a',3),(2,1,'example','tool','b',1)`,
		`DROP TABLE unknown_keys_total`,
	} {
		if _, err := s.wdb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s = open(t, dir)
		rows, err := s.Read().Query(`SELECT source,rtype,path,count FROM unknown_keys_total ORDER BY rtype,path`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var a, b, c string
			var n int
			if err := rows.Scan(&a, &b, &c, &n); err != nil {
				t.Fatal(err)
			}
			got = append(got, strings.Join([]string{a, b, c}, "|")+"="+string(rune('0'+n)))
		}
		_ = rows.Close()
		if strings.Join(got, ",") != "example|tool|b=1,example|user|a=5" {
			t.Fatalf("open %d: aggregate %v", i, got)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A v2 store written before the session tables existed gets them, and the
// session_ref column on records, at the next open; its older rows keep
// session_ref 0 and a second open changes nothing.
func TestOpenAddsTheSessionTablesAndColumnToAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	for _, q := range []string{
		`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns) VALUES(1,1,1,0,-1,1,'claude-transcript','user',1,'example-session-1',5)`,
		`DROP INDEX records_session_ref_source_time`,
		`DELETE FROM meta WHERE key='session_ref_complete'`,
		`ALTER TABLE records DROP COLUMN session_ref`,
		`DROP TABLE session_files`, `DROP TABLE session_stats`, `DROP TABLE sessions`,
	} {
		if _, err := s.wdb.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s = open(t, dir)
		var ref, tables int
		if err := s.Read().QueryRow(`SELECT session_ref FROM records WHERE id=1`).Scan(&ref); err != nil || ref != 0 {
			t.Fatalf("open %d: old row session_ref %d, %v", i, ref, err)
		}
		if err := s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('sessions','session_stats','session_files')`).Scan(&tables); err != nil || tables != 3 {
			t.Fatalf("open %d: %d session tables, %v", i, tables, err)
		}
		if s.SessionRefReady() {
			t.Fatalf("open %d: a store with unreferenced older rows is marked ready", i)
		}
		if s.SchemaVersion() != SchemaVersion {
			t.Fatalf("schema_version %d, the session tables must not change it", s.SchemaVersion())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func hasIndex(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// A database created empty by this code is marked as carrying session_ref on
// every row and has the session_ref index from the start (the table is empty, so
// building it costs nothing); the mark survives a reopen.
func TestFreshStoreIsMarkedSessionRefReadyWithItsIndex(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		s := open(t, dir)
		if !s.SessionRefReady() {
			t.Fatalf("open %d: a store created empty is not marked ready", i)
		}
		if !hasIndex(t, s, "records_session_ref_source_time") {
			t.Fatalf("open %d: no session_ref index", i)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A database that already had a records table without the mark (rows written
// before session_ref existed) is never marked at open and never gets the index:
// building it over the live table would be a full pass for an index no query
// may use until a rebuild.
func TestExistingStoreWithoutTheMarkStaysOnTheTextSessionAndBuildsNoIndex(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	for _, q := range []string{
		`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns,session_ref) VALUES(1,1,1,0,-1,1,'claude-transcript','user',1,'example-session-1',5,0)`,
		`DELETE FROM meta WHERE key='session_ref_complete'`,
		`DROP INDEX records_session_ref_source_time`,
	} {
		if _, err := s.wdb.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s = open(t, dir)
		if s.SessionRefReady() {
			t.Fatalf("open %d: an older store is marked ready", i)
		}
		if hasIndex(t, s, "records_session_ref_source_time") {
			t.Fatalf("open %d: the session_ref index was built over the live table", i)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A bulk rebuild is a new file: it is marked, and its session_ref index is built
// with the other deferred indexes at the end of the load.
func TestBulkRebuildIsMarkedAndBuildsTheSessionRefIndexAfterTheLoad(t *testing.T) {
	dir := seededDir(t)
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.SessionRefReady() {
		t.Fatal("a rebuilt store is not marked ready")
	}
	if hasIndex(t, s, "records_session_ref_source_time") {
		t.Fatal("the session_ref index exists before the load: it must be deferred")
	}
	if err := s.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if !hasIndex(t, s, "records_session_ref_source_time") {
		t.Fatal("FinishRebuild did not build the session_ref index")
	}
	_ = s.Close()
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if !s2.SessionRefReady() {
		t.Fatal("the mark did not survive the rebuild")
	}
}

// A v2 store written before the lookup ids existed gets the five records
// columns and the lookup tables at the next open; older rows keep NULL, a
// second open changes nothing and the schema version stays 2.
func TestOpenAddsTheLookupColumnsAndTablesToAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	for _, q := range []string{
		`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns) VALUES(1,1,1,0,-1,1,'claude-transcript','user',1,'example-session-1',5)`,
		`ALTER TABLE records DROP COLUMN cwd_id`, `ALTER TABLE records DROP COLUMN branch_id`, `ALTER TABLE records DROP COLUMN version_id`,
		`ALTER TABLE records DROP COLUMN entrypoint_id`, `ALTER TABLE records DROP COLUMN user_type_id`,
		`DROP TABLE lk_cwd`, `DROP TABLE lk_branch`, `DROP TABLE lk_version`, `DROP TABLE lk_entrypoint`, `DROP TABLE lk_user_type`,
	} {
		if _, err := s.wdb.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s = open(t, dir)
		var nulls, cols, tables int
		if err := s.Read().QueryRow(`SELECT COUNT(*) FROM records WHERE id=1 AND cwd_id IS NULL AND branch_id IS NULL AND version_id IS NULL AND entrypoint_id IS NULL AND user_type_id IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
			t.Fatalf("open %d: old row lookup ids not NULL: %d, %v", i, nulls, err)
		}
		if err := s.Read().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name IN ('cwd_id','branch_id','version_id','entrypoint_id','user_type_id')`).Scan(&cols); err != nil || cols != 5 {
			t.Fatalf("open %d: %d lookup columns, %v", i, cols, err)
		}
		if err := s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('lk_cwd','lk_branch','lk_version','lk_entrypoint','lk_user_type')`).Scan(&tables); err != nil || tables != 5 {
			t.Fatalf("open %d: %d lookup tables, %v", i, tables, err)
		}
		if s.SchemaVersion() != SchemaVersion {
			t.Fatalf("schema_version %d, the lookup tables must not change it", s.SchemaVersion())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// files.last_copy_ns is the time of the file's newest copy commit, maintained
// by applying the journal: a replay of the journal yields the same value (the
// Dump carries it in the files table), a commit applied late with an older time
// does not lower it, and a snapshot or tombstone does not touch it.
func TestLastCopyNSIsDerivedFromTheJournalAndSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := s.NewFileID()
	if err := s.Commit(
		&Rec{T: TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 2, Birth: 3},
		&Rec{T: TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
		&Rec{T: TCommit, At: 500, File: id, Gen: 1, From: 0, To: 5, Size: 5, Tail: 1, TailLen: 1},
		&Rec{T: TCommit, At: 300, File: id, Gen: 1, From: 5, To: 9, Size: 9, Tail: 1, TailLen: 1},
		&Rec{T: TTomb, At: 900, File: id, Path: "/c/x"},
	); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := s.Read().QueryRow(`SELECT last_copy_ns FROM files WHERE id=?`, id).Scan(&got); err != nil || got != 500 {
		t.Fatalf("last_copy_ns %d, %v; want 500", got, err)
	}
	dump, err := s.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dump, "|500|") {
		t.Fatalf("doctor dump lacks last_copy_ns:\n%s", dump)
	}
	replay, err := ReplayDump(filepath.Join(dir, JournalName))
	if err != nil || replay != dump {
		t.Fatalf("replay differs: %v\n%s\n---\n%s", err, replay, dump)
	}
	var m map[int64]int64
	err = s.ReadTx(func(rd *Reader) (e error) {
		m, e = rd.LastSeenByFile([]int64{id})
		return e
	})
	if err != nil || m[id] != 900 {
		t.Fatalf("LastSeenByFile %v, %v; want the tombstone 900", m, err)
	}
	_ = s.Close()
}

// A database that predates files.last_copy_ns gets the column at the next open,
// filled once from the newest commit of each file (a file with none keeps 0);
// the schema version does not change and a second open changes nothing, even
// with the commits table gone.
func TestOpenAddsLastCopyColumnAndBackfillsItFromCommits(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	a, b := s.NewFileID(), s.NewFileID()
	if err := s.Commit(
		&Rec{T: TFile, At: 10, File: a, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 2, Birth: 3},
		&Rec{T: TGen, At: 11, File: a, Gen: 1, Method: "bytes", Reason: "first"},
		&Rec{T: TCommit, At: 40, File: a, Gen: 1, From: 0, To: 5, Size: 5, Tail: 1, TailLen: 1},
		&Rec{T: TCommit, At: 70, File: a, Gen: 1, From: 5, To: 9, Size: 9, Tail: 1, TailLen: 1},
		&Rec{T: TFile, At: 10, File: b, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 3, Birth: 3},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.Exec(`ALTER TABLE files DROP COLUMN last_copy_ns`); err != nil {
		t.Fatal(err)
	}
	// An old database still holds the commits table the code no longer writes.
	if _, err := s.wdb.Exec(`CREATE TABLE commits(file_id INTEGER NOT NULL, gen INTEGER NOT NULL, seq INTEGER NOT NULL, from_off INTEGER NOT NULL, to_off INTEGER NOT NULL, seen_ns INTEGER NOT NULL, PRIMARY KEY(file_id, gen, seq));
INSERT INTO commits VALUES(?,1,1,0,5,40),(?,1,2,5,9,70)`, a, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s = open(t, dir)
		if i == 1 {
			if _, err := s.wdb.Exec(`DROP TABLE commits`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = open(t, dir)
		}
		var ga, gb int64
		if err := s.Read().QueryRow(`SELECT last_copy_ns FROM files WHERE id=?`, a).Scan(&ga); err != nil || ga != 70 {
			t.Fatalf("open %d: file a last_copy_ns %d, %v; want 70", i, ga, err)
		}
		if err := s.Read().QueryRow(`SELECT last_copy_ns FROM files WHERE id=?`, b).Scan(&gb); err != nil || gb != 0 {
			t.Fatalf("open %d: file b last_copy_ns %d, %v; want 0", i, gb, err)
		}
		if s.SchemaVersion() != SchemaVersion {
			t.Fatalf("schema_version %d, the column must not change it", s.SchemaVersion())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func commitsTableCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='commits'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A journal that holds commit records does not create the commits table, and
// replaying it reproduces the index (the doctor's journal-versus-index check
// compares the two dumps).
func TestJournalCommitRecordsDoNotCreateTheCommitsTable(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := s.NewFileID()
	if err := s.Commit(
		&Rec{T: TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 2, Birth: 3},
		&Rec{T: TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
		&Rec{T: TCommit, At: 40, File: id, Gen: 1, From: 0, To: 5, Size: 5, Tail: 1, TailLen: 1},
		&Rec{T: TCommit, At: 70, File: id, Gen: 1, From: 5, To: 9, Size: 9, Tail: 1, TailLen: 1},
	); err != nil {
		t.Fatal(err)
	}
	if n := commitsTableCount(t, s); n != 0 {
		t.Fatal("applying commit records created the commits table")
	}
	var off int64
	if err := s.Read().QueryRow(`SELECT copy_offset FROM generations WHERE file_id=?`, id).Scan(&off); err != nil || off != 9 {
		t.Fatalf("copy_offset %d, %v; want 9", off, err)
	}
	dump, err := s.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, "commits") {
		t.Fatalf("dump lists the commits table:\n%s", dump)
	}
	replay, err := ReplayDump(filepath.Join(dir, JournalName))
	if err != nil || replay != dump {
		t.Fatalf("replay differs: %v\n%s\n---\n%s", err, replay, dump)
	}
	_ = s.Close()
}

// A database that still holds the commits table (from before it was removed)
// keeps working: Open does not drop it, new commits do not write to it, Dump
// and the counts ignore it, so the journal comparison still passes.
func TestExistingCommitsTableIsLeftAloneAndIgnored(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := s.NewFileID()
	if err := s.Commit(
		&Rec{T: TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 2, Birth: 3},
		&Rec{T: TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.Exec(`CREATE TABLE commits(file_id INTEGER NOT NULL, gen INTEGER NOT NULL, seq INTEGER NOT NULL, from_off INTEGER NOT NULL, to_off INTEGER NOT NULL, seen_ns INTEGER NOT NULL, PRIMARY KEY(file_id, gen, seq));
INSERT INTO commits VALUES(?,1,1,0,5,40)`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	defer func() { _ = s.Close() }()
	if commitsTableCount(t, s) != 1 {
		t.Fatal("Open dropped the existing commits table")
	}
	if err := s.Commit(&Rec{T: TCommit, At: 70, File: id, Gen: 1, From: 0, To: 9, Size: 9, Tail: 1, TailLen: 1}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Read().QueryRow(`SELECT COUNT(*) FROM commits`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("commits rows %d, %v; want the one old row untouched", n, err)
	}
	var counts map[string]int
	if err := s.ReadTx(func(rd *Reader) (e error) {
		counts, e = rd.CountObservations()
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := counts["commits"]; ok {
		t.Fatalf("CountObservations lists commits: %v", counts)
	}
	dump, err := s.Dump()
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ReplayDump(filepath.Join(dir, JournalName))
	if err != nil || replay != dump {
		t.Fatalf("replay differs: %v\n%s\n---\n%s", err, replay, dump)
	}
}
