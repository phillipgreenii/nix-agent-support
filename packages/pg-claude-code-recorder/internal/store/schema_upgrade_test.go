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
	defer s.Close()
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

	s, err := Open(dir, Options{Rebuild: true})
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
		if s.SchemaVersion() != 2 {
			t.Fatalf("schema_version %d, the session tables must not change it", s.SchemaVersion())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
