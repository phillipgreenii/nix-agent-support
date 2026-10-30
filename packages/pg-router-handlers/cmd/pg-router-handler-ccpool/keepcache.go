package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Worktree keep cache (bead pg2-fx5x0l.2, INV-CCH-19).
//
// The handler is one subprocess per request, so a worktree the sweep keeps
// because it is dirty or holds unpushed commits would be re-examined (git status,
// a commit count) on every dispatch. The cache records the keep decision in a
// small file beside the per-bead lock, so it survives the subprocess:
//
//	<lock dir>/worktree-keep--<bead>.keep
//
// An entry is honored only while it has not expired (Config.WorktreeKeepRecheck)
// AND the worktree's signature is unchanged: the mtimes of its git admin HEAD
// and index and of the loose branch ref. A worktree that gained a commit or had
// its index rewritten therefore gets re-examined at once. The cheap guards (age,
// row, process working directory, git lock) are never cached.
//
// The files are written atomically (temp file plus rename), so a reader sees a
// whole entry or none; a torn or foreign file parses as absent. The ".keep"
// suffix keeps them out of sessionlock.SweepStale, which only touches ".lock".
// sweepKeepCache (called from sweepStaleLocks) removes expired, torn and
// orphaned entries.

const (
	keepFilePrefix = "worktree-keep--"
	keepFileSuffix = ".keep"
	keepTmpMarker  = ".tmp-"
	// keepTmpMaxAge is how old a leftover temp file must be before GC removes it.
	keepTmpMaxAge = time.Hour
)

// keepEntry is the on-disk record.
type keepEntry struct {
	Path  string `json:"path"`
	Sig   string `json:"sig"`
	Until int64  `json:"until_unix_nano"`
}

// keepFile maps a bead to its cache file under dir; characters outside
// [A-Za-z0-9._-] become "_" so a name can never escape dir.
func keepFile(dir, bead string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, bead)
	return filepath.Join(dir, keepFilePrefix+name+keepFileSuffix)
}

// keepSignature fingerprints the state a keep decision depends on. A missing
// file contributes 0, so the signature is still comparable.
func keepSignature(c leakedWorktree) string {
	mt := func(p string) string {
		fi, err := os.Stat(p)
		if err != nil {
			return "0"
		}
		return time.Duration(fi.ModTime().UnixNano()).String()
	}
	common := filepath.Dir(filepath.Dir(c.admin))
	return strings.Join([]string{
		mt(filepath.Join(c.admin, "HEAD")),
		mt(filepath.Join(c.admin, "index")),
		mt(filepath.Join(common, "refs", "heads", "pg-router", c.bead)),
	}, "|")
}

// readKeep parses a cache file; torn, unreadable or foreign content is absent.
func readKeep(path string) (keepEntry, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return keepEntry{}, false
	}
	var e keepEntry
	if err := json.Unmarshal(raw, &e); err != nil || e.Path == "" || e.Sig == "" {
		return keepEntry{}, false
	}
	return e, true
}

// keepCached reports whether c has a live, unchanged keep entry at now.
func keepCached(lockDir string, c leakedWorktree, sig string, now time.Time) bool {
	if lockDir == "" {
		return false
	}
	e, ok := readKeep(keepFile(lockDir, c.bead))
	return ok && e.Path == c.path && e.Sig == sig && now.UnixNano() < e.Until
}

// keepRemember records that c was kept at now, for ttl. A non-positive ttl
// disables the cache. Best effort: a failure only means the next pass
// re-examines.
func keepRemember(lockDir string, c leakedWorktree, sig string, now time.Time, ttl time.Duration) {
	if lockDir == "" || ttl <= 0 {
		return
	}
	raw, err := json.Marshal(keepEntry{Path: c.path, Sig: sig, Until: now.Add(ttl).UnixNano()})
	if err != nil {
		return
	}
	dst := keepFile(lockDir, c.bead)
	tmp, err := os.CreateTemp(lockDir, filepath.Base(dst)+keepTmpMarker+"*")
	if err != nil {
		slog.Warn("worktree sweep: cannot write the keep cache", "bead", c.bead, "err", err)
		return
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), dst) != nil {
		_ = os.Remove(tmp.Name())
		slog.Warn("worktree sweep: cannot write the keep cache", "bead", c.bead, "write_err", werr, "close_err", cerr)
	}
}

// sweepKeepCache removes cache entries that can no longer matter: expired,
// unparseable, or naming a worktree that is gone, plus abandoned temp files. It
// returns how many it removed.
func sweepKeepCache(lockDir string, now time.Time) int {
	des, err := os.ReadDir(lockDir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, de := range des {
		name := de.Name()
		if !strings.HasPrefix(name, keepFilePrefix) {
			continue
		}
		p := filepath.Join(lockDir, name)
		switch {
		case strings.Contains(name, keepTmpMarker):
			if fi, err := de.Info(); err == nil && now.Sub(fi.ModTime()) > keepTmpMaxAge && os.Remove(p) == nil {
				removed++
			}
		case strings.HasSuffix(name, keepFileSuffix):
			e, ok := readKeep(p)
			if ok && now.UnixNano() < e.Until {
				if _, err := os.Lstat(e.Path); err == nil {
					continue
				}
			}
			if os.Remove(p) == nil {
				removed++
			}
		}
	}
	return removed
}
