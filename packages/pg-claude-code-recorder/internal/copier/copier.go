// Package copier keeps a byte-exact copy of every ingested file. It is the only
// writer of copies; the store is written through its one serialized connection.
//
// For every changed file (the path is a hint only) it checks identity, copies
// and commits the offset: an append source is read from the committed offset,
// the bytes read are written and fsynced, and only then is the new offset
// committed (never a size observed after the read). A snapshot source is read
// whole, hashed and stored as a new version when the hash is new.
package copier

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lockcheck"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

const (
	// TailSize is the number of trailing bytes of a copy that identify it.
	TailSize = 4096
	// FirstBytes is the number of leading bytes hashed to tell a rename from a
	// reused inode.
	FirstBytes = 64

	defaultChunk = 256 << 10
	maxBatch     = 8 << 20
)

// Config configures a Copier.
type Config struct {
	Store *store.Store
	Scope *scope.Scope
	Clock clock.Clock
	Clone ClonePolicy
	Obs   *obs.Obs
	FS    fsx.FS // state-directory file system; the store's when nil
	// SrcFS is the source-side lstat/open seam; the real file system when nil.
	SrcFS SrcFS

	// InferMissed counts the snapshot versions that can be inferred to have
	// been missed between the previous stored content and the new one. The
	// parser packet supplies the source-specific rule; nil counts none.
	InferMissed func(source string, prev, cur []byte) int
	// JSONRetries and JSONRetryDelay bound the re-read of a snapshot whose JSON
	// is not yet valid (a file rewritten in place).
	JSONRetries    int
	JSONRetryDelay time.Duration
	// Chunk is the read size (default 256 KiB).
	Chunk int
}

// Copier ingests changed files. Calls are serialized.
type Copier struct {
	cfg         Config
	st          *store.Store
	fs          fsx.FS
	src         SrcFS
	mu          lockcheck.Mutex
	cloneOK     bool
	cloneReason string

	// privMu guards the lifetime private-clone figures below (its own lock, so
	// the figures never depend on the copier lock being held).
	privMu    sync.Mutex
	privMax   int64
	privTotal int64

	// scan is the incremental private-size scan of the whole copies/ tree.
	scan privateScan

	// onContended, when set, runs once a lock attempt found the copier busy and
	// before it queues (a test seam that lets a test move the injected clock
	// while the caller is queued).
	onContended func()

	// afterSweepBatch, when set, runs between two batches of SweepMissingWalk
	// with the copier lock released (a test seam).
	afterSweepBatch func(done int)

	// onChange, when set, is told of every file whose persisted state a commit
	// changed (see SetOnFileChange).
	onChange atomic.Pointer[func(file, copyOffset int64)]
}

// lock takes the copier lock. A contended acquisition observes the seconds the
// caller queued (copier_lock_wait_seconds) from the injected clock; an
// uncontended one reads no clock and touches no registry lock, so the
// per-event path pays nothing while the lock is free.
func (c *Copier) lock() {
	if c.mu.TryLock() {
		return
	}
	start := c.cfg.Clock.Now()
	if c.onContended != nil {
		c.onContended()
	}
	c.mu.Lock()
	c.cfg.Obs.Reg.Observe(obs.MCopierLockWait, c.cfg.Clock.Now().Sub(start).Seconds())
}

// ErrNotYetValid is returned when a snapshot's JSON stays invalid after the
// retries; the file is re-read on its next change.
var ErrNotYetValid = errors.New("copier: snapshot JSON not yet valid")

// New returns a Copier and probes the copy method once.
func New(cfg Config) (*Copier, error) {
	if cfg.Store == nil || cfg.Scope == nil {
		return nil, errors.New("copier: Store and Scope are required")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.Obs == nil {
		cfg.Obs = obs.New(nil)
	}
	if cfg.Clone == nil {
		cfg.Clone = SystemClone{}
	}
	if cfg.JSONRetries == 0 {
		cfg.JSONRetries = 5
	}
	if cfg.JSONRetryDelay == 0 {
		cfg.JSONRetryDelay = 20 * time.Millisecond
	}
	if cfg.Chunk == 0 {
		cfg.Chunk = defaultChunk
	}
	c := &Copier{cfg: cfg, st: cfg.Store, fs: cfg.FS, src: cfg.SrcFS}
	if c.src == nil {
		c.src = osSrcFS{}
	}
	if c.fs == nil {
		c.fs = cfg.Store.FS()
	}
	c.cloneOK, c.cloneReason = cfg.Clone.Probe(cfg.Store.Dir(), cfg.Scope.ClaudeDir())
	method := "bytes"
	if c.cloneOK {
		method = "clone"
	}
	cfg.Obs.Event(obs.EvCopyMethod, "method", method, "reason", c.cloneReason)
	return c, nil
}

// Method returns the mode chosen by the probe at start and why.
func (c *Copier) Method() (method, reason string) {
	if c.cloneOK {
		return "clone", c.cloneReason
	}
	return "bytes", c.cloneReason
}

// Result describes what Ingest did.
type Result struct {
	Action string // skipped, unchanged, copied, new-generation, snapshot, link, missing, tombstone, raced
	Reason string
	File   int64
	Bytes  int64
}

func (c *Copier) now() int64 { return c.cfg.Clock.Now().UnixNano() }

// Ingest processes one changed path (a hint only).
func (c *Copier) Ingest(path string) (Result, error) {
	c.lock()
	defer c.mu.Unlock()
	return c.ingestLocked(path)
}

func (c *Copier) ingestLocked(path string) (Result, error) {
	path = filepath.Clean(path)
	if d := c.cfg.Scope.Match(path); !d.Ingest {
		return Result{Action: "skipped", Reason: d.Reason}, nil
	}
	fi, err := c.lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c.handleMissing(path)
	}
	if err != nil {
		return Result{}, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return c.handleLink(path, fi)
	}
	if !fi.Mode().IsRegular() {
		return Result{Action: "skipped", Reason: "not_regular"}, nil
	}
	src, mode := c.cfg.Scope.Classify(path)
	if mode == scope.Snapshot {
		return c.ingestSnapshot(path, fi, src)
	}
	// A new append file takes the first-copy path: one journal commit and one
	// directory sync (the same flush a backfill batch shares across its files).
	if s, err := c.stageFirstCopy(path, nil); err != nil {
		return Result{}, err
	} else if s != nil {
		if err := c.flushStaged([]*staged{s}); err != nil {
			return Result{}, err
		}
		return Result{Action: "new-generation", File: s.fid}, nil
	}
	return c.ingestAppend(path, fi, src)
}

