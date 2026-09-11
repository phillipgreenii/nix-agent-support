package eventqueue

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// opKind is the write-ahead-log record kind.
type opKind string

const (
	opEnqueue opKind = "enqueue"
	opAccept  opKind = "accept"
	opEvict   opKind = "evict"
)

// Record is one durable write-ahead-log entry. The log is append-only; queue
// state is reconstructed by replaying it in order (Store.Replay). Delivery is
// at-least-once because an accept Record is written only AFTER the handler
// confirms acceptance (ADR 0031 req 4) — a crash between the in-memory accept
// and this write re-offers the event on restart (at-most-one redelivery,
// absorbed by idempotent handlers, INV-EVT-2).
//
// There are exactly THREE record kinds and none of them is an attempt log: the
// core keeps no attempt history (INV-EVT-4, DEC-EVENT-1), so a pre-accept decline
// — even the final one past `expiresAt` — writes nothing. An event LEAVING the
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
}

// Store is the durable persistence seam. The queue writes enqueue/accept/evict
// records and replays them on startup. It is an interface so tests can inject a
// fault-injecting fake (crash-window simulation) and an in-memory double, per
// ADR 0031's "storage mechanism is a realization choice".
//
// Store is not internally synchronized: every call is serialized solely by the
// queue's own mutex (q.mu), never by the Store itself. AppendBatch's caller
// chooses the batch's contents, and the BATCH — not the individual Record — is
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
type FileStore struct {
	f *os.File
}

// NewFileStore opens (creating parent dirs) the append-only WAL at path.
func NewFileStore(path string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileStore{f: f}, nil
}

// Append marshals and writes one record as a line, then fsyncs.
func (s *FileStore) Append(rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := s.f.Write(b); err != nil {
		return err
	}
	return s.f.Sync()
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
			return err
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	if _, err := s.f.Write(buf); err != nil {
		return err
	}
	return s.f.Sync()
}

// Replay reads the WAL back into records. A partially-written trailing line
// (torn by a crash mid-write) is tolerated: parsing stops at the first
// undecodable line, mirroring a real WAL's truncate-on-torn-tail recovery.
func (s *FileStore) Replay() ([]Record, error) {
	f, err := os.Open(s.f.Name())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var recs []Record
	sc := bufio.NewScanner(f)
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
			break
		}
		recs = append(recs, rec)
	}
	if err := sc.Err(); err != nil {
		return recs, fmt.Errorf("eventqueue: replay scan: %w", err)
	}
	return recs, nil
}

// Close closes the WAL file.
func (s *FileStore) Close() error { return s.f.Close() }

// MemStore is an in-memory Store double for tests. It keeps records in a slice
// and is not durable across process restarts (tests simulate a "restart" by
// constructing a fresh Queue over the SAME MemStore).
type MemStore struct {
	recs []Record
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{} }

// Append records one operation in memory.
func (m *MemStore) Append(rec Record) error {
	m.recs = append(m.recs, rec)
	return nil
}

// AppendBatch records every rec in recs in memory, in order, as one operation.
func (m *MemStore) AppendBatch(recs []Record) error {
	m.recs = append(m.recs, recs...)
	return nil
}

// Replay returns a copy of the recorded operations in append order.
func (m *MemStore) Replay() ([]Record, error) {
	out := make([]Record, len(m.recs))
	copy(out, m.recs)
	return out, nil
}

// Close is a no-op for the in-memory store.
func (m *MemStore) Close() error { return nil }
