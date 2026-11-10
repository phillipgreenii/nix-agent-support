package store

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// dumpDB renders the store (user_version, every DDL statement, every row of
// every table) as text, so a test can assert that a refused write left the
// file byte-for-byte equivalent.
func dumpDB(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	fmt.Fprintf(&b, "user_version=%d\n", v)

	rows, err := s.sql.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	var tables []string
	for rows.Next() {
		var typ, name, tbl, ddl string
		if err := rows.Scan(&typ, &name, &tbl, &ddl); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		fmt.Fprintf(&b, "%s %s on %s: %s\n", typ, name, tbl, ddl)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	_ = rows.Close()

	for _, table := range tables {
		cols := tableColumns(t, s, table)
		order := make([]string, len(cols))
		for i := range cols {
			order[i] = fmt.Sprint(i + 1)
		}
		rs, err := s.sql.Query(fmt.Sprintf(`SELECT * FROM %q ORDER BY %s`, table, strings.Join(order, ", ")))
		if err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
		fmt.Fprintf(&b, "-- %s\n", table)
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			for _, val := range vals {
				if bs, ok := val.([]byte); ok {
					val = string(bs)
				}
				fmt.Fprintf(&b, "%T:%v|", val, val)
			}
			b.WriteString("\n")
		}
		if err := rs.Err(); err != nil {
			t.Fatalf("iterate %s: %v", table, err)
		}
		_ = rs.Close()
	}
	return b.String()
}

// columnInfo is one row of PRAGMA table_info.
type columnInfo struct {
	Name    string
	Type    string
	NotNull bool
	Default sql.NullString
	PK      int
}

func tableInfo(t *testing.T, s *Store, table string) []columnInfo {
	t.Helper()
	rows, err := s.sql.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []columnInfo
	for rows.Next() {
		var cid int
		var c columnInfo
		var notnull int
		if err := rows.Scan(&cid, &c.Name, &c.Type, &notnull, &c.Default, &c.PK); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		c.NotNull = notnull != 0
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return out
}

func tableColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	var names []string
	for _, c := range tableInfo(t, s, table) {
		names = append(names, c.Name)
	}
	return names
}
