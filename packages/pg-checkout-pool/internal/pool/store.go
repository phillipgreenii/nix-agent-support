package pool

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const (
	slotsDir   = "slots"
	lockName   = "slot.lock"
	recordName = "lease.json"
)

// Sentinel errors.
var (
	ErrBusy      = errors.New("slot is exclusively held by another owner")
	ErrExhausted = errors.New("no free slot")
	ErrExists    = errors.New("slot already exists")
	ErrNoSlot    = errors.New("no such slot")
	ErrOwner     = errors.New("lease request has no owner session id")
	ErrNotLeased = errors.New("slot is not in the leased state")
)

// Store is a directory of slots. Each slot is a subdirectory holding a lock
// file (the exclusive hold, released by the OS when the holder dies) and an
// atomically replaced record file.
type Store struct {
	Root  string
	Clock Clock

	// rename is the atomic-replace step of a record write; tests swap it to
	// prove a failed write leaves the previous record intact.
	rename func(oldpath, newpath string) error
	newID  func() string
}

// NewStore returns a store rooted at root. A nil clock means SystemClock.
func NewStore(root string, clock Clock) *Store {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Store{Root: root, Clock: clock, rename: os.Rename, newID: randomID}
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Store) slotPath(id string) string { return filepath.Join(s.Root, slotsDir, id) }

// CreateSlot registers a new warm-free slot.
func (s *Store) CreateSlot(id string) error {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return fmt.Errorf("invalid slot id %q", id)
	}
	dir := s.slotPath(id)
	if err := os.MkdirAll(filepath.Join(s.Root, slotsDir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrExists, id)
		}
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	_ = f.Close()
	return s.writeRecord(Record{SchemaVersion: SchemaVersion, Slot: id, State: WarmFree})
}

// Slots lists slot ids in sorted order.
func (s *Store) Slots() ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(s.Root, slotsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ReadRecord reads a slot's record without taking the lock. A record is
// replaced atomically, so a reader never sees a torn write.
func (s *Store) ReadRecord(id string) (Record, error) {
	b, err := os.ReadFile(filepath.Join(s.slotPath(id), recordName))
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, fmt.Errorf("%w: %s", ErrNoSlot, id)
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("slot %s: corrupt record: %w", id, err)
	}
	return r, nil
}

// writeRecord replaces the slot record atomically: write a temp file in the
// same directory, fsync it, rename over the record, fsync the directory.
func (s *Store) writeRecord(r Record) error {
	dir := s.slotPath(r.Slot)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, recordName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func(e error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return e
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := s.rename(tmpName, filepath.Join(dir, recordName)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Handle is an exclusive hold on one slot. It is the only way to change a
// record. The hold lasts until Unlock or until the process dies.
type Handle struct {
	s    *Store
	id   string
	f    *os.File
	rec  Record
	done bool
}

// Lock takes the slot's exclusive lock without blocking. ErrBusy means
// another holder (possibly another process) has it.
func (s *Store) Lock(id string) (*Handle, error) {
	f, err := os.OpenFile(filepath.Join(s.slotPath(id), lockName), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoSlot, id)
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrBusy, id)
		}
		return nil, err
	}
	rec, err := s.ReadRecord(id)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Handle{s: s, id: id, f: f, rec: rec}, nil
}

// Held reports whether some holder currently has the slot's exclusive lock,
// by trying to take it. This, not the record, is the liveness signal.
func (s *Store) Held(id string) (bool, error) {
	h, err := s.Lock(id)
	if errors.Is(err, ErrBusy) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	h.Unlock()
	return false, nil
}

// Record returns the record as of the last read or write through the handle.
func (h *Handle) Record() Record { return h.rec }

// Unlock drops the exclusive hold. It is idempotent.
func (h *Handle) Unlock() {
	if h.done {
		return
	}
	h.done = true
	_ = syscall.Flock(int(h.f.Fd()), syscall.LOCK_UN)
	_ = h.f.Close()
}

// Transition moves the slot to state to, after fn (which may be nil) edits a
// copy of the record. Illegal moves return *TransitionError and change
// nothing; so does a failed write.
func (h *Handle) Transition(to State, fn func(*Record)) error {
	if h.done {
		return errors.New("handle is unlocked")
	}
	if !CanTransition(h.rec.State, to) {
		return &TransitionError{Slot: h.id, From: h.rec.State, To: to}
	}
	next := h.rec
	if fn != nil {
		fn(&next)
	}
	next.Slot, next.State, next.SchemaVersion = h.id, to, SchemaVersion
	if err := h.s.writeRecord(next); err != nil {
		return err
	}
	h.rec = next
	return nil
}

// AcquireRequest names what a requester wants. Owner is the lease key; BeadHint
// is only an affinity preference.
type AcquireRequest struct {
	OwnerSessionID string
	OwnerPID       int
	OwnerStart     time.Time
	BeadHint       string
	Repo           string
	Commit         string
	Branch         string
	TTL            time.Duration
}

