// Package parser is the recorder's parse stage. It reads a copy from its parse
// offset to the last newline within the copy offset and commits the records,
// their field indexes, their text indexes and the new parse offset in one
// SQLite transaction. A re-parse replaces a file-and-generation's records
// atomically, recording a reset marker before the new records.
package parser

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// EndsDir is the control mod's ends root, which lies outside the Claude
	// directory; its files are named "ends/<name>" to the sources. "" when none.
	EndsDir string
	Obs     *obs.Obs
	Clock   clock.Clock
	// MaxLineBytes is parse.maxLineBytes: a longer line is a bad line.
	MaxLineBytes int
	// ChunkBytes bounds the bytes parsed per transaction (tests shrink it).
	ChunkBytes int
	// BatchRecords and BatchBytes bound one group-commit transaction of a live
	// parse: it stops taking chunks once it holds this many records or parsed
	// bytes (50000 records and 16 MiB when zero), then commits and goes on.
	BatchRecords int
	BatchBytes   int64
	// GroupWindow is the quiet time Coalesce waits after a wake-up for more
	// files to become dirty (300 ms when zero); GroupMaxWait caps the whole wait
	// from the first wake-up (5 s when zero), which keeps the parse lag far
	// under the 60 s health threshold.
	GroupWindow  time.Duration
	GroupMaxWait time.Duration
	// FeedRetention is feed.retention: the feed marks older than it (bar the
	// newest) are pruned. 14 days when zero.
	FeedRetention time.Duration
}

// Parser parses copies into records.
type Parser struct {
	cfg   Config
	bySrc map[string]source.Source
	// ext routes declared fields into the extension tables (see ext.go); it is
	// used only when the store has them.
	ext map[string]map[string]extRoute

	run sync.Mutex // one parse at a time

	// slow keeps the slowest files of a ParseAll (steps.go).
	slow slowest
	// lineSeq counts the lines inserted, for the statement sampling.
	lineSeq int64

	// filesDone and filesTotal track ParseAll for a progress reader.
	filesDone, filesTotal atomic.Int64

	// drain serializes Drain callers, so a Drain that finds nothing pending
	// returns only after the Drain that took the pending files has finished them.
	drain sync.Mutex

	pmu     sync.Mutex
	pending map[int64]bool
	wake    chan struct{}

	// names interns field names for the parser's lifetime. It is touched only
	// inside a Store.WithWrite (the write lock), and is valid only while
	// namesEpoch equals Store.RollbackEpoch: any rolled-back write (a whole
	// transaction or one bulk savepoint) may have undone a name's row, and
	// field_names ids are reused after a rollback.
	names      map[string]int64
	namesEpoch uint64

	// sessRefs maps a session id to its sessions.session_ref, under the same
	// validity rule as names (see sessionRef).
	sessRefs  map[string]int64
	sessEpoch uint64

	// routes maps source name and field name to a lookup slot (see
	// lookupRoutes); lk interns each slot's values to ids, valid while lkEpoch
	// equals Store.RollbackEpoch (see lookupID).
	routes  map[string]map[string]int
	lk      [lkCount]map[string]int64
	lkEpoch uint64
}

// New returns a Parser.
func New(cfg Config) *Parser {
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 64 << 20
	}
	if cfg.ChunkBytes <= 0 {
		cfg.ChunkBytes = 8 << 20
	}
	if cfg.BatchRecords <= 0 {
		cfg.BatchRecords = 50_000
	}
	if cfg.BatchBytes <= 0 {
		cfg.BatchBytes = 16 << 20
	}
	if cfg.GroupWindow <= 0 {
		cfg.GroupWindow = 300 * time.Millisecond
	}
	if cfg.GroupMaxWait <= 0 {
		cfg.GroupMaxWait = 5 * time.Second
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
	p.routes = lookupRoutes(cfg.Sources)
	p.ext, _ = extRoutes(cfg.Sources)
	p.registerSignals()
	return p
}

// KnownRecordTypes are the record types that get their own value of the type
// label of unknown_keys_total; every other type is counted under "other", so
// the label stays bounded however many types the data invents.
var KnownRecordTypes = []string{
	"user", "assistant", "system", "summary", "progress", "attachment",
	"queue-operation", "file-history-snapshot",
	"text", "thinking", "redacted_thinking", "tool_use", "tool_result", "image",
	"history", "tool_result_body", "registry_deleted", "unknown",
}

// OtherType is the type label value of a record type not in KnownRecordTypes.
const OtherType = "other"

// TypeLabel maps a record type to its bounded label value.
func TypeLabel(t string) string {
	for _, k := range KnownRecordTypes {
		if k == t {
			return t
		}
	}
	return OtherType
}

