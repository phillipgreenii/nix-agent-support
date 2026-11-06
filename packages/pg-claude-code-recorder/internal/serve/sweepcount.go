package serve

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// seenFile is what the daemon last observed of a path: from a sweep walk or a
// watcher event. The store keeps a file's size but not its mtime, so the mtime
// half of the unchanged-candidate rule is compared against this.
type seenFile struct {
	dev, ino uint64
	size     int64
	mtimeNS  int64
	birthNS  int64
}

// sweepSeen is the per-path last observation behind sweep_files_total
// {outcome="unchanged_candidate"}. Every caller already runs under the
// watcher's lock; the mutex only keeps the type safe on its own.
type sweepSeen struct {
	mu sync.Mutex
	m  map[string]seenFile
	// caught is the incremental sweep's cache (see skipUnchanged).
	caught map[string]caughtUp
}

func seenOf(fi watcher.FileInfo) seenFile {
	var birth int64
	if !fi.Birth.IsZero() {
		birth = fi.Birth.UnixNano()
	}
	return seenFile{dev: fi.Dev, ino: fi.Ino, size: fi.Size, mtimeNS: fi.ModTime.UnixNano(), birthNS: birth}
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
		d.seenDB.forget(e.Path)
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
		prev, had := d.seen.m[fi.Path]
		if prev != cur {
			d.obs.Reg.Add(obs.MSweepChanged, map[string]string{"reason": changeReason(prev, cur, had)}, 1)
			continue
		}
		if cu, ok := d.seen.caught[fi.Path]; !ok || cu.seen != cur {
			d.obs.Reg.Add(obs.MSweepChanged, map[string]string{"reason": "not_cached"}, 1)
		}
		birth := cur.birthNS
		if sz, ok := last[copier.Identity{Dev: fi.Dev, Ino: fi.Ino, Birth: birth}]; ok && sz == fi.Size {
			candidates++
		}
	}
	d.seen.m = next
	d.obs.Reg.Add(obs.MSweepFiles, map[string]string{"outcome": obs.SweepWalked}, float64(len(files)))
	d.obs.Reg.Add(obs.MSweepFiles, map[string]string{"outcome": obs.SweepUnchangedCandidate}, float64(candidates))
}

// changeReason names the first way cur differs from the last observation prev
// of the same path (had false: never observed).
func changeReason(prev, cur seenFile, had bool) string {
	switch {
	case !had:
		return "new"
	case prev.size != cur.size:
		return "size"
	case prev.mtimeNS != cur.mtimeNS:
		return "mtime"
	default:
		return "ino"
	}
}

// caughtUp is a path the copier ingested to "unchanged" (fully caught up) when
// it last looked, with what the walk reported then. The entry never expires on
// its own: a rewrite that keeps size, mtime and inode is found by the doctor
// or by a forced sweep (forceSweep), which empties the cache.
type caughtUp struct {
	seen seenFile
	file int64
}

// snapshotSkipMin is the size above which a snapshot file may be skipped by a
// sweep. Snapshot files of this size or smaller (the registry and session-mode
// JSON files) are re-read on every sweep: they are cheap to read, and a rewrite
// that keeps size and mtime is found within one sweep instead of a forced one.
const snapshotSkipMin int64 = 64 << 10

// skipExempt reports whether a sweep must not skip the path: a snapshot-mode
// file of snapshotSkipMin bytes or fewer.
func (d *Daemon) skipExempt(path string, size int64) bool {
	_, mode := d.scope.Classify(path)
	return mode == scope.Snapshot && size <= snapshotSkipMin
}

// skipUnchanged reports whether a sweep event needs no ingest: the path was
// ingested to "unchanged" and the walk reports the same size, mtime and inode.
// A skipped file still tells the parser about itself, so a parse that failed
// earlier is retried exactly as a full ingest would.
func (d *Daemon) skipUnchanged(e watcher.Event) bool {
	file, ok := d.caughtMatches(e.Path, e.Info)
	if !ok {
		return false
	}
	d.prs.Notify(file)
	d.obs.Reg.Add(obs.MSweepFiles, map[string]string{"outcome": obs.SweepSkipped}, 1)
	d.sweepSkipped.Add(1)
	return true
}

