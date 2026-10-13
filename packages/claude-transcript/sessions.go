// sessions.go: a per-session rollup over a Claude Code projects tree. It walks
// the transcript files themselves (not the daemon's PID files), so sessions
// whose process is long gone are still reported, and folds the transcripts of
// one resumed/compacted/forked session into a single SessionRecord.
package claudetranscript

import (
	"encoding/json"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// TokenTotals is a token tally in the four categories the design lists.
type TokenTotals struct{ Input, Output, CacheRead, CacheWrite int64 }

func (t TokenTotals) isZero() bool {
	return t.Input == 0 && t.Output == 0 && t.CacheRead == 0 && t.CacheWrite == 0
}

func (t *TokenTotals) add(o TokenTotals) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// maxInto raises each category of t to at least the matching category of o.
func (t *TokenTotals) maxInto(o TokenTotals) {
	t.Input = max(t.Input, o.Input)
	t.Output = max(t.Output, o.Output)
	t.CacheRead = max(t.CacheRead, o.CacheRead)
	t.CacheWrite = max(t.CacheWrite, o.CacheWrite)
}

// SessionRecord is one session as the transcript tree records it.
//
// Deferred: there is deliberately NO "dispatched" flag. Dispatched sessions
// are launched as ordinary interactive claude processes (no prompt argument,
// events carry the same entrypoint as an operator-started session), and the
// session display name that could tell them apart is a deployment-private
// prefix a public library must not assume. A later change adds the field when
// a reliable marker exists. There is likewise no first-prompt field (callers
// read it from TranscriptPath) and no cost (this package has no price table;
// callers price ModelTokens).
type SessionRecord struct {
	SessionID      string
	Cwd, Branch    string
	Model          string
	StartedAt      time.Time // UTC; the session's first dated event
	EndedAt        time.Time // UTC; the session's last dated event
	UserTurns      int
	AssistantTurns int
	TranscriptPath string                 // the transcript file of the session's earliest member file
	Tokens         TokenTotals            // sum over ModelTokens
	ModelTokens    map[string]TokenTotals // keyed by the assistant message's model id
}

// Sessions walks projectsDir (the directory named "projects" under a Claude
// home) and returns one record per session whose StartedAt is in
// [since, before). A zero bound is open on that side. Result order: StartedAt
// ascending, ties by SessionID.
//
// Only <projectsDir>/<slug>/<file>.jsonl at depth exactly two is a transcript;
// <id>.status.jsonl siblings and anything under <slug>/<id>/subagents/ are not.
// A missing projectsDir yields (nil, nil); an unreadable file or an unparsable
// line is skipped, never fatal; a result with no sessions is (nil, nil).
//
// Resume handling: Claude Code rewrites a transcript's session id (and file
// name) on resume, compact and fork, so the file name is not the session's
// identity. Transcript files that share a canonical session id (the most
// frequent sessionId their events carry, ties to the lexicographically smaller;
// the file name without .jsonl when no event carries one), OR share any
// non-empty event uuid (a resumed transcript replays earlier events with their
// original uuids), form ONE session. For such a merged session SessionID is the
// canonical id of the member whose first dated event is earliest (ties: the
// smaller id), StartedAt/EndedAt span the union, TranscriptPath is that
// earliest member's file, and turn counts and token tallies are computed over
// the union with events de-duplicated by uuid (an event with no uuid never
// de-duplicates). Cwd and Branch come from the first member (in that order)
// that has one; Model from the last member that has one.
//
// A file is scanned exactly once and never skipped on its modification time: a
// resumed session's original file can be older than since while the session
// still resolves through it. Message ids and event uuids are compared by their
// 64-bit FNV-1a hashes.
//
// Sessions reads every file on every call; SessionsWithOptions can keep a
// per-file summary cache so a call re-reads only the files that changed.
func Sessions(projectsDir string, since, before time.Time) ([]SessionRecord, error) {
	return SessionsWithOptions(projectsDir, since, before, SessionsOptions{})
}

// SessionsOptions tunes SessionsWithOptions. The zero value is the uncached
// scan Sessions performs.
type SessionsOptions struct {
	// CacheDir, when non-empty, is a directory in which calls keep one summary
	// per transcript file between calls (see SessionsWithOptions). The caller
	// chooses it: this library assumes no location. It must lie outside the
	// Claude home holding projectsDir (projectsDir's parent directory); a
	// CacheDir inside it is ignored and the call runs uncached. It is created
	// (mode 0700) when missing.
	CacheDir string
	// Stats, when non-nil, receives what the call did.
	Stats *SessionsStats

	// Test seams, settable only from this package's tests.
	checkpointEvery time.Duration     // 0: defaultCheckpointEvery; negative: after every file read
	onCheckpoint    func(entries int) // called after each checkpoint write
}

// SessionsStats reports one SessionsWithOptions call.
type SessionsStats struct {
	Files       int   // transcript files listed under projectsDir
	Parsed      int   // files opened and read by this call
	Reused      int   // files whose summary came from the cache, not read
	CacheWrites int   // cache files written: checkpoints plus the final write
	CacheBytes  int64 // size of the cache file last loaded or written; 0 when none
}

// SessionsWithOptions is Sessions with an optional persistent per-file summary
// cache in opts.CacheDir. With a cache, every transcript is still listed and
// stat'ed, but only a file whose size or modification time (in nanoseconds)
// differs from its cached entry, or that the cache does not hold, is opened and
// read; every file, read or cached, still takes part in the resume, compact and
// fork merge, so the result equals what Sessions returns for the same tree. One
// cache file serves one projectsDir, and its entries are keyed on the file's
// path, size and mtime; entries for files that are gone are dropped the next
// time the cache is written. A cached summary stays in use while a file's size
// and mtime are unchanged, even if the file's permissions change in between.
//
// The cache is written atomically (a temporary file in CacheDir renamed over
// the old one), so concurrent callers sharing a CacheDir never read a partial
// file; the last writer wins and a lost write only costs re-reading. While it
// is still reading files, a long call also writes checkpoints every few
// seconds, so a call killed part-way leaves the files it finished for the next
// call. A cache file that is missing, unreadable, truncated, corrupt, of another
// format version or written for another projectsDir is ignored and rebuilt, and
// a cache that cannot be written is skipped: no cache problem is ever an error.
// A call over a missing or empty tree touches no cache.
func SessionsWithOptions(projectsDir string, since, before time.Time, opts SessionsOptions) ([]SessionRecord, error) {
	files := listTranscriptFiles(projectsDir)
	st := SessionsStats{Files: len(files)}
	var sums []*fileSummary
	if c := newSummaryCache(projectsDir, opts); c != nil && len(files) > 0 {
		sums = c.summarize(files, &st)
	} else {
		sums = summarizeAll(files, &st)
	}
	if opts.Stats != nil {
		*opts.Stats = st
	}
	return mergeSessions(sums, since, before), nil
}

// mergeSessions groups file summaries into sessions and returns the records
// whose StartedAt is in [since, before), ordered as Sessions documents.
func mergeSessions(sums []*fileSummary, since, before time.Time) []SessionRecord {
	if len(sums) == 0 {
		return nil
	}

	// Union-find over files: shared canonical id or shared event uuid.
	parent := make([]int, len(sums))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	nUUIDs := 0
	for _, s := range sums {
		nUUIDs += len(s.uuids)
	}
	byID := make(map[string]int, len(sums))
	byUUID := make(map[uint64]int, nUUIDs)
	for i, s := range sums {
		if j, ok := byID[s.id]; ok {
			union(i, j)
		} else {
			byID[s.id] = i
		}
		for _, h := range s.uuids {
			if j, ok := byUUID[h]; ok {
				union(i, j)
			} else {
				byUUID[h] = i
			}
		}
		s.uuids = nil // no longer needed; the largest per-file allocation
	}
	groups := map[int][]*fileSummary{}
	for i, s := range sums {
		r := find(i)
		groups[r] = append(groups[r], s)
	}

	var out []SessionRecord
	for _, members := range groups {
		rec := mergeSession(members)
		if !since.IsZero() && rec.StartedAt.Before(since) {
			continue
		}
		if !before.IsZero() && !rec.StartedAt.Before(before) {
			continue
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// summarizeAll reads and summarizes every file (no cache). Results keep the
// input order, so the outcome is deterministic; files that yield no summary are
// dropped.
func summarizeAll(files []transcriptFile, st *SessionsStats) []*fileSummary {
	results := make([]*fileSummary, len(files))
	idx := make([]int, len(files))
	for i := range idx {
		idx[i] = i
	}
	parseFiles(files, idx, func(i int, p parsedFile) {
		if p.read {
			st.Parsed++
		}
		results[i] = p.sum
	})
	return compactSummaries(results)
}

// compactSummaries drops the nil entries, keeping the order.
func compactSummaries(results []*fileSummary) []*fileSummary {
	var sums []*fileSummary
	for _, s := range results {
		if s != nil {
			sums = append(sums, s)
		}
	}
	return sums
}

// parseFiles reads files[i] for every i in idx, each in its own single pass,
// across a bounded worker pool (a real projects tree is many GiB of JSON, so the
// per-file work is CPU-bound and independent), and hands each outcome to collect
// on the calling goroutine, in completion order.
func parseFiles(files []transcriptFile, idx []int, collect func(i int, p parsedFile)) {
	workers := min(runtime.GOMAXPROCS(0), len(idx))
	if workers == 0 {
		return
	}
	type done struct {
		i int
		p parsedFile
	}
	next := make(chan int)
	out := make(chan done, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				out <- done{i, parseFile(files[i].path)}
			}
		}()
	}
	go func() {
		for _, i := range idx {
			next <- i
		}
		close(next)
		wg.Wait()
		close(out)
	}()
	for d := range out {
		collect(d.i, d.p)
	}
}

// fileKey identifies the content of a transcript file for the cache: an
// append-only transcript changes size (and mtime) on every write.
type fileKey struct{ size, mtimeNs int64 }

// parsedFile is the outcome of reading one transcript file.
type parsedFile struct {
	read bool         // the file was opened and read
	key  fileKey      // the size and mtime of the bytes read (valid when read)
	sum  *fileSummary // nil when the file holds no dated event
}

// parseFile opens path once and summarizes exactly the bytes its descriptor's
// size reported when opened, so the summary matches key even while the file is
// being appended to (the bytes past key.size are read by a later call).
func parseFile(path string) parsedFile {
	f, err := os.Open(path)
	if err != nil {
		return parsedFile{}
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return parsedFile{}
	}
	key := fileKey{size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
	return parsedFile{read: true, key: key, sum: summarizeTranscript(path, io.LimitReader(f, key.size))}
}

// transcriptFile is one listed transcript.
type transcriptFile struct {
	path string // projectsDir joined with the slug and file name, as the caller spelled projectsDir
	rel  string // "<slug>/<file>.jsonl", the cache's key within one projectsDir
}

// listTranscriptFiles returns every <projectsDir>/<slug>/<file>.jsonl path that
// is a transcript (not a status sibling), in a deterministic order.
func listTranscriptFiles(projectsDir string) []transcriptFile {
	slugs, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var files []transcriptFile
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		dir := filepath.Join(projectsDir, slug.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isSessionTranscriptName(e.Name()) {
				continue
			}
			files = append(files, transcriptFile{
				path: filepath.Join(dir, e.Name()),
				rel:  slug.Name() + "/" + e.Name(),
			})
		}
	}
	return files
}

// isSessionTranscriptName mirrors pa-monitor's transcript-name rule (this
// module cannot import it): a .jsonl file that is not a <id>.status.jsonl
// rate_limits sibling.
func isSessionTranscriptName(name string) bool {
	return strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".status.jsonl")
}

// sessionLine is the subset of a transcript line the rollup reads. It is a
// private struct (rather than Event) so a line with an odd field elsewhere
// costs only itself, and so Event stays untouched.
type sessionLine struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     *struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   Usage           `json:"usage"`
	} `json:"message"`
}

