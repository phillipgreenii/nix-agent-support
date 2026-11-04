package store

import "sync"

// FileTracker tells one consumer which files rows (and their paths) may have
// changed since its last Take, so it can keep a derived view of the files and
// paths tables up to date without rereading them whole.
//
// Every change to the files and paths tables goes through the journal records
// applied by applyAllOn, each carrying the file id it touches, so the tracker
// is fed from the one place a commit lands. It is in memory only: a new tracker
// starts out "full" (nothing is known), and so does one whose store has rolled
// back a write since the last Take (RollbackEpoch moved), because rows it was
// told about may then have been undone. A consumer MUST treat full as "reread
// everything".
type FileTracker struct {
	st    *Store
	mu    sync.Mutex
	dirty map[int64]struct{}
	full  bool
	epoch uint64
}

// TrackFiles registers a tracker whose first Take reports full.
func (s *Store) TrackFiles() *FileTracker {
	t := &FileTracker{st: s, dirty: map[int64]struct{}{}, full: true, epoch: s.RollbackEpoch()}
	s.trkMu.Lock()
	s.trackers = append(s.trackers, t)
	s.trkMu.Unlock()
	return t
}

// Take returns the ids that changed since the last Take (sorted order not
// promised), or full when the consumer must reread everything, and resets the
// tracker. Read the tables AFTER Take: a change committed after it is reported
// by the next Take, and one already visible is merely reread.
func (t *FileTracker) Take() (ids []int64, full bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ep := t.st.RollbackEpoch(); ep != t.epoch {
		t.epoch, t.full = ep, true
	}
	full = t.full
	if !full {
		ids = make([]int64, 0, len(t.dirty))
		for id := range t.dirty {
			ids = append(ids, id)
		}
	}
	t.full = false
	t.dirty = map[int64]struct{}{}
	return ids, full
}

// noteFiles tells every tracker the files the committed recs touch.
func (s *Store) noteFiles(recs []*Rec) {
	s.trkMu.Lock()
	ts := s.trackers
	s.trkMu.Unlock()
	for _, t := range ts {
		t.mu.Lock()
		for _, r := range recs {
			if r.File != 0 {
				t.dirty[r.File] = struct{}{}
			}
		}
		t.mu.Unlock()
	}
}
