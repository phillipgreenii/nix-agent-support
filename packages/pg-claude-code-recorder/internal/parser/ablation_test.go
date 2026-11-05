//go:build ablation

package parser_test

// Opt-in insert-cost ablation harness (V-4: build tag ablation, never part of
// go test ./...). It builds a throwaway rebuild over a bounded corpus of real
// transcript files (symlinked read-only, never modified) and measures one
// variant per process, so each profile and each GC state is clean.
//
//	go test -c -tags ablation -o ablation.test ./internal/parser/
//	PG_CCREC_ABLATION_LIST=corpus.txt PG_CCREC_ABLATION_WORK=/tmp/x \
//	  PG_CCREC_ABLATION_OUT=out PG_CCREC_ABLATION_VARIANT=V1 PG_CCREC_ABLATION_RUN=1 \
//	  ./ablation.test -test.run TestAblation -test.v
//
// PG_CCREC_ABLATION_LIST holds one "<size> <path>" line per transcript file.
// Variants are described in docs/ablation/README.md.

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

type abFile struct {
	size int64
	path string
}

func abLoadList(t *testing.T, list string) []abFile {
	t.Helper()
	fh, err := os.Open(list)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	var out []abFile
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[0], 10, 64)
		out = append(out, abFile{n, f[1]})
	}
	return out
}

func abTranscriptSource(t *testing.T) source.Source {
	t.Helper()
	for _, s := range sources.All() {
		if s.Name() == "claude-transcript" {
			return s
		}
	}
	t.Fatal("no claude-transcript source")
	return nil
}

// abRelPath is the path a corpus file pretends to have under the Claude dir.
func abRelPath(i int) string {
	return fmt.Sprintf("projects/proj%d/sess%d.jsonl", i%40, i)
}

// abSplit calls fn for each non-blank line of buf (up to its last newline).
func abSplit(buf []byte, fn func(line []byte)) {
	for pos := 0; pos < len(buf); {
		e := bytes.IndexByte(buf[pos:], '\n')
		if e < 0 {
			return
		}
		line := buf[pos : pos+e]
		pos += e + 1
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		fn(line)
	}
}

// abResult is one JSON line of results.jsonl.
type abResult struct {
	Variant      string           `json:"variant"`
	Run          string           `json:"run"`
	Files        int              `json:"files"`
	CorpusBytes  int64            `json:"corpus_bytes"`
	Records      int64            `json:"records"`
	ParseSec     float64          `json:"parse_all_sec"`
	FinishSec    float64          `json:"finish_sec"`
	TotalSec     float64          `json:"total_sec"`
	RecPerSec    float64          `json:"rec_per_sec_total"`
	RecPerSecAll float64          `json:"rec_per_sec_parseall"`
	UserSec      float64          `json:"cpu_user_sec"`
	SysSec       float64          `json:"cpu_sys_sec"`
	NumGC        uint32           `json:"num_gc"`
	AllocMiB     float64          `json:"alloc_mib"`
	DBBytes      int64            `json:"db_bytes"`
	WALFinal     int64            `json:"wal_final_bytes"`
	WALPeak      int64            `json:"wal_peak_bytes"`
	Rows         map[string]int64 `json:"rows,omitempty"`
	Profile      any              `json:"rebuild_profile,omitempty"`
	Extra        map[string]any   `json:"extra,omitempty"`
}

func abCPU() (user, sys float64) {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6, float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
}

type abProbe struct {
	stop      chan struct{}
	done      chan struct{}
	walPeak   atomic.Int64
	startUser float64
	startSys  float64
	startMem  runtime.MemStats
	t0        time.Time
	profFile  *os.File
}

func abStart(t *testing.T, walPath, profPath string) *abProbe {
	t.Helper()
	p := &abProbe{stop: make(chan struct{}), done: make(chan struct{})}
	if profPath != "" {
		f, err := os.Create(profPath)
		if err != nil {
			t.Fatal(err)
		}
		p.profFile = f
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		defer close(p.done)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-tick.C:
				if walPath == "" {
					continue
				}
				if st, err := os.Stat(walPath); err == nil && st.Size() > p.walPeak.Load() {
					p.walPeak.Store(st.Size())
				}
			}
		}
	}()
	runtime.ReadMemStats(&p.startMem)
	p.startUser, p.startSys = abCPU()
	p.t0 = time.Now()
	return p
}

