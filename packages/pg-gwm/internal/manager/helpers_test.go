package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

var epoch = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

// env is one hermetic repository with a Manager over it.
type env struct {
	t    *testing.T
	repo *gitfixture.Repo
	g    gitrun.Git
	m    *Manager
	now  time.Time
}

func newEnv(t *testing.T, tune ...func(*config.RepoConfig)) *env {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := newBaseRepo(t)
	e := &env{t: t, repo: r, g: gitruntest.New(t, r), now: epoch}
	e.m = e.manager(tune...)
	return e
}

// manager builds a Manager over e's repository; each call is an independent
// handle, as a separate process would hold.
func (e *env) manager(tune ...func(*config.RepoConfig)) *Manager {
	e.t.Helper()
	rp, err := repo.Resolve(context.Background(), e.g, e.repo.Dir)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg := config.File{}.For(rp)
	for _, f := range tune {
		f(&cfg)
	}
	return &Manager{
		Repo:     rp,
		Cfg:      cfg,
		Store:    state.Open(rp.CommonDir),
		Prov:     provision.NewWorktree(rp.Main, e.g, time.Minute, time.Minute),
		Git:      e.g,
		Registry: state.Registry{Path: state.RegistryPath()},
		Counters: state.Counters{Path: state.CountersPath()},
		Now:      func() time.Time { return e.now },
	}
}

// git runs git in dir through the fixture's hermetic client.
func (e *env) git(dir string, args ...string) string {
	e.t.Helper()
	c := e.repo.Client
	if dir != e.repo.Dir {
		c = gittest.ClientAt(e.t, e.repo, dir)
	}
	out, err := c.Run(e.t.Context(), args...)
	if err != nil {
		e.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func (e *env) lease(r LeaseRequest) (Record, error) {
	e.t.Helper()
	if r.BaseDir == "" {
		r.BaseDir = e.repo.Dir
	}
	return e.m.Lease(e.t.Context(), r)
}

func (e *env) mustLease(r LeaseRequest) Record {
	e.t.Helper()
	rec, err := e.lease(r)
	if err != nil {
		e.t.Fatalf("lease %+v: %v", r, err)
	}
	return rec
}

// wantErr requires err to be the catalog error name with code.
func wantErr(t *testing.T, err error, name string, code int) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want catalog error %s", err, name)
	}
	if e.Name != name || e.Code != code || CodeOf(err) != code {
		t.Fatalf("error = %s/%d (%v), want %s/%d", e.Name, e.Code, e.Message, name, code)
	}
	return e
}

func (e *env) state() state.State {
	e.t.Helper()
	st, err := e.m.Store.ReadUnlocked()
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) events() string {
	e.t.Helper()
	files, _ := filepath.Glob(filepath.Join(e.m.Store.Dir, "events-*.jsonl"))
	var all strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			e.t.Fatal(err)
		}
		all.Write(b)
	}
	return all.String()
}

// blockingProv blocks Create until ctx ends (or release closes), after
// signalling started.
type blockingProv struct {
	provision.Provisioner
	started chan struct{}
	release chan struct{}
}

func newBlocking(p provision.Provisioner) *blockingProv {
	return &blockingProv{Provisioner: p, started: make(chan struct{}, 4), release: make(chan struct{})}
}

func (b *blockingProv) Create(ctx context.Context, dir string, spec provision.BranchSpec, reason string) error {
	b.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
		return b.Provisioner.Create(ctx, dir, spec, reason)
	}
}
