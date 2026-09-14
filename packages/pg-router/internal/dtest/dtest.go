// Package dtest provides shared test fakes for pg-router internal packages.
// It is a normal (non-_test.go) file so it can be imported by multiple test packages.
package dtest

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/internal/usage"
	"github.com/phillipgreenii/x/gitclient"
)

// Ensure the fakes satisfy their interfaces at compile time.
var (
	_ gitclient.WorktreeManager = (*NoopWorktreeManager)(nil)
)

// ErrSend is a sentinel error for faking send failures.
var ErrSend = errors.New("send failed")

// RampReader is a fake usage.Reader that serves a fixed sequence of Snapshots
// (last entry repeats once exhausted). Used to inject a usage ramp into tests.
// Mutex-guarded so it is safe for concurrent use (watchdog goroutine).
type RampReader struct {
	mu  sync.Mutex
	Seq []usage.Snapshot
	i   int
}

func (r *RampReader) Read(_ context.Context, _ string) (usage.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.Seq[min(r.i, len(r.Seq)-1)]
	r.i++
	return s, nil
}

// NoopGit is a recording watchdog.GitRunner that performs no real git
// commands — so tests exercise the watchdog's hard-stop reset/clean seam
// (Deps.Git / Orchestrator.git) without touching any real repo. It has no
// bearing on per-bead worktree CREATION any more (pg2-mj9n0 moved that onto
// x/gitclient's WorktreeManager via GitOpener/NoopGitOpener below); this
// fake's rev-parse/AddOnly plumbing exists only for whatever future watchdog
// (pg2-ljyaj) or other GitRunner test still shells a probe through it.
type NoopGit struct {
	mu      sync.Mutex
	Calls   [][]string
	AddOnly bool // when true, rev-parse fails
}

func (g *NoopGit) Run(_ context.Context, dir string, args ...string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, append([]string{dir}, args...))
	if g.AddOnly && len(args) > 0 && args[0] == "rev-parse" {
		return errors.New("not a git worktree")
	}
	return nil
}

// NoopWorktreeManager is a recording gitclient.WorktreeManager that performs
// no real git commands — the CreateWorktree/RemoveWorktree/PruneWorktrees
// half of NoopGitOpener's fake.
type NoopWorktreeManager struct {
	mu    sync.Mutex
	Calls [][]string
}

func (m *NoopWorktreeManager) CreateWorktree(_ context.Context, path, branch string, opts gitclient.CreateWorktreeOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, []string{"add", path, branch, strconv.FormatBool(opts.ResetBranch)})
	return nil
}

func (m *NoopWorktreeManager) RemoveWorktree(_ context.Context, path string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, []string{"remove", path, strconv.FormatBool(force)})
	return nil
}

func (m *NoopWorktreeManager) PruneWorktrees(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, []string{"prune"})
	return nil
}

// NoopGitOpener is a worktree.Opener (see internal/executor.Deps.GitOpener)
// that never touches a real repo. Open succeeds — returning a shared
// NoopWorktreeManager — for any dir NOT listed in MissingAt; dirs in
// MissingAt fail with gitclient.ErrNotARepository, which is how
// worktree.Ensure decides a worktree doesn't exist yet and must be created.
// The default (MissingAt nil) makes every probe succeed, so Ensure always
// takes the reuse path without shelling out to real git — mirroring
// pre-migration NoopGit's default behavior for the worktree-creation seam.
type NoopGitOpener struct {
	mu        sync.Mutex
	Calls     []string
	MissingAt map[string]bool
	WTM       NoopWorktreeManager
}

func (o *NoopGitOpener) Open(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Calls = append(o.Calls, dir)
	if o.MissingAt[dir] {
		return nil, gitclient.ErrNotARepository
	}
	return &o.WTM, nil
}

// ManualClock advances only when the test ticks it, so waitDone polling is
// deterministic and instant.
// mu guards T so it is safe for concurrent use when workerWaitWithWatchdog runs
// waitDone (which advances via tick) and the watchdog (which reads via Now)
// in parallel goroutines.
type ManualClock struct {
	mu sync.Mutex
	T  time.Time
}

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.T
}

// TickAdvancing returns a tick func that advances the clock by d each poll, so a
// finite-deadline loop terminates without real sleeping.
func (c *ManualClock) TickAdvancing() func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		c.T = c.T.Add(d)
		c.mu.Unlock()
		return nil
	}
}
