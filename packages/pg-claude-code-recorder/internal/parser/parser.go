// Package parser is the recorder's parse stage. It reads a copy from its parse
// offset to the last newline within the copy offset and commits the records,
// their field indexes, their text indexes and the new parse offset in one
// SQLite transaction. A re-parse replaces a file-and-generation's records
// atomically, recording a reset marker before the new records.
package parser

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// Config configures a Parser.
type Config struct {
	Store     *store.Store
	Sources   []source.Source
	ClaudeDir string
	Obs       *obs.Obs
	Clock     clock.Clock
	// MaxLineBytes is parse.maxLineBytes: a longer line is a bad line.
	MaxLineBytes int
	// ChunkBytes bounds the bytes parsed per transaction (tests shrink it).
	ChunkBytes int
	// FeedRetention is feed.retention: the feed marks older than it (bar the
	// newest) are pruned. 14 days when zero.
	FeedRetention time.Duration
}

// Parser parses copies into records.
type Parser struct {
	cfg   Config
	bySrc map[string]source.Source

	run sync.Mutex // one parse at a time

	pmu     sync.Mutex
	pending map[int64]bool
	wake    chan struct{}
}

// New returns a Parser.
func New(cfg Config) *Parser {
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 64 << 20
	}
	if cfg.ChunkBytes <= 0 {
		cfg.ChunkBytes = 8 << 20
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.FeedRetention <= 0 {
		cfg.FeedRetention = 14 * 24 * time.Hour
	}
	if cfg.Obs == nil {
		cfg.Obs = obs.New(nil)
	}
	p := &Parser{cfg: cfg, bySrc: map[string]source.Source{}, pending: map[int64]bool{}, wake: make(chan struct{}, 1)}
	for _, s := range cfg.Sources {
		p.bySrc[s.Name()] = s
	}
	return p
}

// HasSource reports whether a parser exists for the source name.
func (p *Parser) HasSource(name string) bool { _, ok := p.bySrc[name]; return ok }

