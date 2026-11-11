package copier

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// PrivateScanBudget is the most files one PrivateScanStep call examines, so a
// pass stays short however many files copies/ holds.
const PrivateScanBudget = 500

// privateScan is the incremental scan of the private (unshared) size of the
// whole copies/ tree. A sweep lists the directory once, then examines at most
// PrivateScanBudget files per step; when the last file is done it publishes the
// sweep's sum and waits for the next sweep's due time.
type privateScan struct {
	mu      sync.Mutex
	names   []string
	pos     int
	sum     int64
	files   int
	active  bool
	nextDue time.Time
	off     bool // the clone policy reports no private size on this platform
}

// PrivateScanStep advances the private-size scan of copies/ by at most
// PrivateScanBudget files and returns without waiting for the rest. A new sweep
// starts when none is running and every has passed since the previous sweep
// started (the first one starts at once). On completion it sets
// copies_private_size_bytes, copies_private_files and
// copies_private_scan_timestamp_seconds. It takes no copier lock and reads only
// file metadata, so it never delays an ingest.
func (c *Copier) PrivateScanStep(now time.Time, every time.Duration) {
	s := &c.scan
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.off {
		return
	}
	dir := filepath.Join(c.st.Dir(), store.CopiesDir)
	if !s.active {
		if now.Before(s.nextDue) {
			return
		}
		s.nextDue = now.Add(every)
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		s.names = s.names[:0]
		for _, e := range ents {
			if e.Type().IsRegular() {
				s.names = append(s.names, e.Name())
			}
		}
		s.pos, s.sum, s.files, s.active = 0, 0, 0, true
	}
	for n := 0; n < PrivateScanBudget && s.pos < len(s.names); n++ {
		ps, err := c.cfg.Clone.PrivateSize(filepath.Join(dir, s.names[s.pos]))
		s.pos++
		if errors.Is(err, errors.ErrUnsupported) {
			s.off, s.active = true, false
			return
		}
		if err != nil {
			continue // removed or unreadable since the listing: not counted
		}
		s.sum += ps
		s.files++
	}
	if s.pos < len(s.names) {
		return
	}
	s.active = false
	c.cfg.Obs.Reg.Set(obs.MCopiesPrivateSize, nil, float64(s.sum))
	c.cfg.Obs.Reg.Set(obs.MCopiesPrivateFiles, nil, float64(s.files))
	c.cfg.Obs.Reg.Set(obs.MCopiesPrivateScanned, nil, float64(now.UnixNano())/1e9)
}
