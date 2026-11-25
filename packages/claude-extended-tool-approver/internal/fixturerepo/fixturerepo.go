// Package fixturerepo is the registry through which a golden-corpus loader
// tells pathspec about a throwaway repository. It is a leaf package so that
// both pathspec and goldencorpus can import it.
package fixturerepo

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// Repo is the git path state of ONE throwaway directory a golden-corpus
// loader (internal/goldencorpus) materialised for a row: which paths git
// tracks, the worktree's cleanliness, and whether the directory is to be
// classified as OUTSIDE every temp root (so the non-temp-repo policies apply
// to a directory that necessarily lives under one). It is the corpus loader's
// production-legal seam: unlike SetTempClassifierForTest it is keyed by the
// directory, so concurrent rows never see each other's state and a restore is
// the returned func, called when the row's tree is removed.
//
// Only the REAL probes (realWorktreeState, realGitTracked) and the default temp
// classifier consult the registry, so a test that installs its own fake via
// SetWorktreeStateProbe/SetGitTrackedProbe/SetTempClassifierForTest still wins,
// and a path that is not under a registered Root behaves exactly as before.
type Repo struct {
	// Root is the throwaway directory (a repo root with a real .git inside).
	Root string
	// Tracked is the set of repo-relative, slash-separated paths git tracks.
	Tracked []string
	// WorktreeState is reported for Root (and any path under it) when
	// HasWorktreeState; otherwise the real probe runs.
	WorktreeState    string // clean, dirty, clean-ignored or unknown
	HasWorktreeState bool
	// NonTemp makes the temp classifier answer false for Root and below.
	NonTemp bool
}

var (
	fixtureMu    sync.RWMutex
	fixtureRepos = map[string]Repo{}
)

// Register registers f under its Root (and the symlink-resolved
// Root) and returns the unregister func.
func Register(f Repo) (unregister func()) {
	keys := []string{filepath.Clean(f.Root)}
	if r, err := filepath.EvalSymlinks(f.Root); err == nil && filepath.Clean(r) != keys[0] {
		keys = append(keys, filepath.Clean(r))
	}
	fixtureMu.Lock()
	for _, k := range keys {
		fixtureRepos[k] = f
	}
	fixtureMu.Unlock()
	return func() {
		fixtureMu.Lock()
		for _, k := range keys {
			delete(fixtureRepos, k)
		}
		fixtureMu.Unlock()
	}
}

// For returns the registered Repo whose Root is p or an ancestor
// of p, and the key it was registered under.
func For(p string) (Repo, string, bool) {
	fixtureMu.RLock()
	defer fixtureMu.RUnlock()
	if len(fixtureRepos) == 0 {
		return Repo{}, "", false
	}
	for q := filepath.Clean(p); ; q = filepath.Dir(q) {
		if f, ok := fixtureRepos[q]; ok {
			return f, q, true
		}
		if q == filepath.Dir(q) {
			return Repo{}, "", false
		}
	}
}

// Tracked answers realGitTracked for a registered root.
func Tracked(root, rel string, isDir bool) (tracked, ok bool) {
	f, key, found := For(root)
	if !found || filepath.Clean(root) != key {
		return false, false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, t := range f.Tracked {
		t = filepath.ToSlash(filepath.Clean(t))
		if t == rel || (isDir && strings.HasPrefix(t, rel+"/")) {
			return true, true
		}
	}
	return false, true
}

// AwareTemp is the default temp classifier: a registered NonTemp
// fixture is never temp; everything else is temproot.Under.
func AwareTemp(p string) bool {
	if f, _, ok := For(p); ok && f.NonTemp {
		return false
	}
	return temproot.Under(p)
}
