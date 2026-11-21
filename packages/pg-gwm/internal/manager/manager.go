package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// Manager is the engine for one repository. Every field but Repo, Cfg and
// Store may be left zero: Now defaults to time.Now, FreeBytes to
// provision.FreeBytes, and a Registry or Counters with no Path records
// nothing.
type Manager struct {
	Repo      repo.Repo
	Cfg       config.RepoConfig
	Store     state.Store
	Prov      provision.Provisioner
	Git       gitrun.Git
	Registry  state.Registry
	Counters  state.Counters
	Now       func() time.Time
	FreeBytes func(string) (uint64, error)
}

func (m *Manager) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now().UTC()
}

// span turns a configured duration into a bound; "none" is the fallback.
func span(d config.Duration, none time.Duration) time.Duration {
	if d.None || d.D <= 0 {
		return none
	}
	return d.D
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	return m.Git.Exec(ctx, dir, span(m.Cfg.GitTimeout, 24*time.Hour), args...)
}

func (m *Manager) freeBytes(p string) (uint64, error) {
	if m.FreeBytes != nil {
		return m.FreeBytes(p)
	}
	return provision.FreeBytes(p)
}

// phase records one lease phase's duration for the metrics.
func (m *Manager) phase(ctx context.Context, name string, since time.Time) {
	if m.Counters.Path == "" {
		return
	}
	m.Counters.LeasePhase(ctx, m.Repo.Label, name, time.Since(since).Seconds())
}

func (m *Manager) event(e state.Event) {
	e.At = m.now()
	_ = m.Store.Append(e)
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// existingAncestor is the nearest ancestor of p (or p) that exists.
func existingAncestor(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// gitWT is one entry of git worktree list --porcelain.
type gitWT struct {
	Path, Head, Branch string
	Detached           bool
}

func parseWorktreeList(out string) []gitWT {
	var wts []gitWT
	var cur *gitWT
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			wts = append(wts, gitWT{Path: strings.TrimPrefix(line, "worktree ")})
			cur = &wts[len(wts)-1]
		case cur == nil:
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			cur.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "detached":
			cur.Detached = true
		}
	}
	return wts
}
