// sessions_cache.go: the persistent per-file summary cache behind
// SessionsWithOptions. Reading a large projects tree is dominated by decoding
// every transcript's JSON, while only a few files change between calls; the
// cache keeps each file's fileSummary keyed on (path, size, mtime) so a call
// re-reads only what changed. It never decides which files take part in the
// merge: every listed file still contributes its (cached or fresh) summary.
//
// File format (version sessionsCacheVersion), all integers varint-encoded
// unless noted, little-endian where fixed-width:
//
//	magic        sessionsCacheMagic
//	version      uvarint
//	projectsDir  string (the absolute projectsDir the cache serves)
//	count        uvarint, then count entries sorted by rel:
//	  rel          string ("<slug>/<file>.jsonl")
//	  size, mtime  varint, varint (mtime in Unix nanoseconds)
//	  flag         byte: 0 = the file holds no dated event, 1 = a summary follows
//	  summary      id, first, last, cwd, branch, model, a local model table,
//	               uuids and user-turn uuids (8 bytes each), userNoUUID,
//	               message entries and id-less assistant events
//	crc          4 bytes: CRC-32C of everything before it
//
// A string is a uvarint length and the bytes; a time is varint Unix seconds and
// uvarint nanoseconds.
package claudetranscript

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// sessionsCacheMagic opens every cache file.
	sessionsCacheMagic = "claude-transcript sessions cache\n"
	// sessionsCacheVersion is the cache format version. It MUST be bumped
	// whenever the encoding, the fileSummary fields, the fold rules or hashUUID
	// change: a cache of another version is ignored and rebuilt.
	sessionsCacheVersion = 1
	// defaultCheckpointEvery is how often a call that is still reading files
	// writes the cache, so that a call killed part-way (for example by a
	// caller's deadline) keeps most of what it read.
	defaultCheckpointEvery = 5 * time.Second
	// staleTempAge is the age past which a leftover temporary cache file (from
	// a writer killed mid-write) is removed. A live writer finishes within
	// seconds, so the sweep never touches a write in progress.
	staleTempAge = 10 * time.Minute
)

var (
	crcTable = crc32.MakeTable(crc32.Castagnoli)

	errCacheCorrupt   = errors.New("claude-transcript: sessions cache: corrupt or truncated")
	errCacheVersion   = errors.New("claude-transcript: sessions cache: other format version")
	errCacheOtherTree = errors.New("claude-transcript: sessions cache: written for another projects dir")
)

// cacheEntry is one file's cached outcome.
type cacheEntry struct {
	key fileKey
	sum *fileSummary // nil: the file holds no dated event
}

// summaryCache is the cache file of one projectsDir in one cache directory.
type summaryCache struct {
	dir, base, path string
	absProjects     string
	checkpointEvery time.Duration
	onCheckpoint    func(entries int)
	swept           bool
}

// newSummaryCache returns nil (no cache) when opts names no CacheDir, a path
// cannot be made absolute, or the cache directory lies inside the Claude home.
func newSummaryCache(projectsDir string, opts SessionsOptions) *summaryCache {
	if opts.CacheDir == "" {
		return nil
	}
	absProjects, err := filepath.Abs(projectsDir)
	if err != nil {
		return nil
	}
	dir, err := filepath.Abs(opts.CacheDir)
	if err != nil || !cacheDirAllowed(absProjects, dir) {
		return nil
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(absProjects))
	base := fmt.Sprintf("sessions-%016x.cache", h.Sum64())
	every := opts.checkpointEvery
	if every == 0 {
		every = defaultCheckpointEvery
	}
	return &summaryCache{
		dir:             dir,
		base:            base,
		path:            filepath.Join(dir, base),
		absProjects:     absProjects,
		checkpointEvery: every,
		onCheckpoint:    opts.onCheckpoint,
	}
}