// ---- identity and path resolution ----

func (c *Copier) currentPathSet(f store.File) (map[string]bool, error) {
	ps, err := c.st.CurrentPaths(f.ID)
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, p := range ps {
		m[p] = true
	}
	return m, nil
}

func firstHash(f *os.File, size int64) uint64 {
	if size < FirstBytes {
		return 0
	}
	b := make([]byte, FirstBytes)
	if _, err := f.ReadAt(b, 0); err != nil {
		return 0
	}
	return xxhash.Sum64(b)
}

// resolveAppend finds or creates the file row for an append source at path.
func (c *Copier) resolveAppend(path string, id Identity, size int64, src *os.File, source string) (store.File, error) {
	cands, err := c.st.FilesByIdentity(id.Dev, id.Ino)
	if err != nil {
		return store.File{}, err
	}
	var live []store.File
	for _, f := range cands {
		if f.Mode == string(scope.Append) && f.Birth == id.Birth && f.State != "deleted" {
			live = append(live, f)
		}
	}
	for _, f := range live {
		set, err := c.currentPathSet(f)
		if err != nil {
			return store.File{}, err
		}
		if set[path] {
			return f, nil
		}
	}
	fh := firstHash(src, size)
	for _, f := range live {
		// A known inode at a new path is a rename (or a hard link) only when its
		// birth time (checked above) and the hash of its first bytes match.
		if f.FirstHash != 0 && fh != 0 && f.FirstHash != fh {
			continue
		}
		return f, c.addPath(f, path)
	}
	return c.createFile(path, id, source, string(scope.Append), "", fh)
}

// addPath records path as naming f and closes f's other paths that no longer
// name it (a rename or directory rename); a path that still names the same
// inode stays (a hard link: one file with two paths).
func (c *Copier) addPath(f store.File, path string) error {
	now := c.now()
	recs := []*store.Rec{{T: store.TPath, At: now, File: f.ID, Path: path}}
	if err := c.closeOtherHolders(path, f.ID, &recs, now); err != nil {
		return err
	}
	old, err := c.st.CurrentPaths(f.ID)
	if err != nil {
		return err
	}
	for _, p := range old {
		if p == path {
			continue
		}
		fi, err := c.lstat(p)
		if err != nil || !sameInode(fi, f) {
			recs = append(recs, &store.Rec{T: store.TPathClose, At: now, File: f.ID, Path: p})
		}
	}
	return c.commit(recs...)
}

func sameInode(fi fs.FileInfo, f store.File) bool {
	id, ok := identityOf("", fi)
	return ok && id.Dev == f.Dev && id.Ino == f.Ino
}

// closeOtherHolders closes path for any other file that currently holds it
// (a path that now holds a different file is a new file).
func (c *Copier) closeOtherHolders(path string, except int64, recs *[]*store.Rec, now int64) error {
	holders, err := c.st.FilesByCurrentPath(path)
	if err != nil {
		return err
	}
	for _, h := range holders {
		if h.ID != except {
			*recs = append(*recs, &store.Rec{T: store.TPathClose, At: now, File: h.ID, Path: path})
		}
	}
	return nil
}

func (c *Copier) createFile(path string, id Identity, source, mode, link string, fh uint64) (store.File, error) {
	now := c.now()
	fid := c.st.NewFileID()
	recs := []*store.Rec{}
	if err := c.closeOtherHolders(path, fid, &recs, now); err != nil {
		return store.File{}, err
	}
	recs = append(recs,
		&store.Rec{T: store.TFile, At: now, File: fid, Source: source, Mode: mode, Dev: id.Dev, Ino: id.Ino, Birth: id.Birth, FirstHash: fh, Link: link},
		&store.Rec{T: store.TPath, At: now, File: fid, Path: path})
	if err := c.commit(recs...); err != nil {
		return store.File{}, err
	}
	return c.st.File(fid)
}

// ---- links ----

func (c *Copier) handleLink(path string, fi fs.FileInfo) (Result, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return Result{}, err
	}
	holders, err := c.st.FilesByCurrentPath(path)
	if err != nil {
		return Result{}, err
	}
	for _, h := range holders {
		if h.Mode == "link" && h.Link == target {
			return Result{Action: "unchanged", File: h.ID}, nil
		}
	}
	id, _ := identityOf(path, fi)
	f, err := c.createFile(path, id, "symlink", "link", target, 0)
	if err != nil {
		return Result{}, err
	}
	return Result{Action: "link", File: f.ID}, nil
}

