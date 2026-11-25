package goldencorpus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/fixturerepo"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// homeMu serialises the one process-global part of a row's path state: HOME.
// A row without a Home takes the read lock (many rows run concurrently), a row
// naming one takes the write lock for the duration of its evaluation.
var homeMu sync.RWMutex

// needsRepo reports whether the row pins git path state that only a real
// directory carrying a .git can express.
func (fx PathStateFixture) needsRepo() bool {
	return fx.GitRepo || len(fx.GitTracked) > 0 || len(fx.Gitignored) > 0 || fx.WorktreeState != ""
}

// Scope brackets ONE row's evaluation: it pins HOME to the row's Home (a
// synthetic path; no host HOME is read) and returns the func that ends the
// bracket. A consumer MUST call it before evaluating a row and the returned
// func after; a row with no Home only takes a shared lock.
func (t MarkerTree) Scope() (done func()) {
	if t.Home == "" {
		homeMu.RLock()
		return homeMu.RUnlock
	}
	homeMu.Lock()
	prev, had := os.LookupEnv("HOME")
	_ = os.Setenv("HOME", t.Home)
	return func() {
		if had {
			_ = os.Setenv("HOME", prev)
		} else {
			_ = os.Unsetenv("HOME")
		}
		homeMu.Unlock()
	}
}

// relClean returns a safe, slash-separated, repo-relative path or an error.
func relClean(p string) (string, error) {
	c := filepath.ToSlash(filepath.Clean(p))
	if p == "" || filepath.IsAbs(p) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("fixture path %q must be a non-empty relative path inside the repo", p)
	}
	return c, nil
}

func parseWorktreeState(s string) (string, error) {
	switch s {
	case "clean", "dirty", "clean-ignored", "unknown":
		return s, nil
	}
	return "", fmt.Errorf("worktree_state %q must be clean, dirty, clean-ignored or unknown", s)
}

// materializeRepo builds the throwaway git repo a row's git facts describe: a
// real .git directory (no git subprocess; the engine reads git state through
// pathspec's probes, which the registered FixtureRepo answers from the row's
// data), the tracked and gitignored files, and a .gitignore naming the latter.
// The temp-versus-non-temp status of the row's project_root decides whether the
// directory is classified as a temp repo.
func (fx PathStateFixture) materializeRepo(none MarkerTree) (MarkerTree, error) {
	state := "unknown"
	hasState := fx.WorktreeState != ""
	if hasState {
		var err error
		if state, err = parseWorktreeState(fx.WorktreeState); err != nil {
			return none, err
		}
	}
	base, err := os.MkdirTemp("/tmp", "ceta-corpus-repo-")
	if err != nil {
		return none, fmt.Errorf("creating the fixture repo: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(base) }
	fail := func(err error) (MarkerTree, error) { cleanup(); return none, err }
	root := filepath.Join(base, "repo")
	for _, d := range []string{filepath.Join(root, ".git", "hooks"), filepath.Join(root, ".git", "refs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail(fmt.Errorf("creating the fixture repo: %w", err))
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		return fail(err)
	}
	touch := func(rel string) error {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	}
	var tracked, ignored []string
	for _, p := range fx.GitTracked {
		c, err := relClean(p)
		if err != nil {
			return fail(err)
		}
		if err := touch(c); err != nil {
			return fail(err)
		}
		tracked = append(tracked, c)
	}
	for _, p := range fx.Gitignored {
		c, err := relClean(p)
		if err != nil {
			return fail(err)
		}
		if err := touch(c); err != nil {
			return fail(err)
		}
		ignored = append(ignored, "/"+c)
	}
	if len(ignored) > 0 {
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(strings.Join(ignored, "\n")+"\n"), 0o644); err != nil {
			return fail(err)
		}
	}
	cwd := root
	if fx.ProjectRoot != "" && fx.CWD != fx.ProjectRoot {
		if rel, err := filepath.Rel(fx.ProjectRoot, fx.CWD); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
			cwd = filepath.Join(root, rel)
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				return fail(err)
			}
		}
	}
	declared := fx.ProjectRoot
	if declared == "" {
		declared = fx.CWD
	}
	unregister := fixturerepo.Register(fixturerepo.Repo{
		Root:             root,
		Tracked:          tracked,
		WorktreeState:    state,
		HasWorktreeState: hasState,
		NonTemp:          declared != "" && !temproot.Under(declared),
	})
	return MarkerTree{
		CWD: cwd, ProjectRoot: root, Home: fx.Home,
		Cleanup: func() { unregister(); cleanup() },
	}, nil
}