// cacheDirAllowed reports whether dir lies outside the Claude home holding
// absProjects (its parent directory), or outside absProjects itself when that
// parent is a filesystem root. The test is lexical, on cleaned absolute paths.
func cacheDirAllowed(absProjects, dir string) bool {
	guard := filepath.Dir(absProjects)
	if filepath.Dir(guard) == guard {
		guard = absProjects
	}
	rel, err := filepath.Rel(guard, dir)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// summarize returns the summaries of files (in their order, files without one
// dropped), reading only the files the cache cannot answer for, and rewrites
// the cache when its content changed.
func (c *summaryCache) summarize(files []transcriptFile, st *SessionsStats) []*fileSummary {
	old, oldBytes := c.load()
	st.CacheBytes = oldBytes
	entries := make(map[string]*cacheEntry, len(files))
	results := make([]*fileSummary, len(files))
	type miss struct {
		i    int
		size int64
	}
	var misses []miss
	for i, f := range files {
		fi, err := os.Stat(f.path)
		if err != nil {
			continue // gone since the listing, or not stat-able: no record either way
		}
		k := fileKey{size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
		if e, ok := old[f.rel]; ok && e.key == k {
			if e.sum != nil {
				e.sum.path = f.path
			}
			entries[f.rel] = e
			results[i] = e.sum
			st.Reused++
			continue
		}
		misses = append(misses, miss{i, k.size})
	}
	// Dropped entries (files gone or changed) already make the cache stale.
	unsaved := len(entries) != len(old)

	// Largest first, so the biggest files do not start last on a cold call and
	// stretch its tail; the order does not affect the result.
	sort.SliceStable(misses, func(a, b int) bool { return misses[a].size > misses[b].size })
	idx := make([]int, len(misses))
	for j, m := range misses {
		idx[j] = m.i
	}
	lastWrite := time.Now()
	parseFiles(files, idx, func(i int, p parsedFile) {
		if !p.read {
			return // unreadable: never cached, retried on the next call
		}
		st.Parsed++
		entries[files[i].rel] = &cacheEntry{key: p.key, sum: p.sum}
		results[i] = p.sum
		unsaved = true
		if c.checkpointEvery < 0 || time.Since(lastWrite) >= c.checkpointEvery {
			if c.save(entries, st) {
				unsaved = false
				if c.onCheckpoint != nil {
					c.onCheckpoint(len(entries))
				}
			}
			lastWrite = time.Now()
		}
	})
	if unsaved {
		c.save(entries, st)
	}
	return compactSummaries(results)
}

// save writes entries and records the write in st; it reports success.
func (c *summaryCache) save(entries map[string]*cacheEntry, st *SessionsStats) bool {
	n, err := c.write(entries)
	if err != nil {
		return false
	}
	st.CacheWrites++
	st.CacheBytes = n
	return true
}

// load reads and decodes the cache file; any problem yields an empty cache.
func (c *summaryCache) load() (map[string]*cacheEntry, int64) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, 0
	}
	m, err := decodeCache(data, c.absProjects)
	if err != nil {
		return nil, 0
	}
	return m, int64(len(data))
}

// write replaces the cache file atomically: the content goes to a temporary
// file in the same directory, which is then renamed over the cache file, so a
// reader sees either the old file or the new one, never a partial one. It
// returns the size written.
func (c *summaryCache) write(entries map[string]*cacheEntry) (int64, error) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return 0, err
	}
	if !c.swept {
		c.swept = true
		c.sweepTemps(time.Now())
	}
	tmp, err := os.CreateTemp(c.dir, c.base+".tmp-*")
	if err != nil {
		return 0, err
	}
	name := tmp.Name()
	n, err := encodeCache(tmp, sessionsCacheVersion, c.absProjects, entries)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, c.path)
	}
	if err != nil {
		_ = os.Remove(name)
		return 0, err
	}
	return n, nil
}

// sweepTemps removes this cache's temporary files older than staleTempAge.
func (c *summaryCache) sweepTemps(now time.Time) {
	ents, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	prefix := c.base + ".tmp-"
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > staleTempAge {
			_ = os.Remove(filepath.Join(c.dir, e.Name()))
		}
	}
}

