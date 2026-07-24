package primarycommit

import (
	"os"
	"path/filepath"
	"strings"
)

// FileResolver answers branch/tree questions by reading git's on-disk files directly —
// NO git subprocess (avoids index/fsmonitor locks in the hook path).
type FileResolver struct{}

func NewFileResolver() *FileResolver { return &FileResolver{} }

// gitRoot walks up from dir to the first ".git" entry. gitIsDir==true ⇒ main working
// tree (canonical); false ⇒ a linked worktree (.git is a gitdir: file).
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

func (r *FileResolver) IsCanonical(dir string) (bool, error) {
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