// registerSignals registers every series of bad_lines_total{source} and
// unknown_keys_total{source,type} at zero for the configured sources, so each
// series exists from start and increase() sees a first burst.
func (p *Parser) registerSignals() {
	for _, s := range p.cfg.Sources {
		p.cfg.Obs.Reg.Add(MBadLines, map[string]string{"source": s.Name()}, 0)
		for _, t := range append(append([]string{}, KnownRecordTypes...), OtherType) {
			p.cfg.Obs.Reg.Add(MUnknownKeys, map[string]string{"source": s.Name(), "type": t}, 0)
		}
	}
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
	p.drain.Lock()
	defer p.drain.Unlock()
	p.pmu.Lock()
	ids := make([]int64, 0, len(p.pending))
	for id := range p.pending {
		ids = append(ids, id)
	}
	p.pending = map[int64]bool{}
	p.pmu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	_, err := p.parseFiles(ids)
	return err
}

// Coalesce is called after a wake-up and returns once the wake-ups have settled:
// no further Notify for GroupWindow, or GroupMaxWait since the first, whichever
// is first. A Drain after it then parses every file that became dirty in the
// window in one transaction. It returns false when stop closed first.
func (p *Parser) Coalesce(stop <-chan struct{}) bool {
	maxWait := time.NewTimer(p.cfg.GroupMaxWait)
	defer maxWait.Stop()
	quiet := time.NewTimer(p.cfg.GroupWindow)
	defer quiet.Stop()
	for {
		select {
		case <-stop:
			return false
		case <-maxWait.C:
			return true
		case <-quiet.C:
			return true
		case <-p.wake:
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(p.cfg.GroupWindow)
		}
	}
}

// ParseAll parses every file with unparsed bytes and re-parses generations
// stored under an older parser version.
//
// On a store opened for a bulk rebuild the writes of many files share one
// transaction that commits on a record or byte budget (see store.BulkBegin);
// otherwise each chunk of a file commits on its own.
func (p *Parser) ParseAll() error { return p.ParseAllUntil(nil) }

// ErrParseStopped is what ParseAllUntil returns when stop closed before every
// file was visited. The files not visited keep their old parse_state version,
// so the next start re-parses them.
var ErrParseStopped = errors.New("parse stopped before every file was visited")

