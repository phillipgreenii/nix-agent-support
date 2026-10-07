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
// HISTORY (bead pg2-n7da9). One thing is deliberately KEPT beyond "live": a bounded,
// timestamped history of the events that have left the queue, as opArchive records
// (one per departed event: its type, enqueue instants, each accept's listener and
// settle/start instants, and its eviction instant and reason). A replay ignores
// them, so replay equivalence is unchanged; they exist so queue.jsonl still answers
// "how long did this listener wait vs run" for events that were evicted before the
// compaction ran, rather than losing ~all of it on every restart (the log is
// compacted at startup). The newest maxArchiveRecords are kept; older ones fall out
// of the fold, so the history cannot grow the log without bound.
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

// maxArchiveRecords bounds the opArchive history a compaction keeps (the newest
// win). At roughly 250-350 bytes a record this is well under 1 MiB, far below the
// default compact_threshold_bytes (8 MiB), so history cannot by itself keep the log
// above the compaction trigger.
const maxArchiveRecords = 2048

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

// ArchiveBudgeter is implemented by a Store whose compaction keeps the timestamped
// opArchive history (FileStore): SetArchiveBudget caps that history's encoded size
// in bytes (0 removes the cap; the record-count bound maxArchiveRecords always
// applies). The queue sets it from the log-size limits so history is the first
// thing a limit-driven compaction gives up and can never be what keeps a log over
// its soft threshold.
type ArchiveBudgeter interface {
	SetArchiveBudget(bytes int64)
}

// archiveBudgetDivisor sets the history's byte budget to 1/N of the soft log limit.
const archiveBudgetDivisor = 10

// CompactStats describes one compaction.
type CompactStats struct {
	BytesBefore   int64
	BytesAfter    int64
	RecordsBefore int
	RecordsAfter  int
	Duration      time.Duration
}

// CompactPlan is what a compaction WOULD do, computed without changing anything
// (the dry run): the same fold a real compaction runs, minus the write.
type CompactPlan struct {
	// BytesBefore / RecordsBefore describe the log as it is; BytesAfter /
	// RecordsAfter the compacted log a real run would leave.
	BytesBefore, BytesAfter     int64
	RecordsBefore, RecordsAfter int
	// EventsKept are the events still live (retained); EventsDropped the events
	// whose final state is evicted and whose records compaction would discard.
	EventsKept, EventsDropped int
	// GatesKept are the active gates the compacted log would still carry.
	GatesKept int
	// Torn is true when the log has an undecodable line: everything from it on is
	// ignored by a replay and a real compaction would discard it.
	Torn bool
}

// NoProgress is true when a real compaction would not make the log smaller.
func (p CompactPlan) NoProgress() bool { return p.BytesAfter >= p.BytesBefore }

// Planner is implemented by a Store that can compute a CompactPlan without
// writing anything. Like Compactor it is OPTIONAL (like RestoreObserver), so a
// store double that only implements Store keeps compiling.
type Planner interface {
	PlanCompaction() (CompactPlan, error)
}

// CompactionInfo describes the most recent compaction this process ran.
type CompactionInfo struct {
	// At is when it finished (the queue's clock).
	At time.Time
	// Trigger is what started it: "startup", "threshold" (compact_threshold_bytes),
	// "limit" (the soft log-size threshold) or "manual" (log compact).
	Trigger string
	CompactStats
}

