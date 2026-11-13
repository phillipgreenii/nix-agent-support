//go:build soak

package parser_test

// The sizing measurement is a one-off investigation tool, not a unit test (V-4):
// it writes a synthetic transcript tree through the real parser and store and
// reports bytes per table and index from the dbstat virtual table, plus a few
// what-if variants (a partial numeric index, other FTS options). It runs only
// under the soak tag and only when PG_CCREC_SIZING_TURNS is set, and it is not
// named TestSoak*, so the soak check does not pick it up. Reproduction:
// packages/pg-claude-code-recorder/docs/investigations/2026-11-03-index-db-size.md.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudetranscript"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

type sizer struct {
	r     *rand.Rand
	vocab []string
	zipf  *rand.Zipf
}

func newSizer(seed int64) *sizer {
	r := rand.New(rand.NewSource(seed))
	syl := []string{"ra", "ko", "min", "tal", "ver", "sto", "pel", "an", "dor", "lin", "ex", "ca", "ter", "ni", "fo", "gra", "sy", "bu", "mo", "qui", "re", "la", "ton", "ch", "ph"}
	v := make([]string, 20000)
	for i := range v {
		n := 1 + r.Intn(4)
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteString(syl[r.Intn(len(syl))])
		}
		v[i] = b.String()
	}
	return &sizer{r: r, vocab: v, zipf: rand.NewZipf(r, 1.15, 4, uint64(len(v)-1))}
}

func (s *sizer) word() string { return s.vocab[s.zipf.Uint64()] }

func (s *sizer) hex(n int) string {
	const h = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = h[s.r.Intn(16)]
	}
	return string(b)
}

// size draws a log-normal byte count with the given median and sigma.
func (s *sizer) size(median, sigma float64) int {
	return int(math.Exp(math.Log(median) + sigma*s.r.NormFloat64()))
}

