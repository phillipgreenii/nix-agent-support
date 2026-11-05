package parser_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// queryer is what both a store reader and a plain *sql.DB offer.
type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// digestQueries are the tables whose content the output-equivalence digest
// covers. Rows are keyed by the natural key (file_id, gen, off, item), and the
// allocation-order ids (records.id, texts.id, rec_id, name_id, session_ref and
// the lk_* ids) are joined to their values, so the digest does not move when
// ids are assigned in a different order. The rebuild timestamp, meta and
// parse_state are left out.
var digestQueries = []struct{ table, query string }{
	{"records", `
SELECT r.file_id, r.gen, r.off, r.item, r.len, r.source, r.type, r.parser_version, r.time_ns, r.own_time,
  COALESCE(s.session_id,''), COALESCE(s.project,''),
  COALESCE((SELECT v FROM lk_cwd WHERE id=r.cwd_id),''), COALESCE((SELECT v FROM lk_branch WHERE id=r.branch_id),''),
  COALESCE((SELECT v FROM lk_version WHERE id=r.version_id),''), COALESCE((SELECT v FROM lk_entrypoint WHERE id=r.entrypoint_id),''),
  COALESCE((SELECT v FROM lk_user_type WHERE id=r.user_type_id),'')
FROM records r LEFT JOIN sessions s ON s.session_ref=r.session_ref
ORDER BY r.file_id, r.gen, r.off, r.item`},
	{"fields", `
SELECT r.file_id, r.gen, r.off, r.item, fn.name, COALESCE(f.s,'<null>'), COALESCE(f.n,'<null>'), COALESCE(f.f,'<null>')
FROM fields f JOIN records r ON r.id=f.rec_id JOIN field_names fn ON fn.id=f.name_id
ORDER BY r.file_id, r.gen, r.off, r.item, fn.name`},
	{"texts", `
SELECT r.file_id, r.gen, r.off, r.item, t.field, t.kind
FROM texts t JOIN records r ON r.id=t.rec_id
ORDER BY r.file_id, r.gen, r.off, r.item, t.id`},
	{"ext_assistant_usage", `
SELECT r.file_id, r.gen, r.off, r.item, COALESCE(u.input_tokens,'<null>'), COALESCE(u.output_tokens,'<null>'),
  COALESCE(u.cache_creation_input_tokens,'<null>'), COALESCE(u.cache_read_input_tokens,'<null>'),
  COALESCE(u.cache_1h_input_tokens,'<null>'), COALESCE(u.cache_5m_input_tokens,'<null>'),
  COALESCE(u.service_tier,'<null>'), COALESCE(u.inference_geo,'<null>'), COALESCE(u.speed,'<null>')
FROM ext_assistant_usage u JOIN records r ON r.id=u.rec_id
ORDER BY r.file_id, r.gen, r.off, r.item`},
	{"unknown_keys", `SELECT file_id, gen, source, rtype, path, count FROM unknown_keys ORDER BY file_id, gen, source, rtype, path`},
	{"unknown_keys_total", `SELECT source, rtype, path, count FROM unknown_keys_total ORDER BY source, rtype, path`},
	{"bad_lines", `SELECT file_id, gen, off, len, reason FROM bad_lines ORDER BY file_id, gen, off`},
}

// tableDigests returns one "<table> <rows> <sha256>" line per digested table.
func tableDigests(tb testing.TB, q queryer) []string {
	tb.Helper()
	var out []string
	for _, d := range digestQueries {
		rows, err := q.Query(d.query)
		if err != nil {
			tb.Fatalf("%s: %v", d.table, err)
		}
		cols, _ := rows.Columns()
		h := sha256.New()
		n := 0
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				tb.Fatal(err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = fmt.Sprintf("%T:%v", v, v)
			}
			fmt.Fprintf(h, "%d\x1f%s\x1e", len(parts), strings.Join(parts, "\x1f"))
			n++
		}
		if err := rows.Err(); err != nil {
			tb.Fatal(err)
		}
		_ = rows.Close()
		out = append(out, fmt.Sprintf("%s %d %s", d.table, n, hex.EncodeToString(h.Sum(nil))))
	}
	return out
}