func (p *abProbe) end(r *abResult) {
	u, s := abCPU()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if p.profFile != nil {
		pprof.StopCPUProfile()
		_ = p.profFile.Close()
	}
	close(p.stop)
	<-p.done
	r.UserSec, r.SysSec = u-p.startUser, s-p.startSys
	r.NumGC = m.NumGC - p.startMem.NumGC
	r.AllocMiB = float64(m.TotalAlloc-p.startMem.TotalAlloc) / (1 << 20)
	r.WALPeak = p.walPeak.Load()
}

type abVariant struct {
	name string
	// journal is a journal_mode override ("" keeps WAL).
	journal     string
	dsnExtra    string // extra DSN pragmas for every connection
	noSavepoint bool
	noPrepare   bool
	setup       []string // DDL run on the empty index before the parse
	dropFTS     bool     // replace fts_word with a plain table
}

const abIgnore = `CREATE TRIGGER spike_ign_%s BEFORE INSERT ON %s BEGIN SELECT RAISE(IGNORE); END`

func abIgnoreDDL(tables ...string) []string {
	var out []string
	for _, tb := range tables {
		out = append(out, fmt.Sprintf(abIgnore, tb, tb))
	}
	return out
}

func abVariants() map[string]abVariant {
	return map[string]abVariant{
		"V1":  {name: "V1"},
		"V2":  {name: "V2", dropFTS: true},
		"V2b": {name: "V2b", dropFTS: true, setup: abIgnoreDDL("texts")},
		"V3":  {name: "V3", setup: abIgnoreDDL("fields")},
		"V4":  {name: "V4", noSavepoint: true},
		"V5":  {name: "V5", journal: "MEMORY"},
		"V5b": {name: "V5b", journal: "OFF"},
		"V6":  {name: "V6", noPrepare: true},
		// V9 keeps the savepoint but asks for temp_store=MEMORY (does the
		// sub-journal stay in memory without changing journal_mode?).
		"V9": {name: "V9", dsnExtra: "&_pragma=temp_store(2)"},
		// V8 is the statement-call floor: every row insert is ignored by a
		// trigger, so the parse, the statement dispatch and the lookups remain
		// but no table or index page is written for them.
		"V8": {name: "V8", dropFTS: true, setup: abIgnoreDDL("records", "texts", "fields")},
	}
}

