package sync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// authCheckerVCS is a VCSProvider that also satisfies vcs.AuthChecker (and no
// fingerprint capability), used to exercise the daemon's startup preflight in
// isolation. CheckAuth returns checkAuthErr.
type authCheckerVCS struct {
	fakeVCS
	checkAuthErr error
}

func (a *authCheckerVCS) CheckAuth(_ context.Context) error { return a.checkAuthErr }

func makeAuthEngine(t *testing.T, vp VCSProvider, repos []config.RepoConfig) *Engine {
	t.Helper()
	e, err := New(Deps{
		Cfg:      &config.Config{SelfLogin: "me", WorktreeRoot: t.TempDir(), Repos: repos},
		VCS:      map[string]VCSProvider{"github": vp},
		Beads:    noopBeads{},
		StateDir: t.TempDir(),
		Now:      func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// TestDaemon_PreflightAuthInvalid_ExitsNonNil: a provider whose CheckAuth
// returns an error wrapping vcs.ErrAuthInvalid causes Daemon to exit fast with
// a non-nil error wrapping the same sentinel — before any goroutine spins up.
func TestDaemon_PreflightAuthInvalid_ExitsNonNil(t *testing.T) {
	vp := &authCheckerVCS{checkAuthErr: fmt.Errorf("gh api graphql: %w", vcs.ErrAuthInvalid)}
	e := makeAuthEngine(t, vp, nil)

	// Short-lived ctx as a safety net; the preflight should return well before.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval: 1 * time.Hour,
			LockDir:  t.TempDir(),
			Logger:   discardLogger(),
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Daemon returned nil; want non-nil auth-preflight error")
		}
		if !errors.Is(err, vcs.ErrAuthInvalid) {
			t.Fatalf("errors.Is(err, vcs.ErrAuthInvalid) = false; err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Daemon did not exit on preflight auth failure within 2s")
	}
}

// TestDaemon_PreflightTransient_Continues: a provider whose CheckAuth returns a
// plain (non-auth) error is tolerated — the daemon proceeds into its loop and
// then exits cleanly (nil) on ctx cancel.
func TestDaemon_PreflightTransient_Continues(t *testing.T) {
	vp := &authCheckerVCS{checkAuthErr: errors.New("could not resolve host")}
	e := makeAuthEngine(t, vp, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval: 1 * time.Hour,
			LockDir:  t.TempDir(),
			Logger:   discardLogger(),
		})
	}()

	// Let the daemon get past preflight and into its select loop, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Daemon: got %v; want nil after transient preflight + ctx cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Daemon did not exit within 2s after ctx cancel")
	}
}

// TestDaemon_PollAuthEscalates_ExitsNonNil: preflight passes (CheckAuth nil) but
// every fingerprint poll returns vcs.ErrAuthInvalid. After maxAuthFailStreak
// consecutive ticks the daemon escalates a restart-to-refresh, returning a
// non-nil error wrapping the sentinel.
func TestDaemon_PollAuthEscalates_ExitsNonNil(t *testing.T) {
	vp := &fakeFingerprintVCS{
		fpErr:        fmt.Errorf("gh api graphql: %w", vcs.ErrAuthInvalid),
		checkAuthErr: nil, // pass preflight
	}
	// A configured repo so the mine poll runs each tick.
	e := makeAuthEngine(t, vp, []config.RepoConfig{{Remote: "o/r"}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval: 5 * time.Millisecond,
			LockDir:  t.TempDir(),
			Logger:   discardLogger(),
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Daemon returned nil; want non-nil auth-escalation error")
		}
		if !errors.Is(err, vcs.ErrAuthInvalid) {
			t.Fatalf("errors.Is(err, vcs.ErrAuthInvalid) = false; err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Daemon did not escalate within 2s of sustained poll auth failures")
	}
}
