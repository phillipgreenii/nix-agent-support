// Package session is ccpool's lifecycle logic over small ports (tmux, trust,
// store, wait). Ensure routes name -> a live, ready session, launching or
// resuming as needed (spec §8.2). It never touches exec/tmux/sql directly.
package session

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/ccpool/internal/launch"
	"github.com/phillipgreenii/ccpool/internal/notify"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

type Tmux interface {
	HasSession(name string) bool
	NewSession(name, cwd string, env map[string]string, argv []string) error
	SendKeys(name string, keys ...string) error
	Paste(name, body string) error
	KillSession(name string) error
	CapturePane(name string) (string, error)
}
type Truster interface{ EnsureTrusted(cwd string) error }
type Store interface {
	GetByName(ctx context.Context, name string) (store.Session, bool, error)
	Insert(ctx context.Context, s store.Session) error
	Transition(ctx context.Context, name string, to store.State, uuid, transcriptPath string) (store.State, error)
	Delete(ctx context.Context, name string) error
	List(ctx context.Context) ([]store.Session, error)
}

// Locker serializes operations on one session name (spec §15). A nil Locker on
// Deps means "no locking" (used by unit tests with fakes).
type Locker interface {
	Lock(name string) (unlock func(), err error)
}
type Waiter interface {
	Wait(ctx context.Context, name string, since int64) (wait.Outcome, error)
}

// Transcript reads reply text / awaiting-input state from a transcript file.
type Transcript interface {
	LastAssistantText(path string) (string, error)
	IsAwaitingInput(path string) (bool, error)
}

// Mode selects send behavior when (and how) to deliver.
type Mode int

const (
	ModeRefuseIfBusy Mode = iota // default: error if the session isn't idle
	ModeNoWait                   // deliver and return immediately
	ModeQueue                    // skip idle check; deliver into Claude's native queue; implies no-wait
	ModeInterrupt                // cancel the current turn, then deliver
)

// Result is the outcome of a Send.
type Result struct {
	State    store.State
	Reply    string
	TimedOut bool
}

// waitFunc adapts a func to Waiter (used in tests and the default wiring).
type waitFunc func(ctx context.Context, name string, since int64) (wait.Outcome, error)

func (f waitFunc) Wait(ctx context.Context, name string, since int64) (wait.Outcome, error) {
	return f(ctx, name, since)
}

type Deps struct {
	Tmux       Tmux
	Trust      Truster
	Store      Store
	Wait       Waiter
	Transcript Transcript
	Lock       Locker
	Notify     notify.Notifier // optional (nil = no-op); fires the §8.3 fallback edge (§10)
	NotifyOn   []string        // states that trigger a notification
	Socket     string
	Prefix     string
	PluginDir  string
	ClaudeBin  string
	NewUUID    func() string
	Now        func() time.Time
	Sleep      func(time.Duration) // injected delay for the cancel Escape burst (nil = no-op, for tests)
}

type Service struct{ d Deps }

func New(d Deps) *Service { return &Service{d: d} }

type Handle struct {
	Name        string
	UUID        string
	TmuxSession string
	State       store.State
}

// EnsureOpts carries the per-call launch extras threaded into a launched or
// resumed session. The zero value is a valid "no extras" launch, so callers
// that only need resume-or-reuse (e.g. reply) can pass EnsureOpts{}.
type EnsureOpts struct {
	// Env is caller-supplied environment injected into the session at launch
	// (e.g. BEADS_ACTOR/BEADS_DIR/WORKSPACE_ROOT for pool workers). It is merged
	// with ccpool's own correlation markers at launch time; see launchAndWait for
	// the merge policy (ccpool's markers are authoritative on conflict).
	Env map[string]string
}

// Ensure returns a live, ready handle for name, launching or resuming as needed.
func (s *Service) Ensure(ctx context.Context, name, cwd, model string, opts EnsureOpts) (Handle, error) {
	var h Handle
	err := s.withLock(name, func() error {
		var e error
		h, e = s.ensureLocked(ctx, name, cwd, model, opts)
		return e
	})
	return h, err
}

func (s *Service) ensureLocked(ctx context.Context, name, cwd, model string, opts EnsureOpts) (Handle, error) {
	tmuxName := s.d.Prefix + name
	row, exists, err := s.d.Store.GetByName(ctx, name)
	if err != nil {
		return Handle{}, err
	}

	// Already live → reuse.
	if s.d.Tmux.HasSession(tmuxName) {
		return Handle{Name: name, UUID: row.UUID, TmuxSession: tmuxName, State: row.State}, nil
	}

	// Canonicalize the cwd so the trust key matches what Claude records (it
	// resolves symlinks, e.g. macOS /tmp -> /private/tmp). Without this, a
	// symlinked --cwd is pre-trusted under the wrong key and the launch stalls
	// on the folder-trust prompt. Best-effort: a non-existent dir keeps as-is.
	if resolved, rerr := filepath.EvalSymlinks(cwd); rerr == nil {
		cwd = resolved
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
		return s.launchAndWait(ctx, name, tmuxName, row.UUID, row.CWD, since, argv, opts.Env)
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
	return s.launchAndWait(ctx, name, tmuxName, uuid, cwd, since, argv, opts.Env)
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

// launchAndWait starts the tmux session (in cwd) and blocks until generation > since.
// extraEnv (caller-supplied) is injected first; ccpool's own correlation markers
// are written last so they are authoritative — hooks key the store row off
// CCPOOL_NAME/CCPOOL_UUID, so a caller must never be able to clobber them.
func (s *Service) launchAndWait(ctx context.Context, name, tmuxName, uuid, cwd string, since int64, argv []string, extraEnv map[string]string) (Handle, error) {
	env := make(map[string]string, len(extraEnv)+3)
	maps.Copy(env, extraEnv)
	env["CCPOOL_NAME"] = name
	env["CCPOOL_UUID"] = uuid
	env["PA_MONITOR_NO_NUDGE"] = "1"
	if err := s.d.Tmux.NewSession(tmuxName, cwd, env, argv); err != nil {
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

// fireNotify edge-triggers the configured notifier for a transition that the
// hook does NOT see (the §8.3 step-6 AskUserQuestion fallback, spec §10). No-op
// when no notifier is wired or the edge/membership test fails.
func (s *Service) fireNotify(ctx context.Context, name string, prior, to store.State) {
	if s.d.Notify == nil || !notify.ShouldNotify(s.d.NotifyOn, string(prior), string(to)) {
		return
	}
	uuid, cwd := "", ""
	if row, ok, _ := s.d.Store.GetByName(ctx, name); ok {
		uuid, cwd = row.UUID, row.CWD
	}
	_ = s.d.Notify.Notify(notify.Event{Name: name, UUID: uuid, State: string(to), CWD: cwd})
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// sleep is the injected delay; nil (tests) is a no-op so fakes need no wiring.
func (s *Service) sleep(d time.Duration) {
	if s.d.Sleep != nil {
		s.d.Sleep(d)
	}
}

// withLock runs fn while holding the per-name lock. A nil Locker (tests) is a
// no-op so existing fakes need no Lock wiring.
func (s *Service) withLock(name string, fn func() error) error {
	if s.d.Lock == nil {
		return fn()
	}
	unlock, err := s.d.Lock.Lock(name)
	if err != nil {
		return fmt.Errorf("lock %q: %w", name, err)
	}
	defer unlock()
	return fn()
}