func (s *sizer) prose(n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(s.word())
		if s.r.Intn(12) == 0 {
			b.WriteString(". ")
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// code returns tool-output-like text: paths, identifiers, numbers, hashes.
func (s *sizer) code(n int) string {
	var b strings.Builder
	for b.Len() < n {
		switch s.r.Intn(6) {
		case 0:
			fmt.Fprintf(&b, "/example/work/%s/%s/%s.go:%d:\n", s.word(), s.word(), s.word(), 1+s.r.Intn(900))
		case 1:
			fmt.Fprintf(&b, "    func %s%s(%s string) error {\n", strings.ToUpper(s.word()[:1]), s.word(), s.word())
		case 2:
			fmt.Fprintf(&b, "commit %s %s\n", s.hex(40), s.prose(40))
		case 3:
			fmt.Fprintf(&b, "%d %d %s\n", s.r.Intn(100000), s.r.Intn(1000), s.word())
		default:
			b.WriteString(s.prose(60 + s.r.Intn(80)))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

type sline map[string]any

func ts(i int) string {
	return time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC).Add(time.Duration(i) * 1500 * time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func (s *sizer) common(typ, sess string, seq int, parent string) sline {
	uu := fmt.Sprintf("%s-%06d", sess, seq)
	return sline{
		"type": typ, "uuid": uu, "parentUuid": parent, "sessionId": sess, "timestamp": ts(seq),
		"cwd": "/example/work/project-a", "isSidechain": false, "gitBranch": "feature/example-branch",
		"version": "2.1.0", "entrypoint": "cli", "userType": "external", "slug": "example-slug-" + sess[len(sess)-1:],
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// session returns one transcript of turns tool-use cycles. Real Claude Code
// writes each assistant content block as its own line sharing one message id,
// and a user line per tool_result.
func (s *sizer) session(sess string, turns int) string {
	var out strings.Builder
	seq := 0
	parent := ""
	next := func(typ string) sline {
		seq++
		l := s.common(typ, sess, seq, parent)
		parent = l["uuid"].(string)
		return l
	}
	// first prompt
	u := next("user")
	u["permissionMode"] = "default"
	u["promptId"] = "p-" + s.hex(12)
	u["message"] = sline{"role": "user", "content": s.prose(s.size(200, 0.8))}
	out.WriteString(mustJSON(u))
	for t := 0; t < turns; t++ {
		msgID := "msg_" + s.hex(20)
		usage := func() sline {
			return sline{
				"input_tokens": 3 + s.r.Intn(8), "output_tokens": 20 + s.r.Intn(900),
				"cache_creation_input_tokens": s.r.Intn(20000), "cache_read_input_tokens": s.r.Intn(150000),
				"cache_creation": sline{"ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": s.r.Intn(20000)},
				"service_tier":   "standard", "inference_geo": "global", "speed": "standard",
			}
		}
		asst := func(block sline) sline {
			a := next("assistant")
			a["requestId"] = "req_" + s.hex(20)
			a["message"] = sline{
				"id": msgID, "type": "message", "role": "assistant", "model": "example-model-a",
				"stop_reason": "tool_use", "usage": usage(), "content": []any{block},
			}
			return a
		}
		if s.r.Intn(4) != 0 {
			out.WriteString(mustJSON(asst(sline{"type": "text", "text": s.prose(s.size(350, 0.9))})))
		}
		tuID := "toolu_" + s.hex(22)
		input := sline{"command": s.code(s.size(120, 0.8)), "description": s.prose(40)}
		out.WriteString(mustJSON(asst(sline{"type": "tool_use", "id": tuID, "name": []string{"Bash", "Read", "Edit", "Grep"}[s.r.Intn(4)], "input": input})))
		// progress noise lines (hooks)
		if s.r.Intn(2) == 0 {
			out.WriteString(mustJSON(sline{"type": "file-history-snapshot", "messageId": "m-" + s.hex(8), "snapshot": sline{"x": 1}}))
		}
		for p := s.r.Intn(5); p > 0; p-- {
			pl := next("progress")
			pl["toolUseID"] = tuID
			pl["parentToolUseID"] = tuID
			pl["data"] = sline{"type": "hook_progress", "hookEvent": "PostToolUse"}
			out.WriteString(mustJSON(pl))
		}
		ur := next("user")
		ur["promptId"] = "p-" + s.hex(12)
		ur["message"] = sline{"role": "user", "content": []any{sline{
			"type": "tool_result", "tool_use_id": tuID, "is_error": s.r.Intn(15) == 0,
			"content": s.code(s.size(1100, 1.4)),
		}}}
		out.WriteString(mustJSON(ur))
	}
	return out.String()
}

type objSize struct {
	name            string
	pages, bytes    int64
	payload, unused int64
	rows            int64
}

func dbstatObjects(t *testing.T, k *testkit.Kit) []objSize {
	t.Helper()
	rows, err := k.Store.Read().QueryContext(context.Background(), `SELECT name, COUNT(*), SUM(pgsize), SUM(payload), SUM(unused), SUM(ncell) FROM dbstat GROUP BY name ORDER BY SUM(pgsize) DESC`)
	if err != nil {
		t.Fatalf("dbstat not available: %v", err)
	}
	defer rows.Close()
	var out []objSize
	for rows.Next() {
		var o objSize
		if err := rows.Scan(&o.name, &o.pages, &o.bytes, &o.payload, &o.unused, &o.rows); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

func TestSizingFixture(t *testing.T) {
	turnsEnv := os.Getenv("PG_CCREC_SIZING_TURNS")
	if turnsEnv == "" {
		t.Skip("set PG_CCREC_SIZING_TURNS to run the sizing measurement")
	}
	turns, _ := strconv.Atoi(turnsEnv)
	perSession := 60
	k := testkit.New(t)
	z := newSizer(42)
	var inBytes int64
	var opaque []string
	src := claudetranscript.New()
	files := 0
	for done := 0; done < turns; done += perSession {
		n := perSession
		if turns-done < n {
			n = turns - done
		}
		name := fmt.Sprintf("example-session-%04d", files)
		body := z.session(name, n)
		inBytes += int64(len(body))
		k.Add("example-proj", name+".jsonl", body)
		files++
		for _, ln := range strings.Split(strings.TrimSpace(body), "\n") {
			recs, err := src.Parse(source.Captures{"project": "example-proj", "session": name}, []byte(ln))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range recs {
				for _, f := range r.Fields {
					if f.Kind == source.KindOpaque {
						opaque = append(opaque, f.S)
					}
				}
			}
		}
	}
	start := time.Now()
	k.Parse()
	secs := time.Since(start).Seconds()

	q := func(s string) int64 { return int64(k.Count(s)) }
	records, fields, texts := q(`SELECT COUNT(*) FROM records`), q(`SELECT COUNT(*) FROM fields`), q(`SELECT COUNT(*) FROM texts`)
	fmt.Printf("SIZING turns=%d files=%d input_bytes=%d parse_seconds=%.2f\n", turns, files, inBytes, secs)
	fmt.Printf("SIZING records=%d fields=%d texts=%d fields_per_record=%.2f texts_per_record=%.3f opaque_texts=%d\n",
		records, fields, texts, float64(fields)/float64(records), float64(texts)/float64(records), len(opaque))
	var wordBytes, triBytes int64
	_ = k.Store.Read().QueryRowContext(context.Background(), `SELECT COALESCE(SUM(length(n.name)+length(COALESCE(f.s,''))),0) FROM fields f JOIN field_names n ON n.id=f.name_id`).Scan(&fields)
	for _, b := range opaque {
		triBytes += int64(len(b))
	}
	fmt.Printf("SIZING opaque_text_bytes=%d avg_opaque_bytes=%.0f\n", triBytes, float64(triBytes)/float64(len(opaque)))
	_ = wordBytes

	// Field-name census.
	rows, err := k.Store.Read().QueryContext(context.Background(), `SELECT n.name, COUNT(*), SUM(length(n.name)), SUM(length(COALESCE(f.s,''))) FROM fields f JOIN field_names n ON n.id=f.name_id GROUP BY n.name ORDER BY COUNT(*) DESC`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var nm string
		var c, ln, ls int64
		_ = rows.Scan(&nm, &c, &ln, &ls)
		fmt.Printf("FIELDNAME %s rows=%d name_bytes=%d s_bytes=%d\n", nm, c, ln, ls)
	}
	rows.Close()
	fmt.Printf("SIZING dup_of_record_cols rows=%d\n", q(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name IN ('type','session_id','timestamp')`))

	// What-if variants, built in the same database so dbstat sizes them alike.
	err = k.Store.WithWrite("", func(tx *sql.Tx) error {
		stmts := []string{
			`CREATE VIRTUAL TABLE alt_tri_nocolsize USING fts5(body, content='', tokenize="trigram case_sensitive 1", columnsize=0)`,
			`CREATE VIRTUAL TABLE alt_tri_detailcol USING fts5(body, content='', contentless_delete=1, tokenize="trigram case_sensitive 1", detail=column)`,
			`CREATE VIRTUAL TABLE alt_tri_nocase USING fts5(body, content='', contentless_delete=1, tokenize="trigram")`,
			`CREATE VIRTUAL TABLE alt_word_opaque USING fts5(body, content='', contentless_delete=1, tokenize='unicode61')`,
			`CREATE VIRTUAL TABLE alt_word_opaque_detailnone USING fts5(body, content='', tokenize='unicode61', detail=none, columnsize=0)`,
			`CREATE INDEX alt_fields_partial_name_n ON fields(name_id, n) WHERE n IS NOT NULL`,
		}
		for _, s := range stmts {
			if _, err := tx.ExecContext(context.Background(), s); err != nil {
				return fmt.Errorf("%s: %w", s, err)
			}
		}
		for i, b := range opaque {
			for _, tbl := range []string{"alt_tri_nocolsize", "alt_tri_detailcol", "alt_tri_nocase", "alt_word_opaque", "alt_word_opaque_detailnone"} {
				if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+tbl+`(rowid, body) VALUES(?,?)`, i+1, b); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Does a quoted substring query still work with detail=column?
	for _, tbl := range []string{"fts_tri", "alt_tri_detailcol"} {
		var n int
		err := k.Store.Read().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+tbl+` WHERE `+tbl+` MATCH '"example/work"'`).Scan(&n)
		fmt.Printf("SIZING substring_query table=%s hits=%d err=%v\n", tbl, n, err)
	}
	var pageCount, pageSize int64
	_ = k.Store.Read().QueryRowContext(context.Background(), `PRAGMA page_count`).Scan(&pageCount)
	_ = k.Store.Read().QueryRowContext(context.Background(), `PRAGMA page_size`).Scan(&pageSize)
	fmt.Printf("SIZING page_size=%d page_count=%d\n", pageSize, pageCount)
	objs := dbstatObjects(t, k)
	sort.Slice(objs, func(i, j int) bool { return objs[i].bytes > objs[j].bytes })
	for _, o := range objs {
		fmt.Printf("OBJECT %s pages=%d bytes=%d payload=%d unused=%d cells=%d\n", o.name, o.pages, o.bytes, o.payload, o.unused, o.rows)
	}
}
