package settle

// The dirty-set feed. Every site that changes a file's persisted state calls
// Dirty, so the event-driven settle loop can later evaluate only the
// Conversations whose inputs changed. Nothing consumes the set in the per-minute
// Tick; the full scan stays the reconciliation (and the oracle).

import (
	"sync"
	"time"
)

// dirtyFeed is the Settler's dirty state. It has its own lock, never the run
// lock, so Dirty never waits for a pass.
type dirtyFeed struct {
	mu sync.Mutex
	// fileSession maps a file to the Conversation it is placed in; it is kept
	// equal to the settler's own owner map (see syncFileSession).
	fileSession map[int64]string
	// sessions holds each dirty Conversation with the newest copy offset a
	// caller passed for it.
	sessions map[string]int64
	// unknown holds files Dirty could not place yet, with their offsets; the
	// next TakeDirty rebuilds the map once and resolves them.
	unknown     map[int64]int64
	liveChanged bool
	// wake is a one-slot channel signalled by every Dirty and LiveChanged.
	wake chan struct{}
}

func (s *Settler) feed() *dirtyFeed {
	s.feedOnce.Do(func() {
		s.df = &dirtyFeed{
			fileSession: map[int64]string{},
			sessions:    map[string]int64{},
			unknown:     map[int64]int64{},
			wake:        make(chan struct{}, 1),
		}
	})
	return s.df
}

func (d *dirtyFeed) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Dirty records that file's persisted state changed and its copy now stands at
// copyOffset. It never blocks on a pass and never queries the store: a file the
// settler has placed adds its Conversation (idempotently; the newest offset
// wins); a file it has not placed yet is remembered and resolved by the next
// TakeDirty with one rebuild of the file-to-Conversation map.
func (s *Settler) Dirty(file int64, copyOffset int64) {
	d := s.feed()
	d.mu.Lock()
	if session, ok := d.fileSession[file]; ok {
		d.sessions[session] = copyOffset
	} else {
		d.unknown[file] = copyOffset
	}
	d.mu.Unlock()
	d.signal()
}

// LiveChanged tells the settler that the set of live Conversations may have
// changed (the lifecycle tracker saw a Process appear or exit). It never blocks.
func (s *Settler) LiveChanged() {
	d := s.feed()
	d.mu.Lock()
	d.liveChanged = true
	d.mu.Unlock()
	d.signal()
}

// DirtyWake returns the channel Dirty and LiveChanged signal (one slot, so a
// burst leaves one wake).
func (s *Settler) DirtyWake() <-chan struct{} { return s.feed().wake }

// TakeLiveChanged reports whether LiveChanged ran since the last call, and
// clears it.
func (s *Settler) TakeLiveChanged() bool {
	d := s.feed()
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.liveChanged
	d.liveChanged = false
	return v
}

// TakeDirty returns the dirty Conversations with the newest offset passed for
// each, and clears the set. Files Dirty could not place are resolved first, with
// one groups() rebuild under the run lock; a file that belongs to no
// Conversation is dropped.
func (s *Settler) TakeDirty() map[string]int64 {
	out, _ := s.takeDirty()
	return out
}

// takeDirty is TakeDirty that also returns the time the group rebuild queued for
// the run lock (0 when no rebuild was needed). The wait is counted under op
// "take_dirty" in settle_run_lock_wait_*, like the other lock takers.
func (s *Settler) takeDirty() (map[string]int64, time.Duration) {
	ctx := s.detached()
	d := s.feed()
	var waited time.Duration
	d.mu.Lock()
	need := len(d.unknown) > 0
	d.mu.Unlock()
	if need {
		unlock, w := s.lockRunWait("take_dirty")
		waited = w
		_, err := s.groups(ctx) // refreshes fileSession
		unlock()
		_ = err // on error the unknown files stay for the next call
		if err != nil {
			d.mu.Lock()
			out := d.sessions
			d.sessions = map[string]int64{}
			d.mu.Unlock()
			return out, waited
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for f, off := range d.unknown {
		if session, ok := d.fileSession[f]; ok {
			d.sessions[session] = off
		}
		delete(d.unknown, f)
	}
	out := d.sessions
	d.sessions = map[string]int64{}
	return out, waited
}

// syncFileSession brings fileSession in line with owner after groups changed it:
// wholly when full, else for the files in ids. The caller holds the run lock.
func (s *Settler) syncFileSession(ids []int64, full bool) {
	d := s.feed()
	d.mu.Lock()
	defer d.mu.Unlock()
	if full || s.owner == nil {
		m := make(map[int64]string, len(s.owner))
		for f, session := range s.owner {
			m[f] = session
		}
		d.fileSession = m
		return
	}
	for _, id := range ids {
		if session, ok := s.owner[id]; ok {
			d.fileSession[id] = session
		} else {
			delete(d.fileSession, id)
		}
	}
}

// dirtyRecorded feeds Dirty for a file whose caller has no new copy offset (a
// path change), with the file's current recorded copy offset.
func (s *Settler) dirtyRecorded(file int64) {
	var off int64
	if f, err := s.st.File(file); err == nil && f.CurGen > 0 {
		if g, err := s.st.Generation(file, f.CurGen); err == nil {
			off = g.CopyOffset
		}
	}
	s.Dirty(file, off)
}
