package eventqueue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// This file is the write-ahead log's COMPACTION (bead pg2-8e0m6). The log is
// append-only — an eviction is itself an appended record — so without
// compaction it grows without bound and Queue.replay re-reads all of it on every
// start. Compaction folds the log down to the records a replay would actually
// keep.
//
// WHAT "LIVE" MEANS. compact(L) is the shortest record sequence whose replay
// rebuilds exactly the state Replay(L) rebuilds: the retained events in their
// FIFO order (each with its resolved At/ExpiresAt/EnqueuedAt), the accepts of
// those events, the active gate projection (last gate_set per TYPE, unless a
// later gate_cleared/gate_expired removed it), and the "ever enqueued" type
// set. Everything else is history a replay discards: enqueue/accept records of
// an event a later evict removed, the evict itself, a gate_set that a later
// set/clear/expiry superseded, and the clear/expiry records.
//
// It is deliberately NOT a retention sweep. Whether an unevicted event's
// retention is over (INV-EVT-1) depends on which listeners are bound and on the
// clock, neither of which exist at the store level or at startup; the queue's
// Expire sweep owns that decision and writes the evict record, and the next
// compaction then drops the event. This also keeps equivalence trivially exact:
// compaction never makes a retention decision of its own, so it can never differ
// from what replay would do.

// compactTempSuffix names the temp file the new log is written to. It lives in
// the log's own directory so the final rename is atomic (same filesystem).
const compactTempSuffix = ".compact.tmp"

func compactTempPath(logPath string) string { return logPath + compactTempSuffix }

// Compaction steps, reported to FileStore.compactHook (a crash-injection seam).
const (
	// compactStageTempWritten: the compacted log is fully written to the temp
	// file but not yet fsynced.
	compactStageTempWritten = "temp-written"
	// compactStageTempSynced: the temp file is fsynced; the old log is still the
	// log.
	compactStageTempSynced = "temp-synced"
	// compactStageRenamed: the temp file has been renamed over the log path.
	compactStageRenamed = "renamed"
	// compactStageDirSynced: the directory entry is fsynced; the new log is
	// durably the log.
	compactStageDirSynced = "dir-synced"
)

// ErrCompactionUnsupported is returned by Queue.CompactNow when the queue's
// Store does not implement Compactor.
var ErrCompactionUnsupported = errors.New("eventqueue: store does not support compaction")

// errTornPrefix aborts a RUNTIME compaction whose input hit an undecodable line
// before records that were appended after the compaction started: re-attaching
// those later records would resurrect lines the existing replay ignores.
var errTornPrefix = errors.New("eventqueue: log has an undecodable record mid-file; compaction skipped")

// LogSizer is implemented by a Store that can report its durable log's size in
// bytes (the size gauge, status and the TUI read it).
type LogSizer interface {
	LogSize() int64
}

// Compactor is implemented by a Store that can rewrite its log down to live
// state. Compact MUST be safe to call concurrently with Append/AppendBatch —
// the queue runs it off its own lock — and MUST lose and reorder nothing.
type Compactor interface {
	LogSizer
	Compact() (CompactStats, error)
}

// CompactStats describes one compaction.
type CompactStats struct {
	BytesBefore   int64
	BytesAfter    int64
	RecordsBefore int
	RecordsAfter  int
	Duration      time.Duration
}

// foldEvent is one live event in a logFold.
type foldEvent struct {
	pos     int      // FIFO position: assigned at first insert, kept across a re-enqueue
	enq     Record   // the latest enqueue record for the id
	accepts []string // listener ids that accepted it, in first-accept order
}

// logFold replays records into the minimal live state, mirroring Queue.replay's
// semantics record for record (that function is the authority; keep them in
// step).
type logFold struct {
	events  map[string]*foldEvent
	nextPos int
	gates   map[string]Record
	seen    map[string]struct{}
	applied int
}

func newLogFold() *logFold {
	return &logFold{
		events: map[string]*foldEvent{},
		gates:  map[string]Record{},
		seen:   map[string]struct{}{},
	}
}

func (f *logFold) apply(r Record) {
	f.applied++
	switch r.Op {
	case opEnqueue:
		if e, ok := f.events[r.EventID]; ok {
			// Queue.replay: a repeated enqueue of a live id replaces the entry (its
			// accepts reset) but keeps its FIFO position and does not re-mark the type.
			e.enq = r
			e.accepts = nil
			return
		}
		f.events[r.EventID] = &foldEvent{pos: f.nextPos, enq: r}
		f.nextPos++
		if r.Type != "" {
			f.seen[r.Type] = struct{}{}
		}
	case opAccept:
		e, ok := f.events[r.EventID]
		if !ok {
			return
		}
		for _, l := range e.accepts {
			if l == r.ListenerID {
				return
			}
		}
		e.accepts = append(e.accepts, r.ListenerID)
	case opEvict:
		delete(f.events, r.EventID)
	case opGateSet:
		f.gates[r.GateType] = r
	case opGateCleared, opGateExpired:
		delete(f.gates, r.GateType)
	case opSeen:
		if r.Type != "" {
			f.seen[r.Type] = struct{}{}
		}
	}
}

