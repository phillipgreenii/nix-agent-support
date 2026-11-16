// Package repo resolves the repository a path belongs to, the same way from
// the main checkout, a linked worktree or any subdirectory of either.
package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
)

// ErrNotARepo reports a path inside no git repository.
var ErrNotARepo = errors.New("not a git repository")

// ErrCwdGone reports a path that no longer exists.
var ErrCwdGone = errors.New("directory no longer exists; pass --repo <main checkout>, or run: pg-gwm preserved list --repo <main checkout>")

const resolveTimeout = 2 * time.Minute

// Repo identifies one repository.
type Repo struct {
	// Main is the real path of the first entry of git worktree list: the main
	// checkout, except that for a repository made with --separate-git-dir git
	// lists the git directory itself (it keeps no record of the checkout).
	// CommonDir is the real path of the common git directory.
	Main, CommonDir string
	// Name is the base name of Main; Label is Name, a hyphen and the first six
	// hex characters of the SHA-256 of CommonDir.
	Name, Label string
}

// Resolve finds the repository containing path.
func Resolve(ctx context.Context, g gitrun.Git, path string) (Repo, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Repo{}, fmt.Errorf("%s: %w", path, ErrCwdGone)
		}
		return Repo{}, err
	}
	out, err := g.Exec(ctx, path, resolveTimeout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		var ge *gitrun.GitError
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "not a git repository") {
			return Repo{}, fmt.Errorf("%s: %w", path, ErrNotARepo)
		}
		return Repo{}, err
	}
	common, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil {
		return Repo{}, err
	}
	list, err := g.Exec(ctx, path, resolveTimeout, "worktree", "list", "--porcelain")
	if err != nil {
		return Repo{}, err
	}
	var main string
	for _, line := range strings.Split(list, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			main = p
			break
		}
	}
	if main == "" {
		return Repo{}, fmt.Errorf("%s: %w", path, ErrNotARepo)
	}
	if main, err = filepath.EvalSymlinks(main); err != nil {
		return Repo{}, err
	}
	sum := sha256.Sum256([]byte(common))
	name := filepath.Base(main)
	return Repo{
		Main:      main,
		CommonDir: common,
		Name:      name,
		Label:     name + "-" + hex.EncodeToString(sum[:])[:6],
	}, nil
}

// PathLabel is the label of a repository that cannot be opened: the base name
// of path, a hyphen and the first six hex characters of the SHA-256 of path
// itself (its common git directory cannot be computed).
func PathLabel(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Base(path) + "-" + hex.EncodeToString(sum[:])[:6]
}

// FromMain describes the repository whose main checkout is at main without
// running git: it reads the checkout's .git entry (a directory, or a
// "gitdir:" file that may point into a linked worktree's directory) and, for
// a path that is itself a git directory, uses that. It fails with ErrNotARepo
// when main is no repository.
func FromMain(main string) (Repo, error) {
	real, err := filepath.EvalSymlinks(main)
	if err != nil {
		return Repo{}, fmt.Errorf("%s: %w", main, err)
	}
	gitDir := filepath.Join(real, ".git")
	fi, err := os.Stat(gitDir)
	switch {
	case err == nil && fi.IsDir():
	case err == nil:
		b, rerr := os.ReadFile(gitDir)
		target, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if rerr != nil || !ok {
			return Repo{}, fmt.Errorf("%s: %w", main, ErrNotARepo)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(real, target)
		}
		gitDir = target
	default:
		// A repository made with --separate-git-dir is listed by its git
		// directory itself.
		if _, herr := os.Stat(filepath.Join(real, "HEAD")); herr != nil {
			return Repo{}, fmt.Errorf("%s: %w", main, ErrNotARepo)
		}
		if _, oerr := os.Stat(filepath.Join(real, "objects")); oerr != nil {
			return Repo{}, fmt.Errorf("%s: %w", main, ErrNotARepo)
		}
		gitDir = real
	}
	if b, cerr := os.ReadFile(filepath.Join(gitDir, "commondir")); cerr == nil {
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		gitDir = c
	}
	common, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return Repo{}, fmt.Errorf("%s: %w", main, ErrNotARepo)
	}
	sum := sha256.Sum256([]byte(common))
	name := filepath.Base(real)
	return Repo{Main: real, CommonDir: common, Name: name, Label: name + "-" + hex.EncodeToString(sum[:])[:6]}, nil
}
