package serve

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// seenEpoch is this build's version of what a seen row means. Raise it when
// the rule that makes a row trustworthy changes; every database written under
// another epoch has its table truncated at start.
const seenEpoch = 1

const (
	// seenFlushRows and seenFlushEvery bound how much observation can sit
	// unwritten: a batch goes out when it has this many rows or the oldest
	// pending one is this old (checked when the next one arrives), and at the
	// end of each sweep and on close.
	seenFlushRows  = 256
	seenFlushEvery = 2 * time.Second
)

// seenStore persists the daemon's observations (the seen table) in small
// batches written AFTER the ingest and its commit. A crash between the two, or
// a failed write, costs only a re-ingest of the file, so it is never part of a
// store transaction or the journal.
type seenStore struct {
	mu      sync.Mutex
	st      *store.Store
	put     map[string]store.SeenRow
	del     map[string]bool
	oldest  time.Time
	clock   interface{ Now() time.Time }
	onError func(error)
}

func newSeenStore(st *store.Store, clk interface{ Now() time.Time }, onError func(error)) *seenStore {
	return &seenStore{st: st, put: map[string]store.SeenRow{}, del: map[string]bool{}, clock: clk, onError: onError}
}

func (s *seenStore) note() {
	if s.oldest.IsZero() {
		s.oldest = s.clock.Now()
	}
}

// remember queues the observation of a path found caught up.
func (s *seenStore) remember(path string, file int64, sf seenFile) {
	s.mu.Lock()
	s.note()
	delete(s.del, path)
	s.put[path] = store.SeenRow{Path: path, File: file, Dev: sf.dev, Ino: sf.ino, BirthNS: sf.birthNS, Size: sf.size, MTimeNS: sf.mtimeNS}
	s.mu.Unlock()
	s.maybeFlush()
}

// forget queues the removal of a path's observation.
func (s *seenStore) forget(path string) {
	s.mu.Lock()
	s.note()
	delete(s.put, path)
	s.del[path] = true
	s.mu.Unlock()
	s.maybeFlush()
}

func (s *seenStore) maybeFlush() {
	s.mu.Lock()
	due := len(s.put)+len(s.del) >= seenFlushRows || (!s.oldest.IsZero() && s.clock.Now().Sub(s.oldest) >= seenFlushEvery)
	s.mu.Unlock()
	if due {
		s.flush()
	}
}

// clear drops the pending batch and empties the table.
func (s *seenStore) clear() {
	s.mu.Lock()
	s.put, s.del, s.oldest = map[string]store.SeenRow{}, map[string]bool{}, time.Time{}
	s.mu.Unlock()
	if err := s.st.SeenTruncate(); err != nil && s.onError != nil {
		s.onError(err)
	}
}

// flush writes the pending batch. A failure is reported and the batch dropped.
func (s *seenStore) flush() {
	s.mu.Lock()
	put := make([]store.SeenRow, 0, len(s.put))
	for _, r := range s.put {
		put = append(put, r)
	}
	del := make([]string, 0, len(s.del))
	for p := range s.del {
		del = append(del, p)
	}
	s.put, s.del, s.oldest = map[string]store.SeenRow{}, map[string]bool{}, time.Time{}
	s.mu.Unlock()
	if err := s.st.SeenWrite(put, del); err != nil && s.onError != nil {
		s.onError(err)
	}
}

// prune flushes, then drops the rows of paths a complete walk did not find, and
// refreshes the seen_rows gauge.
func (s *seenStore) prune(walked []watcher.FileInfo) {
	s.flush()
	keep := make(map[string]bool, len(walked))
	for _, f := range walked {
		keep[f.Path] = true
	}
	if _, err := s.st.SeenPrune(keep); err != nil && s.onError != nil {
		s.onError(err)
	}
}

// loadSeen is the start-up read: it makes the table safe to trust, loads the
// rows that still match the store into the caught-up cache and the last
// observations, and sets the gauge and log event.
func (d *Daemon) loadSeen() {
	start := d.clock.Now()
	reset, err := d.st.SeenPrepare(seenEpoch)
	if err != nil {
		d.obs.Log.Warn("seen_prepare_failed", "err", err.Error())
		reset = true
	}
	var rows []store.SeenRow
	if !reset {
		if rows, err = d.st.SeenLoad(); err != nil {
			d.obs.Log.Warn("seen_load_failed", "err", err.Error())
			rows = nil
		}
	}
	d.seen.mu.Lock()
	d.seen.m = make(map[string]seenFile, len(rows))
	d.seen.caught = make(map[string]caughtUp, len(rows))
	for _, r := range rows {
		sf := seenFile{dev: r.Dev, ino: r.Ino, size: r.Size, mtimeNS: r.MTimeNS, birthNS: r.BirthNS}
		d.seen.m[r.Path] = sf
		d.seen.caught[r.Path] = caughtUp{seen: sf, file: r.File}
	}
	d.seen.mu.Unlock()
	d.obs.Reg.Set(obs.MSeenLoadSeconds, nil, d.clock.Now().Sub(start).Seconds())
	d.obs.Reg.Set(obs.MSeenRows, nil, float64(len(rows)))
	if len(rows) == 0 {
		d.obs.Log.Info("seen empty: full verification sweep")
	}
}

// setSeenGauge refreshes seen_rows from the table.
func (d *Daemon) setSeenGauge() {
	if n, err := d.st.SeenCount(); err == nil {
		d.obs.Reg.Set(obs.MSeenRows, nil, float64(n))
	}
}
