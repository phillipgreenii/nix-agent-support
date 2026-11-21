// Package state holds pg-gwm's persistent state: the per-repository state file
// under the common git directory with its repo lock, per-operation holds and
// event log, and the user-level registry, counters and reaper record.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
)

// SchemaVersion is the version written into every state file.
const SchemaVersion = 1

// Status is a worktree's status.
type Status string

// Stored statuses.
const (
	Preparing  Status = "preparing"
	Leased     Status = "leased"
	Released   Status = "released"
	Available  Status = "available"
	Creating   Status = "creating"
	Reclaiming Status = "reclaiming"
	Broken     Status = "broken"
)

// Derived-only statuses; never stored.
const (
	LeaseExpired Status = "lease-expired"
	Unmanaged    Status = "unmanaged"
)

// Lease is the lease a worktree currently carries.
type Lease struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Owner    string            `json:"owner"`
	TTL      config.Duration   `json:"-"`
	Expiry   *time.Time        `json:"expiry"`
	Metadata map[string]string `json:"metadata"`
	Acquired time.Time         `json:"acquired"`
}

type leaseJSON Lease

type leaseWire struct {
	*leaseJSON
	TTL string `json:"ttl"`
}

// MarshalJSON writes the TTL as "none", "<int><unit>" or "" when unset.
func (l Lease) MarshalJSON() ([]byte, error) {
	w := leaseWire{leaseJSON: (*leaseJSON)(&l)}
	if !l.TTL.IsZero() {
		w.TTL = l.TTL.String()
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads what MarshalJSON wrote.
func (l *Lease) UnmarshalJSON(b []byte) error {
	w := leaseWire{leaseJSON: (*leaseJSON)(l)}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	l.TTL = config.Duration{}
	if w.TTL != "" {
		d, err := config.ParseDuration(w.TTL)
		if err != nil {
			return err
		}
		l.TTL = d
	}
	if l.Metadata == nil {
		l.Metadata = map[string]string{}
	}
	return nil
}

// Worktree is one managed worktree's record.
type Worktree struct {
	ID          string    `json:"id"`
	Slot        int       `json:"slot"`
	Path        string    `json:"path"`
	Branch      string    `json:"branch"`
	Status      Status    `json:"status"`
	Lease       *Lease    `json:"lease"`
	CreatedAt   time.Time `json:"created_at"`
	StatusSince time.Time `json:"status_since"`
	Reason      string    `json:"reason"`
	// NeverLeased is true for a worktree reap created to meet min that no
	// lease has taken yet; nothing was written into it on anyone's behalf.
	// A record that lacks it (written by an older build) is treated as
	// possibly leased.
	NeverLeased bool `json:"never_leased,omitempty"`
}

// Effective is the status a reader reports: a Leased worktree whose expiry has
// passed is LeaseExpired.
func (w Worktree) Effective(now time.Time) Status {
	if w.Status == Leased && w.Lease != nil && w.Lease.Expiry != nil && !w.Lease.Expiry.After(now) {
		return LeaseExpired
	}
	return w.Status
}

// Tombstone is what an ended lease leaves behind, so a later renew or release
// of its id answers lease-released or lease-lost for the retention period.
type Tombstone struct {
	Kind            string    `json:"kind"` // "released" or "reclaimed"
	LeaseID         string    `json:"lease_id"`
	Name            string    `json:"name"`
	Owner           string    `json:"owner"`
	PreservedBranch string    `json:"preserved_branch"`
	At              time.Time `json:"at"`
}

// Preserved records a branch that holds a dirty worktree's saved work.
type Preserved struct {
	Branch  string    `json:"branch"`
	LeaseID string    `json:"lease_id"`
	Name    string    `json:"name"`
	At      time.Time `json:"at"`
}

// State is the content of state.json.
type State struct {
	SchemaVersion int         `json:"schema_version"`
	Worktrees     []Worktree  `json:"worktrees"`
	Tombstones    []Tombstone `json:"tombstones"`
	Preserved     []Preserved `json:"preserved"`
}

// Store is the per-repository state directory, <common git dir>/pg-gwm.
type Store struct {
	Dir   string
	Clock func() time.Time
	// Retention is tombstone-retention; Append uses it to bound the event
	// log. "none" or an unset value keeps every file.
	Retention config.Duration

	rename func(oldpath, newpath string) error
}

// Open returns the store of the repository whose common git directory is
// commonDir. It touches nothing on disk.
func Open(commonDir string) Store {
	return Store{Dir: filepath.Join(commonDir, "pg-gwm"), Clock: time.Now, rename: os.Rename}
}

func (s Store) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock()
}

func (s Store) statePath() string { return filepath.Join(s.Dir, "state.json") }

// ReadUnlocked reads state.json without the lock; safe because writes are
// rename-atomic. A missing file reads as an empty state.
func (s Store) ReadUnlocked() (State, error) {
	b, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return State{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("%s: corrupt state: %w", s.statePath(), err)
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	return st, nil
}

// Locked is the state while the repo lock is held.
type Locked struct {
	s   Store
	st  State
	unl func()
}

// State returns the mutable in-memory state; Commit persists it.
func (l *Locked) State() *State { return &l.st }

// Commit writes the state to a temp file, fsyncs it and renames it over
// state.json.
func (l *Locked) Commit() error {
	l.st.SchemaVersion = SchemaVersion
	b, err := json.MarshalIndent(&l.st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(l.s.statePath(), append(b, '\n'), l.s.rename)
}

// Unlock releases the repo lock; calling it again is harmless.
func (l *Locked) Unlock() {
	if l.unl != nil {
		l.unl()
		l.unl = nil
	}
}

// writeAtomic replaces path with data: temp file in the same directory,
// fsync, rename, then fsync of the directory. rename may be nil.
func writeAtomic(path string, data []byte, rename func(string, string) error) error {
	if rename == nil {
		rename = os.Rename
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
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
	if err := rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// FindName returns the worktree whose lease carries name (compared
// case-insensitively) among Preparing, Leased and LeaseExpired worktrees, else
// the newest tombstone with that name, else nil, nil.
func (l *Locked) FindName(name string, now time.Time) (*Worktree, *Tombstone) {
	if name == "" {
		return nil, nil
	}
	for i := range l.st.Worktrees {
		w := &l.st.Worktrees[i]
		if w.Lease == nil || !strings.EqualFold(w.Lease.Name, name) {
			continue
		}
		switch w.Effective(now) {
		case Preparing, Leased, LeaseExpired:
			return w, nil
		}
	}
	var best *Tombstone
	for i := range l.st.Tombstones {
		t := &l.st.Tombstones[i]
		if strings.EqualFold(t.Name, name) && (best == nil || !t.At.Before(best.At)) {
			best = t
		}
	}
	return nil, best
}

// FindLease returns the worktree carrying lease id, else the newest tombstone
// of that id, else nil, nil.
func (l *Locked) FindLease(id string) (*Worktree, *Tombstone) {
	if id == "" {
		return nil, nil
	}
	for i := range l.st.Worktrees {
		if w := &l.st.Worktrees[i]; w.Lease != nil && w.Lease.ID == id {
			return w, nil
		}
	}
	var best *Tombstone
	for i := range l.st.Tombstones {
		t := &l.st.Tombstones[i]
		if t.LeaseID == id && (best == nil || !t.At.Before(best.At)) {
			best = t
		}
	}
	return nil, best
}

// PruneTombstones drops tombstones with At + retention <= now. A caller whose
// tombstone-retention is "none" does not call it.
func (l *Locked) PruneTombstones(retention time.Duration, now time.Time) {
	kept := l.st.Tombstones[:0]
	for _, t := range l.st.Tombstones {
		if t.At.Add(retention).After(now) {
			kept = append(kept, t)
		}
	}
	l.st.Tombstones = kept
}