// msgAgg is one distinct assistant message id within a session.
type msgAgg struct {
	model   string
	tok     TokenTotals // largest value per category over the id's lines
	nonSide bool        // at least one non-sidechain line: counts as a turn
}

// noIDEvent is an assistant line with no message id: it cannot be folded by
// id, so it is kept individually (and de-duplicated by uuid across files).
type noIDEvent struct {
	uuid      uint64
	hasUUID   bool
	model     string
	tok       TokenTotals
	sidechain bool
}

// msgEntry is one distinct assistant message id of a file.
type msgEntry struct {
	id uint64 // hashed message id
	msgAgg
}

// fileSummary is everything the rollup needs from one transcript file, folded
// during its single streaming pass. It is also what the cache stores per file
// (sessions_cache.go encodes every field above the fold-time ones).
type fileSummary struct {
	path        string
	id          string // canonical session id
	first, last time.Time
	cwd, branch string
	model       string

	uuids      []uint64   // every non-empty event uuid (hashed), in file order
	userUUIDs  []uint64   // uuids of user turns, in file order
	userNoUUID int        // user turns with no uuid
	msgs       []msgEntry // one per distinct message id, in first-seen order
	noID       []noIDEvent

	// Fold-time only: nil once the summary is complete.
	msgIdx      map[uint64]int // msgs index by hashed message id
	idFreq      map[string]int
	fileNameID  string
	hasDatedEvt bool
}