// ---- missing paths ----

func (c *Copier) handleMissing(path string) (Result, error) {
	holders, err := c.st.FilesByCurrentPath(path)
	if err != nil {
		return Result{}, err
	}
	if len(holders) == 0 {
		return Result{Action: "skipped", Reason: "unknown_path"}, nil
	}
	res := Result{Action: "missing"}
	for _, h := range holders {
		res.File = h.ID
		if h.Mode == string(scope.Snapshot) {
			// A snapshot source is keyed by path: a vanished path is a deletion record.
			if err := c.tombstone(h, path); err != nil {
				return Result{}, err
			}
			res.Action = "tombstone"
		}
		// An append file's missing path becomes a deletion only after a full sweep
		// finds no file with its identity elsewhere (SweepMissing).
	}
	return res, nil
}

func (c *Copier) tombstone(f store.File, path string) error {
	now := c.now()
	if err := c.commit(&store.Rec{T: store.TTomb, At: now, File: f.ID, Path: path}); err != nil {
		return err
	}
	var lost int64
	if f.Mode == string(scope.Append) && f.CurGen > 0 {
		if g, err := c.st.Generation(f.ID, f.CurGen); err == nil && f.LastSize > g.CopyOffset {
			lost = f.LastSize - g.CopyOffset
		}
	}
	if lost > 0 {
		c.cfg.Obs.Reg.Add(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonVanished}, float64(lost))
	}
	c.cfg.Obs.Event(obs.EvFileVanished, "file", fmt.Sprint(f.ID), "path", path, "lost_bytes", fmt.Sprint(lost))
	return nil
}

// sweepBatch is the number of files SweepMissing handles per hold of the copier
// lock, so a live ingest waits for at most one batch.
const sweepBatch = 256

// SweepStats is what one SweepMissing pass did.
type SweepStats struct {
	// Present is the number of files the walk showed present (no lstat).
	Present int
	// LstatFallbacks is the number of files that were not shown present by the
	// walk and so got the per-file lstat check.
	LstatFallbacks int
	// Tombstoned is the number of files recorded deleted; PathsClosed the number
	// of files that had at least one path closed without being deleted.
	Tombstoned, PathsClosed int
}

// SweepMissing is SweepMissingWalk with no path information from the walk: every
// stored path is checked with an lstat.
func (c *Copier) SweepMissing(found map[Identity]bool) error {
	_, err := c.SweepMissingWalk(found, nil)
	return err
}

// SweepMissingWalk finishes the missing-path rule after a full sweep of the
// Claude directory. found holds the identity of every file the sweep saw and
// walked maps every walked path to the identity the walk saw there. A stored
// file whose current paths are all in walked with the stored identity is
// present and costs no lstat. Any other file gets the lstat check for itself
// only: a known path that no longer names its file is closed, and a file left
// with no path whose identity is not in found is deleted (a Tombstone).
//
// The copier lock is held per batch of sweepBatch files, not for the whole
// pass. A file the walk does not show present is re-read (its row and paths)
// under the lock before anything is recorded, so a file ingested between
// batches is judged on its current state, not on the snapshot taken first.
func (c *Copier) SweepMissingWalk(found map[Identity]bool, walked map[string]Identity) (SweepStats, error) {
	var st SweepStats
	c.lock()
	files, err := c.st.AllFiles()
	if err != nil {
		c.mu.Unlock()
		return st, err
	}
	all, err := c.st.AllCurrentPaths()
	c.mu.Unlock()
	if err != nil {
		return st, err
	}
	for start := 0; start < len(files); start += sweepBatch {
		if start > 0 && c.afterSweepBatch != nil {
			c.afterSweepBatch(start / sweepBatch)
		}
		if err := c.sweepBatchLocked(files[start:min(start+sweepBatch, len(files))], all, found, walked, &st); err != nil {
			return st, err
		}
	}
	return st, nil
}

// presentInWalk reports whether every path stored for f is in the walk with
// f's stored identity (a file with no stored path has nothing to check).
func presentInWalk(f store.File, paths []string, walked map[string]Identity) bool {
	if walked == nil {
		return false
	}
	want := Identity{Dev: f.Dev, Ino: f.Ino, Birth: f.Birth}
	for _, p := range paths {
		if id, ok := walked[p]; !ok || id != want {
			return false
		}
	}
	return true
}

func (c *Copier) sweepBatchLocked(files []store.File, all map[int64][]string, found map[Identity]bool, walked map[string]Identity, st *SweepStats) error {
	c.lock()
	defer c.mu.Unlock()
	for _, f := range files {
		if f.State == "deleted" {
			continue
		}
		paths := all[f.ID]
		if len(paths) == 0 {
			continue
		}
		if presentInWalk(f, paths, walked) {
			st.Present++
			continue
		}
		// Not shown present: re-read the row and its paths under the lock.
		fresh, err := c.st.File(f.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		f = fresh
		if f.State == "deleted" {
			continue
		}
		paths, err = c.st.CurrentPaths(f.ID)
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			continue
		}
		if presentInWalk(f, paths, walked) {
			st.Present++
			continue
		}
		st.LstatFallbacks++
		var gone []string
		for _, p := range paths {
			fi, err := c.lstat(p)
			if err != nil || (f.Mode != "link" && !sameInode(fi, f) && f.Mode != string(scope.Snapshot)) {
				gone = append(gone, p)
			}
		}
		if len(gone) == 0 {
			continue
		}
		if len(gone) == len(paths) && !found[Identity{Dev: f.Dev, Ino: f.Ino, Birth: f.Birth}] {
			if err := c.tombstone(f, gone[0]); err != nil {
				return err
			}
			st.Tombstoned++
			continue
		}
		now := c.now()
		var recs []*store.Rec
		for _, p := range gone {
			recs = append(recs, &store.Rec{T: store.TPathClose, At: now, File: f.ID, Path: p})
		}
		if err := c.commit(recs...); err != nil {
			return err
		}
		st.PathsClosed++
	}
	return nil
}

