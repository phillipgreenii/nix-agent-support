package store

import (
	"strings"
	"testing"
)

func TestExtensionLayoutIsOptInAndSticks(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.HasExt() || hasTable(t, s, "ext_assistant_usage") {
		t.Fatal("the running layout gained an extension table")
	}
	_ = s.Close()
	d2 := t.TempDir()
	s, err = Open(d2, Options{ExtensionTables: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasExt() || !hasTable(t, s, "ext_assistant_usage") {
		t.Fatal("the new layout has no ext_assistant_usage")
	}
	_ = s.Close()
	s, err = Open(d2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if !s.HasExt() {
		t.Fatal("a reopen without the option lost the extension tables")
	}
}

func TestExtensionTablesFollowTheConvention(t *testing.T) {
	s, err := Open(t.TempDir(), Options{ExtensionTables: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, tb := range ExtTables() {
		var sql string
		if err := s.Read().QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, tb.Table()).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tb.Table(), "ext_") || !strings.Contains(sql, "rec_id INTEGER PRIMARY KEY") || !strings.HasSuffix(strings.TrimSpace(sql), "STRICT") {
			t.Errorf("%s breaks the convention: %s", tb.Table(), sql)
		}
		for _, c := range tb.Columns {
			if strings.Contains(c.Name, " ") || strings.Contains(sql, c.Name+" "+c.Type+" NOT NULL") {
				t.Errorf("column %s must be nullable", c.Name)
			}
		}
	}
	tb, ok := LookupExt("assistant_usage")
	if !ok || len(tb.Columns) != 9 {
		t.Fatalf("assistant_usage: %v %d", ok, len(tb.Columns))
	}
}