// encodeCache writes entries in the cache format to w and returns the number
// of bytes written. version is a parameter so tests can write a mismatch.
func encodeCache(w io.Writer, version uint64, absProjects string, entries map[string]*cacheEntry) (int64, error) {
	rels := make([]string, 0, len(entries))
	for r := range entries {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	e := &cacheEncoder{w: w, buf: make([]byte, 0, 1<<20)}
	e.buf = append(e.buf, sessionsCacheMagic...)
	e.uvarint(version)
	e.str(absProjects)
	e.uvarint(uint64(len(rels)))
	for _, r := range rels {
		e.entry(r, entries[r])
		e.maybeFlush()
	}
	return e.finish()
}

type cacheEncoder struct {
	w   io.Writer
	buf []byte
	crc uint32
	n   int64
	err error
}

func (e *cacheEncoder) flush() {
	if e.err != nil || len(e.buf) == 0 {
		return
	}
	e.crc = crc32.Update(e.crc, crcTable, e.buf)
	m, err := e.w.Write(e.buf)
	e.n += int64(m)
	e.err = err
	e.buf = e.buf[:0]
}

func (e *cacheEncoder) maybeFlush() {
	if len(e.buf) >= 1<<20 {
		e.flush()
	}
}

func (e *cacheEncoder) finish() (int64, error) {
	e.flush()
	if e.err != nil {
		return 0, e.err
	}
	var t [4]byte
	binary.LittleEndian.PutUint32(t[:], e.crc)
	m, err := e.w.Write(t[:])
	return e.n + int64(m), err
}

func (e *cacheEncoder) uvarint(v uint64) { e.buf = binary.AppendUvarint(e.buf, v) }
func (e *cacheEncoder) varint(v int64)   { e.buf = binary.AppendVarint(e.buf, v) }
func (e *cacheEncoder) u64(v uint64)     { e.buf = binary.LittleEndian.AppendUint64(e.buf, v) }
func (e *cacheEncoder) byte1(b byte)     { e.buf = append(e.buf, b) }

func (e *cacheEncoder) str(s string) {
	e.uvarint(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *cacheEncoder) time(t time.Time) {
	e.varint(t.Unix())
	e.uvarint(uint64(t.Nanosecond()))
}

func (e *cacheEncoder) hashes(hs []uint64) {
	e.uvarint(uint64(len(hs)))
	for _, h := range hs {
		e.u64(h)
	}
	e.maybeFlush()
}

func (e *cacheEncoder) tokens(t TokenTotals) {
	e.varint(t.Input)
	e.varint(t.Output)
	e.varint(t.CacheRead)
	e.varint(t.CacheWrite)
}

func (e *cacheEncoder) entry(rel string, ce *cacheEntry) {
	e.str(rel)
	e.varint(ce.key.size)
	e.varint(ce.key.mtimeNs)
	s := ce.sum
	if s == nil {
		e.byte1(0)
		return
	}
	e.byte1(1)
	e.str(s.id)
	e.time(s.first)
	e.time(s.last)
	e.str(s.cwd)
	e.str(s.branch)
	e.str(s.model)

	// Local model table: index 0 is "", 1..n the distinct non-empty models.
	index := map[string]uint64{"": 0}
	var models []string
	add := func(m string) {
		if _, ok := index[m]; !ok {
			models = append(models, m)
			index[m] = uint64(len(models))
		}
	}
	for i := range s.msgs {
		add(s.msgs[i].model)
	}
	for i := range s.noID {
		add(s.noID[i].model)
	}
	e.uvarint(uint64(len(models)))
	for _, m := range models {
		e.str(m)
	}

	e.hashes(s.uuids)
	e.hashes(s.userUUIDs)
	e.uvarint(uint64(s.userNoUUID))
	e.uvarint(uint64(len(s.msgs)))
	for i := range s.msgs {
		m := &s.msgs[i]
		e.u64(m.id)
		e.uvarint(index[m.model])
		var flag byte
		if m.nonSide {
			flag = 1
		}
		e.byte1(flag)
		e.tokens(m.tok)
	}
	e.maybeFlush()
	e.uvarint(uint64(len(s.noID)))
	for i := range s.noID {
		n := &s.noID[i]
		var flag byte
		if n.hasUUID {
			flag |= 1
		}
		if n.sidechain {
			flag |= 2
		}
		e.byte1(flag)
		if n.hasUUID {
			e.u64(n.uuid)
		}
		e.uvarint(index[n.model])
		e.tokens(n.tok)
	}
}

// decodeCache parses a whole cache file. It fails on a bad magic, checksum,
// version or projects dir, and on any structural inconsistency; it never
// allocates more than the remaining input could hold.
func decodeCache(data []byte, absProjects string) (map[string]*cacheEntry, error) {
	if len(data) < len(sessionsCacheMagic)+4 || string(data[:len(sessionsCacheMagic)]) != sessionsCacheMagic {
		return nil, errCacheCorrupt
	}
	body := data[:len(data)-4]
	if crc32.Checksum(body, crcTable) != binary.LittleEndian.Uint32(data[len(data)-4:]) {
		return nil, errCacheCorrupt
	}
	d := &cacheDecoder{b: body, off: len(sessionsCacheMagic)}
	if v := d.uvarint(); d.err == nil && v != sessionsCacheVersion {
		return nil, errCacheVersion
	}
	if p := d.str(); d.err == nil && p != absProjects {
		return nil, errCacheOtherTree
	}
	n := d.count(4)
	m := make(map[string]*cacheEntry, n)
	for range n {
		rel := d.str()
		ce := &cacheEntry{}
		ce.key.size = d.varint()
		ce.key.mtimeNs = d.varint()
		switch d.byte1() {
		case 0:
		case 1:
			ce.sum = d.summary()
		default:
			d.fail()
		}
		if d.err != nil {
			return nil, d.err
		}
		if _, dup := m[rel]; dup {
			return nil, errCacheCorrupt
		}
		m[rel] = ce
	}
	if d.err == nil && d.off != len(d.b) {
		d.fail()
	}
	if d.err != nil {
		return nil, d.err
	}
	return m, nil
}

type cacheDecoder struct {
	b   []byte
	off int
	err error
}

func (d *cacheDecoder) fail() {
	if d.err == nil {
		d.err = errCacheCorrupt
	}
}

func (d *cacheDecoder) left() int { return len(d.b) - d.off }

func (d *cacheDecoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b[d.off:])
	if n <= 0 {
		d.fail()
		return 0
	}
	d.off += n
	return v
}

func (d *cacheDecoder) varint() int64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Varint(d.b[d.off:])
	if n <= 0 {
		d.fail()
		return 0
	}
	d.off += n
	return v
}

