// Package gitlocal provides pure-git helpers used by the `pg-pr pr files`
// and `pg-pr pr commits` subcommands. Everything here operates on the local
// repository — no GitHub API calls.
package gitlocal

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/x/gitclient"
)

// FileChange is one entry from `git diff --numstat`.
type FileChange struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

// Commit is one entry from `git log`.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Author  string `json:"author"`
}

// opener anchors an x/gitclient at dir, sized to the one role this package
// needs (design §4.5/§4.6: "pg-pr gitlocal -> HistoryReader", app-local
// opener seam). A package-level var, not a plain function, so tests can
// substitute a fake HistoryReader without threading a new testing seam
// through ChangedFiles/Commits themselves — mirrors pg-pr's
// internal/worktree.opener and pr-pool's internal/watchdog.gitOpener.
type opener func(ctx context.Context, dir string) (gitclient.HistoryReader, error)

// openGit is the production opener. gitclient.Client builds its child
// process's environment from its own allowlist (PATH/HOME/SSH_AUTH_SOCK
// plus explicit extras) rather than inheriting os.Environ(), so it no
// longer needs this package's own gitenv helper (pg2-lx41y's fix) to stay
// inside dir under a leaked ambient GIT_DIR/GIT_WORK_TREE (pg2-kcucl).
var openGit opener = func(ctx context.Context, dir string) (gitclient.HistoryReader, error) {
	return gitclient.New(ctx, dir)
}

// ChangedFiles returns the file changes between base and HEAD via
// x/gitclient's HistoryReader role (`git diff --numstat base...HEAD`,
// merge-base semantics). base defaults to "origin/main". r is nil in
// production (openGit anchors a client at dir); tests inject a fake
// HistoryReader.
func ChangedFiles(ctx context.Context, r gitclient.HistoryReader, dir, base string) ([]FileChange, error) {
	if base == "" {
		base = "origin/main"
	}
	if r == nil {
		var err error
		r, err = openGit(ctx, dir)
		if err != nil {
			return nil, err
		}
	}
	changes, err := r.ChangedFiles(ctx, base)
	if err != nil {
		return nil, err
	}
	files := make([]FileChange, 0, len(changes))
	for _, c := range changes {
		files = append(files, FileChange{
			Path:      c.Path,
			Additions: c.Additions,
			Deletions: c.Deletions,
			Binary:    c.Binary,
		})
	}
	return files, nil
}

// Commits returns the commits between base and HEAD via x/gitclient's
// HistoryReader role (`git log base..HEAD`). base defaults to
// "origin/main". Author is rendered as "Name <email>", matching the
// previous `%an <%ae>` log format; Body is trimmed, matching the previous
// hand-rolled `-z`/NUL-field parser's explicit strings.TrimSpace. r is nil
// in production (openGit anchors a client at dir); tests inject a fake
// HistoryReader.
func Commits(ctx context.Context, r gitclient.HistoryReader, dir, base string) ([]Commit, error) {
	if base == "" {
		base = "origin/main"
	}
	if r == nil {
		var err error
		r, err = openGit(ctx, dir)
		if err != nil {
			return nil, err
		}
	}
	cs, err := r.Commits(ctx, gitclient.LogOptions{Base: base})
	if err != nil {
		return nil, err
	}
	commits := make([]Commit, 0, len(cs))
	for _, c := range cs {
		commits = append(commits, Commit{
			SHA:     c.SHA,
			Subject: c.Subject,
			Body:    strings.TrimSpace(c.Body),
			Author:  fmt.Sprintf("%s <%s>", c.Author.Name, c.Author.Email),
		})
	}
	return commits, nil
}