// ParseAllUntil is ParseAll that gives up between files once stop closes, so a
// daemon being closed does not wait out a long startup re-parse (a stopped
// daemon once held the store writer for over two hours that way). A nil stop
// never fires.
func (p *Parser) ParseAllUntil(stop <-chan struct{}) (first error) {
	files, err := p.cfg.Store.AllFiles()
	if err != nil {
		return err
	}
	p.filesDone.Store(0)
	p.filesTotal.Store(int64(len(files)))
	p.cfg.Store.BulkBegin()
	p.slow.mu.Lock()
	p.slow.files = nil
	p.slow.mu.Unlock()
	timed := !ablateNoStepTimers && p.cfg.Store.StepTimers()
	now := p.cfg.Store.StepNow()
	var t0 time.Duration
	var before store.StepStats
	if timed {
		t0, before = now(), p.cfg.Store.Steps()
	}
	defer func() {
		p.filesDone.Store(int64(len(files)))
		if err := p.cfg.Store.BulkEnd(); err != nil && first == nil {
			first = err
		}
		if timed {
			// other is the remainder of the call: the wall time less the five
			// measured steps, so the six steps always sum to the call.
			d := p.cfg.Store.Steps().Sub(before)
			other := int64(now()-t0) - (d.StepNS[store.StepRead] + d.StepNS[store.StepParse] + d.StepNS[store.StepInsert] + d.StepNS[store.StepSavepoint] + d.StepNS[store.StepCommit])
			var o store.StepStats
			o.StepNS[store.StepOther], o.StepCalls[store.StepOther] = max(other, 0), 1
			p.cfg.Store.AddSteps(o)
		}
	}()
	for i, f := range files {
		select {
		case <-stop:
			if first == nil {
				first = ErrParseStopped
			}
			return first
		default:
		}
		p.filesDone.Store(int64(i))
		if !p.HasSource(f.Source) {
			continue
		}
		if f.Mode == "snapshot" {
			// Snapshot parsing reads back what it stored: commit the batch first.
			if err := p.cfg.Store.BulkFlush(); err != nil && first == nil {
				first = err
			}
			if _, err := p.parseLoaded(f); err != nil && first == nil {
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
		if _, err := p.parseLoaded(f); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// FileProgress reports how many files ParseAll has finished and how many it
// has to visit (both 0 before it runs).
func (p *Parser) FileProgress() (done, total int) {
	return int(p.filesDone.Load()), int(p.filesTotal.Load())
}

// reparseStale re-parses each parsed generation of f whose parse_state version
// differs from the source's. It reads parse_state through Store.Read(), which
// sees committed state only and is blind to the batch a bulk rebuild still has
// open; that is safe here because a generation's version row is written by the
// parse of that generation, never by the pass before it in the same file.
// ReparseGen advances ParseOffset, so a caller MUST re-read Generations after
// this returns: a Generation fetched before it is stale and would be parsed
// again from the old offset (in bulk mode a duplicate in records_ident,
// reported only at FinishRebuild).
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
func (p *Parser) ParseFile(file int64) (int, error) { return p.parseFiles([]int64{file}) }

// genWork is one generation with bytes to parse: everything a transaction needs
// to continue it from start.
type genWork struct {
	f     store.File
	src   source.Source
	caps  source.Captures
	g     store.Generation
	fh    *store.Pointer
	start int64
	end   int64
	// rel, start0, end0 and stepNS feed the per-file step figures: the path
	// (hashed, never printed), the bytes the call had to parse and the
	// nanoseconds of read, parse and insert it took.
	rel          string
	start0, end0 int64
	stepNS       int64
}

// parseFiles parses the unparsed bytes of every generation of the files, in one
// group-commit transaction per batch budget (a snapshot file keeps its own
// transactions), and returns the number of records committed.
func (p *Parser) parseFiles(ids []int64) (int, error) {
	p.run.Lock()
	defer p.run.Unlock()
	var files []store.File
	var first error
	for _, id := range ids {
		f, err := p.cfg.Store.File(id)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		files = append(files, f)
	}
	return p.parseStored(files, first)
}

// parseLoaded is ParseFile for a File the caller already holds (ParseAll got it
// from AllFiles), so the file is not fetched a second time.
func (p *Parser) parseLoaded(f store.File) (int, error) {
	p.run.Lock()
	defer p.run.Unlock()
	return p.parseStored([]store.File{f}, nil)
}

// parseStored is the body of parseFiles for files already fetched; first is an
// error from fetching them. The caller holds p.run. Generations are read here,
// after any ReparseGen the caller ran, and never cached across it.
func (p *Parser) parseStored(files []store.File, first error) (int, error) {
	var work []*genWork
	defer func() {
		for _, w := range work {
			_ = w.fh.Close()
		}
	}()
	total := 0
	for _, f := range files {
		id := f.ID
		src, ok := p.bySrc[f.Source]
		if ok && f.Mode == "snapshot" {
			n, err := p.parseSnapshots(f, src, false)
			total += n
			if err != nil && first == nil {
				first = err
			}
			continue
		}
		if !ok || f.Mode != "append" {
			continue
		}
		gens, err := p.cfg.Store.Generations(id)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		// The path and its source match depend on the file, not the generation:
		// resolve them once, at the first generation with bytes to parse.
		var rel string
		var caps source.Captures
		resolved, matched := false, false
		for _, g := range gens {
			if g.ParseOffset >= g.CopyOffset {
				continue
			}
			if !resolved {
				resolved = true
				if rel, matched = p.relPath(f.ID); matched {
					caps, matched = src.Match(rel)
				}
			}
			if !matched {
				continue
			}
			w, err := p.prepGen(f, src, g, rel, caps)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			work = append(work, w)
		}
	}
	n, err := p.runWork(work)
	for _, w := range work {
		p.noteFile(w)
	}
	total += n
	if err != nil && first == nil {
		first = err
	}
	return total, first
}

// prepGen opens a generation's copy for a live parse; rel and caps are the
// file's path under the Claude directory and its source match, resolved once
// per file by the caller.
func (p *Parser) prepGen(f store.File, src source.Source, g store.Generation, rel string, caps source.Captures) (*genWork, error) {
	fh, err := p.cfg.Store.OpenPointer(f.ID, g.Gen)
	if err != nil {
		return nil, err
	}
	return &genWork{
		f: f, src: src, caps: caps, g: g, fh: fh, start: g.ParseOffset, end: g.CopyOffset,
		rel: rel, start0: g.ParseOffset, end0: g.CopyOffset,
	}, nil
}

// runWork parses the generations in group-commit transactions. When a
// multi-generation transaction fails it retries each generation alone, so one
// bad file cannot hold the others back.
func (p *Parser) runWork(work []*genWork) (int, error) {
	n, err := p.runBatches(work)
	if err == nil || len(work) < 2 {
		return n, err
	}
	var first error
	for _, w := range work {
		if w.start >= w.end {
			continue
		}
		m, err := p.runBatches([]*genWork{w})
		n += m
		if err != nil && first == nil {
			first = err
		}
	}
	return n, first
}

// runBatches loops transactions until every generation is parsed to its end (or
// to a torn line). Each transaction takes chunks, generation by generation,
// until it holds BatchRecords records or BatchBytes parsed bytes, then writes
// the parse offsets and parse state of what it touched, bumps parse_seq ONCE and
// commits.
func (p *Parser) runBatches(work []*genWork) (int, error) {
	total := 0
	next := make([]int64, len(work))
	for {
		pending := false
		for _, w := range work {
			if w.start < w.end {
				pending = true
			}
		}
		if !pending {
			return total, nil
		}
		cnt := 0
		var bytes int64
		advancedAny := false
		finished := make([]bool, len(work))
		fn := func(rawTx *sql.Tx) error {
			tx := newPS(rawTx)
			defer tx.Close()
			tx.acc = p.newAcc()
			cnt, bytes, advancedAny = 0, 0, false
			for i, w := range work {
				next[i] = w.start
				finished[i] = false
			}
			for i, w := range work {
				cur := w.start
				for cur < w.end {
					if cnt >= p.cfg.BatchRecords || bytes >= p.cfg.BatchBytes {
						break
					}
					if err := p.genReset(tx, w.f, w.src, w.g, cur); err != nil {
						return err
					}
					n, err := p.chunk(tx, w.fh, w.f, w.src, w.caps, w.g, cur, w.end, &cnt)
					w.stepNS += p.flushSteps(tx)
					if err != nil {
						return err
					}
					if n == 0 { // a torn line waits for its newline
						finished[i] = true
						break
					}
					cur += n
					bytes += n
					advancedAny = true
				}
				next[i] = cur
				if cur >= w.end {
					finished[i] = true
				}
				if cur == w.start {
					continue
				}
				res, err := tx.Exec(`UPDATE generations SET parse_offset=? WHERE file_id=? AND gen=? AND ?<=copy_offset`, cur, w.f.ID, w.g.Gen, cur)
				if err != nil {
					return err
				}
				p.noteParseOffset(res, w.f.ID, w.g.Gen)
				if _, err := tx.Exec(`INSERT INTO parse_state(file_id,gen,version) VALUES(?,?,?) ON CONFLICT(file_id,gen) DO UPDATE SET version=excluded.version`,
					w.f.ID, w.g.Gen, w.src.Version()); err != nil {
					return err
				}
			}
			if !advancedAny {
				return nil
			}
			if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('parse_seq','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
				return err
			}
			if err := p.flushSessions(tx); err != nil {
				return err
			}
			if err := p.flushFileFacts(tx); err != nil {
				return err
			}
			return p.mark(tx)
		}
		if err := p.cfg.Store.WithWrite("parse.before-commit", fn); err != nil {
			return total, err
		}
		if err := p.cfg.Store.BulkAdd(cnt, bytes); err != nil {
			return total, err
		}
		total += cnt
		for i, w := range work {
			w.start = next[i]
			if finished[i] {
				w.end = w.start // done for this pass (a torn tail waits)
			}
		}
	}
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
		if p.cfg.EndsDir != "" {
			if r, err := relTo(p.cfg.EndsDir, ap); err == nil {
				return "ends/" + r, true
			}
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

// parseGen parses a generation from its parse offset (from 0 for a re-parse,
// which also replaces what was stored) to the last newline in the copy.
func (p *Parser) parseGen(f store.File, src source.Source, g store.Generation, reparse bool) (int, error) {
	if reparse {
		// Clearing the generation deletes by (file_id, gen): that needs the
		// secondary indexes a bulk rebuild defers.
		if err := p.cfg.Store.EnsureIndexes(); err != nil {
			return 0, err
		}
	}
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

	start := g.ParseOffset
	if reparse {
		start = 0
	}
	end := g.CopyOffset
	count := 0

	run := func(rawTx *sql.Tx) error {
		tx := newPS(rawTx)
		defer tx.Close()
		tx.acc = p.newAcc()
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
			p.cfg.Obs.EventDebug("parser_reset", "file", strconv.FormatInt(f.ID, 10), "gen", strconv.Itoa(g.Gen), "source", src.Name())
		}
		off := start
		for off < end {
			n, err := p.chunk(tx, fh, f, src, caps, g, off, end, &count)
			p.flushSteps(tx)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			off += n
		}
		res, err := tx.Exec(`UPDATE generations SET parse_offset=? WHERE file_id=? AND gen=? AND ?<=copy_offset`, off, f.ID, g.Gen, off)
		if err != nil {
			return err
		}
		p.noteParseOffset(res, f.ID, g.Gen)
		if _, err := tx.Exec(`INSERT INTO parse_state(file_id,gen,version) VALUES(?,?,?) ON CONFLICT(file_id,gen) DO UPDATE SET version=excluded.version`,
			f.ID, g.Gen, src.Version()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('parse_seq','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
			return err
		}
		if err := p.flushSessions(tx); err != nil {
			return err
		}
		if err := p.flushFileFacts(tx); err != nil {
			return err
		}
		return p.mark(tx)
	}
	// A re-parse is one transaction so the new records appear together.
	if err := p.cfg.Store.WithWrite("parse.before-commit", run); err != nil {
		return count, err
	}
	return count, p.cfg.Store.BulkAdd(count, end-start)
}

func (p *Parser) clearGen(tx *pstx, file int64, gen int) error {
	p.cfg.Store.NoteRowsRemoved() // rows leave the store: a cached lifecycle model may hold them
	// The sessions that lose rows get their stats recomputed once the rows are gone.
	affected, err := p.sessionsOfGen(tx, file, gen)
	if err != nil {
		return err
	}
	wordOpaque, triStmt := ",'opaque'", ""
	if p.cfg.Store.HasTrigram() {
		wordOpaque = ""
		triStmt = `DELETE FROM fts_tri WHERE rowid IN (SELECT t.id FROM texts t JOIN records r ON r.id=t.rec_id WHERE r.file_id=?1 AND r.gen=?2 AND t.kind='opaque')`
	}
	stmts := []string{
		// Opaque fields are in fts_tri when the index has it and in fts_word
		// otherwise, so fts_word is cleared of every kind it can hold.
		`DELETE FROM fts_word WHERE rowid IN (SELECT t.id FROM texts t JOIN records r ON r.id=t.rec_id WHERE r.file_id=?1 AND r.gen=?2 AND t.kind IN ('text'` + wordOpaque + `))`,
		`DELETE FROM texts WHERE rec_id IN (SELECT id FROM records WHERE file_id=?1 AND gen=?2)`,
		`DELETE FROM fields WHERE rec_id IN (SELECT id FROM records WHERE file_id=?1 AND gen=?2)`,
		`DELETE FROM records WHERE file_id=?1 AND gen=?2`,
		`DELETE FROM bad_lines WHERE file_id=?1 AND gen=?2`,
		// The aggregate gives back this file's counts before its rows go.
		`UPDATE unknown_keys_total SET count=count-(SELECT u.count FROM unknown_keys u
WHERE u.file_id=?1 AND u.gen=?2 AND u.source=unknown_keys_total.source AND u.rtype=unknown_keys_total.rtype AND u.path=unknown_keys_total.path)
WHERE EXISTS(SELECT 1 FROM unknown_keys u WHERE u.file_id=?1 AND u.gen=?2 AND u.source=unknown_keys_total.source AND u.rtype=unknown_keys_total.rtype AND u.path=unknown_keys_total.path)`,
		`DELETE FROM unknown_keys_total WHERE count<=0`,
		`DELETE FROM unknown_keys WHERE file_id=?1 AND gen=?2`,
	}
	if triStmt != "" {
		stmts = append([]string{triStmt}, stmts...)
	}
	if err := p.clearExt(tx, file, gen); err != nil {
		return err
	}
	if err := p.clearAsk(tx, file, gen); err != nil {
		return err
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s, file, gen); err != nil {
			return err
		}
	}
	if err := p.recomputeSessionStats(tx, affected); err != nil {
		return err
	}
	return recomputeFileFact(tx, file)
}

// chunk parses lines from off, at most ChunkBytes of them, never past the last
// newline within end. It returns the bytes consumed (0 when only a torn line is
// left).
func (p *Parser) chunk(tx *pstx, fh *store.Pointer, f store.File, src source.Source, caps source.Captures,
	g store.Generation, off, end int64, count *int,
) (int64, error) {
	size := int64(p.cfg.ChunkBytes)
	a := tx.acc
	timed := a != nil && a.on
	for {
		if off+size > end {
			size = end - off
		}
		buf := make([]byte, size)
		var tr time.Duration
		if timed {
			tr = a.now()
		}
		_, err := fh.ReadAt(buf, off)
		if timed {
			a.st.StepNS[store.StepRead] += int64(a.now() - tr)
			a.st.StepCalls[store.StepRead]++
		}
		if err != nil && err != io.EOF {
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
		if timed {
			a.st.BytesRead += consumed
		}
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
			// Three clock reads per line: before and after Parse, and after insert.
			var t0, t1 time.Duration
			if timed {
				t0 = a.now()
			}
			recs, err := src.Parse(caps, line)
			if timed {
				t1 = a.now()
				a.st.StepNS[store.StepParse] += int64(t1 - t0)
				a.st.StepCalls[store.StepParse]++
				a.st.Lines++
			}
			if err != nil {
				if err := p.bad(tx, f, g, abs, ll, err.Error()); err != nil {
					return 0, err
				}
				continue
			}
			if timed {
				*a.seq++
				a.sample, a.inInsert = *a.seq%sampleEvery == 0, true
			}
			ierr := p.insert(tx, f, src, caps, g, abs, ll, recs)
			if timed {
				a.inInsert = false
				a.st.StepNS[store.StepInsert] += int64(a.now() - t1)
				a.st.StepCalls[store.StepInsert]++
				a.st.Records += int64(len(recs))
			}
			if ierr != nil {
				return 0, ierr
			}
			*count += len(recs)
		}
		return consumed, nil
	}
}

func (p *Parser) skipLong(tx *pstx, fh *store.Pointer, f store.File, g store.Generation, off, end int64) (int64, error) {
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
	p.cfg.Obs.Reg.Add(MLinesParsed, nil, 1)
	p.cfg.Obs.Reg.Add(MBadLines, map[string]string{"source": f.Source}, 1)
	_, err := tx.Exec(`INSERT OR REPLACE INTO bad_lines(file_id,gen,off,len,reason) VALUES(?,?,?,?,?)`, f.ID, g.Gen, off, ln, reason)
	return err
}

// Metric names the parser adds to the shared registry.
const (
	// MLinesParsed counts every line handed to the parser, stored or bad, so
	// bad_lines_total over it is the bad-line rate.
	MLinesParsed   = "lines_parsed_total"
	MBadLines      = "bad_lines_total"
	MUnknownKeys   = "unknown_keys_total"
	MParseLagSecs  = "parse_lag_seconds"
	MParseLagBytes = "parse_lag_bytes"
	MParseProgress = "parse_progress"
	MUnknownKinds  = "unknown_kinds"
	// MFieldTypeMismatch counts a declared field whose value did not convert
	// losslessly to its declared type, by source, record type and field name.
	MFieldTypeMismatch = "field_type_mismatch_total"
)

// derivable reports whether a typed field's value is already answered by
// another table, so no fields row is stored for it: type and session_id equal
// the record's own type and session columns (the query layer reads those names
// from records), and a project or file_session that is just the path capture
// is read back from the file's path (query.builder.pathFact). The history
// source's project is a JSON value, not a capture, and is still stored.
func derivable(fl source.Field, caps source.Captures) bool {
	switch fl.Name {
	case "type", "session_id":
		return true
	case "project":
		return fl.Kind == source.KindKeyword && caps["project"] != "" && fl.S == caps["project"]
	case "file_session":
		return fl.Kind == source.KindID && caps["session"] != "" && fl.S == caps["session"]
	}
	return false
}

// ownTimestampField reports whether fl is the timestamp field row whose value
// is exactly the record's time_ns, which a record carrying the own_time marker
// serves from time_ns instead of from a stored row.
func ownTimestampField(fl source.Field, tns int64) bool {
	return fl.Name == "timestamp" && fl.Kind == source.KindTime && fl.N == tns
}

// ownTimestamp is the records.own_time marker: 1 when the record has its own
// timestamp (Record.Time, not the observation time) and its timestamp field
// holds exactly that value, so the timestamp field is answered from time_ns. A
// child record has no timestamp field and so is never marked: its time_ns is the
// line's time, but the child has no timestamp of its own to report.
func ownTimestamp(r source.Record, tns int64) int {
	if r.Time.IsZero() {
		return 0
	}
	for _, fl := range r.Fields {
		if ownTimestampField(fl, tns) {
			return 1
		}
	}
	return 0
}

func (p *Parser) insert(tx *pstx, f store.File, src source.Source, caps source.Captures, g store.Generation, off int64, ln int, recs []source.Record) error {
	// One line handed to the parser and stored: the denominator of the bad-line
	// rate alert (a bad line counts in bad() instead).
	p.cfg.Obs.Reg.Add(MLinesParsed, nil, 1)
	unk := map[[2]string]int{}
	nameID := func(name string) (int64, error) { return p.fieldNameID(tx, name) }
	// Fields a source declares into an extension table, only where the table exists.
	var routes map[string]extRoute
	if p.cfg.Store.HasExt() {
		routes = p.ext[src.Name()]
	}
	for _, r := range recs {
		// An untimed record stores 0: the copy time is not the record time.
		var tns int64
		if !r.Time.IsZero() {
			tns = r.Time.UnixNano()
		}
		ownTime := ownTimestamp(r, tns)
		ref, err := p.sessionRef(tx, r.Session, caps["project"], tns)
		if err != nil {
			return err
		}
		lk, err := p.recordLookups(tx, src.Name(), r)
		if err != nil {
			return err
		}
		var res sql.Result
		if p.cfg.Store.HasSessionText() {
			res, err = tx.ExecL(tblRecords, `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns,session_ref,cwd_id,branch_id,version_id,entrypoint_id,user_type_id,own_time) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				f.ID, g.Gen, off, r.Item, ln, src.Name(), r.Type, src.Version(), r.Session, tns, ref,
				lk[lkCwd], lk[lkBranch], lk[lkVersion], lk[lkEntrypoint], lk[lkUserType], ownTime)
		} else {
			res, err = tx.ExecL(tblRecords, `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref,cwd_id,branch_id,version_id,entrypoint_id,user_type_id,own_time) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				f.ID, g.Gen, off, r.Item, ln, src.Name(), r.Type, src.Version(), tns, ref,
				lk[lkCwd], lk[lkBranch], lk[lkVersion], lk[lkEntrypoint], lk[lkUserType], ownTime)
		}
		if err != nil {
			return err
		}
		switch src.Name() {
		case "session-registry":
			p.cfg.Store.NoteLiveInput() // moves the live-Process input counter at commit
		case "claude-history", "claude-control-end":
			p.cfg.Store.NoteModelInput() // moves the lifecycle model input counter at commit
		}
		tx.noteSession(ref, tns, f.ID, lk)
		tx.noteFileFact(f.ID, tns, caps)
		id, _ := res.LastInsertId()
		var extRows map[int]*extRow
		for _, fl := range r.Fields {
			if fl.Kind.Searchable() {
				if fl.S == "" {
					continue
				}
				tr, err := tx.ExecL(tblTexts, `INSERT INTO texts(rec_id,field,kind) VALUES(?,?,?)`, id, fl.Name, string(fl.Kind))
				if err != nil {
					return err
				}
				tid, _ := tr.LastInsertId()
				tbl := "fts_word"
				if fl.Kind == source.KindOpaque && p.cfg.Store.HasTrigram() {
					tbl = "fts_tri"
				}
				if _, err := tx.ExecL(tblFTS, `INSERT INTO `+tbl+`(rowid, body) VALUES(?,?)`, tid, fl.S); err != nil {
					return err
				}
				continue
			}
			if derivable(fl, caps) || (ownTime == 1 && ownTimestampField(fl, tns)) {
				continue
			}
			var s any
			var n any
			var fv any
			if rt, ok := routes[fl.Name]; ok {
				if extRows == nil {
					extRows = map[int]*extRow{}
				}
				row := extRows[rt.table]
				if row == nil {
					row = &extRow{vals: map[string]any{}}
					extRows[rt.table] = row
				}
				switch fl.Kind {
				case source.KindID, source.KindKeyword, source.KindPath, source.KindSession:
					row.vals[rt.col] = fl.S
				case source.KindFloat:
					row.vals[rt.col] = fl.F
				default:
					row.vals[rt.col] = fl.N
				}
				continue
			}
			switch fl.Kind {
			case source.KindID, source.KindKeyword, source.KindPath, source.KindSession:
				s = fl.S
			case source.KindFloat:
				fv = fl.F
			default:
				n = fl.N
			}
			nid, err := nameID(fl.Name)
			if err != nil {
				return err
			}
			// One row per (rec_id, name_id): a repeated name keeps the LAST value
			// and replaces the whole row, so s, n and f never mix two values.
			if _, err := tx.ExecL(tblFields, `INSERT INTO fields(rec_id,name_id,s,n,f) VALUES(?,?,?,?,?)
ON CONFLICT(rec_id,name_id) DO UPDATE SET s=excluded.s, n=excluded.n, f=excluded.f`, id, nid, s, n, fv); err != nil {
				return err
			}
		}
		if err := insertExt(tx, id, extRows); err != nil {
			return err
		}
		if err := p.noteAsk(tx, src.Name(), caps, r, ref, tns, id); err != nil {
			return err
		}
		for _, u := range r.Unknown {
			unk[[2]string{r.Type, u}]++
		}
		p.countMismatches(src.Name(), r)
	}
	for k, c := range unk {
		if _, err := tx.Exec(`INSERT INTO unknown_keys(file_id,gen,source,rtype,path,count) VALUES(?,?,?,?,?,?)
ON CONFLICT(file_id,gen,source,rtype,path) DO UPDATE SET count=count+excluded.count`, f.ID, g.Gen, src.Name(), k[0], k[1], c); err != nil {
			return err
		}
		// unknown_keys_total counts first-seen distinct (source, type, key)
		// tuples: it moves only when the aggregate row is created, so a backfill
		// or steady traffic of already-seen keys does not move it.
		res, err := tx.Exec(`INSERT INTO unknown_keys_total(source,rtype,path,count) VALUES(?,?,?,?)
ON CONFLICT(source,rtype,path) DO NOTHING`, src.Name(), k[0], k[1], c)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			p.cfg.Obs.Reg.Add(MUnknownKeys, map[string]string{"source": src.Name(), "type": TypeLabel(k[0])}, 1)
		} else if _, err := tx.Exec(`UPDATE unknown_keys_total SET count=count+? WHERE source=? AND rtype=? AND path=?`, c, src.Name(), k[0], k[1]); err != nil {
			return err
		}
	}
	return nil
}

// countsLag reports whether a file of this source and mode is part of the parse
// lag: its source has a parser and it is parsed by offset. A snapshot file is
// parsed by version (parse_state) and never advances parse_offset, so a
// generation row it carries would read as a backlog for good.
func (p *Parser) countsLag(src, mode string) bool {
	return p.HasSource(src) && mode == "append"
}

// Rebuilt is the lag figures RebuildLag computes from the whole tables.
type Rebuilt struct {
	LagBytes int64
	Progress float64
	Kinds    int // distinct sources with a live file but no parser
}

// RebuildLag computes the parse lag in bytes, parse progress and the unknown
// kinds count from the whole tables (AllFiles, OffsetTotalsByFile and the
// lagging generations). It computes no lag seconds: the origin of that figure
// (when copy got ahead of parse) is in-memory only, and nothing in the tables
// or a journal replay records it. It is the reference the maintained state is compared
// against: run once at start and again by the reconcile, never by a gauge
// refresh. It sets no gauge. ok is false when a store read failed.
func (p *Parser) RebuildLag() (r Rebuilt, ok bool) {
	files, err := p.cfg.Store.AllFiles()
	if err != nil {
		return r, false
	}
	totals, err := p.cfg.Store.OffsetTotalsByFile()
	if err != nil {
		return r, false
	}
	lagging, err := p.cfg.Store.LaggingGenerations()
	if err != nil {
		return r, false
	}
	kinds := map[string]bool{}
	var copied, parsed int64
	for _, f := range files {
		if !p.HasSource(f.Source) {
			if f.State != "deleted" {
				kinds[f.Source] = true
			}
			continue
		}
		if !p.countsLag(f.Source, f.Mode) {
			continue
		}
		t := totals[f.ID]
		copied += t.Copy
		parsed += t.Parse
	}
	for _, g := range lagging {
		f, found := fileByID(files, g.File)
		if !found || !p.countsLag(f.Source, f.Mode) {
			continue
		}
		if lag := g.CopyOffset - g.ParseOffset; lag > r.LagBytes {
			r.LagBytes = lag
		}
	}
	r.Progress = 1.0
	if copied > 0 {
		r.Progress = float64(parsed) / float64(copied)
	}
	r.Kinds = len(kinds)
	return r, true
}

// SetLagGauges sets the four gauges from one set of figures.
func (p *Parser) SetLagGauges(lagBytes int64, lagSecs, progress float64, kinds int) {
	r := p.cfg.Obs.Reg
	r.Set(MParseLagBytes, nil, float64(lagBytes))
	r.Set(MParseLagSecs, nil, lagSecs)
	r.Set(MParseProgress, nil, progress)
	r.Set(MUnknownKinds, nil, float64(kinds))
}

// noteParseOffset tells the store's OnOffsets subscribers that the UPDATE just
// run in the open write transaction moved a generation's parse offset. An
// UPDATE the parse_offset <= copy_offset guard rejected matched no row and is
// not noted.
func (p *Parser) noteParseOffset(res sql.Result, file int64, gen int) {
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		p.cfg.Store.NoteParseOffset(file, gen)
	}
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
// key per source and record type, summed over files; read from the
// unknown_keys_total aggregate the writers maintain, never the per-file rows).
func Summarize(st *store.Store) (Summary, error) { return SummarizeFrom(&st.Reader) }

// SummarizeFrom is Summarize over a Reader, so a caller holding one read
// transaction takes both queries from its snapshot.
func SummarizeFrom(rd *store.Reader) (Summary, error) {
	var s Summary
	if err := rd.Q().QueryRow(`SELECT COUNT(*) FROM bad_lines`).Scan(&s.BadLines); err != nil {
		return s, err
	}
	rows, err := rd.Q().Query(`SELECT source,rtype,path,count FROM unknown_keys_total ORDER BY source,rtype,path`)
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

// fieldNameID returns the id of a field name, creating its row on first sight.
// The name cache survives across lines and transactions, so a name costs
// statements only the first time. Called inside a write transaction.
func (p *Parser) fieldNameID(tx *pstx, name string) (int64, error) {
	if ep := p.cfg.Store.RollbackEpoch(); p.names == nil || p.namesEpoch != ep {
		m := map[string]int64{}
		rows, err := tx.Query(`SELECT id, name FROM field_names`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id int64
			var n string
			if err := rows.Scan(&id, &n); err != nil {
				_ = rows.Close()
				return 0, err
			}
			m[n] = id
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
		if err := rows.Err(); err != nil {
			return 0, err
		}
		p.names, p.namesEpoch = m, ep
	}
	if id, ok := p.names[name]; ok {
		return id, nil
	}
	if _, err := tx.Exec(`INSERT INTO field_names(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM field_names WHERE name=?`, name).Scan(&id); err != nil {
		return 0, err
	}
	p.names[name] = id
	return id, nil
}

// QueryRow runs a single-row query on a statement prepared once per transaction.
func (p *pstx) QueryRow(q string, args ...any) *sql.Row {
	if ablateNoPrepare {
		return p.QueryRow(q, args...)
	}
	st, ok := p.st[q]
	if !ok {
		var err error
		if st, err = p.Prepare(q); err != nil {
			return p.QueryRow(q, args...) // reports the same error from Scan
		}
		p.st[q] = st
	}
	return st.QueryRow(args...)
}

// pstx is a transaction whose Exec statements are prepared once and reused
// (a parse inserts tens of thousands of rows per transaction).
type pstx struct {
	*sql.Tx
	st   map[string]*sql.Stmt
	sess *sessionBatch  // session activity of this transaction, see flushSessions
	ffs  *fileFactBatch // per-file facts of this transaction, see flushFileFacts
	acc  *stepAcc       // step timers of this transaction (nil: none)
}

// The insert tables of ExecL, matching store.Table* and obs.InsertTables.
const (
	tblRecords  = store.TableRecords
	tblFields   = store.TableFields
	tblTexts    = store.TableTexts
	tblFTS      = store.TableFTS
	tblSessions = store.TableSessions
	tblExt      = store.TableExt
	tblOther    = store.TableOther
)

func newPS(tx *sql.Tx) *pstx { return &pstx{Tx: tx, st: map[string]*sql.Stmt{}} }

// Exec prepares q on first use. Inside insert it is attributed to the "other"
// table; use ExecL to name the table.
func (p *pstx) Exec(q string, args ...any) (sql.Result, error) {
	return p.ExecL(tblOther, q, args...)
}

// exec runs q on its prepared statement.
func (p *pstx) exec(q string, args ...any) (sql.Result, error) {
	if ablateNoPrepare {
		return p.Tx.Exec(q, args...)
	}
	st, ok := p.st[q]
	if !ok {
		var err error
		if st, err = p.Prepare(q); err != nil {
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

// countMismatches counts each field of r that did not convert losslessly to
// its declared type. It never blocks ingest and stores nothing.
func (p *Parser) countMismatches(srcName string, r source.Record) {
	for _, name := range r.Mismatches {
		p.cfg.Obs.Reg.Add(MFieldTypeMismatch, map[string]string{"source": srcName, "type": r.Type, "field": name}, 1)
	}
}
