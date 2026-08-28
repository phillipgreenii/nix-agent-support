// Package dtest provides shared test fakes for pr-pool internal packages.
// It is a normal (non-_test.go) file so it can be imported by multiple test packages.
package dtest

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/usage"
	"github.com/phillipgreenii/x/gitclient"
)

// Ensure the fakes satisfy their interfaces at compile time.
var (
	_ ccpool.Runner             = (*FakeCC)(nil)
	_ beads.Runner              = (*ScriptBD)(nil)
	_ gitclient.WorktreeManager = (*NoopWorktreeManager)(nil)
)

// TestStamp is the fixed per-attempt stamp injected in tests so external_ids are
// deterministic.
const TestStamp = "20260616T010203"

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

// FakeCC records calls and serves scripted List results.
// mu guards ListIdx so List is safe for concurrent goroutines (workerWaitWithWatchdog).
type FakeCC struct {
	mu          sync.Mutex
	Ensured     []string
	EnsureNames []string
	EnsuredCwd  string            // the cwd of the last Ensure call (the per-bead worktree)
	EnsuredMeta map[string]string // the meta of the last Ensure call
	Sent        []string
	Closed      []string
	ClosedPurge []bool
	SendErr     error
	EnsureErr   error
	ListSeq     [][]ccpool.Session // one entry consumed per List call (last repeats)
	ListIdx     int
}

func (f *FakeCC) Ensure(_ context.Context, externalID, name, cwd string, _, meta map[string]string) error {
	f.Ensured = append(f.Ensured, externalID)
	f.EnsureNames = append(f.EnsureNames, name)
	f.EnsuredCwd = cwd
	f.EnsuredMeta = meta
	return f.EnsureErr
}

func (f *FakeCC) Send(_ context.Context, externalID, _ string, _ ccpool.SendMode) error {
	f.Sent = append(f.Sent, externalID)
	return f.SendErr
}
func (f *FakeCC) Cancel(_ context.Context, _ string) error { return nil }
func (f *FakeCC) Close(_ context.Context, externalID string, purge bool) error {
	f.Closed = append(f.Closed, externalID)
	f.ClosedPurge = append(f.ClosedPurge, purge)
	return nil
}

func (f *FakeCC) List(_ context.Context) ([]ccpool.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ListSeq) == 0 {
		return nil, nil
	}
	i := f.ListIdx
	if i >= len(f.ListSeq) {
		i = len(f.ListSeq) - 1
	}
	f.ListIdx++
	return f.ListSeq[i], nil
}

// ScriptBD serves a status sequence per bead id and records update + comment calls.
// mu guards shared state so Run is safe for concurrent goroutines
// (workerWaitWithWatchdog runs waitDone + watchdog in parallel; both call BD.Run).
type ScriptBD struct {
	mu          sync.Mutex
	StatusSeq   map[string][]string
	Idx         map[string]int
	Updates     []string
	Comments    []string          // joined `comment <id> <text>` calls (so tests can prove no comment to an unrelated bead)
	Ready       map[string]string // keyed by "feedback"/"worker"
	ReadyErr    error             // if set, every `bd ready` returns this error
	Show        map[string]string
	ShowErrOnce map[string]error // returns error once per id, then clears
}

func (s *ScriptBD) Run(_ context.Context, args ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch args[0] {
	case "ready":
		if s.ReadyErr != nil {
			return "", s.ReadyErr
		}
		if Contains(args, "worker-ready") {
			return s.Ready["worker"], nil
		}
		return s.Ready["feedback"], nil
	case "show":
		id := args[1]
		if v, ok := s.Show[id]; ok {
			return v, nil
		}
		// one-shot error injection for status reads
		if s.ShowErrOnce != nil {
			if err, ok := s.ShowErrOnce[id]; ok {
				delete(s.ShowErrOnce, id)
				return "", err
			}
		}
		// status sequence
		if s.Idx == nil {
			s.Idx = map[string]int{}
		}
		seq := s.StatusSeq[id]
		i := s.Idx[id]
		if i >= len(seq) {
			i = len(seq) - 1
		}
		s.Idx[id]++
		return `{"id":"` + id + `","status":"` + seq[i] + `"}`, nil
	case "update":
		s.Updates = append(s.Updates, join(args))
	case "comment":
		s.Comments = append(s.Comments, join(args))
	}
	return "", nil
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

// Contains reports whether a is in the slice x.
func Contains(a []string, x string) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}

// join is unexported; used only inside ScriptBD.Run.
func join(a []string) string {
	out := ""
	for i, x := range a {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

// HasUpdate reports whether bd.Updates contains an entry equal to sub.
func HasUpdate(bd *ScriptBD, sub string) bool {
	for _, u := range bd.Updates {
		if u == sub {
			return true
		}
	}
	return false
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