// ---- snapshot sources ----

func needsValidJSON(source string) bool {
	switch source {
	case scope.SrcRegistry, scope.SrcSubagentMD, scope.SrcSessionMode, scope.SrcCompaction, scope.SrcControlEnd:
		return true
	}
	return false
}

func (c *Copier) ingestSnapshot(path string, fi fs.FileInfo, source string) (Result, error) {
	id, _ := identityOf(path, fi)
	var f store.File
	holders, err := c.st.FilesByCurrentPath(path)
	if err != nil {
		return Result{}, err
	}
	for _, h := range holders {
		// A holder recorded under another source (the path was unparsed before a
		// scope change) is not continued: a file's source never changes, so the
		// path starts a new file under the current source and closes the old one.
		if h.Mode == string(scope.Snapshot) && h.Source == source {
			f = h
			break
		}
	}
	if f.ID == 0 {
		if h, err := c.st.SnapshotFileByPathHistory(path); err == nil {
			if h.Source == source {
				f = h
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return Result{}, err
		}
	}
	var data []byte
	var seenID Identity
	for attempt := 0; ; attempt++ {
		fh, err := c.openNoFollow(path)
		if errors.Is(err, fs.ErrNotExist) {
			return c.handleMissing(path)
		}
		if err != nil {
			return Result{}, err
		}
		fst, err := fh.Stat()
		if err == nil {
			data, err = io.ReadAll(fh)
			seenID, _ = identityOf(path, fst)
		}
		_ = fh.Close()
		if err != nil {
			return Result{}, err
		}
		if !needsValidJSON(source) || json.Valid(data) {
			break
		}
		if attempt >= c.cfg.JSONRetries {
			return Result{Action: "skipped", Reason: "json_invalid"}, ErrNotYetValid
		}
		c.cfg.Clock.Sleep(c.cfg.JSONRetryDelay)
	}
	if seenID != (Identity{}) {
		id = seenID
	}
	now := c.now()
	if f.ID == 0 {
		f, err = c.createFile(path, id, source, string(scope.Snapshot), "", 0)
		if err != nil {
			return Result{}, err
		}
	}
	h := xxhash.Sum64(data)
	prev, perr := c.st.LatestSnapshot(f.ID)
	if perr != nil && !errors.Is(perr, store.ErrNotFound) {
		return Result{}, perr
	}
	revived := f.State == "deleted"
	if perr == nil && prev.Hash == h && !revived {
		// A file whose identity moved under an unchanged content (a device
		// number that changed across a reboot or a remount) keeps its row's
		// stale identity otherwise: the seen table compares its rows against
		// the row's identity, so every such file would look new at each start.
		if f.Dev != id.Dev || f.Ino != id.Ino || f.Birth != id.Birth {
			if err := c.commit(&store.Rec{T: store.TIdent, At: now, File: f.ID, Dev: id.Dev, Ino: id.Ino, Birth: id.Birth}); err != nil {
				return Result{}, err
			}
		}
		return Result{Action: "unchanged", File: f.ID}, nil
	}
	version := prev.Version + 1
	if errors.Is(perr, store.ErrNotFound) {
		version = 1
	}
	if perr == nil && prev.Hash == h && revived {
		// The same content reappeared after a deletion: a new version is still a
		// fact (the path came back).
		version = prev.Version + 1
	}
	if err := c.writeSnapshot(f, version, data); err != nil {
		return Result{}, err
	}
	faults.At("snapshot.after-copy")
	recs := []*store.Rec{}
	if f.Dev != id.Dev || f.Ino != id.Ino || f.Birth != id.Birth {
		recs = append(recs, &store.Rec{T: store.TIdent, At: now, File: f.ID, Dev: id.Dev, Ino: id.Ino, Birth: id.Birth})
	}
	recs = append(recs, &store.Rec{T: store.TSnap, At: now, File: f.ID, Version: version, Hash: h, Size: int64(len(data))})
	if revived {
		recs = append(recs, &store.Rec{T: store.TPath, At: now, File: f.ID, Path: path})
	}
	if err := c.commit(recs...); err != nil {
		return Result{}, err
	}
	if perr == nil && c.cfg.InferMissed != nil {
		if prevData, err := os.ReadFile(c.st.SnapshotPath(f.ID, prev.Version)); err == nil {
			if n := c.cfg.InferMissed(source, prevData, data); n > 0 {
				c.cfg.Obs.Reg.Add(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonSnapMissed}, float64(n))
				c.cfg.Obs.Event(obs.EvSnapshotMissed, "file", fmt.Sprint(f.ID), "missed", fmt.Sprint(n))
			}
		}
	}
	return Result{Action: "snapshot", File: f.ID, Bytes: int64(len(data))}, nil
}

