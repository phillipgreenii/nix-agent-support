package state

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// UserDir is the user-level state directory, $XDG_STATE_HOME/pg-gwm (default
// ~/.local/state/pg-gwm).
func UserDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pg-gwm")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-gwm")
}

// RegistryPath, CountersPath and ReaperPath are the default file locations.
func RegistryPath() string { return filepath.Join(UserDir(), "registry.json") }
func CountersPath() string { return filepath.Join(UserDir(), "counters.json") }
func ReaperPath() string   { return filepath.Join(UserDir(), "reaper.json") }

// UnmanagedPath is the count file the weekly unmanaged-worktree audit (wtaudit
// unmanaged --metrics-file) writes: {"count": N, "checked_at": RFC 3339}.
func UnmanagedPath() string { return filepath.Join(UserDir(), "unmanaged-worktrees.json") }

const userLockWait = 5 * time.Second

// RegistryEntry is one known repository.
type RegistryEntry struct {
	// Main is the real path of the repository's main checkout.
	Main string `json:"main"`
	// AddedAt is when the repository was first registered.
	AddedAt time.Time `json:"added_at"`
	// MissingSince is when the repository was first found missing; nil while
	// it is present.
	MissingSince *time.Time `json:"missing_since"`
}

type registryFile struct {
	SchemaVersion int             `json:"schema_version"`
	Repos         []RegistryEntry `json:"repos"`
}

// Registry is the user-level list of repositories ($XDG_STATE_HOME/pg-gwm/registry.json).
type Registry struct{ Path string }

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func (r Registry) read() (registryFile, error) {
	b, err := os.ReadFile(r.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return registryFile{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return registryFile{}, err
	}
	var f registryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return registryFile{}, err
	}
	return f, nil
}

// update runs fn on the registry under its own flock and writes the result.
func (r Registry) update(fn func(*registryFile)) error {
	unlock, err := flockFile(context.Background(), r.Path+".lock", userLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := r.read()
	if err != nil {
		return err
	}
	fn(&f)
	f.SchemaVersion = SchemaVersion
	sort.Slice(f.Repos, func(i, j int) bool { return f.Repos[i].Main < f.Repos[j].Main })
	b, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(r.Path, append(b, '\n'), nil)
}

// Add registers the main checkout (deduped by real path) and clears any
// missing mark on it. Its added time comes from the wall clock.
func (r Registry) Add(main string) error {
	p := realPath(main)
	return r.update(func(f *registryFile) {
		for i := range f.Repos {
			if f.Repos[i].Main == p {
				f.Repos[i].MissingSince = nil
				return
			}
		}
		f.Repos = append(f.Repos, RegistryEntry{Main: p, AddedAt: time.Now().UTC()})
	})
}

// List returns the entries sorted by path; a missing file lists nothing.
func (r Registry) List() ([]RegistryEntry, error) {
	f, err := r.read()
	return f.Repos, err
}

// MarkMissing records that main was found missing at now; the first mark
// stays. An unknown path is ignored.
func (r Registry) MarkMissing(main string, now time.Time) error {
	p := realPath(main)
	return r.update(func(f *registryFile) {
		for i := range f.Repos {
			if (f.Repos[i].Main == p || f.Repos[i].Main == filepath.Clean(main)) && f.Repos[i].MissingSince == nil {
				t := now.UTC()
				f.Repos[i].MissingSince = &t
			}
		}
	})
}

// Prune drops entries missing since at least retention before now
// (MissingSince + retention <= now). A caller whose retention is "none" does
// not call it.
func (r Registry) Prune(retention time.Duration, now time.Time) error {
	return r.update(func(f *registryFile) {
		kept := f.Repos[:0]
		for _, e := range f.Repos {
			if e.MissingSince != nil && !e.MissingSince.Add(retention).After(now) {
				continue
			}
			kept = append(kept, e)
		}
		f.Repos = kept
	})
}