// caughtMatches reports the file id of a path whose cached caught-up entry
// still matches the walk's view of it.
// It changes nothing: the start-up backfill uses it to leave such a path alone,
// and the sweep event that follows takes the skip (and counts it).
func (d *Daemon) caughtMatches(path string, fi watcher.FileInfo) (int64, bool) {
	d.seen.mu.Lock()
	c, ok := d.seen.caught[path]
	d.seen.mu.Unlock()
	if !ok || c.seen != seenOf(fi) || d.skipExempt(path, fi.Size) {
		return 0, false
	}
	return c.file, true
}

// noteCaughtUp records the outcome of an ingest. Only a file the copier found
// already caught up ("unchanged": an append file with nothing left to copy, or a
// snapshot whose content hash matches its latest version) is cached; anything
// else (a copy that may be partial, an error) is ingested again next sweep.
// Snapshot files (tool results, subagent metadata, registry files) are about
// half of what a sweep walks, so they are cached too, except those of
// snapshotSkipMin bytes or fewer, which are re-read on every sweep. The sweep walk only reports regular files, so
// a link never reaches here.
func (d *Daemon) noteCaughtUp(e watcher.Event, res copier.Result, err error) {
	d.seen.mu.Lock()
	defer d.seen.mu.Unlock()
	if err != nil || e.Gone || res.Action != "unchanged" || res.File == 0 || d.skipExempt(e.Path, e.Info.Size) {
		delete(d.seen.caught, e.Path)
		d.seenDB.forget(e.Path)
		return
	}
	if d.seen.caught == nil {
		d.seen.caught = map[string]caughtUp{}
	}
	d.seen.caught[e.Path] = caughtUp{seen: seenOf(e.Info), file: res.File}
	d.seenDB.remember(e.Path, res.File, seenOf(e.Info))
}

// clearSkipCache empties the in-memory caught-up cache and the persisted seen
// table, so the next sweep ingests every file. The caller holds the watcher's
// lock (it runs in the sweep's OnSweep hook, before the sweep's events).
func (d *Daemon) clearSkipCache() {
	d.seen.mu.Lock()
	d.seen.caught = map[string]caughtUp{}
	d.seen.mu.Unlock()
	d.seenDB.clear()
	d.setSeenGauge()
}

// beginSweepStats starts the figures of the sweep that onSweep opens: the
// walked count and whether the sweep is a full verification (a forced sweep,
// or the first one after a start with an empty seen table).
func (d *Daemon) beginSweepStats(walked int, verify bool) {
	d.sweepWalked.Store(int64(walked))
	d.sweepSkipped.Store(0)
	d.sweepIngested.Store(0)
	d.sweepMissing.Store(0)
	d.sweepVerify.Store(verify)
	d.sweepActive.Store(true)
	if verify {
		d.obs.Reg.Add(obs.MSweepVerifyForced, nil, 1)
	}
}

// timeSweep runs one sweep pass as the watcher_sweep loop (which records
// loop_duration_seconds) and, when the pass got as far as its walk callback,
// sets sweep_skipped_ratio and logs one sweep_done line.
func (d *Daemon) timeSweep(step func() error) error {
	start := d.clock.Now()
	d.sweepActive.Store(false)
	err := d.runLoop(obs.LoopWatcherSweep, step)
	if d.sweepActive.Swap(false) {
		d.finishSweep(d.clock.Now().Sub(start))
	}
	return err
}

func (d *Daemon) finishSweep(dur time.Duration) {
	walked, skipped := d.sweepWalked.Load(), d.sweepSkipped.Load()
	ratio := 0.0
	if walked > 0 {
		ratio = float64(skipped) / float64(walked)
	}
	d.obs.Reg.Set(obs.MSweepSkippedRatio, nil, ratio)
	d.obs.Log.Info("sweep_done", "walked", walked, "skipped", skipped,
		"ingested", d.sweepIngested.Load(), "missing", d.sweepMissing.Load(),
		"duration_ms", dur.Milliseconds(), "verify", d.sweepVerify.Load())
}