// writeSnapshot stores the bytes: temporary file, fsync, rename, directory fsync.
func (c *Copier) writeSnapshot(f store.File, version int, data []byte) error {
	final := c.st.SnapshotPath(f.ID, version)
	tmp := final + ".tmp"
	tf, err := c.fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := tf.WriteAt(data, 0); err != nil {
		_ = tf.Close()
		return err
	}
	if err := tf.Sync(); err != nil {
		_ = tf.Close()
		return err
	}
	if err := tf.Close(); err != nil {
		return err
	}
	if err := c.fs.Rename(tmp, final); err != nil {
		return err
	}
	return c.fs.SyncDir(filepath.Dir(final))
}

// ---- append sources ----

func (c *Copier) ingestAppend(path string, fi fs.FileInfo, source string) (Result, error) {
	src, err := c.openNoFollow(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c.handleMissing(path)
	}
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = src.Close() }()
	fst, err := src.Stat()
	if err != nil {
		return Result{}, err
	}
	id, ok := identityOf(path, fst)
	if !ok {
		return Result{}, errors.New("copier: no identity for " + path)
	}
	if lid, ok := identityOf(path, fi); ok && lid != id {
		return Result{Action: "raced", Reason: "replaced_during_open"}, nil
	}
	size := fst.Size()
	f, err := c.resolveAppend(path, id, size, src, source)
	if err != nil {
		return Result{}, err
	}
	if f.FirstHash == 0 && size >= FirstBytes && f.CurGen > 0 {
		if err := c.commit(&store.Rec{T: store.TIdent, At: c.now(), File: f.ID, Dev: f.Dev, Ino: f.Ino, Birth: f.Birth, FirstHash: firstHash(src, size)}); err != nil {
			return Result{}, err
		}
	}
	res := Result{File: f.ID, Action: "unchanged"}
	if f.State == "deleted" {
		// The deleted file's inode reappeared at a path.
		if err := c.commit(&store.Rec{T: store.TState, At: c.now(), File: f.ID, State: "ok"}); err != nil {
			return Result{}, err
		}
	}
	for round := 0; round < 64; round++ {
		f, err = c.st.File(f.ID)
		if err != nil {
			return Result{}, err
		}
		step, err := c.appendStep(f, src, size, path)
		if err != nil {
			return Result{}, err
		}
		if step.Action == "new-generation" || res.Action == "unchanged" {
			res.Action = step.Action
		} else if step.Action == "copied" && res.Action != "new-generation" {
			res.Action = "copied"
		}
		res.Bytes += step.Bytes
		if step.Done {
			break
		}
	}
	c.recordLag(src, f.ID, res.Bytes > 0)
	return res, nil
}

type step struct {
	Action string
	Bytes  int64
	Done   bool
}

func (c *Copier) copyPathLen(path string) (int64, bool, error) {
	fi, err := c.fs.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return fi.Size(), true, nil
}

func (c *Copier) appendStep(f store.File, src *os.File, size int64, path string) (step, error) {
	if f.CurGen == 0 {
		if err := c.startGeneration(f, src, "first", false); err != nil {
			return step{}, err
		}
		return step{Action: "new-generation"}, nil
	}
	g, err := c.st.Generation(f.ID, f.CurGen)
	if err != nil {
		return step{}, err
	}
	cp := c.st.CopyPath(f.ID, g.Gen)
	clen, exists, err := c.copyPathLen(cp)
	if err != nil {
		return step{}, err
	}
	// A settled generation keeps its archived prefix in the archive, not in the
	// copy file: a copy file that is gone while the archive covers every
	// committed byte is the settled state, not a mismatch.
	settled := !exists && g.CopyOffset > 0 && c.st.ArchivedTo(f.ID, g.Gen) >= g.CopyOffset
	switch {
	case settled:
	case clen > g.CopyOffset:
		// A crash between fsync and the offset commit (or an ENOSPC mid-copy).
		if err := c.trimCopy(cp, g.CopyOffset); err != nil {
			return step{}, err
		}
		c.cfg.Obs.Reg.Add(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonTrimmed}, float64(clen-g.CopyOffset))
		c.cfg.Obs.Event(obs.EvCopyTrimmed, "file", fmt.Sprint(f.ID), "gen", fmt.Sprint(g.Gen), "trimmed", fmt.Sprint(clen-g.CopyOffset))
	case clen < g.CopyOffset || (!exists && g.CopyOffset > 0):
		// After a power loss a copy can be shorter than its committed offset.
		c.cfg.Obs.Event(obs.EvCopyMismatch, "file", fmt.Sprint(f.ID), "gen", fmt.Sprint(g.Gen))
		if err := c.commit(&store.Rec{T: store.TState, At: c.now(), File: f.ID, State: "copy_mismatch"}); err != nil {
			return step{}, err
		}
		f.State = "copy_mismatch"
		if err := c.startGeneration(f, src, "copy_mismatch", true); err != nil {
			return step{}, err
		}
		return step{Action: "new-generation"}, nil
	}
	if size < g.CopyOffset {
		if err := c.startGeneration(f, src, "shrink", true); err != nil {
			return step{}, err
		}
		return step{Action: "new-generation"}, nil
	}
	// prevTail is the last bytes of the copy when the tail check already read
	// them (or the copy is empty), so the append can build the new tail from it.
	var prevTail []byte
	havePrev := g.CopyOffset == 0
	if g.CopyOffset > 0 {
		ok, tail, err := c.tailMatches(f, g, src)
		if err != nil {
			return step{}, err
		}
		if ok && int64(len(tail)) == min(int64(TailSize), g.CopyOffset) {
			prevTail, havePrev = tail, true
		}
		if !ok {
			if err := c.startGeneration(f, src, "tail_mismatch", true); err != nil {
				return step{}, err
			}
			return step{Action: "new-generation"}, nil
		}
	}
	n, err := c.appendBytes(f, g, src, size, prevTail, havePrev)
	if err != nil {
		return step{}, err
	}
	if n == 0 {
		return step{Action: "unchanged", Done: true}, nil
	}
	return step{Action: "copied", Bytes: n, Done: n < maxBatch}, nil
}

