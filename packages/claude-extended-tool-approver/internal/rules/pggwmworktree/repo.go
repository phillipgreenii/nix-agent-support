package pggwmworktree

import (
	"os"
	"path/filepath"
	"strings"
)

// mainCheckout returns the real path of the main checkout that encloses dir,
// reading only the filesystem (no git subprocess): walk up to the first `.git`;
// a directory is the main checkout itself, a `gitdir:` file belongs to a linked
// worktree whose commondir names the main `.git`. dir need not exist (the walk
// starts at its nearest existing ancestor). ok is false outside any repository.
func mainCheckout(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		gi := filepath.Join(d, ".git")
		if st, err := os.Lstat(gi); err == nil {
			if st.IsDir() {
				return realPath(d), true
			}
			return mainFromLinked(d, gi)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

func mainFromLinked(worktree, gitFile string) (string, bool) {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(b))
	gd, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", false
	}
	gd = strings.TrimSpace(gd)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(worktree, gd)
	}
	common := gd
	if cb, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		c := strings.TrimSpace(string(cb))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gd, c)
		}
		common = filepath.Clean(c)
	}
	if filepath.Base(common) != ".git" {
		return "", false
	}
	return realPath(filepath.Dir(common)), true
}