// Notify marks a file as having new bytes to parse and wakes the worker.
func (p *Parser) Notify(file int64) {
	p.pmu.Lock()
	p.pending[file] = true
	p.pmu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Wake returns the channel the worker waits on.
func (p *Parser) Wake() <-chan struct{} { return p.wake }

// Drain parses every pending file now.
func (p *Parser) Drain() error {
	p.pmu.Lock()
	ids := make([]int64, 0, len(p.pending))
	for id := range p.pending {
		ids = append(ids, id)
	}
	p.pending = map[int64]bool{}
	p.pmu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var first error
	for _, id := range ids {
		if _, err := p.ParseFile(id); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ParseAll parses every file with unparsed bytes and re-parses generations
// stored under an older parser version.
func (p *Parser) ParseAll() error {
	files, err := p.cfg.Store.AllFiles()
	if err != nil {
		return err
	}
	var first error
	for _, f := range files {
		if !p.HasSource(f.Source) {
			continue
		}
		if f.Mode == "snapshot" {
			if _, err := p.ParseFile(f.ID); err != nil && first == nil {
				first = err
			}
			continue
		}
		if f.Mode != "append" {
			continue
		}
		if err := p.reparseStale(f); err != nil && first == nil {
			first = err
		}
		if _, err := p.ParseFile(f.ID); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (p *Parser) reparseStale(f store.File) error {
	src := p.bySrc[f.Source]
	gens, err := p.cfg.Store.Generations(f.ID)
	if err != nil {
		return err
	}
	for _, g := range gens {
		if g.ParseOffset == 0 {
			continue
		}
		var v int
		err := p.cfg.Store.Read().QueryRow(`SELECT version FROM parse_state WHERE file_id=? AND gen=?`, f.ID, g.Gen).Scan(&v)
		if err == sql.ErrNoRows || (err == nil && v != src.Version()) {
			if err := p.ReparseGen(f.ID, g.Gen); err != nil {
				return err
			}
		}
	}
	return nil
}

// Reparse re-parses every file of a source (or only the named files).
func (p *Parser) Reparse(sourceName string, files ...int64) error {
	all, err := p.cfg.Store.AllFiles()
	if err != nil {
		return err
	}
	want := map[int64]bool{}
	for _, f := range files {
		want[f] = true
	}
	for _, f := range all {
		if f.Source != sourceName || (len(want) > 0 && !want[f.ID]) {
			continue
		}
		if f.Mode == "snapshot" {
			if err := p.reparseSnapshots(f); err != nil {
				return err
			}
			continue
		}
		gens, err := p.cfg.Store.Generations(f.ID)
		if err != nil {
			return err
		}
		for _, g := range gens {
			if err := p.ReparseGen(f.ID, g.Gen); err != nil {
				return err
			}
		}
	}
	return nil
}

// ParseFile parses the unparsed bytes of every generation of a file and
// returns the number of records committed.
func (p *Parser) ParseFile(file int64) (int, error) {
	p.run.Lock()
	defer p.run.Unlock()
	f, err := p.cfg.Store.File(file)
	if err != nil {
		return 0, err
	}
	src, ok := p.bySrc[f.Source]
	if ok && f.Mode == "snapshot" {
		return p.parseSnapshots(f, src, false)
	}
	if !ok || f.Mode != "append" {
		return 0, nil
	}
	gens, err := p.cfg.Store.Generations(file)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, g := range gens {
		if g.ParseOffset >= g.CopyOffset {
			continue
		}
		n, err := p.parseGen(f, src, g, false)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ReparseGen replaces the records of one file generation atomically under the
// current parser version.
func (p *Parser) ReparseGen(file int64, gen int) error {
	p.run.Lock()
	defer p.run.Unlock()
	f, err := p.cfg.Store.File(file)
	if err != nil {
		return err
	}
	src, ok := p.bySrc[f.Source]
	if !ok {
		return nil
	}
	g, err := p.cfg.Store.Generation(file, gen)
	if err != nil {
		return err
	}
	_, err = p.parseGen(f, src, g, true)
	return err
}

func (p *Parser) relPath(file int64) (string, bool) {
	paths, _ := p.cfg.Store.CurrentPaths(file)
	if len(paths) == 0 {
		paths, _ = p.cfg.Store.PathHistory(file)
	}
	for _, ap := range paths {
		if r, err := relTo(p.cfg.ClaudeDir, ap); err == nil {
			return r, true
		}
	}
	return "", false
}

func relTo(root, path string) (string, error) {
	root = strings.TrimRight(root, "/")
	if !strings.HasPrefix(path, root+"/") {
		return "", fmt.Errorf("outside")
	}
	return path[len(root)+1:], nil
}

type pendingRec struct {
	off int64
	ln  int
	rec source.Record
}

type badLine struct {
	off    int64
	ln     int
	reason string
}

// parseGen parses a generation from its parse offset (from 0 for a re-parse,
// which also replaces what was stored) to the last newline in the copy.
func (p *Parser) parseGen(f store.File, src source.Source, g store.Generation, reparse bool) (int, error) {
	rel, ok := p.relPath(f.ID)
	if !ok {
		return 0, nil
	}
	caps, ok := src.Match(rel)
	if !ok {
		return 0, nil
	}
	fh, err := p.cfg.Store.OpenPointer(f.ID, g.Gen)
	if err != nil {
		return 0, err
	}
	defer func() { _ = fh.Close() }()
	commits, _ := p.cfg.Store.CopyCommits(f.ID)

	start := g.ParseOffset
	if reparse {
		start = 0
	}
	end := g.CopyOffset
	count := 0

	run := func(rawTx *sql.Tx) error {
		tx := newPS(rawTx)
		defer tx.Close()
		if reparse {
			if err := p.clearGen(tx, f.ID, g.Gen); err != nil {
				return err
			}
			var next int64
			_ = tx.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='records'),0)+1`).Scan(&next)
			if _, err := tx.Exec(`INSERT INTO parse_resets(file_id,gen,parser,at_ns,next_rec) VALUES(?,?,?,?,?)`,
				f.ID, g.Gen, src.Name()+"@"+strconv.Itoa(src.Version()), p.cfg.Clock.Now().UnixNano(), next); err != nil {
				return err
			}
			p.cfg.Obs.Event("parser_reset", "file", strconv.FormatInt(f.ID, 10), "gen", strconv.Itoa(g.Gen), "source", src.Name())
		}
		off := start
		for off < end {
			n, err := p.chunk(tx, fh, f, src, caps, g, commits, off, end, &count)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			off += n
		}
		if _, err := tx.Exec(`UPDATE generations SET parse_offset=? WHERE file_id=? AND gen=? AND ?<=copy_offset`, off, f.ID, g.Gen, off); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO parse_state(file_id,gen,version) VALUES(?,?,?) ON CONFLICT(file_id,gen) DO UPDATE SET version=excluded.version`,
			f.ID, g.Gen, src.Version()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('parse_seq','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
			return err
		}
		return p.mark(tx)
	}
	// A normal parse commits a bounded run of lines per transaction; a re-parse
	// is one transaction so the new records appear together.
	if reparse {
		return count, p.cfg.Store.WithWrite("parse.before-commit", run)
	}
	for start < end {
		var advanced int64
		one := func(rawTx *sql.Tx) error {
			tx := newPS(rawTx)
			defer tx.Close()
			if err := p.genReset(tx, f, src, g, start); err != nil {
				return err
			}
			n, err := p.chunk(tx, fh, f, src, caps, g, commits, start, end, &count)
			if err != nil {
				return err
			}
			advanced = n
			if _, err := tx.Exec(`UPDATE generations SET parse_offset=? WHERE file_id=? AND gen=? AND ?<=copy_offset`, start+n, f.ID, g.Gen, start+n); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO parse_state(file_id,gen,version) VALUES(?,?,?) ON CONFLICT(file_id,gen) DO UPDATE SET version=excluded.version`,
				f.ID, g.Gen, src.Version()); err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('parse_seq','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
				return err
			}
			return p.mark(tx)
		}
		if err := p.cfg.Store.WithWrite("parse.before-commit", one); err != nil {
			return count, err
		}
		if advanced == 0 {
			break
		}
		start += advanced
	}
	return count, nil
}

func (p *Parser) clearGen(tx *pstx, file int64, gen int) error {
	stmts := []string{
		`DELETE FROM fts_word WHERE rowid IN (SELECT t.id FROM texts t JOIN records r ON r.id=t.rec_id WHERE r.file_id=?1 AND r.gen=?2 AND t.kind='text')`,
		`DELETE FROM fts_tri WHERE rowid IN (SELECT t.id FROM texts t JOIN records r ON r.id=t.rec_id WHERE r.file_id=?1 AND r.gen=?2 AND t.kind='opaque')`,
		`DELETE FROM texts WHERE rec_id IN (SELECT id FROM records WHERE file_id=?1 AND gen=?2)`,
		`DELETE FROM fields WHERE rec_id IN (SELECT id FROM records WHERE file_id=?1 AND gen=?2)`,
		`DELETE FROM records WHERE file_id=?1 AND gen=?2`,
		`DELETE FROM bad_lines WHERE file_id=?1 AND gen=?2`,
		`DELETE FROM unknown_keys WHERE file_id=?1 AND gen=?2`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s, file, gen); err != nil {
			return err
		}
	}
	return nil
}

// chunk parses lines from off, at most ChunkBytes of them, never past the last
// newline within end. It returns the bytes consumed (0 when only a torn line is
// left).
func (p *Parser) chunk(tx *pstx, fh *os.File, f store.File, src source.Source, caps source.Captures,
	g store.Generation, commits []store.CopyCommit, off, end int64, count *int,
) (int64, error) {
	size := int64(p.cfg.ChunkBytes)
	for {
		if off+size > end {
			size = end - off
		}
		buf := make([]byte, size)
		if _, err := fh.ReadAt(buf, off); err != nil && err != io.EOF {
			return 0, err
		}
		nl := bytes.LastIndexByte(buf, '\n')
		if nl < 0 {
			if off+size >= end || size >= int64(p.cfg.MaxLineBytes) {
				if size >= int64(p.cfg.MaxLineBytes) && off+size < end {
					// A line over maxLineBytes is a bad line; skip to its end.
					return p.skipLong(tx, fh, f, g, off, end)
				}
				return 0, nil // a torn line: it waits for its newline
			}
			size *= 2
			continue
		}
		buf = buf[:nl+1]
		consumed := int64(len(buf))
		pos := int64(0)
		for pos < consumed {
			e := bytes.IndexByte(buf[pos:], '\n')
			line := buf[pos : pos+int64(e)]
			abs := off + pos
			ll := e + 1
			pos += int64(ll)
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if len(line) > p.cfg.MaxLineBytes {
				if err := p.bad(tx, f, g, abs, ll, "line_too_long"); err != nil {
					return 0, err
				}
				continue
			}
			recs, err := src.Parse(caps, line)
			if err != nil {
				if err := p.bad(tx, f, g, abs, ll, err.Error()); err != nil {
					return 0, err
				}
				continue
			}
			seen := seenAt(commits, g.Gen, abs)
			if err := p.insert(tx, f, src, g, abs, ll, recs, seen); err != nil {
				return 0, err
			}
			*count += len(recs)
		}
		return consumed, nil
	}
}

func (p *Parser) skipLong(tx *pstx, fh *os.File, f store.File, g store.Generation, off, end int64) (int64, error) {
	buf := make([]byte, 1<<20)
	pos := off
	for pos < end {
		n := int64(len(buf))
		if pos+n > end {
			n = end - pos
		}
		m, _ := fh.ReadAt(buf[:n], pos)
		if i := bytes.IndexByte(buf[:m], '\n'); i >= 0 {
			total := pos + int64(i) + 1 - off
			return total, p.bad(tx, f, g, off, int(total), "line_too_long")
		}
		if m == 0 {
			break
		}
		pos += int64(m)
	}
	return 0, nil
}

func (p *Parser) bad(tx *pstx, f store.File, g store.Generation, off int64, ln int, reason string) error {
	if len(reason) > 200 {
		reason = reason[:200]
	}
	p.cfg.Obs.Reg.Add(MBadLines, nil, 1)
	_, err := tx.Exec(`INSERT OR REPLACE INTO bad_lines(file_id,gen,off,len,reason) VALUES(?,?,?,?,?)`, f.ID, g.Gen, off, ln, reason)
	return err
}

// seenAt returns the time the recorder saw the byte at off (the commit that
// covered it), or 0.
func seenAt(commits []store.CopyCommit, gen int, off int64) int64 {
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		if c.Gen == gen && c.From <= off && off < c.To {
			return c.SeenNS
		}
	}
	return 0
}

// Metric names the parser adds to the shared registry.
const (
	MBadLines      = "bad_lines_total"
	MUnknownKeys   = "unknown_keys_total"
	MParseLagSecs  = "parse_lag_seconds"
	MParseLagBytes = "parse_lag_bytes"
	MParseProgress = "parse_progress"
	MUnknownKinds  = "unknown_kinds"
)

func (p *Parser) insert(tx *pstx, f store.File, src source.Source, g store.Generation, off int64, ln int, recs []source.Record, seen int64) error {
	unk := map[[2]string]int{}
	for _, r := range recs {
		tns := seen
		if !r.Time.IsZero() {
			tns = r.Time.UnixNano()
		}
		res, err := tx.Exec(`INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			f.ID, g.Gen, off, r.Item, ln, src.Name(), r.Type, src.Version(), r.Session, tns)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		for _, fl := range r.Fields {
			if fl.Kind.Searchable() {
				if fl.S == "" {
					continue
				}
				tr, err := tx.Exec(`INSERT INTO texts(rec_id,field,kind) VALUES(?,?,?)`, id, fl.Name, string(fl.Kind))
				if err != nil {
					return err
				}
				tid, _ := tr.LastInsertId()
				tbl := "fts_word"
				if fl.Kind == source.KindOpaque {
					tbl = "fts_tri"
				}
				if _, err := tx.Exec(`INSERT INTO `+tbl+`(rowid, body) VALUES(?,?)`, tid, fl.S); err != nil {
					return err
				}
				continue
			}
			var s any
			var n any
			var fv any
			switch fl.Kind {
			case source.KindID, source.KindKeyword, source.KindPath, source.KindSession:
				s = fl.S
			case source.KindFloat:
				fv = fl.F
			default:
				n = fl.N
			}
			if _, err := tx.Exec(`INSERT INTO fields(rec_id,name,s,n,f) VALUES(?,?,?,?,?)`, id, fl.Name, s, n, fv); err != nil {
				return err
			}
		}
		for _, u := range r.Unknown {
			unk[[2]string{r.Type, u}]++
		}
	}
	for k, c := range unk {
		p.cfg.Obs.Reg.Add(MUnknownKeys, nil, float64(c))
		if _, err := tx.Exec(`INSERT INTO unknown_keys(file_id,gen,source,rtype,path,count) VALUES(?,?,?,?,?,?)
ON CONFLICT(file_id,gen,source,rtype,path) DO UPDATE SET count=count+excluded.count`, f.ID, g.Gen, src.Name(), k[0], k[1], c); err != nil {
			return err
		}
	}
	return nil
}

// UpdateGauges sets the parse-lag gauges, parse progress and the unknown-kinds
// gauge from the store.
func (p *Parser) UpdateGauges() {
	files, err := p.cfg.Store.AllFiles()
	if err != nil {
		return
	}
	gens, err := p.cfg.Store.AllGenerations()
	if err != nil {
		return
	}
	cur := map[int64]int{}
	kinds := map[string]bool{}
	for _, f := range files {
		cur[f.ID] = f.CurGen
		if !p.HasSource(f.Source) && f.State != "deleted" {
			kinds[f.Source] = true
		}
	}
	var lagBytes, copied, parsed int64
	var lagSecs float64
	now := p.cfg.Clock.Now().UnixNano()
	for _, g := range gens {
		f, ok := fileByID(files, g.File)
		if !ok || !p.HasSource(f.Source) {
			continue
		}
		copied += g.CopyOffset
		parsed += g.ParseOffset
		if lag := g.CopyOffset - g.ParseOffset; lag > 0 {
			if lag > lagBytes {
				lagBytes = lag
			}
			cc, _ := p.cfg.Store.CopyCommits(g.File)
			if s := seenAt(cc, g.Gen, g.ParseOffset); s > 0 {
				if d := float64(now-s) / 1e9; d > lagSecs {
					lagSecs = d
				}
			}
		}
	}
	prog := 1.0
	if copied > 0 {
		prog = float64(parsed) / float64(copied)
	}
	r := p.cfg.Obs.Reg
	r.Set(MParseLagBytes, nil, float64(lagBytes))
	r.Set(MParseLagSecs, nil, lagSecs)
	r.Set(MParseProgress, nil, prog)
	r.Set(MUnknownKinds, nil, float64(len(kinds)))
}

func fileByID(files []store.File, id int64) (store.File, bool) {
	i := sort.Search(len(files), func(i int) bool { return files[i].ID >= id })
	if i < len(files) && files[i].ID == id {
		return files[i], true
	}
	return store.File{}, false
}

// Summary is what coverage reads.
type Summary struct {
	BadLines    int
	UnknownKeys []UnknownKey
}

// UnknownKey is one distinct key with its occurrence count.
type UnknownKey struct {
	Source string `json:"source"`
	Type   string `json:"record_type"`
	Path   string `json:"path"`
	Count  int    `json:"count"`
}

// Summarize reads the bad-line count and the unknown keys (once per distinct
// key per source and record type, summed over files).
func Summarize(st *store.Store) (Summary, error) {
	var s Summary
	if err := st.Read().QueryRow(`SELECT COUNT(*) FROM bad_lines`).Scan(&s.BadLines); err != nil {
		return s, err
	}
	rows, err := st.Read().Query(`SELECT source,rtype,path,SUM(count) FROM unknown_keys GROUP BY source,rtype,path ORDER BY source,rtype,path`)
	if err != nil {
		return s, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var u UnknownKey
		if err := rows.Scan(&u.Source, &u.Type, &u.Path, &u.Count); err != nil {
			return s, err
		}
		s.UnknownKeys = append(s.UnknownKeys, u)
	}
	return s, rows.Err()
}

// BadLine is a bad line with its position.
type BadLine struct {
	File   int64
	Gen    int
	Offset int64
	Len    int
	Reason string
}

// BadLines lists the bad lines of a file.
func BadLines(st *store.Store, file int64) ([]BadLine, error) {
	rows, err := st.Read().Query(`SELECT file_id,gen,off,len,reason FROM bad_lines WHERE file_id=? ORDER BY gen,off`, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []BadLine
	for rows.Next() {
		var b BadLine
		if err := rows.Scan(&b.File, &b.Gen, &b.Offset, &b.Len, &b.Reason); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

var _ = time.Second

// Value returns the text of one field of a stored record, re-reading the line
// from the copy and parsing it again (the database holds no file contents).
func (p *Parser) Value(file int64, gen int, off int64, ln int, srcName string, item int, field string) (string, error) {
	src, ok := p.bySrc[srcName]
	if !ok {
		return "", fmt.Errorf("no parser for %s", srcName)
	}
	rel, ok := p.relPath(file)
	if !ok {
		return "", fmt.Errorf("file %d has no path under the Claude directory", file)
	}
	caps, ok := src.Match(rel)
	if !ok {
		return "", fmt.Errorf("file %d does not match %s", file, srcName)
	}
	fh, err := p.cfg.Store.OpenPointer(file, gen)
	if err != nil {
		return "", err
	}
	defer func() { _ = fh.Close() }()
	buf := make([]byte, ln)
	if _, err := fh.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", err
	}
	recs, err := src.Parse(caps, bytes.TrimRight(buf, "\n"))
	if err != nil {
		return "", err
	}
	for _, r := range recs {
		if r.Item != item {
			continue
		}
		for _, f := range r.Fields {
			if f.Name == field {
				return f.S, nil
			}
		}
	}
	return "", fmt.Errorf("field %s not found", field)
}

// Line returns the raw bytes of a stored line from the copy.
func (p *Parser) Line(file int64, gen int, off int64, ln int) ([]byte, error) {
	fh, err := p.cfg.Store.OpenPointer(file, gen)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	buf := make([]byte, ln)
	if _, err := fh.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil, err
	}
	return bytes.TrimRight(buf, "\n"), nil
}

// pstx is a transaction whose Exec statements are prepared once and reused
// (a parse inserts tens of thousands of rows per transaction).
type pstx struct {
	*sql.Tx
	st map[string]*sql.Stmt
}

func newPS(tx *sql.Tx) *pstx { return &pstx{Tx: tx, st: map[string]*sql.Stmt{}} }

// Exec prepares q on first use.
func (p *pstx) Exec(q string, args ...any) (sql.Result, error) {
	st, ok := p.st[q]
	if !ok {
		var err error
		if st, err = p.Tx.Prepare(q); err != nil {
			return nil, err
		}
		p.st[q] = st
	}
	return st.Exec(args...)
}

// Close releases the prepared statements.
func (p *pstx) Close() {
	for _, st := range p.st {
		_ = st.Close()
	}
}