// records emits the live state as a record sequence whose replay rebuilds it.
func (f *logFold) records() []Record {
	live := make([]*foldEvent, 0, len(f.events))
	liveTypes := map[string]struct{}{}
	for _, e := range f.events {
		live = append(live, e)
		liveTypes[e.enq.Type] = struct{}{}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].pos < live[j].pos })

	// Types enqueued at some point whose events are all gone: keep them "seen".
	var seenOnly []string
	for t := range f.seen {
		if _, ok := liveTypes[t]; !ok {
			seenOnly = append(seenOnly, t)
		}
	}
	sort.Strings(seenOnly)

	gateTypes := make([]string, 0, len(f.gates))
	for t := range f.gates {
		gateTypes = append(gateTypes, t)
	}
	sort.Strings(gateTypes)

	out := make([]Record, 0, len(seenOnly)+len(live)+len(gateTypes))
	for _, t := range seenOnly {
		out = append(out, Record{Op: opSeen, Type: t})
	}
	for _, e := range live {
		out = append(out, e.enq)
		for _, l := range e.accepts {
			out = append(out, Record{Op: opAccept, EventID: e.enq.EventID, ListenerID: l})
		}
	}
	for _, t := range gateTypes {
		out = append(out, f.gates[t])
	}
	return out
}

// compactRecords returns the compacted form of recs: a record sequence whose
// replay is equivalent to replaying recs. It is the pure core of every Store's
// compaction.
func compactRecords(recs []Record) []Record {
	f := newLogFold()
	for _, r := range recs {
		f.apply(r)
	}
	return f.records()
}

// syncDir fsyncs a directory so a rename inside it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// Compact rewrites the log down to live state without blocking appends for the
// bulk of the work. Approach (the "rewrite then splice the tail" scheme, as an
// append-only-file rewrite does it):
//
//  1. Under s.mu, note n, the log's current byte length.
//  2. WITHOUT any lock, stream-fold the first n bytes (appends keep landing
//     after n, untouched) and write the compacted records to a temp file in the
//     same directory.
//  3. Under s.mu again — appends wait, briefly — copy the bytes appended since
//     step 1 (the tail) onto the temp file verbatim, fsync it, rename it over the
//     log, fsync the directory, and swap the append handle to the new file.
//
// Nothing is lost or reordered: every record written after step 1 is in the
// tail, and the tail follows the compacted prefix exactly as it followed the
// original one. Because compact(P) replays to the same state as P, compact(P)
// followed by T replays to the same state as P followed by T.
//
// Crash safety: until the rename the log path still holds the complete old log;
// from the rename on it holds the complete, fsynced new log. The temp file is
// never the log, and NewFileStore removes a leftover one.
//
// At startup nothing appends, so the tail is empty and the call degenerates to a
// plain rewrite — which also drops a torn trailing line (steps 1-2 stop at it).
func (s *FileStore) Compact() (stats CompactStats, err error) {
	s.compactMu.Lock()
	defer s.compactMu.Unlock()
	began := time.Now()
	defer func() { stats.Duration = time.Since(began) }()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return stats, os.ErrClosed
	}
	n := s.size
	s.mu.Unlock()
	stats.BytesBefore = n

	rf, err := os.Open(s.path)
	if err != nil {
		return stats, err
	}
	defer func() { _ = rf.Close() }()

	fold := newLogFold()
	torn, err := scanRecords(io.NewSectionReader(rf, 0, n), fold.apply)
	if err != nil {
		return stats, err
	}
	stats.RecordsBefore = fold.applied
	live := fold.records()
	stats.RecordsAfter = len(live)

	tmpPath := compactTempPath(s.path)
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return stats, err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	bw := bufio.NewWriterSize(tmp, 256*1024)
	var written int64
	for _, rec := range live {
		b, merr := json.Marshal(rec)
		if merr != nil {
			return stats, merr
		}
		b = append(b, '\n')
		if _, werr := bw.Write(b); werr != nil {
			return stats, werr
		}
		written += int64(len(b))
	}
	if err := bw.Flush(); err != nil {
		return stats, err
	}
	s.hook(compactStageTempWritten)

	s.mu.Lock()
	defer s.mu.Unlock()
	tail := s.size - n
	if tail > 0 {
		if torn {
			return stats, errTornPrefix
		}
		copied, cerr := io.Copy(tmp, io.NewSectionReader(rf, n, tail))
		if cerr != nil {
			return stats, cerr
		}
		if copied != tail {
			return stats, fmt.Errorf("eventqueue: compaction tail copy short: %d of %d bytes", copied, tail)
		}
		written += tail
		stats.BytesBefore += tail
	}
	if err := tmp.Sync(); err != nil {
		return stats, err
	}
	s.hook(compactStageTempSynced)
	if err := os.Rename(tmpPath, s.path); err != nil {
		return stats, err
	}
	renamed = true // tmp is the log now (its handle follows the inode)
	s.hook(compactStageRenamed)
	// From here the new log IS the log: swap the append handle first so a
	// directory-fsync failure cannot leave appends going to the unlinked old file.
	old := s.f
	s.f = tmp
	s.size = written
	s.sizeGauge.Store(written)
	_ = old.Close()
	stats.BytesAfter = written
	if err := syncDir(filepath.Dir(s.path)); err != nil {
		return stats, fmt.Errorf("eventqueue: compaction fsync dir: %w", err)
	}
	s.hook(compactStageDirSynced)
	return stats, nil
}

