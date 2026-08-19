package primarycommit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// FileResolver answers branch/tree questions by reading git's on-disk files directly —
// NO git subprocess (avoids index/fsmonitor locks in the hook path).
type FileResolver struct{}

func NewFileResolver() *FileResolver { return &FileResolver{} }

// ErrDirNotExist is the ONE sentinel a PrimaryResolver.IsCanonical implementation MAY
// return to mean "I checked, and the directory does not exist" — distinct from every
// other resolver error, which primarycommit.go's inspectCommit still treats fail-OPEN
// (findingNone, unchanged since before pg2-5adzj). FileResolver is the implementation
// that returns it, because gitRoot below walks UP to the nearest ENCLOSING ".git"
// regardless of whether the STARTING directory exists — so, unguarded, a resolved
// literal path that is merely a typo, not-yet-created, or already cleaned up after
// landing (production asks.db row 326758: a worktree removed after its bead merged)
// walks past the missing directory and lands on whichever repository encloses it,
// typically the canonical clone, and confidently misreports "yes, primary". See
// IsCanonical below and primarycommit.go's pg2-5adzj package-doc paragraph for why the
// check lives HERE rather than in the resolver-agnostic rule logic.
var ErrDirNotExist = errors.New("primary-commit: directory does not exist")

// dirExists reports whether dir is a real, existing directory on disk.
func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// gitRoot walks up from dir to the first ".git" entry. gitIsDir==true ⇒ main working
// tree (canonical); false ⇒ a linked worktree (.git is a gitdir: file). Callers that
// need to distinguish "dir does not exist" from "dir exists but is not (in) a git
// repo" MUST check dirExists(dir) themselves BEFORE calling this — gitRoot walks up
// regardless, by design, so that an EXISTING nested subdirectory with no ".git" of its
// own still resolves to its enclosing repo (TestFileResolver_WalkUpAndDetached).
func gitRoot(dir string) (root string, gitIsDir bool, found bool) {
	d := dir
	for {
		if info, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, info.IsDir(), true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false, false
		}
		d = parent
	}
}

// IsCanonical reports whether dir is the canonical clone's main working tree.
// Returns ErrDirNotExist (see above) when dir itself does not exist, BEFORE gitRoot
// ever walks up from it — checked here, and only here, rather than in gitRoot, because
// every OTHER gitRoot caller (CurrentBranch, PrimaryBranch, PushDefault, Aliases) only
// ever runs after IsCanonical has already confirmed dir is real.
func (r *FileResolver) IsCanonical(dir string) (bool, error) {
	if !dirExists(dir) {
		return false, ErrDirNotExist
	}
	_, gitIsDir, found := gitRoot(dir)
	return found && gitIsDir, nil
}

func (r *FileResolver) CurrentBranch(dir string) (string, error) {
	root, gitIsDir, found := gitRoot(dir)
	if !found || !gitIsDir {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	const pfx = "ref: refs/heads/"
	if strings.HasPrefix(line, pfx) {
		return strings.TrimPrefix(line, pfx), nil // works on an unborn branch too
	}
	return "", nil // raw SHA => detached HEAD
}

func (r *FileResolver) PrimaryBranch(dir string) (string, error) {
	root, gitIsDir, found := gitRoot(dir)
	if found && gitIsDir {
		if v := gitConfigValue(filepath.Join(root, ".git", "config"), "pgii-integrate-branch", "primaryBranch"); v != "" {
			return v, nil
		}
	}
	return "main", nil
}

// PushDefault returns the effective push.default value: the local .git/config value
// wins over the global one, matching git's precedence. "" when unset in both.
func (r *FileResolver) PushDefault(dir string) (string, error) {
	if root, gitIsDir, found := gitRoot(dir); found && gitIsDir {
		if v := gitConfigValue(filepath.Join(root, ".git", "config"), "push", "default"); v != "" {
			return v, nil
		}
	}
	return gitConfigValue(globalConfigPath(), "push", "default"), nil
}

// Aliases returns the merged `[alias]` sections of the global and local configs, with
// local overriding global per-alias, keys lowered (git config keys are case-insensitive).
// nil when no alias is defined in either. Both reads are best-effort: a missing or
// unreadable file contributes nothing rather than erroring, keeping the rule fail-open.
func (r *FileResolver) Aliases(dir string) (map[string]string, error) {
	merged := gitConfigSection(globalConfigPath(), "alias")
	if root, gitIsDir, found := gitRoot(dir); found && gitIsDir {
		for k, v := range gitConfigSection(filepath.Join(root, ".git", "config"), "alias") {
			if merged == nil {
				merged = map[string]string{}
			}
			merged[k] = v // local overrides global
		}
	}
	return merged, nil
}

// globalConfigPath resolves git's user-global config path (best-effort, git's
// precedence): $GIT_CONFIG_GLOBAL → $XDG_CONFIG_HOME/git/config → $HOME/.config/git/config
// (only when it exists) → $HOME/.gitconfig. "" when $HOME is unset and no override applies.
func globalConfigPath() string {
	if p := os.Getenv("GIT_CONFIG_GLOBAL"); p != "" {
		return p
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git", "config")
	}
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	if xdgDefault := filepath.Join(home, ".config", "git", "config"); fileExists(xdgDefault) {
		return xdgDefault
	}
	return filepath.Join(home, ".gitconfig")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// gitConfigSection reads an ENTIRE config section into a map keyed by lowered variable
// name (git config keys are case-insensitive). It shares gitConfigValue's minimal
// section parser (case-insensitive `[section]` header, `key = value` bodies). nil when
// the file is missing/unreadable or the section is absent/empty.
func gitConfigSection(path, section string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]string
	inSection := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), section)
			continue
		}
		if !inSection {
			continue
		}
		if i := strings.Index(line, "="); i >= 0 {
			key := strings.ToLower(strings.TrimSpace(line[:i]))
			if key == "" {
				continue
			}
			if out == nil {
				out = map[string]string{}
			}
			// git quotes a config value only when it carries special chars; strip that
			// quoting so an aliased body splits the same whether or not it was quoted.
			out[key] = shellUnquote(strings.TrimSpace(line[i+1:]))
		}
	}
	return out
}

// gitConfigValue parses a local .git/config for section.key (case-insensitive section/key).
func gitConfigValue(path, section, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	inSection := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), section)
			continue
		}
		if inSection {
			if i := strings.Index(line, "="); i >= 0 && strings.EqualFold(strings.TrimSpace(line[:i]), key) {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}
