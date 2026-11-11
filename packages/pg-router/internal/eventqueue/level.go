package eventqueue

import (
	"sort"
	"sync"
	"time"
)

// The durable level path: the level_set and level_cleared log records, the
// append API that bypasses the queue's admission limits, the bounded refresh
// throttle, the replay that hands the records to the level projection, and the
// fold compaction uses.
//
// Exported names: RouterResource, LevelPaused, LevelOwnerOperator, LevelReading,
// LevelRecord (IsOperator), PauseRecord, ResumeRecord, Queue.AppendLevel,
// Queue.ReplayLevels.
//
// Levels are their OWN durable records, not queue events: they are appended
// without passing the admission limit (log_full) or the unwritable mark
// (log_unwritable), they never enter the queue's entries, and an older binary
// ignores them as unknown kinds. The in-memory projection (internal/core) is
// updated independently of the append and takes its receipt sequence from the
// value AppendLevel returns.

// RouterResource and LevelPaused name the durable router pause: a level_set
// record with resource "router", level "PAUSED", the caller's owner string and an
// optional lease (ExpiresAt); a level_cleared record for the resource resumes.
const (
	RouterResource = "router"
	LevelPaused    = "PAUSED"
	// LevelOwnerOperator is the default owner of an operator override and of a
	// pause.
	LevelOwnerOperator = "operator"
)

// LevelReading is the optional numeric reading behind a level.
type LevelReading struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// LevelRecord is the durable form of an accepted level (level_set) or a cleared
// operator override (level_cleared). ExpiresAt is absolute; zero means no expiry
// (an until-cleared override). A sensor level has ValidForS > 0; an operator
// override has ValidForS == 0 (see IsOperator).
type LevelRecord struct {
	Cleared    bool
	Resource   string
	Level      string
	Owner      string
	Reading    *LevelReading
	ObservedAt time.Time
	ReceivedAt time.Time
	ExpiresAt  time.Time
	ValidForS  int
}

// IsOperator reports whether the record belongs to the operator-override slot of
// its resource (an override set or any clear) rather than to a sensor's level.
func (r LevelRecord) IsOperator() bool { return r.Cleared || r.ValidForS == 0 }

// PauseRecord builds the level_set record of a router pause. A zero lease is an
// until-cleared pause; an empty owner means "operator".
func PauseRecord(owner string, receivedAt time.Time, lease time.Duration) LevelRecord {
	if owner == "" {
		owner = LevelOwnerOperator
	}
	rec := LevelRecord{
		Resource: RouterResource, Level: LevelPaused, Owner: owner,
		ObservedAt: receivedAt.UTC(), ReceivedAt: receivedAt.UTC(),
	}
	if lease > 0 {
		rec.ExpiresAt = receivedAt.UTC().Add(lease)
	}
	return rec
}

// ResumeRecord builds the level_cleared record that clears the router pause.
func ResumeRecord(receivedAt time.Time) LevelRecord {
	return LevelRecord{Cleared: true, Resource: RouterResource, Owner: LevelOwnerOperator, ReceivedAt: receivedAt.UTC()}
}

func (r LevelRecord) record() Record {
	op := opLevelSet
	if r.Cleared {
		op = opLevelCleared
	}
	return Record{
		Op: op, Resource: r.Resource, LevelName: r.Level, Owner: r.Owner, Reading: r.Reading,
		ObservedAt: r.ObservedAt, ReceivedAt: r.ReceivedAt, ExpiresAt: r.ExpiresAt, ValidForS: r.ValidForS,
	}
}

func levelFromRecord(r Record) LevelRecord {
	return LevelRecord{
		Cleared: r.Op == opLevelCleared, Resource: r.Resource, Level: r.LevelName, Owner: r.Owner,
		Reading: r.Reading, ObservedAt: r.ObservedAt, ReceivedAt: r.ReceivedAt, ExpiresAt: r.ExpiresAt,
		ValidForS: r.ValidForS,
	}
}

func isLevelOp(op opKind) bool { return op == opLevelSet || op == opLevelCleared }

// levelState is the queue's level-path state: the receipt-sequence high-water
// mark, the replayed records not yet handed over, and the refresh throttle.
type levelState struct {
	mu sync.Mutex
	// hi is the highest receipt sequence handed out or replayed (-1: none yet).
	hi int64
	// replayed are the folded records of the replayed log, in log order, until
	// ReplayLevels hands them over.
	replayed []levelItem
	// last is the most recent PERSISTED sensor record per (resource, owner), the
	// refresh throttle's memory (it is empty after a restart: the first answer of
	// each sensor is persisted).
	last map[levelKey]LevelRecord
}

// nextSeq returns the receipt sequence for a record whose log offset is off
// (off < 0: a record that was not persisted). It is the log offset whenever that
// is above every sequence handed out so far; otherwise (after a compaction shrank
// the file, or for an answer the refresh throttle did not persist) it is one more
// than the high-water mark, so the sequence is strictly increasing in the order
// the projection received the records. Caller holds l.mu.
func (l *levelState) nextSeq(off int64) int64 {
	seq := off
	if seq <= l.hi {
		seq = l.hi + 1
	}
	l.hi = seq
	return seq
}