// Acquire leases a warm-free slot: it takes the slot's exclusive lock and
// moves the slot to Resetting with the lease written, ready for the
// provisioner to reset and then call MarkLeased. A slot last used for the
// request's BeadHint is tried first; every other slot is tried in id order.
// Slots held or not warm-free are skipped. The caller owns the returned
// handle (and so the lock) until Unlock.
func (s *Store) Acquire(req AcquireRequest) (*Handle, error) {
	if req.OwnerSessionID == "" {
		return nil, ErrOwner
	}
	ids, err := s.Slots()
	if err != nil {
		return nil, err
	}
	if req.BeadHint != "" {
		var pref, rest []string
		for _, id := range ids {
			if r, err := s.ReadRecord(id); err == nil && r.LastBeadHint == req.BeadHint {
				pref = append(pref, id)
			} else {
				rest = append(rest, id)
			}
		}
		ids = append(pref, rest...)
	}
	for _, id := range ids {
		h, err := s.Lock(id)
		if err != nil {
			if errors.Is(err, ErrBusy) {
				continue
			}
			return nil, err
		}
		if h.rec.State != WarmFree {
			h.Unlock()
			continue
		}
		now := s.Clock.Now()
		err = h.Transition(Resetting, func(r *Record) {
			r.LeaseID = s.newID()
			r.OwnerSessionID, r.OwnerPID, r.OwnerStart = req.OwnerSessionID, req.OwnerPID, req.OwnerStart
			r.BeadHint, r.Repo, r.Commit, r.Branch = req.BeadHint, req.Repo, req.Commit, req.Branch
			r.Acquired, r.Heartbeat, r.Expiry = now, now, now.Add(req.TTL)
			if req.BeadHint != "" {
				r.LastBeadHint = req.BeadHint
			}
		})
		if err != nil {
			h.Unlock()
			return nil, err
		}
		return h, nil
	}
	return nil, ErrExhausted
}

// MarkLeased finishes the reset on acquire: Resetting -> Leased.
func (h *Handle) MarkLeased() error { return h.Transition(Leased, nil) }

// Renew extends a leased slot's expiry from now and stamps the heartbeat.
func (h *Handle) Renew(ttl time.Duration) error {
	if h.rec.State != Leased {
		return fmt.Errorf("%w: %s is %s", ErrNotLeased, h.id, h.rec.State)
	}
	now := h.s.Clock.Now()
	next := h.rec
	next.Heartbeat, next.Expiry = now, now.Add(ttl)
	if err := h.s.writeRecord(next); err != nil {
		return err
	}
	h.rec = next
	return nil
}

// BeginReset starts a release (or a reap): Leased -> Resetting, lease intact
// so the provisioner can still read the owner's branch and commit.
func (h *Handle) BeginReset() error { return h.Transition(Resetting, nil) }

// FinishReset ends a reset: Resetting -> WarmFree with the lease cleared.
func (h *Handle) FinishReset() error {
	return h.Transition(WarmFree, func(r *Record) { r.clearLease(); r.Reason = "" })
}

// Quarantine takes the slot out of rotation, keeping the lease fields as
// evidence and recording why.
func (h *Handle) Quarantine(reason string) error {
	return h.Transition(Quarantined, func(r *Record) { r.Reason = reason })
}

// Retire ends the slot's life (from WarmFree or Quarantined).
func (h *Handle) Retire(reason string) error {
	return h.Transition(Retired, func(r *Record) { r.clearLease(); r.Reason = reason })
}

// SlotStatus is a read-only view of one slot.
type SlotStatus struct {
	Record Record `json:"record"`
	// Held is true while some holder has the slot's exclusive lock.
	Held bool `json:"held"`
	// Leak is true for a leased or resetting slot whose lock is free and
	// whose expiry has passed: held by nobody.
	Leak bool `json:"leak"`
}

// Inspect reports a slot's record, whether its lock is held, and whether it
// is a leak.
func (s *Store) Inspect(id string) (SlotStatus, error) {
	rec, err := s.ReadRecord(id)
	if err != nil {
		return SlotStatus{}, err
	}
	held, err := s.Held(id)
	if err != nil {
		return SlotStatus{}, err
	}
	st := SlotStatus{Record: rec, Held: held}
	if (rec.State == Leased || rec.State == Resetting) && !held && !rec.Expiry.IsZero() && s.Clock.Now().After(rec.Expiry) {
		st.Leak = true
	}
	return st, nil
}

// Status inspects every slot.
func (s *Store) Status() ([]SlotStatus, error) {
	ids, err := s.Slots()
	if err != nil {
		return nil, err
	}
	out := make([]SlotStatus, 0, len(ids))
	for _, id := range ids {
		st, err := s.Inspect(id)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// FindByOwner returns the slot leased to a session id. The session id is the
// lease key; there is deliberately no lookup by work item.
func (s *Store) FindByOwner(sessionID string) (Record, bool, error) {
	ids, err := s.Slots()
	if err != nil {
		return Record{}, false, err
	}
	for _, id := range ids {
		r, err := s.ReadRecord(id)
		if err != nil {
			return Record{}, false, err
		}
		if r.OwnerSessionID == sessionID && (r.State == Leased || r.State == Resetting) {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}
