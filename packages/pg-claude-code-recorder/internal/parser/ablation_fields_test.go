//go:build ablation

package parser_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

type abFRow struct {
	rec, name int64
	s, n, f   any
}

const abFieldsDDL = `CREATE TABLE fields(
  rec_id INTEGER NOT NULL,
  name_id INTEGER NOT NULL,
  s TEXT,
  n INTEGER,
  f REAL,
  PRIMARY KEY(rec_id, name_id)
) WITHOUT ROWID`

const abUpsert = ` ON CONFLICT(rec_id,name_id) DO UPDATE SET s=excluded.s, n=excluded.n, f=excluded.f`

func abMultiSQL(n int) string {
	var b strings.Builder
	b.WriteString(`INSERT INTO fields(rec_id,name_id,s,n,f) VALUES `)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`(?,?,?,?,?)`)
	}
	b.WriteString(abUpsert)
	return b.String()
}

func abArgs(rs []abFRow, dst []any) []any {
	dst = dst[:0]
	for _, r := range rs {
		dst = append(dst, r.rec, r.name, r.s, r.n, r.f)
	}
	return dst
}

// abFieldsBatching is V7, a harness-local copy of the fields insert: the rows
// every non-searchable field of the corpus would produce (derivable fields are
// not filtered out, so it is an upper bound on the real row count), inserted
// into a table with the production DDL and the bulk pragmas, in transactions of
// 50,000 records, three ways: one prepared statement per row (production), one
// multi-row statement per record, one multi-row statement per 500 rows.
func abFieldsBatching(t *testing.T, files []abFile, work, out, run string) {
	src := abTranscriptSource(t)
	var rows []abFRow
	var recStart []int // index of the first row of each record
	names := map[string]int64{}
	var rec int64
	for i, f := range files {
		buf, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		caps, _ := src.Match(abRelPath(i))
		abSplit(buf[:min(int64(len(buf)), f.size)], func(line []byte) {
			rs, err := src.Parse(caps, line)
			if err != nil {
				return
			}
			for _, r := range rs {
				rec++
				recStart = append(recStart, len(rows))
				for _, fl := range r.Fields {
					if fl.Kind.Searchable() {
						continue
					}
					id, ok := names[fl.Name]
					if !ok {
						id = int64(len(names) + 1)
						names[fl.Name] = id
					}
					row := abFRow{rec: rec, name: id}
					switch fl.Kind {
					case source.KindID, source.KindKeyword, source.KindPath, source.KindSession:
						row.s = fl.S
					case source.KindFloat:
						row.f = fl.F
					default:
						row.n = fl.N
					}
					rows = append(rows, row)
				}
			}
		})
	}
	recStart = append(recStart, len(rows))
	nrec := len(recStart) - 1

	const recsPerTx = 50_000
	modes := []string{"A_per_row_prepared", "B_multi_row_per_record", "C_multi_row_500"}
	if n, _ := strconv.Atoi(run); n%2 == 0 {
		modes = []string{modes[2], modes[1], modes[0]}
	}
	for _, mode := range modes {
		dir := filepath.Join(work, "V7."+run+"."+mode)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "f.db")+"?_pragma=journal_mode(WAL)&_pragma=synchronous(OFF)&_pragma=cache_size(-262144)&_pragma=wal_autocheckpoint(262144)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.ExecContext(context.Background(), abFieldsDDL); err != nil {
			t.Fatal(err)
		}
		probe := abStart(t, "", filepath.Join(out, "V7."+mode+"."+run+".cpu.pb.gz"))
		t0 := time.Now()
		var stmts int64
		buf := make([]any, 0, 5*600)
		for r0 := 0; r0 < nrec; r0 += recsPerTx {
			r1 := min(r0+recsPerTx, nrec)
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "A_per_row_prepared":
				st, err := tx.PrepareContext(context.Background(), `INSERT INTO fields(rec_id,name_id,s,n,f) VALUES(?,?,?,?,?)`+abUpsert)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows[recStart[r0]:recStart[r1]] {
					if _, err := st.ExecContext(context.Background(), r.rec, r.name, r.s, r.n, r.f); err != nil {
						t.Fatal(err)
					}
					stmts++
				}
				_ = st.Close()
			case "B_multi_row_per_record":
				cache := map[int]*sql.Stmt{}
				for r := r0; r < r1; r++ {
					rs := rows[recStart[r]:recStart[r+1]]
					if len(rs) == 0 {
						continue
					}
					st := cache[len(rs)]
					if st == nil {
						if st, err = tx.PrepareContext(context.Background(), abMultiSQL(len(rs))); err != nil {
							t.Fatal(err)
						}
						cache[len(rs)] = st
					}
					buf = abArgs(rs, buf)
					if _, err := st.ExecContext(context.Background(), buf...); err != nil {
						t.Fatal(err)
					}
					stmts++
				}
				for _, st := range cache {
					_ = st.Close()
				}
			case "C_multi_row_500":
				all := rows[recStart[r0]:recStart[r1]]
				full, err := tx.PrepareContext(context.Background(), abMultiSQL(500))
				if err != nil {
					t.Fatal(err)
				}
				for len(all) > 0 {
					n := min(500, len(all))
					buf = abArgs(all[:n], buf)
					if n == 500 {
						_, err = full.ExecContext(context.Background(), buf...)
					} else {
						_, err = tx.ExecContext(context.Background(), abMultiSQL(n), buf...)
					}
					if err != nil {
						t.Fatal(err)
					}
					stmts++
					all = all[n:]
				}
				_ = full.Close()
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		sec := time.Since(t0).Seconds()
		var res abResult
		probe.end(&res)
		var cnt int64
		_ = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM fields`).Scan(&cnt)
		_ = db.Close()
		res.Variant, res.Run, res.Files = "V7", run, len(files)
		res.Records, res.ParseSec, res.TotalSec = int64(nrec), sec, sec
		res.RecPerSec = float64(nrec) / sec
		res.Rows = map[string]int64{"fields": cnt}
		res.Extra = map[string]any{"mode": mode, "rows_inserted": len(rows), "statements_executed": stmts, "rows_per_sec": float64(len(rows)) / sec}
		abEmit(t, out, res)
		_ = os.RemoveAll(dir)
	}
}
