// Package registry maintains the permanent pool registry: a directory of symlinks,
// one per pool, each named by the pool's socket hash and pointing at the pool's
// canonical directory. It is the durable, machine-wide pool inventory that
// `ccpool reap-all` sweeps. The package owns lazy creation of the registry dir, so
// the reap timer and tests never depend on the nix module provisioning it.
package registry

import (
	"os"
	"path/filepath"
	"strings"
)

// Entry is one registered pool: the symlink and the (possibly stale) target it
// points at. Target is returned verbatim from Readlink — callers validate it.
type Entry struct {
	Name   string // symlink basename (the pool's socket hash)
	Link   string // absolute path to the symlink
	Target string // what the symlink points to (the canonical pool dir)
}

// tmpMarker tags the temp symlink an interrupted Ensure may leave behind. A real
// entry name is SocketFor's "cc-"+hex, which never contains a dot, so List can
// safely skip anything carrying this marker.
const tmpMarker = ".tmp-"

// Dir returns the registry directory, creating it (0700) if missing. Resolution:
// CCPOOL_REGISTRY_DIR, else $XDG_STATE_HOME/ccpool/pools.d (with the usual
// ~/.local/state fallback when XDG_STATE_HOME is unset). Go owns this creation so
// the timer/tests never rely on the nix module provisioning the dir.
func Dir() (string, error) {
	dir := os.Getenv("CCPOOL_REGISTRY_DIR")
	if dir == "" {
		state := os.Getenv("XDG_STATE_HOME")
		if state == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			state = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(state, "ccpool", "pools.d")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Ensure idempotently and atomically registers name → target. A link already
// pointing at target is left untouched; one pointing elsewhere is repaired. The link
// is created via symlink-to-temp-then-rename so a concurrent reader never observes a
// half-made link.
func Ensure(name, target string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	if cur, err := os.Readlink(final); err == nil && cur == target {
		return nil // already correct — nothing to do
	}
	tmp, err := os.CreateTemp(dir, name+tmpMarker+"*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	if err := os.Remove(tmpPath); err != nil { // reserve the unique name, then symlink it
		return err
	}
	if err := os.Symlink(target, tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, final); err != nil { // atomic replace
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// Remove deletes the registry symlink named name, tolerating an already-absent link
// (ENOENT) as success — GC may race with another sweep or a manual unlink.
func Remove(name string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns every registered pool. Temp files left by an interrupted Ensure are
// skipped, as is any entry that is not a symlink. A missing registry dir yields an
// empty list, not an error (Dir creates it lazily).
func List() ([]Entry, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		name := de.Name()
		if strings.Contains(name, tmpMarker) {
			continue
		}
		link := filepath.Join(dir, name)
		target, err := os.Readlink(link)
		if err != nil {
			continue // not a symlink (or vanished) — skip
		}
		out = append(out, Entry{Name: name, Link: link, Target: target})
	}
	return out, nil
}