func (d *cacheDecoder) byte1() byte {
	if d.err != nil || d.left() < 1 {
		d.fail()
		return 0
	}
	b := d.b[d.off]
	d.off++
	return b
}

func (d *cacheDecoder) u64() uint64 {
	if d.err != nil || d.left() < 8 {
		d.fail()
		return 0
	}
	v := binary.LittleEndian.Uint64(d.b[d.off:])
	d.off += 8
	return v
}

func (d *cacheDecoder) str() string {
	n := d.uvarint()
	if d.err != nil {
		return ""
	}
	if n > uint64(d.left()) {
		d.fail()
		return ""
	}
	s := string(d.b[d.off : d.off+int(n)])
	d.off += int(n)
	return s
}

// count reads an element count whose elements take at least minSize bytes
// each, failing when the remaining input cannot hold that many.
func (d *cacheDecoder) count(minSize int) int {
	n := d.uvarint()
	if d.err != nil {
		return 0
	}
	if n > uint64(d.left()/minSize) {
		d.fail()
		return 0
	}
	return int(n)
}

func (d *cacheDecoder) time() time.Time {
	sec := d.varint()
	nsec := d.uvarint()
	if nsec > 999_999_999 {
		d.fail()
	}
	if d.err != nil {
		return time.Time{}
	}
	return time.Unix(sec, int64(nsec)).UTC()
}

func (d *cacheDecoder) hashes() []uint64 {
	n := d.count(8)
	if n == 0 {
		return nil
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(d.b[d.off:])
		d.off += 8
	}
	return out
}

func (d *cacheDecoder) model(models []string) string {
	i := d.uvarint()
	if d.err != nil {
		return ""
	}
	if i >= uint64(len(models)) {
		d.fail()
		return ""
	}
	return models[i]
}

func (d *cacheDecoder) tokens() TokenTotals {
	return TokenTotals{Input: d.varint(), Output: d.varint(), CacheRead: d.varint(), CacheWrite: d.varint()}
}

func (d *cacheDecoder) summary() *fileSummary {
	s := &fileSummary{hasDatedEvt: true}
	s.id = d.str()
	s.first = d.time()
	s.last = d.time()
	s.cwd = d.str()
	s.branch = d.str()
	s.model = d.str()
	models := make([]string, d.count(1)+1) // index 0 is ""
	for i := 1; i < len(models); i++ {
		models[i] = d.str()
	}
	s.uuids = d.hashes()
	s.userUUIDs = d.hashes()
	if v := d.uvarint(); v > math.MaxInt32 {
		d.fail()
	} else {
		s.userNoUUID = int(v)
	}
	// A message entry is at least 8 (id) + 1 (model) + 1 (flag) + 4 (tokens) bytes.
	if n := d.count(14); n > 0 {
		s.msgs = make([]msgEntry, n)
		for i := range s.msgs {
			m := &s.msgs[i]
			m.id = d.u64()
			m.model = d.model(models)
			switch d.byte1() {
			case 0:
			case 1:
				m.nonSide = true
			default:
				d.fail()
			}
			m.tok = d.tokens()
		}
	}
	// An id-less event is at least 1 (flag) + 1 (model) + 4 (tokens) bytes.
	if n := d.count(6); n > 0 {
		s.noID = make([]noIDEvent, n)
		for i := range s.noID {
			e := &s.noID[i]
			flag := d.byte1()
			if flag&^3 != 0 {
				d.fail()
			}
			e.hasUUID = flag&1 != 0
			e.sidechain = flag&2 != 0
			if e.hasUUID {
				e.uuid = d.u64()
			}
			e.model = d.model(models)
			e.tok = d.tokens()
		}
	}
	return s
}