func (s *FileStore) hook(stage string) {
	if s.compactHook != nil {
		s.compactHook(stage)
	}
}

// --- Queue side -----------------------------------------------------------

// WithCompaction enables log compaction. thresholdBytes > 0 turns on RUNTIME
// compaction: once the log exceeds it, the queue's Expire sweep starts a
// background compaction (single-flight, off the queue lock). After a compaction
// the trigger rises to twice the compacted size when that exceeds thresholdBytes,
// so a live set that itself outgrows the threshold cannot make the queue
// compact on every sweep. onStart compacts the log in New, before the queue
// replays it or accepts any event. A Store that is not a Compactor is left alone.
func WithCompaction(thresholdBytes int64, onStart bool) Option {
	return func(q *Queue) {
		q.compactThreshold = thresholdBytes
		q.compactOnStart = onStart
	}
}

// LogSize reports the durable log's size in bytes (0 when the Store cannot say).
// Lock-free; safe from the status path and metric callbacks.
func (q *Queue) LogSize() int64 {
	if ls, ok := q.store.(LogSizer); ok {
		return ls.LogSize()
	}
	return 0
}

// Compactions reports how many compactions have completed in this process.
func (q *Queue) Compactions() int64 { return q.compactions.Load() }

// CompactNow compacts the log synchronously, regardless of threshold. Safe to
// call concurrently with every other Queue method.
func (q *Queue) CompactNow() (CompactStats, error) { return q.compact("manual") }

func (q *Queue) compact(trigger string) (CompactStats, error) {
	c, ok := q.store.(Compactor)
	if !ok {
		return CompactStats{}, ErrCompactionUnsupported
	}
	st, err := c.Compact()
	if err != nil {
		return st, err
	}
	q.compactions.Add(1)
	next := q.compactThreshold
	if twice := 2 * st.BytesAfter; twice > next {
		next = twice
	}
	q.compactNext.Store(next)
	slog.Info("eventqueue: queue log compacted",
		"trigger", trigger,
		"bytesBefore", st.BytesBefore, "bytesAfter", st.BytesAfter,
		"recordsBefore", st.RecordsBefore, "recordsAfter", st.RecordsAfter,
		"duration", st.Duration.String())
	return st, nil
}

// maybeCompact starts a background compaction when the log has outgrown the
// threshold. Called after each Expire sweep; never blocks on the compaction.
func (q *Queue) maybeCompact() {
	if q.compactThreshold <= 0 {
		return
	}
	c, ok := q.store.(Compactor)
	if !ok || c.LogSize() < q.compactNext.Load() {
		return
	}
	if !q.compacting.CompareAndSwap(false, true) {
		return // one already running
	}
	q.compactWG.Add(1)
	go func() {
		defer q.compactWG.Done()
		defer q.compacting.Store(false)
		if _, err := q.compact("threshold"); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.Error("eventqueue: queue log compaction failed; the log keeps growing until a later attempt succeeds", "err", err)
			// Back off: do not retry on the very next sweep.
			q.compactNext.Store(c.LogSize() + q.compactThreshold)
		}
	}()
}

// waitCompaction blocks until any background compaction has finished.
func (q *Queue) waitCompaction() { q.compactWG.Wait() }
