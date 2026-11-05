package serve

import (
	"sync"
	"time"

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
	// caught is the incremental sweep's cache (see skipUnchanged); verifiedAt
	// is when it was last emptied for a full verification.
	caught     map[string]caughtUp
	verifiedAt time.Time
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
// size. It only counts; skipUnchanged takes the saving.
// It runs before the sweep's own events.
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

// verifyEvery is how long the incremental sweep trusts its caught-up cache. A
// sweep that finds the cache older than this ingests every file again (a full
// verification), which catches what size, mtime and inode cannot: a rewrite
// that keeps all three.
const verifyEvery = 24 * time.Hour

// caughtUp is a path the copier ingested to "unchanged" (fully caught up) when
// it last looked, with what the walk reported then.
type caughtUp struct {
	seen seenFile
	file int64
}

// verifyIfDue empties the caught-up cache when the last full verification is
// older than verifyEvery, so this sweep ingests everything. The cache starts
// empty, so the first sweep after a start is always a full one. It runs under
// the watcher's lock before the sweep's events.
func (d *Daemon) verifyIfDue() {
	now := d.clock.Now()
	d.seen.mu.Lock()
	defer d.seen.mu.Unlock()
	if d.seen.verifiedAt.IsZero() || now.Sub(d.seen.verifiedAt) >= verifyEvery {
		d.seen.caught = nil
		d.seen.verifiedAt = now
	}
}

// skipUnchanged reports whether a sweep event needs no ingest: the path was
// ingested to "unchanged" and the walk reports the same size, mtime and inode.
// A skipped file still tells the parser about itself, so a parse that failed
// earlier is retried exactly as a full ingest would.
func (d *Daemon) skipUnchanged(e watcher.Event) bool {
	d.seen.mu.Lock()
	c, ok := d.seen.caught[e.Path]
	d.seen.mu.Unlock()
	if !ok || c.seen != seenOf(e.Info) {
		return false
	}
	d.prs.Notify(c.file)
	return true
}

// noteCaughtUp records the outcome of an ingest. Only a file the copier found
// already caught up ("unchanged": an append file with nothing left to copy, or a
// snapshot whose content hash matches its latest version) is cached; anything
// else (a copy that may be partial, an error) is ingested again next sweep.
// Snapshot files (tool results, subagent metadata, registry files) are about
// half of what a sweep walks, so leaving them out would keep every one of them
// read and hashed on every sweep. The sweep walk only reports regular files, so
// a link never reaches here.
func (d *Daemon) noteCaughtUp(e watcher.Event, res copier.Result, err error) {
	d.seen.mu.Lock()
	defer d.seen.mu.Unlock()
	if err != nil || e.Gone || res.Action != "unchanged" || res.File == 0 {
		delete(d.seen.caught, e.Path)
		return
	}
	if d.seen.caught == nil {
		d.seen.caught = map[string]caughtUp{}
	}
	d.seen.caught[e.Path] = caughtUp{seen: seenOf(e.Info), file: res.File}
}
