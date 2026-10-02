package eventqueue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// opKind is the write-ahead-log record kind.
type opKind string

const (
	opEnqueue opKind = "enqueue"
	opAccept  opKind = "accept"
	opEvict   opKind = "evict"
	// The Gate Registry's three record kinds (bead pg2-h63eu, gate.go): they ride
	// the SAME log, through the SAME Append/AppendBatch interface, as the event
	// records above. A replay folds them into the active-gate projection
	// (Queue.gates) rather than the event queue.
	opGateSet     opKind = "gate_set"
	opGateCleared opKind = "gate_cleared"
	opGateExpired opKind = "gate_expired"
	// opSeen is written ONLY by log compaction (compact.go): it carries, in Type,
	// an event TYPE the log once enqueued but whose events are all gone by now.
	// A replay marks every enqueued type "ever seen" (Queue.UnmatchedBindings), so
	// a compaction that dropped the enqueue records outright would silently
	// change that projection; one opSeen record per such type keeps it intact. A
	// replay of an older binary ignores the unknown kind.
	opSeen opKind = "seen"
)

// Record is one durable write-ahead-log entry. The log is append-only; queue
// state is reconstructed by replaying it in order (Store.Replay). Delivery is
// at-least-once because an accept Record is written only AFTER the handler
// confirms acceptance (ADR 0031 req 4) — a crash between the in-memory accept
// and this write re-offers the event on restart (at-most-one redelivery,
// absorbed by idempotent handlers, INV-EVT-2).
//
// There are exactly THREE EVENT record kinds (plus the Gate Registry's three
// gate_* kinds, gate.go) and none of them is an attempt log: the core keeps no
// attempt history (INV-EVT-4, DEC-EVENT-1), so a pre-accept decline — even the
// final one past `expiresAt` — writes nothing. An event LEAVING the
// queue is recorded (opEvict) rather than re-derived on replay, because a
// past-expiry event is not necessarily finished: it is retained until every
// matching handler has had the one attempt INV-EVT-1 owes it, and only the
// process that made those attempts knows they happened.
type Record struct {
	Op opKind `json:"op"`
	// EventID is set on every record.
	EventID string `json:"eventId"`
	// Enqueue fields. At and ExpiresAt are the RESOLVED instants (Event.Resolve),
	// so a replay reconstructs the same expiry bound rather than re-defaulting it
	// against a clock that has since moved.
	Type          string         `json:"type,omitempty"`
	SchemaVersion string         `json:"schemaVersion,omitempty"`
	At            time.Time      `json:"at,omitzero"`
	ExpiresAt     time.Time      `json:"expiresAt,omitzero"`
	EnqueuedAt    time.Time      `json:"enqueuedAt,omitzero"`
	Payload       map[string]any `json:"payload,omitempty"`
	// Accept fields.
	ListenerID string `json:"listenerId,omitempty"`
	// Gate fields (gate_set / gate_cleared / gate_expired; gate.go). At is the
	// instant the record was written (a gate_set's SetAt) and ExpiresAt a
	// gate_set's lease end (zero: no lease). Owner is the setter's identity on a
	// gate_set and the clearing caller's on a gate_cleared — debug only.
	GateType    string `json:"gateType,omitempty"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

// Store is the durable persistence seam. The queue writes enqueue/accept/evict
// records and replays them on startup. It is an interface so tests can inject a
// fault-injecting fake (crash-window simulation) and an in-memory double, per
// ADR 0031's "storage mechanism is a realization choice".
//
// Store is not required to synchronize its own appends: every call is serialized
// by the queue's own mutex (q.mu). The one exception is compaction (Compactor):
// a Store that offers it runs it OFF the queue lock, and so MUST make it safe
// against concurrent Append/AppendBatch itself (FileStore does, with its own
// mutex). AppendBatch's caller chooses the batch's contents, and the BATCH — not the individual Record — is
// the caller's atomicity unit: either every record in one AppendBatch call is
// durable before it returns, or a caller MUST NOT treat any of them as durable.
type Store interface {
	// Append durably records one operation. It MUST return only after the record
	// is persisted (the queue's crash-window semantics depend on this ordering).
	Append(rec Record) error
	// AppendBatch durably records every rec in recs as one atomic unit — one
	// underlying write and one fsync, not one per record — so a caller with
	// several records to persist together (e.g. an evict paired with the
	// re-enqueue that displaced it, or a pass's worth of evictions) pays a single
	// fsync instead of len(recs). It MUST return only after every record in recs
	// is persisted, or persist none of them.
	AppendBatch(recs []Record) error
	// Replay returns every persisted record in append order.
	Replay() ([]Record, error)
	// Close releases the underlying resource.
	Close() error
}

// recordFromEvent builds an enqueue Record for an ALREADY-RESOLVED event
// (Event.Resolve), also capturing the ingest (enqueue) instant. EnqueuedAt is
// kept alongside the resolved At even though the two coincide for an event that
// carried no source stamp: when a source DID stamp `at`, the pair records both
// what the source claimed and when the core actually took it.
func recordFromEvent(e Event, enqueuedAt time.Time) Record {
	return Record{
		Op:            opEnqueue,
		EventID:       e.ID,
		Type:          e.Type,
		SchemaVersion: e.SchemaVersion,
		At:            e.At,
		ExpiresAt:     e.ExpiresAt,
		EnqueuedAt:    enqueuedAt,
		Payload:       e.Payload,
	}
}

// event reconstructs the (resolved) Event carried by an enqueue Record.
func (r Record) event() Event {
	return Event{
		SchemaVersion: r.SchemaVersion,
		ID:            r.EventID,
		Type:          r.Type,
		At:            r.At,
		ExpiresAt:     r.ExpiresAt,
		Payload:       r.Payload,
	}
}

// FileStore is a JSONL write-ahead log on disk — the default durable Store.
// Each line is one Record. Append writes and fsyncs one line; AppendBatch writes
// every line of the batch in one Write call and fsyncs once for the whole batch.
// Either way a persisted line survives a crash.
//
// Appends are serialized by the queue's own mutex (see Store), but FileStore
// ALSO carries an internal mutex because Compact (compact.go) runs on its own
// goroutine at runtime and swaps the underlying file: mu guards f, size and
// closed, and compactMu serializes Compact/Close against each other.
type FileStore struct {
	path string

	compactMu sync.Mutex // serializes Compact calls and Close against a running Compact

	mu     sync.Mutex
	f      *os.File
	size   int64 // logical byte length of the log file; guarded by mu
	closed bool
	// poisoned is set when a failed write could not be rolled back (truncate-back
	// failed too), leaving a partial line at the tail that the next append would
	// fuse onto. Every append fails until a Compact rewrites the file. Guarded by mu.
	poisoned bool

	sizeGauge atomic.Int64 // lock-free mirror of size, for LogSize

	// compactHook, when non-nil, is called at each durable step of Compact with
	// the step's name — a test seam for crash injection (see compactStage*).
	compactHook func(stage string)

	// writeFn, when non-nil, replaces f.Write — a test seam for short-write and
	// ENOSPC injection (the real file is still the one truncated back).
	writeFn func(f *os.File, b []byte) (int, error)
}

// NewFileStore opens (creating parent dirs) the append-only WAL at path. A
// leftover compaction temp file from a crashed run is removed: it was never
// renamed over the log, so it is not part of the log.
func NewFileStore(path string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(compactTempPath(path)); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("eventqueue: remove stale compaction temp file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	s := &FileStore{path: path, f: f, size: fi.Size()}
	s.sizeGauge.Store(s.size)
	return s, nil
}

// LogSize reports the log file's current byte length. Lock-free.
func (s *FileStore) LogSize() int64 { return s.sizeGauge.Load() }

// EncodeError marks a record that could not be marshalled. It is a fault in the
// record's content, NOT a failure of the log's storage, so the queue does not
// treat it as an unwritable log.
type EncodeError struct{ Err error }

func (e *EncodeError) Error() string { return "eventqueue: encode record: " + e.Err.Error() }
func (e *EncodeError) Unwrap() error { return e.Err }

// writeLocked writes b to the log and fsyncs. Caller holds s.mu.
//
// On ANY write or sync error the file is truncated back to its pre-write length
// and the logical size restored (bead pg2-5d3ui, hardening what pg2-8e0m6's
// item 9 asked for): a short write (ENOSPC) would otherwise leave a partial line
// that the next successful append fuses onto, silently losing that later record
// — and recovery from an unwritable log (Queue.EnforceLogLimits) depends on a
// retried append starting on a line boundary. If the truncate itself fails the
// store is poisoned until a Compact rewrites the file.
func (s *FileStore) writeLocked(b []byte) error {
	if s.closed {
		return os.ErrClosed
	}
	if s.poisoned {
		return errPoisoned
	}
	prev := s.size
	write := s.f.Write
	if s.writeFn != nil {
		write = func(b []byte) (int, error) { return s.writeFn(s.f, b) }
	}
	n, err := write(b)
	if err == nil {
		err = s.f.Sync()
	}
	if err != nil {
		if terr := s.f.Truncate(prev); terr != nil {
			s.poisoned = true
			s.size += int64(n)
			s.sizeGauge.Store(s.size)
			return fmt.Errorf("%w (and rolling the partial write back failed: %v)", err, terr)
		}
		return err
	}
	s.size += int64(n)
	s.sizeGauge.Store(s.size)
	return nil
}

var errPoisoned = errors.New("eventqueue: log has an unrecoverable partial write; appends are refused until a compaction rewrites it")

// Probe checks that the log's directory can still be written to without
// touching the log itself: it writes and fsyncs a small file beside it and
// removes it. It is Queue.EnforceLogLimits' recovery probe for an unwritable log
// (there is no no-op log record, so appending one is not an option); a
// compaction writes its temp file in the same directory and is the other,
// heavier, probe. Returns nil when the write and fsync succeeded.
func (s *FileStore) Probe() error {
	p := s.path + probeSuffix
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(p) }()
	if _, err := f.Write(make([]byte, probeBytes)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	poisoned := s.poisoned
	s.mu.Unlock()
	if poisoned {
		// The tail holds a partial line a failed rollback left behind; a compaction
		// rewrites the file from its decodable prefix and clears the condition.
		_, err := s.Compact()
		return err
	}
	return nil
}

const (
	probeSuffix = ".probe"
	probeBytes  = 4096
)

// Append marshals and writes one record as a line, then fsyncs.
func (s *FileStore) Append(rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return &EncodeError{Err: err}
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(b)
}

// AppendBatch marshals every record in recs as its own line — the same per-line
// shape Append produces, so Replay's line-based parsing is unchanged — but
// performs exactly one Write and one Sync for the whole slice, collapsing what
// would otherwise be len(recs) fsyncs (one per Append call) into one.
func (s *FileStore) AppendBatch(recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	var buf []byte
	for _, rec := range recs {
		b, err := json.Marshal(rec)
		if err != nil {
			return &EncodeError{Err: err}
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(buf)
}

// scanRecords decodes the JSONL stream r, calling fn for each record in order.
// A partially-written trailing line (torn by a crash mid-write) is tolerated:
// scanning stops at the first undecodable line, mirroring a real WAL's
// truncate-on-torn-tail recovery, and torn reports that it happened.
func scanRecords(r io.Reader, fn func(Record)) (torn bool, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			// Torn trailing record from a crash mid-write: stop here; everything
			// before it is intact and durable.
			return true, nil
		}
		fn(rec)
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("eventqueue: replay scan: %w", err)
	}
	return false, nil
}

// Replay reads the WAL back into records. A partially-written trailing line
// (torn by a crash mid-write) is tolerated: parsing stops at the first
// undecodable line, mirroring a real WAL's truncate-on-torn-tail recovery.
func (s *FileStore) Replay() ([]Record, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var recs []Record
	_, err = scanRecords(f, func(r Record) { recs = append(recs, r) })
	return recs, err
}

// Close closes the WAL file. It waits for a running Compact to finish first, so
// a compaction never races a close.
func (s *FileStore) Close() error {
	s.compactMu.Lock()
	defer s.compactMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.f.Close()
}

// MemStore is an in-memory Store double for tests. It keeps records in a slice
// and is not durable across process restarts (tests simulate a "restart" by
// constructing a fresh Queue over the SAME MemStore).
type MemStore struct {
	recs []Record
	// size tracks the encoded byte length of recs (one JSON line per record, the
	// shape FileStore writes), so a MemStore reports a LogSize like a FileStore
	// does; sizeSet, when SetLogSize was called, overrides it. Guarded by mu — a
	// status/metrics reader calls LogSize without the queue lock.
	mu      sync.Mutex
	size    int64
	sizeSet *int64
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{} }

// encodedLen is the byte length rec occupies as a log line.
func encodedLen(rec Record) int64 {
	b, err := json.Marshal(rec)
	if err != nil {
		return 0
	}
	return int64(len(b)) + 1
}

// Append records one operation in memory.
func (m *MemStore) Append(rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, rec)
	m.size += encodedLen(rec)
	return nil
}

// AppendBatch records every rec in recs in memory, in order, as one operation.
func (m *MemStore) AppendBatch(recs []Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, recs...)
	for _, r := range recs {
		m.size += encodedLen(r)
	}
	return nil
}

// Replay returns a copy of the recorded operations in append order.
func (m *MemStore) Replay() ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, len(m.recs))
	copy(out, m.recs)
	return out, nil
}

// LogSize reports the log's byte length: the encoded size of the records
// appended so far, or the value SetLogSize pinned. It is the sizing seam that
// lets a test drive the queue's size limits (Queue.EnforceLogLimits) without
// writing megabytes.
func (m *MemStore) LogSize() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sizeSet != nil {
		return *m.sizeSet
	}
	return m.size
}

// SetLogSize pins the size LogSize reports, regardless of what is appended
// afterwards (a test seam). Call UnsetLogSize to go back to tracking.
func (m *MemStore) SetLogSize(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sizeSet = &n
}

// UnsetLogSize stops pinning LogSize.
func (m *MemStore) UnsetLogSize() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sizeSet = nil
}

// Close is a no-op for the in-memory store.
func (m *MemStore) Close() error { return nil }
