package store

import (
	"fmt"
	"strings"
	"testing"
)

// schemaShape renders the structure of every table and index of the store
// (columns in order with type, not-null, default and primary-key position,
// foreign keys, indexes, user_version, meta.schema_version) as text, so a
// test can compare a store's shape with a committed golden.
func schemaShape(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	var uv int
	if err := s.sql.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil {
		t.Fatal(err)
	}
	var mv string
	if err := s.sql.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&mv); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "user_version=%d\nmeta.schema_version=%s\n", uv, mv)
	var tables []string
	rows, err := s.sql.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	_ = rows.Close()
	for _, tb := range tables {
		fmt.Fprintf(&b, "\ntable %s\n", tb)
		cr, err := s.sql.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, '<none>'), pk FROM pragma_table_info(?) ORDER BY name`, tb)
		if err != nil {
			t.Fatal(err)
		}
		for cr.Next() {
			var nn, pk int
			var name, ty, dflt string
			if err := cr.Scan(&name, &ty, &nn, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "  col %s %s notnull=%d default=%s pk=%d\n", name, ty, nn, dflt, pk)
		}
		_ = cr.Close()
		fr, err := s.sql.Query(`SELECT "table", "from", "to" FROM pragma_foreign_key_list(?) ORDER BY id, seq`, tb)
		if err != nil {
			t.Fatal(err)
		}
		for fr.Next() {
			var rt, from, to string
			if err := fr.Scan(&rt, &from, &to); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "  fk %s -> %s.%s\n", from, rt, to)
		}
		_ = fr.Close()
		ir, err := s.sql.Query(`SELECT name, "unique", origin FROM pragma_index_list(?) ORDER BY name`, tb)
		if err != nil {
			t.Fatal(err)
		}
		type idx struct {
			name, origin string
			uniq         int
		}
		var idxs []idx
		for ir.Next() {
			var x idx
			if err := ir.Scan(&x.name, &x.uniq, &x.origin); err != nil {
				t.Fatal(err)
			}
			idxs = append(idxs, x)
		}
		_ = ir.Close()
		for _, x := range idxs {
			name := x.name
			if strings.HasPrefix(name, "sqlite_autoindex_") {
				name = "<auto>"
			}
			var cols []string
			xr, err := s.sql.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, x.name)
			if err != nil {
				t.Fatal(err)
			}
			for xr.Next() {
				var c string
				if err := xr.Scan(&c); err != nil {
					t.Fatal(err)
				}
				cols = append(cols, c)
			}
			_ = xr.Close()
			fmt.Fprintf(&b, "  index %s unique=%d origin=%s (%s)\n", name, x.uniq, x.origin, strings.Join(cols, ","))
		}
	}
	return b.String()
}