func (c *Copier) trimCopy(cp string, to int64) error {
	f, err := c.fs.OpenFile(cp, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(to); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// tailMatches compares the last bytes of the copy with the file at the same
// position. The tail comes from the sidecar when it matches its recorded hash;
// otherwise it is re-read from the copy (a lost sidecar costs nothing).
func (c *Copier) tailMatches(f store.File, g store.Generation, src *os.File) (bool, []byte, error) {
	tail, err := c.tailBytes(f, g)
	if err != nil {
		return false, nil, err
	}
	if len(tail) == 0 {
		return true, nil, nil
	}
	got := make([]byte, len(tail))
	n, err := src.ReadAt(got, g.CopyOffset-int64(len(tail)))
	if err != nil && !errors.Is(err, io.EOF) {
		return false, nil, err
	}
	if n != len(tail) {
		return false, nil, nil
	}
	return string(got) == string(tail), tail, nil
}

func (c *Copier) tailBytes(f store.File, g store.Generation) ([]byte, error) {
	if g.TailLen == 0 {
		return nil, nil
	}
	if b, err := os.ReadFile(c.st.TailPath(f.ID)); err == nil && len(b) == g.TailLen && xxhash.Sum64(b) == g.TailHash {
		return b, nil
	}
	if c.st.ArchivedTo(f.ID, g.Gen) > 0 {
		return c.readArchived(f.ID, g.Gen, g.CopyOffset-int64(g.TailLen), g.TailLen)
	}
	cf, err := c.fs.OpenFile(c.st.CopyPath(f.ID, g.Gen), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cf.Close() }()
	b := make([]byte, g.TailLen)
	if _, err := cf.ReadAt(b, g.CopyOffset-int64(g.TailLen)); err != nil {
		return nil, err
	}
	return b, nil
}

// readArchived reads n bytes at off of a generation through the pointer, which
// reads a settled prefix from its archive.
func (c *Copier) readArchived(file int64, gen int, off int64, n int) ([]byte, error) {
	p, err := c.st.OpenPointer(file, gen)
	if err != nil {
		return nil, err
	}
	defer func() { _ = p.Close() }()
	b := make([]byte, n)
	if _, err := p.ReadAt(b, off); err != nil {
		return nil, err
	}
	return b, nil
}

// readTailArchived reads the last min(TailSize, off) bytes of a generation
// that has an archived prefix.
func (c *Copier) readTailArchived(file int64, gen int, off int64) ([]byte, error) {
	n := int64(TailSize)
	if off < n {
		n = off
	}
	if n == 0 {
		return nil, nil
	}
	return c.readArchived(file, gen, off-n, int(n))
}

func (c *Copier) writeTail(f store.File, tail []byte) error {
	final := c.st.TailPath(f.ID)
	tmp := final + ".tmp"
	tf, err := c.fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := tf.WriteAt(tail, 0); err != nil {
		_ = tf.Close()
		return err
	}
	if err := tf.Close(); err != nil {
		return err
	}
	return c.fs.Rename(tmp, final)
}

// readTailOf reads the last min(TailSize, off) bytes of a copy file.
func (c *Copier) readTailOf(cp string, off int64) ([]byte, error) {
	n := int64(TailSize)
	if off < n {
		n = off
	}
	if n == 0 {
		return nil, nil
	}
	cf, err := c.fs.OpenFile(cp, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cf.Close() }()
	b := make([]byte, n)
	if _, err := cf.ReadAt(b, off-n); err != nil {
		return nil, err
	}
	return b, nil
}

// appendBytes reads from the committed offset, writes exactly the bytes read,
// fsyncs, and only then commits the new offset. prevTail is the copy's last bytes before this
// append when havePrev (it saves re-reading them from the copy).
func (c *Copier) appendBytes(f store.File, g store.Generation, src *os.File, size int64, prevTail []byte, havePrev bool) (int64, error) {
	off := g.CopyOffset
	var data []byte
	chunk := make([]byte, c.cfg.Chunk)
	for len(data) < maxBatch {
		n, err := src.ReadAt(chunk, off+int64(len(data)))
		data = append(data, chunk[:n]...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, err
		}
		if n == 0 {
			break
		}
	}
	if len(data) == 0 {
		return 0, nil
	}
	faults.At("copy.after-read")
	cp := c.st.CopyPath(f.ID, g.Gen)
	cf, err := c.fs.OpenFile(cp, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = cf.Close() }()
	if _, err := cf.WriteAt(data, off); err != nil {
		return 0, err
	}
	faults.At("copy.after-append")
	if err := cf.Sync(); err != nil {
		return 0, err
	}
	faults.At("copy.after-fsync")
	newOff := off + int64(len(data))
	var tail []byte
	if len(data) >= TailSize {
		tail = data[len(data)-TailSize:]
	} else if havePrev {
		joined := append(append(make([]byte, 0, len(prevTail)+len(data)), prevTail...), data...)
		if len(joined) > TailSize {
			joined = joined[len(joined)-TailSize:]
		}
		tail = joined
	} else if c.st.ArchivedTo(f.ID, g.Gen) > 0 {
		tail, err = c.readTailArchived(f.ID, g.Gen, newOff)
		if err != nil {
			return 0, err
		}
	} else {
		tail, err = c.readTailOf(cp, newOff)
		if err != nil {
			return 0, err
		}
	}
	obsSize := size
	if newOff > obsSize {
		obsSize = newOff
	}
	if err := c.commit(&store.Rec{
		T: store.TCommit, At: c.now(), File: f.ID, Gen: g.Gen, From: off, To: newOff,
		Size: obsSize, Tail: xxhash.Sum64(tail), TailLen: len(tail),
	}); err != nil {
		return 0, err
	}
	faults.At("copy.after-commit")
	if err := c.writeTail(f, tail); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

// startGeneration begins a new generation from byte 0, keeping the old one. It
// seeds the copy with a clone when clone mode applies; the copy offset after a
// clone is the clone's size, and a later append starts from there.
func (c *Copier) startGeneration(f store.File, src *os.File, reason string, reset bool) error {
	gen := f.CurGen + 1
	dst := c.st.CopyPath(f.ID, gen)
	for _, p := range []string{dst, dst + ".tmp"} {
		if err := c.fs.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	method, why := "bytes", c.cloneReason
	var off int64
	var tail []byte
	if c.cloneOK {
		o, t, err := c.tryClone(src, dst, true)
		switch {
		case err == nil && o >= 0:
			method, why, off, tail = "clone", "clone ok", o, t
		case err != nil:
			return err
		default:
			why = "clone fell back to bytes"
		}
	}
	if method == "bytes" {
		cf, err := c.fs.OpenFile(dst, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		if err := cf.Sync(); err != nil {
			_ = cf.Close()
			return err
		}
		_ = cf.Close()
		if err := c.fs.SyncDir(filepath.Dir(dst)); err != nil {
			return err
		}
	}
	fst, _ := src.Stat()
	var observed int64
	if fst != nil {
		observed = fst.Size()
	}
	if off > observed {
		observed = off
	}
	if reset {
		lost := f.LastSize
		if g, err := c.st.Generation(f.ID, f.CurGen); err == nil && lost > g.CopyOffset {
			lost = lost - g.CopyOffset
		} else {
			lost = 0
		}
		if lost > 0 {
			c.cfg.Obs.Reg.Add(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonGeneration}, float64(lost))
		}
	}
	now := c.now()
	recs := []*store.Rec{{T: store.TGen, At: now, File: f.ID, Gen: gen, Method: method, Reason: reason, Reset: reset}}
	if off > 0 {
		recs = append(recs, &store.Rec{
			T: store.TCommit, At: now, File: f.ID, Gen: gen, From: 0, To: off, Size: observed,
			Tail: xxhash.Sum64(tail), TailLen: len(tail),
		})
	}
	if f.State == "copy_mismatch" {
		recs = append(recs, &store.Rec{T: store.TState, At: now, File: f.ID, State: "ok"})
	}
	if err := c.commit(recs...); err != nil {
		return err
	}
	c.cfg.Obs.Event(obs.EvGenerationStart, "file", fmt.Sprint(f.ID), "gen", fmt.Sprint(gen), "reason", reason, "method", method)
	c.cfg.Obs.Event(obs.EvCopyMethod, "method", method, "reason", why)
	if off > 0 {
		if err := c.writeTail(f, tail); err != nil {
			return err
		}
	}
	return nil
}

// tryClone clones src to a temporary name, fsyncs it, checks its tail against
// the original, renames it into place and (when syncDir) fsyncs the directory;
// a batching caller passes false and syncs the directory once per batch. It returns
// off < 0 (and no error) when byte mode must be used: ENOTSUP or EXDEV (logged
// at info level), or a clone whose tail does not match the original. Any other
// errno is a fault.
func (c *Copier) tryClone(src *os.File, dst string, syncDir bool) (off int64, tail []byte, err error) {
	srcFi, err := src.Stat()
	if err != nil {
		return 0, nil, err
	}
	if srcFi.Size() == 0 {
		return -1, nil, nil
	}
	tmp := dst + ".tmp"
	if err := c.cfg.Clone.Clone(src.Name(), tmp); err != nil {
		if name, ok := byteModeErr(err); ok {
			c.cfg.Obs.Event(obs.EvCopyMethod, "method", "bytes", "reason", name)
			return -1, nil, nil
		}
		return 0, nil, fmt.Errorf("copier: clone fault: %w", err)
	}
	tf, err := c.fs.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		return 0, nil, err
	}
	cfi, err := tf.Stat()
	if err != nil {
		_ = tf.Close()
		return 0, nil, err
	}
	off = cfi.Size()
	n := int64(TailSize)
	if off < n {
		n = off
	}
	tail = make([]byte, n)
	if n > 0 {
		if _, err := tf.ReadAt(tail, off-n); err != nil {
			_ = tf.Close()
			return 0, nil, err
		}
	}
	if err := tf.Sync(); err != nil {
		_ = tf.Close()
		return 0, nil, err
	}
	_ = tf.Close()
	orig := make([]byte, n)
	m, rerr := src.ReadAt(orig, off-n)
	if (rerr != nil && !errors.Is(rerr, io.EOF)) || int64(m) != n || string(orig) != string(tail) {
		_ = c.fs.Remove(tmp)
		c.cfg.Obs.Event(obs.EvCopyMethod, "method", "bytes", "reason", "clone tail mismatch")
		return -1, nil, nil
	}
	if err := c.fs.Rename(tmp, dst); err != nil {
		return 0, nil, err
	}
	if syncDir {
		if err := c.fs.SyncDir(filepath.Dir(dst)); err != nil {
			return 0, nil, err
		}
	}
	if ps, err := c.cfg.Clone.PrivateSize(dst); err == nil {
		c.recordPrivateClone(filepath.Base(dst), ps)
	}
	return off, tail, nil
}

// recordPrivateClone exports the bounded private-clone series (largest and
// total private size, count of private clones created) and logs the per-file
// size as a clone_private event, so no metric carries a per-file label.
func (c *Copier) recordPrivateClone(file string, size int64) {
	c.privMu.Lock()
	if size > c.privMax {
		c.privMax = size
	}
	c.privTotal += size
	mx, total := c.privMax, c.privTotal
	c.privMu.Unlock()
	c.cfg.Obs.Reg.Set(obs.MClonePrivateMax, nil, float64(mx))
	c.cfg.Obs.Reg.Set(obs.MClonePrivateTotal, nil, float64(total))
	c.cfg.Obs.Reg.Add(obs.MClonePrivateFiles, nil, 1)
	c.cfg.Obs.Event(obs.EvClonePrivate, "file", file, "bytes", fmt.Sprint(size))
}

// recordLag exports the copy-lag gauges (bytes behind after the copy, and how
// long the copied bytes waited since the file's modification time) and the
// histogram of the wait.
func (c *Copier) recordLag(src *os.File, file int64, copied bool) {
	fst, err := src.Stat()
	if err != nil {
		return
	}
	f, err := c.st.File(file)
	if err != nil || f.CurGen == 0 {
		return
	}
	g, err := c.st.Generation(file, f.CurGen)
	if err != nil {
		return
	}
	lag := fst.Size() - g.CopyOffset
	if lag < 0 {
		lag = 0
	}
	var secs float64
	if copied {
		secs = c.cfg.Clock.Now().Sub(fst.ModTime()).Seconds()
		if secs < 0 {
			secs = 0
		}
		c.cfg.Obs.Reg.Observe(obs.MCopyLagHistogram, secs)
	}
	c.cfg.Obs.Reg.Set(obs.MCopyLagBytes, nil, float64(lag))
	c.cfg.Obs.Reg.Set(obs.MCopyLagSeconds, nil, secs)
}

// ---- startup recovery ----

// Recover runs on start. A copy longer than its committed offset is trimmed to
// it and the bytes are copied again from the original; a copy shorter than its
// committed offset marks the file copy_mismatch and re-copies from the original
// while it exists, or starts a new generation. Temporary files left by a crash
// are removed.
func (c *Copier) Recover() error {
	c.mu.Lock()
	var retry []string
	err := c.recoverLocked(&retry)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	for _, p := range retry {
		if _, err := c.Ingest(p); err != nil {
			return err
		}
	}
	return nil
}

func (c *Copier) recoverLocked(retry *[]string) error {
	dir := filepath.Join(c.st.Dir(), store.CopiesDir)
	ents, err := c.fs.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			if err := c.fs.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	files, err := c.st.AllFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Mode != string(scope.Append) || f.CurGen == 0 || f.State == "deleted" {
			continue
		}
		g, err := c.st.Generation(f.ID, f.CurGen)
		if err != nil {
			return err
		}
		cp := c.st.CopyPath(f.ID, g.Gen)
		clen, exists, err := c.copyPathLen(cp)
		if err != nil {
			return err
		}
		orig := c.existingPath(f)
		settled := !exists && g.CopyOffset > 0 && c.st.ArchivedTo(f.ID, g.Gen) >= g.CopyOffset
		switch {
		case settled:
		case clen > g.CopyOffset:
			if err := c.trimCopy(cp, g.CopyOffset); err != nil {
				return err
			}
			c.cfg.Obs.Reg.Add(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonTrimmed}, float64(clen-g.CopyOffset))
			c.cfg.Obs.Event(obs.EvCopyTrimmed, "file", fmt.Sprint(f.ID), "gen", fmt.Sprint(g.Gen), "trimmed", fmt.Sprint(clen-g.CopyOffset))
			if orig != "" {
				*retry = append(*retry, orig)
			}
		case clen < g.CopyOffset || (!exists && g.CopyOffset > 0):
			if orig != "" {
				// Ingest logs the mismatch and re-copies from the original.
				*retry = append(*retry, orig)
				continue
			}
			c.cfg.Obs.Event(obs.EvCopyMismatch, "file", fmt.Sprint(f.ID), "gen", fmt.Sprint(g.Gen))
			if err := c.commit(&store.Rec{T: store.TState, At: c.now(), File: f.ID, State: "copy_mismatch"}); err != nil {
				return err
			}
			if _, err := c.st.StartGeneration(f.ID, "copy_mismatch", "bytes", c.now()); err != nil {
				return err
			}
			c.noteFiles(f.ID)
		}
	}
	return nil
}

// existingPath returns a current path of f that still names the file, or "".
func (c *Copier) existingPath(f store.File) string {
	ps, _ := c.st.CurrentPaths(f.ID)
	for _, p := range ps {
		if fi, err := c.lstat(p); err == nil && sameInode(fi, f) {
			return p
		}
	}
	return ""
}
