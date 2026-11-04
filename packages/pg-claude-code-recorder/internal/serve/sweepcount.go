package serve

import (
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// seenFile is what the daemon last observed of a path: from a sweep walk or a
// watcher event. The store keeps a file's size but not its mtime, so the mtime
// half of the unchanged-candidate rule is compared against this.
type seenFile struct {
	dev, ino uint64
	size     int64
	mtimeNS  int64
}

// sweepSeen is the per-path last observation behind sweep_files_total
// {outcome="unchanged_candidate"}. Every caller already runs under the
// watcher's lock; the mutex only keeps the type safe on its own.
type sweepSeen struct {
	mu sync.Mutex
	m  map[string]seenFile
}

func seenOf(fi watcher.FileInfo) seenFile {
	return seenFile{dev: fi.Dev, ino: fi.Ino, size: fi.Size, mtimeNS: fi.ModTime.UnixNano()}
}

// noteSeen records a watcher event's observation of its path (and forgets a
// path that is gone).
func (d *Daemon) noteSeen(e watcher.Event) {
	d.seen.mu.Lock()
	defer d.seen.mu.Unlock()
	if d.seen.m == nil {
		d.seen.m = map[string]seenFile{}
	}
	if e.Gone {
		delete(d.seen.m, e.Path)
		return
	}
	d.seen.m[e.Path] = seenOf(e.Info)
}

// countSweepFiles adds one sweep's file counts: walked is every file the
// directory walk reported; unchanged_candidate is every one whose size, mtime
// and inode match the last observation of its path AND whose stored
// files.last_size row (found by dev, inode and birth time) equals the walked
// size. It only observes: nothing is skipped, so phase 4 can see the saving
// before it takes it. It runs before the sweep's own events.
func (d *Daemon) countSweepFiles(files []watcher.FileInfo) {
	d.seen.mu.Lock()
	defer d.seen.mu.Unlock()
	last := map[copier.Identity]int64{}
	if all, err := d.st.AllFiles(); err == nil {
		for _, f := range all {
			if f.State != "deleted" {
				last[copier.Identity{Dev: f.Dev, Ino: f.Ino, Birth: f.Birth}] = f.LastSize
			}
		}
	}
	candidates := 0
	next := make(map[string]seenFile, len(files))
	for _, fi := range files {
		cur := seenOf(fi)
		next[fi.Path] = cur
		if d.seen.m[fi.Path] != cur {
			continue
		}
		var birth int64
		if !fi.Birth.IsZero() {
			birth = fi.Birth.UnixNano()
		}
		if sz, ok := last[copier.Identity{Dev: fi.Dev, Ino: fi.Ino, Birth: birth}]; ok && sz == fi.Size {
			candidates++
		}
	}
	d.seen.m = next
	d.obs.Reg.Add(obs.MSweepFiles, map[string]string{"outcome": obs.SweepWalked}, float64(len(files)))
	d.obs.Reg.Add(obs.MSweepFiles, map[string]string{"outcome": obs.SweepUnchangedCandidate}, float64(candidates))
}
