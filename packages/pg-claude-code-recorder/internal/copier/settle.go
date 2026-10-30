package copier

import (
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// WithLock runs fn while no Ingest runs (the settle stage holds a Conversation's
// bytes still while it checks, archives and deletes). fn MUST NOT call Ingest.
func (c *Copier) WithLock(fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fn()
}

// CloneMode reports whether the probe chose clone mode.
func (c *Copier) CloneMode() bool { return c.cloneOK }

// ClonePolicy returns the clone seam (the settle stage re-clones through it).
func (c *Copier) ClonePolicy() ClonePolicy { return c.cfg.Clone }

// NewGenerationLocked starts a new generation of f from byte 0 from the file at
// path, keeping the old one (a reset entry follows). The caller holds WithLock.
func (c *Copier) NewGenerationLocked(f store.File, path, reason string) error {
	src, err := openNoFollow(path)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	return c.startGeneration(f, src, reason, true)
}

// ReclaimLocked deletes the copy bytes an archive now covers: it removes the
// copy file of a generation whose committed bytes are all archived, and the
// stored bytes of an archived snapshot version. It is idempotent, so a restart
// after "settled" finishes a delete the crash cut short.
func (c *Copier) ReclaimLocked(f store.File, gen int) error {
	arch := c.st.ArchivedTo(f.ID, gen)
	if arch == 0 {
		return nil
	}
	var p string
	switch f.Mode {
	case "snapshot":
		p = c.st.SnapshotPath(f.ID, gen)
	default:
		g, err := c.st.Generation(f.ID, gen)
		if err != nil {
			return err
		}
		if g.CopyOffset > arch {
			return nil // an uncompressed tail follows the archived part
		}
		p = c.st.CopyPath(f.ID, gen)
	}
	if err := c.fs.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return c.fs.SyncDir(filepath.Dir(p))
}

// ReplaceCopyLocked atomically replaces the copy of a generation with the file
// at tmp (a verified clone): rename, then directory flush.
func (c *Copier) ReplaceCopyLocked(f store.File, gen int, tmp string) error {
	dst := c.st.CopyPath(f.ID, gen)
	if err := c.fs.Rename(tmp, dst); err != nil {
		return err
	}
	return c.fs.SyncDir(filepath.Dir(dst))
}

// ReadOriginalPath returns a current path of f that still names the file in the
// Claude directory ("" when none does).
func (c *Copier) ReadOriginalPath(f store.File) string { return c.existingPath(f) }