// hashUUID is the 64-bit FNV-1a hash used for event uuids and message ids. The
// cache stores its values, so changing it requires a sessionsCacheVersion bump.
func hashUUID(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// summarizeTranscript streams r (the content of the file at path) once. It
// returns nil when the content holds no dated event (such a file yields no
// record).
func summarizeTranscript(path string, r io.Reader) *fileSummary {
	s := &fileSummary{
		path:       path,
		msgIdx:     map[uint64]int{},
		idFreq:     map[string]int{},
		fileNameID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}
	sc := newTranscriptScanner(r)
	for sc.Scan() {
		var ln sessionLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue
		}
		s.fold(&ln)
	}
	// A scanner error (e.g. a line over the ceiling) ends the scan; whatever was
	// folded before it still stands.
	if !s.hasDatedEvt {
		return nil
	}
	s.id = canonicalID(s.idFreq, s.fileNameID)
	s.idFreq, s.msgIdx = nil, nil
	return s
}

func canonicalID(freq map[string]int, fallback string) string {
	best, bestN := "", 0
	for id, n := range freq {
		if n > bestN || (n == bestN && id < best) {
			best, bestN = id, n
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

func (s *fileSummary) fold(ln *sessionLine) {
	if ln.SessionID != "" {
		s.idFreq[ln.SessionID]++
	}
	if ts, err := time.Parse(time.RFC3339Nano, ln.Timestamp); err == nil && !ts.IsZero() {
		ts = ts.UTC()
		if !s.hasDatedEvt || ts.Before(s.first) {
			s.first = ts
		}
		if !s.hasDatedEvt || ts.After(s.last) {
			s.last = ts
		}
		s.hasDatedEvt = true
	}
	var uh uint64
	hasUUID := ln.UUID != ""
	if hasUUID {
		uh = hashUUID(ln.UUID)
		s.uuids = append(s.uuids, uh)
	}
	if s.cwd == "" {
		s.cwd = ln.Cwd
	}
	if s.branch == "" {
		s.branch = ln.GitBranch
	}

	switch ln.Type {
	case "user":
		if ln.IsSidechain || ln.IsMeta || ln.Message == nil || !hasTextBlock(ln.Message.Content) {
			return
		}
		if hasUUID {
			s.userUUIDs = append(s.userUUIDs, uh)
		} else {
			s.userNoUUID++
		}
	case "assistant":
		var id, model string
		var tok TokenTotals
		if ln.Message != nil {
			id, model = ln.Message.ID, ln.Message.Model
			u := ln.Message.Usage
			tok = TokenTotals{
				Input:      int64(u.InputTokens),
				Output:     int64(u.OutputTokens),
				CacheRead:  int64(u.CacheReadInputTokens),
				CacheWrite: int64(u.CacheCreationInputTokens),
			}
		}
		if model != "" && !strings.HasPrefix(model, "<") {
			s.model = model
		}
		if id == "" {
			s.noID = append(s.noID, noIDEvent{uuid: uh, hasUUID: hasUUID, model: model, tok: tok, sidechain: ln.IsSidechain})
			return
		}
		h := hashUUID(id)
		k, seen := s.msgIdx[h]
		if !seen {
			k = len(s.msgs)
			s.msgs = append(s.msgs, msgEntry{id: h})
			s.msgIdx[h] = k
		}
		m := &s.msgs[k].msgAgg
		if model != "" {
			m.model = model
		}
		m.tok.maxInto(tok)
		if !ln.IsSidechain {
			m.nonSide = true
		}
	}
}

// hasTextBlock reports whether a message's content holds at least one text
// block with non-whitespace text; a plain-string content counts as one block.
func hasTextBlock(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var c ContentList
	if err := json.Unmarshal(raw, &c); err != nil {
		return false
	}
	for _, b := range c {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// mergeSession folds the member files of one session into its record.
func mergeSession(members []*fileSummary) SessionRecord {
	sort.Slice(members, func(i, j int) bool {
		if !members[i].first.Equal(members[j].first) {
			return members[i].first.Before(members[j].first)
		}
		if members[i].id != members[j].id {
			return members[i].id < members[j].id
		}
		return members[i].path < members[j].path
	})
	earliest := members[0]
	rec := SessionRecord{
		SessionID:      earliest.id,
		StartedAt:      earliest.first,
		EndedAt:        earliest.last,
		TranscriptPath: earliest.path,
	}

	seenUUID := map[uint64]struct{}{}
	// firstSeen reports whether the uuid is new, recording it.
	firstSeen := func(h uint64) bool {
		if _, dup := seenUUID[h]; dup {
			return false
		}
		seenUUID[h] = struct{}{}
		return true
	}

	msgs := map[uint64]*msgAgg{}
	perModel := map[string]TokenTotals{}
	for _, m := range members {
		if m.first.Before(rec.StartedAt) {
			rec.StartedAt = m.first
		}
		if m.last.After(rec.EndedAt) {
			rec.EndedAt = m.last
		}
		if rec.Cwd == "" {
			rec.Cwd = m.cwd
		}
		if rec.Branch == "" {
			rec.Branch = m.branch
		}
		if m.model != "" {
			rec.Model = m.model
		}
		for _, h := range m.userUUIDs {
			if firstSeen(h) {
				rec.UserTurns++
			}
		}
		rec.UserTurns += m.userNoUUID
		for _, e := range m.msgs {
			a := &e.msgAgg
			cur := msgs[e.id]
			if cur == nil {
				cur = &msgAgg{}
				msgs[e.id] = cur
			}
			if a.model != "" {
				cur.model = a.model
			}
			cur.tok.maxInto(a.tok)
			cur.nonSide = cur.nonSide || a.nonSide
		}
		for _, e := range m.noID {
			if e.hasUUID && !firstSeen(e.uuid) {
				continue
			}
			if !e.sidechain {
				rec.AssistantTurns++
			}
			addModelTokens(perModel, e.model, e.tok)
		}
	}
	for _, a := range msgs {
		if a.nonSide {
			rec.AssistantTurns++
		}
		addModelTokens(perModel, a.model, a.tok)
	}
	if len(perModel) > 0 {
		rec.ModelTokens = perModel
	}
	for _, t := range perModel {
		rec.Tokens.add(t)
	}
	return rec
}

// addModelTokens folds tok into perModel[model]; an all-zero usage block
// contributes nothing.
func addModelTokens(perModel map[string]TokenTotals, model string, tok TokenTotals) {
	if tok.isZero() {
		return
	}
	t := perModel[model]
	t.add(tok)
	perModel[model] = t
}