// foldEvent is one live event in a logFold.
type foldEvent struct {
	pos     int           // FIFO position: assigned at first insert, kept across a re-enqueue
	enq     Record        // the latest enqueue record for the id
	accepts []AcceptStamp // the accepts, in first-accept order (listener, settle and start instants)
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
	// dropped counts events a later evict removed (the dead history compaction
	// discards); only the dry-run plan reports it.
	dropped int
	// archive is the bounded history of departed events (opArchive), oldest first,
	// trimmed to the newest maxArchiveRecords as it grows.
	archive []Record
	// archiveBudget, when > 0, additionally caps the encoded size of the archive a
	// compaction keeps (the newest records win), so history can never be what holds
	// a log above its size limits (see ArchiveBudgeter). 0: no byte cap.
	archiveBudget int64
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
		for _, a := range e.accepts {
			if a.ListenerID == r.ListenerID {
				return
			}
		}
		e.accepts = append(e.accepts, AcceptStamp{ListenerID: r.ListenerID, At: r.At, StartedAt: r.StartedAt})
	case opEvict:
		if e, ok := f.events[r.EventID]; ok {
			f.dropped++
			f.addArchive(archiveRecord(e, r))
		}
		delete(f.events, r.EventID)
	case opArchive:
		f.addArchive(r)
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

// addArchive appends one history record, dropping the oldest beyond the bound.
func (f *logFold) addArchive(r Record) {
	f.archive = append(f.archive, r)
	if over := len(f.archive) - maxArchiveRecords; over > 0 {
		f.archive = append(f.archive[:0:0], f.archive[over:]...)
	}
}

// archiveRecord builds the history record for event e leaving the queue by evict.
func archiveRecord(e *foldEvent, evict Record) Record {
	return Record{
		Op:        opArchive,
		EventID:   e.enq.EventID,
		Type:      e.enq.Type,
		At:        e.enq.At,
		EvictedAt: evict.At,
		Reason:    evict.Reason,
		Accepts:   e.accepts,
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

	archive := f.archive
	if f.archiveBudget > 0 {
		var total int64
		keepFrom := len(archive)
		for i := len(archive) - 1; i >= 0; i-- {
			total += encodedLen(archive[i])
			if total > f.archiveBudget {
				break
			}
			keepFrom = i
		}
		archive = archive[keepFrom:]
	}
	out := make([]Record, 0, len(seenOnly)+len(archive)+len(live)+len(gateTypes))
	for _, t := range seenOnly {
		out = append(out, Record{Op: opSeen, Type: t})
	}
	out = append(out, archive...)
	for _, e := range live {
		out = append(out, e.enq)
		for _, a := range e.accepts {
			out = append(out, Record{Op: opAccept, EventID: e.enq.EventID, ListenerID: a.ListenerID, At: a.At, StartedAt: a.StartedAt})
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

// planFrom folds the log bytes r (size of them, torn tail included) into the
// CompactPlan a real compaction of them would execute. It writes nothing: the
// compacted size is the encoded length of the records a real run would write.
func planFrom(r io.Reader, size int64, archiveBudget int64) (CompactPlan, error) {
	fold := newLogFold()
	fold.archiveBudget = archiveBudget
	torn, err := scanRecords(r, fold.apply)
	if err != nil {
		return CompactPlan{}, err
	}
	live := fold.records()
	plan := CompactPlan{
		BytesBefore:   size,
		RecordsBefore: fold.applied,
		RecordsAfter:  len(live),
		EventsKept:    len(fold.events),
		EventsDropped: fold.dropped,
		GatesKept:     len(fold.gates),
		Torn:          torn,
	}
	for _, rec := range live {
		b, merr := json.Marshal(rec)
		if merr != nil {
			return CompactPlan{}, merr
		}
		plan.BytesAfter += int64(len(b)) + 1
	}
	return plan, nil
}

// PlanCompactionFile computes the CompactPlan for the log at path WITHOUT opening
// it as a store: it only reads, takes no lock, creates no file and leaves no
// temp file, so it is safe beside a live daemon (it folds the bytes present when
// it stats the file) and is how an offline dry run works. A log that does not
// exist plans to nothing.
func PlanCompactionFile(path string) (CompactPlan, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CompactPlan{}, nil
		}
		return CompactPlan{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return CompactPlan{}, err
	}
	return planFrom(io.NewSectionReader(f, 0, fi.Size()), fi.Size(), 0)
}

// PlanCompaction is the dry run of Compact: it folds the log's current prefix
// (appends keep landing meanwhile, untouched) and reports what Compact would
// leave, changing nothing — no temp file, no rename, no handle swap.
func (s *FileStore) PlanCompaction() (CompactPlan, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return CompactPlan{}, os.ErrClosed
	}
	n := s.size
	s.mu.Unlock()
	rf, err := os.Open(s.path)
	if err != nil {
		return CompactPlan{}, err
	}
	defer func() { _ = rf.Close() }()
	return planFrom(io.NewSectionReader(rf, 0, n), n, s.archiveBudget.Load())
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
	fold.archiveBudget = s.archiveBudget.Load()
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
	s.poisoned = false // the new file has no partial tail
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

// LastCompaction reports the most recent compaction this process completed
// (startup, threshold, limit or manual); ok is false before the first one.
// Lock-free; safe from the status path.
func (q *Queue) LastCompaction() (info CompactionInfo, ok bool) {
	p := q.lastCompaction.Load()
	if p == nil {
		return CompactionInfo{}, false
	}
	return *p, true
}

// PlanCompaction is the dry run of CompactNow: what a compaction would do right
// now, changing nothing. Safe to call concurrently with every other Queue method.
func (q *Queue) PlanCompaction() (CompactPlan, error) {
	p, ok := q.store.(Planner)
	if !ok {
		return CompactPlan{}, ErrCompactionUnsupported
	}
	return p.PlanCompaction()
}

// CompactNow compacts the log synchronously, regardless of threshold. Safe to
// call concurrently with every other Queue method.
func (q *Queue) CompactNow() (CompactStats, error) { return q.compact("manual") }

// CompactResult is the outcome of an operator's compaction request
// (`pg-router log compact [--dry-run]`).
type CompactResult struct {
	// DryRun is true when nothing was (or could have been) changed.
	DryRun bool
	// Compacted is true when a real run rewrote the log. A real run that would
	// make no progress (the log is already live state only) rewrites nothing and
	// reports false: there is nothing to refuse and nothing to do.
	Compacted bool
	// Plan is the fold computed BEFORE the run: what the run was about to do (and,
	// for a dry run, all there is to report).
	Plan CompactPlan
	// Stats is what the run actually did; zero unless Compacted.
	Stats CompactStats
}

// CompactManual serves an operator's `log compact` (trigger "manual") or, with
// dryRun, its dry run. A real run is skipped when the plan finds no progress
// possible: rewriting a log to the same size would only cost an fsync and a
// rename. Safe to call concurrently with every other Queue method.
func (q *Queue) CompactManual(dryRun bool) (CompactResult, error) {
	plan, err := q.PlanCompaction()
	if err != nil {
		return CompactResult{}, err
	}
	res := CompactResult{DryRun: dryRun, Plan: plan}
	if dryRun || plan.NoProgress() {
		return res, nil
	}
	st, err := q.CompactNow()
	if err != nil {
		return res, err
	}
	res.Compacted, res.Stats = true, st
	return res, nil
}

// CompactFileOffline serves `log compact [--dry-run]` when no daemon is running:
// a dry run only reads the file (PlanCompactionFile: no lock, no temp file, no
// side effect). A real run takes the exclusive log lock itself and fails with
// *ErrLogLocked if another process holds it, so it can never compact a log a
// live daemon is appending to.
func CompactFileOffline(path string, dryRun bool) (CompactResult, error) {
	if dryRun {
		plan, err := PlanCompactionFile(path)
		return CompactResult{DryRun: true, Plan: plan}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return CompactResult{}, nil // nothing to compact, and no reason to create the log
	}
	fs, err := NewFileStore(path)
	if err != nil {
		return CompactResult{}, err
	}
	defer func() { _ = fs.Close() }()
	plan, err := fs.PlanCompaction()
	if err != nil {
		return CompactResult{}, err
	}
	res := CompactResult{Plan: plan}
	if plan.NoProgress() {
		return res, nil
	}
	st, err := fs.Compact()
	if err != nil {
		return res, err
	}
	res.Compacted, res.Stats = true, st
	return res, nil
}

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
	q.lastCompaction.Store(&CompactionInfo{At: q.now(), Trigger: trigger, CompactStats: st})
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
