// Package session is ccpool's lifecycle logic over small ports (tmux, trust,
// store, wait). Ensure routes name -> a live, ready session, launching or
// resuming as needed (spec §8.2). It never touches exec/tmux/sql directly.
package session

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/ccpool/internal/launch"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

type Tmux interface {
	HasSession(name string) bool
	NewSession(name string, env map[string]string, argv []string) error
}
type Truster interface{ EnsureTrusted(cwd string) error }
type Store interface {
	GetByName(ctx context.Context, name string) (store.Session, bool, error)
	Insert(ctx context.Context, s store.Session) error
	Transition(ctx context.Context, name string, to store.State, uuid, transcriptPath string) (store.State, error)
}
type Waiter interface {
	Wait(ctx context.Context, name string, since int64) (wait.Outcome, error)
}

// waitFunc adapts a func to Waiter (used in tests and the default wiring).
type waitFunc func(ctx context.Context, name string, since int64) (wait.Outcome, error)

func (f waitFunc) Wait(ctx context.Context, name string, since int64) (wait.Outcome, error) {
	return f(ctx, name, since)
}

type Deps struct {
	Tmux      Tmux
	Trust     Truster
	Store     Store
	Wait      Waiter
	Socket    string
	Prefix    string
	PluginDir string
	ClaudeBin string
	NewUUID   func() string
	Now       func() time.Time
}

type Service struct{ d Deps }

func New(d Deps) *Service { return &Service{d: d} }

type Handle struct {
	Name        string
	UUID        string
	TmuxSession string
	State       store.State
}

// Ensure returns a live, ready handle for name, launching or resuming as needed.
func (s *Service) Ensure(ctx context.Context, name, cwd, model string) (Handle, error) {
	tmuxName := s.d.Prefix + name
	row, exists, err := s.d.Store.GetByName(ctx, name)
	if err != nil {
		return Handle{}, err
	}

	// Already live → reuse.
	if s.d.Tmux.HasSession(tmuxName) {
		return Handle{Name: name, UUID: row.UUID, TmuxSession: tmuxName, State: row.State}, nil
	}

	if err := s.d.Trust.EnsureTrusted(cwd); err != nil {
		return Handle{}, fmt.Errorf("pre-trust %q: %w", cwd, err)
	}

	if exists {
		// Cold row → resume by name. Flip to `starting` BEFORE launch (spec §8.2 step 3)
		// so `list` doesn't show the stale prior outcome during the launch window, and
		// snapshot THAT generation as the wait baseline.
		if _, err := s.d.Store.Transition(ctx, name, store.Starting, "", ""); err != nil {
			return Handle{}, fmt.Errorf("mark resuming: %w", err)
		}
		since, err := s.currentGeneration(ctx, name)
		if err != nil {
			return Handle{}, err
		}
		argv := launch.BuildResume(launch.Spec{ClaudeBin: s.d.ClaudeBin, Name: name, PluginDir: s.d.PluginDir, Model: orDefault(model, row.Model)})
		return s.launchAndWait(ctx, name, tmuxName, row.UUID, since, argv)
	}

	// Brand new.
	uuid := s.d.NewUUID()
	if err := s.d.Store.Insert(ctx, store.Session{
		Name: name, UUID: uuid, CWD: cwd, State: store.Starting,
		TmuxSession: tmuxName, Model: model,
	}); err != nil {
		return Handle{}, fmt.Errorf("insert row: %w", err)
	}
	// Snapshot the inserted row's generation (read back, not a magic literal) and
	// wait for the SessionStart hook's Transition to advance past it.
	since, err := s.currentGeneration(ctx, name)
	if err != nil {
		return Handle{}, err
	}
	argv := launch.BuildNew(launch.Spec{ClaudeBin: s.d.ClaudeBin, UUID: uuid, Name: name, PluginDir: s.d.PluginDir, Model: model})
	return s.launchAndWait(ctx, name, tmuxName, uuid, since, argv)
}

// currentGeneration reads the row's current generation (the wait baseline).
func (s *Service) currentGeneration(ctx context.Context, name string) (int64, error) {
	row, ok, err := s.d.Store.GetByName(ctx, name)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("row %q vanished before launch", name)
	}
	return row.Generation, nil
}

// launchAndWait starts the tmux session and blocks until generation > since.
func (s *Service) launchAndWait(ctx context.Context, name, tmuxName, uuid string, since int64, argv []string) (Handle, error) {
	env := map[string]string{
		"CCPOOL_NAME":         name,
		"CCPOOL_UUID":         uuid,
		"PA_MONITOR_NO_NUDGE": "1",
	}
	if err := s.d.Tmux.NewSession(tmuxName, env, argv); err != nil {
		return Handle{}, fmt.Errorf("tmux new-session: %w", err)
	}
	out, err := s.d.Wait.Wait(ctx, name, since)
	if err != nil {
		return Handle{}, fmt.Errorf("wait ready: %w", err)
	}
	if out.TimedOut {
		return Handle{Name: name, UUID: uuid, TmuxSession: tmuxName, State: out.State},
			fmt.Errorf("session %q did not reach ready before timeout (state=%s)", name, out.State)
	}
	return Handle{Name: name, UUID: uuid, TmuxSession: tmuxName, State: out.State}, nil
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