// persistSensor reports whether a sensor record must be written: on a change of
// level or validity, and as a bounded refresh once valid_for_s / 2 has passed since
// the last persisted record of the same (resource, owner). There is no per-pull
// record. Caller holds l.mu.
func (l *levelState) persistSensor(rec LevelRecord) bool {
	last, ok := l.last[levelKey{rec.Resource, rec.Owner}]
	if !ok || last.Level != rec.Level || last.ValidForS != rec.ValidForS {
		return true
	}
	if rec.ReceivedAt.Before(last.ReceivedAt) {
		return true
	}
	return rec.ReceivedAt.Sub(last.ReceivedAt) >= time.Duration(rec.ValidForS)*time.Second/2
}

// AppendLevel durably records one level record and returns its receipt sequence
// (the log offset of the record). It is the level path's only write and it
// BYPASSES the admission limit (log_full) and the unwritable mark
// (log_unwritable): the append is attempted against the real log whatever
// those say, so a push succeeds with the log full. A failed write is reported and
// marks the log unwritable like any other failed write; a success clears the mark.
//
// A sensor record is persisted on a change and as a refresh every valid_for_s / 2
// of the record's receipt time; an answer between those points is NOT written (and
// the return value is a fresh sequence all the same, so the projection still
// extends its in-memory expiry). Operator overrides and clears are always written.
//
// AppendLevel does not take the queue's mutex (the projection holds its own lock
// while calling it, and the queue calls the projection's snapshot under q.mu), so
// the Store must tolerate a concurrent Append (see Store).
func (q *Queue) AppendLevel(rec LevelRecord) (int64, error) {
	l := &q.level
	l.mu.Lock()
	defer l.mu.Unlock()
	sensor := !rec.IsOperator()
	if sensor && !l.persistSensor(rec) {
		return l.nextSeq(-1), nil
	}
	off, err := q.store.Append(rec.record())
	if err = q.noteWrite(err); err != nil {
		return 0, err
	}
	if sensor {
		if l.last == nil {
			l.last = map[levelKey]LevelRecord{}
		}
		l.last[levelKey{rec.Resource, rec.Owner}] = rec
	}
	return l.nextSeq(off), nil
}

// ReplayLevels hands the level records the startup replay found to fn, in log
// order, each with its stored offset (the receipt sequence the live append gave
// it), and forgets them. The records are the folded form compaction would keep
// (the latest of each (resource, owner); the latest operator record of each
// resource), so an override that lapsed during downtime is still handed over:
// the projection's ApplyRecord drops it. It returns how many records it handed
// over. Records of an unknown op were ignored by the replay.
func (q *Queue) ReplayLevels(fn func(rec LevelRecord, offset int64)) int {
	l := &q.level
	l.mu.Lock()
	items := l.replayed
	l.replayed = nil
	l.mu.Unlock()
	for _, it := range items {
		fn(levelFromRecord(it.rec), it.off)
	}
	return len(items)
}

// levelKey identifies the slot a record occupies in the fold: a sensor's latest
// record per (resource, owner). The operator-override slot of a resource is keyed
// by the resource alone (owner ""), because the projection holds ONE override per
// resource: a later set or clear by any owner replaces it.
type levelKey struct {
	resource string
	owner    string
}

func keyOf(r Record) levelKey {
	if r.Op == opLevelCleared || r.ValidForS == 0 {
		return levelKey{resource: r.Resource}
	}
	return levelKey{resource: r.Resource, owner: r.Owner}
}

type levelItem struct {
	rec   Record
	off   int64
	order int
}

// levelFold folds level records into the minimal sequence a replay needs:
// compaction's level half and the startup replay's hand-over.
type levelFold struct {
	latest map[levelKey]*levelItem
	n      int
}

func newLevelFold() *levelFold { return &levelFold{latest: map[levelKey]*levelItem{}} }

func (f *levelFold) apply(r Record, off int64) {
	f.latest[keyOf(r)] = &levelItem{rec: r, off: off, order: f.n}
	f.n++
}

// items returns the kept records in log order. A sensor record is always kept
// (its last-known value feeds `unknown` and grace). The operator slot of a
// resource is kept only when its latest record is a set; with dropLapsed it is
// dropped too when its expiry is strictly before now (an until-cleared override
// has none and never lapses). An operator override and a sensor record are in
// different slots, so neither displaces the other.
func (f *levelFold) items(now time.Time, dropLapsed bool) []levelItem {
	out := make([]levelItem, 0, len(f.latest))
	for k, it := range f.latest {
		if k.owner == "" {
			if it.rec.Op == opLevelCleared {
				continue
			}
			if dropLapsed && !it.rec.ExpiresAt.IsZero() && it.rec.ExpiresAt.Before(now) {
				continue
			}
		}
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}