func abCountRows(path string) map[string]int64 {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND sql NOT LIKE 'CREATE VIRTUAL%'`)
	if err != nil {
		return nil
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	_ = rows.Close()
	out := map[string]int64{}
	for _, n := range names {
		if strings.HasSuffix(n, "_config") || strings.HasSuffix(n, "_data") || strings.HasSuffix(n, "_idx") {
			continue
		}
		var c int64
		if db.QueryRow(`SELECT COUNT(*) FROM "`+n+`"`).Scan(&c) == nil && c > 0 {
			out[n] = c
		}
	}
	return out
}

func abEmit(t *testing.T, out string, r abResult) {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(out, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
	t.Logf("%s", b)
}

// abBuildState writes the journal of a state directory that holds one append
// file per corpus file, whose copy is a symlink to the live copy (read-only).
func abBuildState(t *testing.T, dir string, files []abFile) {
	t.Helper()
	st, err := store.Open(dir, store.Options{V3Layout: true, Obs: obs.New(nil)})
	if err != nil {
		t.Fatal(err)
	}
	at := int64(1_700_000_000_000_000_000)
	next := func() int64 { at += 1000; return at }
	for i, f := range files {
		id := st.NewFileID()
		path := filepath.Join("/claude", abRelPath(i))
		if err := st.Commit(
			&store.Rec{T: store.TFile, At: next(), File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(id)},
			&store.Rec{T: store.TPath, At: next(), File: id, Path: path},
			&store.Rec{T: store.TGen, At: next(), File: id, Gen: 1, Method: "bytes", Reason: "first"},
		); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(f.path, st.CopyPath(id, 1)); err != nil {
			t.Fatal(err)
		}
		if err := st.Commit(&store.Rec{T: store.TCommit, At: next(), File: id, Gen: 1, From: 0, To: f.size, Size: f.size, Tail: 1, TailLen: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAblation(t *testing.T) {
	list := os.Getenv("PG_CCREC_ABLATION_LIST")
	if list == "" {
		t.Skip("PG_CCREC_ABLATION_LIST not set")
	}
	work, out := os.Getenv("PG_CCREC_ABLATION_WORK"), os.Getenv("PG_CCREC_ABLATION_OUT")
	vname, run := os.Getenv("PG_CCREC_ABLATION_VARIANT"), os.Getenv("PG_CCREC_ABLATION_RUN")
	if work == "" || out == "" || vname == "" {
		t.Fatal("PG_CCREC_ABLATION_WORK, _OUT and _VARIANT are required")
	}
	files := abLoadList(t, list)
	var corpusBytes int64
	for _, f := range files {
		corpusBytes += f.size
	}
	res := abResult{Variant: vname, Run: run, Files: len(files), CorpusBytes: corpusBytes}
	prof := filepath.Join(out, vname+"."+run+".cpu.pb.gz")

	switch vname {
	case "V0":
		abParseOnly(t, files, prof, out, &res)
		return
	case "V7":
		abFieldsBatching(t, files, work, out, run)
		return
	}
	v, ok := abVariants()[vname]
	if !ok {
		t.Fatalf("unknown variant %q", vname)
	}
	dir := filepath.Join(work, vname+"."+run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	abBuildState(t, dir, files)

	store.SetAblation(v.journal, v.dsnExtra, v.noSavepoint)
	parser.SetAblationNoPrepare(v.noPrepare)
	o := obs.New(nil)
	st, err := store.Open(dir, store.Options{V3Layout: true, Obs: o, Rebuild: true, BulkRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "index.db")
	if v.dropFTS || len(v.setup) > 0 {
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		var ddl []string
		if v.dropFTS {
			ddl = append(ddl,
				`DROP TABLE fts_word`,
				`CREATE TABLE fts_word(body, fts_word, rank)`)
			ddl = append(ddl, abIgnoreDDL("fts_word")...)
		}
		ddl = append(ddl, v.setup...)
		for _, q := range ddl {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		_ = db.Close()
	}
	p := parser.New(parser.Config{Store: st, Sources: sources.All(), ClaudeDir: "/claude", Obs: o})

	probe := abStart(t, dbPath+"-wal", prof)
	t0 := time.Now()
	if err := p.ParseAll(); err != nil {
		t.Fatal(err)
	}
	res.ParseSec = time.Since(t0).Seconds()
	t1 := time.Now()
	if err := st.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	res.FinishSec = time.Since(t1).Seconds()
	res.TotalSec = time.Since(t0).Seconds()
	probe.end(&res)
	res.Records, _ = st.BulkTotals()
	res.Profile = st.RebuildProfile()
	res.RecPerSec = float64(res.Records) / res.TotalSec
	res.RecPerSecAll = float64(res.Records) / res.ParseSec
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dbPath); err == nil {
		res.DBBytes = fi.Size()
	}
	if fi, err := os.Stat(dbPath + "-wal"); err == nil {
		res.WALFinal = fi.Size()
	}
	res.Rows = abCountRows(dbPath)
	abEmit(t, out, res)
}

// abParseOnly is V0: Source.Parse over the corpus and no database.
func abParseOnly(t *testing.T, files []abFile, prof, out string, res *abResult) {
	src := abTranscriptSource(t)
	probe := abStart(t, "", prof)
	t0 := time.Now()
	var recs, fields, lines int64
	for i, f := range files {
		buf, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(buf)) > f.size {
			buf = buf[:f.size]
		}
		caps, ok := src.Match(abRelPath(i))
		if !ok {
			t.Fatalf("no match for %s", abRelPath(i))
		}
		abSplit(buf, func(line []byte) {
			lines++
			rs, err := src.Parse(caps, line)
			if err != nil {
				return
			}
			recs += int64(len(rs))
			for _, r := range rs {
				fields += int64(len(r.Fields))
			}
		})
	}
	res.ParseSec = time.Since(t0).Seconds()
	res.TotalSec = res.ParseSec
	probe.end(res)
	res.Records = recs
	res.RecPerSec = float64(recs) / res.TotalSec
	res.RecPerSecAll = res.RecPerSec
	res.Extra = map[string]any{"lines": lines, "fields_total": fields}
	abEmit(t, out, *res)
}
