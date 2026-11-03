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
