package copier

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cespare/xxhash/v2"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// staged is the first copy of a new append file whose clone is in place (cloned
// to a temporary name, fsynced, tail-checked and renamed) but whose journal
// records are not yet committed. Until flushStaged commits them nothing refers
// to the clone: a crash leaves an unreferenced file that the next first copy of
// that file id removes (startGeneration-style) and Recover never trusts.
type staged struct {
	path string
	fid  int64
	id   Identity
	src  *os.File
	dst  string
	off  int64
	tail []byte
	recs []*store.Rec
}

// errStageConflict is returned by stageFirstCopy when the path or identity is
// already staged in the caller's batch: the caller flushes and retries.
var errStageConflict = errors.New("copier: path or identity already staged")

// stageFirstCopy prepares the first copy of a NEW append file (no row holds the
// path or the identity yet, clone mode, non-empty file). It returns nil, nil
// when the file is not eligible: the caller takes the ordinary path. conflict,
// when non-nil, reports a path or identity already staged in the same batch.
// The caller holds c.mu. The returned staged owns an open src until flushStaged.
func (c *Copier) stageFirstCopy(path string, conflict func(path string, id Identity) bool) (*staged, error) {
	if !c.cloneOK {
		return nil, nil
	}
	if holders, err := c.st.FilesByCurrentPath(path); err != nil || len(holders) > 0 {
		return nil, nil
	}
	src, err := openNoFollow(path)
	if err != nil {
		return nil, nil
	}
	keep := false
	defer func() {
		if !keep {
			_ = src.Close()
		}
	}()
	fst, err := src.Stat()
	if err != nil || fst.Size() == 0 {
		return nil, nil
	}
	id, ok := identityOf(path, fst)
	if !ok {
		return nil, nil
	}
	if fi, err := os.Lstat(path); err != nil {
		return nil, nil
	} else if lid, ok := identityOf(path, fi); ok && lid != id {
		return nil, nil
	}
	if cands, err := c.st.FilesByIdentity(id.Dev, id.Ino); err != nil || len(cands) > 0 {
		return nil, nil
	}
	if conflict != nil && conflict(path, id) {
		return nil, errStageConflict
	}
	source, mode := c.cfg.Scope.Classify(path)
	if mode != scope.Append {
		return nil, nil
	}
	fid := c.st.NewFileID()
	dst := c.st.CopyPath(fid, 1)
	for _, p := range []string{dst, dst + ".tmp"} {
		if err := c.fs.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	off, tail, err := c.tryClone(src, dst, false)
	if err != nil {
		return nil, err
	}
	if off < 0 {
		return nil, nil // byte mode: the ordinary path copies it
	}
	fh := firstHash(src, fst.Size())
	observed := fst.Size()
	if off > observed {
		observed = off
	}
	now := c.now()
	recs := []*store.Rec{
		{T: store.TFile, At: now, File: fid, Source: source, Mode: string(scope.Append), Dev: id.Dev, Ino: id.Ino, Birth: id.Birth, FirstHash: fh},
		{T: store.TPath, At: now, File: fid, Path: path},
		{T: store.TGen, At: now, File: fid, Gen: 1, Method: "clone", Reason: "first"},
		{T: store.TCommit, At: now, File: fid, Gen: 1, From: 0, To: off, Size: observed, Tail: xxhash.Sum64(tail), TailLen: len(tail)},
	}
	keep = true
	return &staged{path: path, fid: fid, id: id, src: src, dst: dst, off: off, tail: tail, recs: recs}, nil
}

// flushStaged makes a batch durable: one directory sync covers every rename,
// then one journal commit (one fsync, one SQLite transaction) records every
// file. Each clone was fsynced before its rename and before this commit, so the
// order is the single-file order: fsync, rename, directory sync, commit. The
// tail sidecars are written after the commit, as before. It closes every src.
func (c *Copier) flushStaged(batch []*staged) error {
	defer func() {
		for _, s := range batch {
			_ = s.src.Close()
		}
	}()
	if len(batch) == 0 {
		return nil
	}
	faults.At("copy.batch-staged")
	if err := c.fs.SyncDir(filepath.Dir(batch[0].dst)); err != nil {
		return err
	}
	var recs []*store.Rec
	for _, s := range batch {
		recs = append(recs, s.recs...)
	}
	if err := c.commit(recs...); err != nil {
		return err
	}
	for _, s := range batch {
		c.cfg.Obs.Event(obs.EvGenerationStart, "file", fmt.Sprint(s.fid), "gen", "1", "reason", "first", "method", "clone")
		c.cfg.Obs.Event(obs.EvCopyMethod, "method", "clone", "reason", "clone ok")
		f := store.File{ID: s.fid}
		if err := c.writeTail(f, s.tail); err != nil {
			return err
		}
		c.recordLag(s.src, s.fid, false)
	}
	return nil
}

// BatchResult is the outcome of one path of an IngestBatch call.
type BatchResult struct {
	Path   string
	Result Result
	Err    error
}

// IngestBatch ingests paths in order like repeated Ingest calls, but the first
// copy of new append files is group-committed: up to len(paths) clones share
// one directory sync and one journal commit. A path that is not a plain new
// append file (a known file, a snapshot, a link, a missing path, byte mode)
// flushes what is staged and takes the ordinary Ingest path, so ordering
// between paths is preserved. It holds the copier for the whole batch.
func (c *Copier) IngestBatch(paths []string) []BatchResult {
	c.lock()
	defer c.mu.Unlock()
	out := make([]BatchResult, len(paths))
	var pend []*staged
	var idx []int
	pendPaths := map[string]bool{}
	pendIDs := map[Identity]bool{}
	conflict := func(p string, id Identity) bool { return pendPaths[p] || pendIDs[id] }
	flush := func() {
		if len(pend) == 0 {
			return
		}
		err := c.flushStaged(pend)
		for k, s := range pend {
			out[idx[k]].Err = err
			if err == nil {
				out[idx[k]].Result = Result{Action: "new-generation", File: s.fid}
			}
		}
		pend, idx = nil, nil
		pendPaths = map[string]bool{}
		pendIDs = map[Identity]bool{}
	}
	for i, raw := range paths {
		p := filepath.Clean(raw)
		out[i].Path = raw
		oc := c.stageForBatch(p, conflict)
		if oc == errBatchConflict {
			flush()
			oc = c.stageForBatch(p, nil)
		}
		if oc.err != nil {
			flush()
			out[i].Err = oc.err
			continue
		}
		if oc.s != nil {
			pend = append(pend, oc.s)
			idx = append(idx, i)
			pendPaths[p] = true
			pendIDs[oc.s.id] = true
			continue
		}
		flush()
		out[i].Result, out[i].Err = c.ingestLocked(p)
	}
	flush()
	return out
}

type stageOutcome struct {
	s   *staged
	err error
}

var errBatchConflict = stageOutcome{err: errStageConflict}

// stageForBatch applies the cheap preconditions of ingestLocked (scope, regular
// file, append mode) and then tries stageFirstCopy.
func (c *Copier) stageForBatch(p string, conflict func(string, Identity) bool) stageOutcome {
	if d := c.cfg.Scope.Match(p); !d.Ingest {
		return stageOutcome{}
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return stageOutcome{}
	}
	if _, mode := c.cfg.Scope.Classify(p); mode != scope.Append {
		return stageOutcome{}
	}
	s, err := c.stageFirstCopy(p, conflict)
	if errors.Is(err, errStageConflict) {
		return errBatchConflict
	}
	return stageOutcome{s: s, err: err}
}
