package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/gofrs/flock"

	"github.com/phillipgreenii/claude-agents-tui/internal/core/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/block"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/poller"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/week"
	"github.com/phillipgreenii/claude-agents-tui/internal/otel"
)

// PIDLock holds the pidfile flock for the lifetime of the daemon process.
// Release MUST be called to remove the file and free the lock. Safe to
// call multiple times.
type PIDLock struct {
	file     *flock.Flock
	path     string
	released bool
}

// AcquirePIDFile creates Paths.Dir if missing, opens the pidfile, takes
// a non-blocking exclusive flock, and writes the current pid into the
// file.
//
// If a previous daemon died without releasing the lock, the kernel has
// already freed it — TryLock will succeed and we overwrite the stale
// pid content. No explicit stale-detection is needed for that case.
//
// Returns an error if the lock is held by a LIVE process.
func AcquirePIDFile(p Paths) (*PIDLock, error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir state dir: %w", err)
	}

	fl := flock.New(p.PIDFile)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("flock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("pidfile %s is locked by another process", p.PIDFile)
	}

	pid := []byte(strconv.Itoa(os.Getpid()))
	if err := os.WriteFile(p.PIDFile, pid, 0o600); err != nil {
		_ = fl.Unlock()
		return nil, fmt.Errorf("write pid: %w", err)
	}

	return &PIDLock{file: fl, path: p.PIDFile}, nil
}

// Release frees the lock and removes the pidfile. Safe to call multiple
// times; subsequent calls are no-ops.
func (l *PIDLock) Release() {
	if l == nil || l.released {
		return
	}
	l.released = true
	_ = l.file.Unlock()
	_ = os.Remove(l.path)
}

// BindSocket removes any pre-existing socket file at p.Socket, binds a
// fresh Unix listener, and chmods it 0600. The returned listener removes
// the socket file on Close.
func BindSocket(p Paths) (net.Listener, error) {
	if err := os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	l, err := net.Listen("unix", p.Socket)
	if err != nil {
		return nil, fmt.Errorf("listen unix: %w", err)
	}
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}

	return &socketListener{Listener: l, path: p.Socket}, nil
}

// socketListener wraps net.Listener so that Close unlinks the socket
// file in addition to closing the underlying fd.
type socketListener struct {
	net.Listener
	path string
}

func (s *socketListener) Close() error {
	err := s.Listener.Close()
	_ = os.Remove(s.path)
	return err
}

// RunOptions configures a daemon run. Paths is required; everything else
// is optional. Emitter, when non-nil, is shut down on Run return so any
// batched metrics/logs flush before the process exits.
//
// When Poller is non-nil, each tick calls Snapshot, folds the result
// into the shared state visible to gRPC handlers, and feeds the block
// and week trackers (if provided).
type RunOptions struct {
	Paths        Paths
	Emitter      *otel.Emitter
	Tick         time.Duration
	Poller       *poller.Poller
	BlockTracker *block.Tracker
	WeekTracker  *week.Tracker
	// Caffeinate, when non-nil, has its Tick advanced each main tick
	// with the current any-working signal derived from the snapshot.
	Caffeinate *caffeinate.Manager
	// InitialCaffeinateOn applies the persisted user toggle at startup.
	InitialCaffeinateOn bool
	// RuntimePath is the runtime.json file path. Empty disables persistence
	// of caffeinate toggle changes from Caffeinate RPC.
	RuntimePath string
	// WeeklyFn fetches the current week entry. Nil → never polled.
	WeeklyFn func(ctx context.Context) (*ccusage.WeeklyEntry, error)
	// PlanTier is forwarded as the `plan_tier` attribute on emitted
	// limit-hit events/counters.
	PlanTier string
	// WeeklyEvery controls how often WeeklyFn is invoked relative to
	// the main tick. 0 means once per tick.
	WeeklyEvery int
	// NudgeFn dispatches a signal to the given pid. nil → Nudge RPC
	// returns FailedPrecondition.
	NudgeFn func(pid int, text string) error
}

// RunWith is the daemon's main loop. It acquires the pidfile, binds the
// socket, starts the gRPC server, and blocks until ctx is done.
func RunWith(ctx context.Context, opts RunOptions) error {
	lock, err := AcquirePIDFile(opts.Paths)
	if err != nil {
		return err
	}
	defer lock.Release()

	lis, err := BindSocket(opts.Paths)
	if err != nil {
		return err
	}
	defer lis.Close()

	state := newSharedState()
	state.mu.Lock()
	state.runtimePath = opts.RuntimePath
	state.mu.Unlock()
	state.setCaffeinateOn(opts.InitialCaffeinateOn)
	if opts.Caffeinate != nil {
		opts.Caffeinate.SetToggle(opts.InitialCaffeinateOn)
	}

	_, stop := serve(lis, state, opts.NudgeFn)
	defer stop()

	defer opts.Emitter.Shutdown(context.Background())

	// Wire tracker callbacks to emitter counters/events.
	if opts.BlockTracker != nil && opts.Emitter != nil {
		opts.BlockTracker.OnLimitHit = func() {
			opts.Emitter.RecordBlockLimitHit(map[string]string{
				"plan_tier": opts.PlanTier,
				"block.id":  opts.BlockTracker.ID(),
			})
		}
	}
	if opts.WeekTracker != nil && opts.Emitter != nil {
		opts.WeekTracker.OnLimitHit = func() {
			opts.Emitter.RecordWeekLimitHit(map[string]string{
				"plan_tier": opts.PlanTier,
				"week.id":   opts.WeekTracker.ID(),
				"source":    "computed",
			})
		}
	}

	tick := opts.Tick
	if tick <= 0 {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	tickCount := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			tickCount++
			if opts.Poller == nil {
				// no poller — still advance the (toggle-driven) caffeinate
				// tick to honour RPC-driven on/off requests
				if opts.Caffeinate != nil {
					// re-read user toggle from shared state in case Caffeinate RPC changed it
					opts.Caffeinate.SetToggle(state.isCaffeinateOn())
					opts.Caffeinate.Tick(false)
				}
				continue
			}
			tree, _, err := opts.Poller.Snapshot(ctx)
			if err != nil {
				continue
			}
			fetchWeek := opts.WeeklyFn != nil && (opts.WeeklyEvery <= 0 || tickCount%opts.WeeklyEvery == 0)
			if fetchWeek {
				if w, err := opts.WeeklyFn(ctx); err == nil && w != nil {
					tree.ActiveWeek = w
				}
			}
			if opts.BlockTracker != nil {
				opts.BlockTracker.Update(tree.ActiveBlock)
			}
			if opts.WeekTracker != nil {
				opts.WeekTracker.Update(tree.ActiveWeek)
			}
			anyWorking := false
			for _, d := range tree.Dirs {
				if d.WorkingN > 0 {
					anyWorking = true
					break
				}
			}
			if opts.Caffeinate != nil {
				opts.Caffeinate.SetToggle(state.isCaffeinateOn())
				prevState := opts.Caffeinate.State()
				opts.Caffeinate.Tick(anyWorking)
				newState := opts.Caffeinate.State()
				active := newState != caffeinate.StateOff
				cause := ""
				if active {
					if state.isCaffeinateOn() {
						cause = "manual"
					}
					if anyWorking {
						cause = "agents_active"
					}
				}
				state.setCaffeinateActive(active, cause)
				if prevState == caffeinate.StateOff && newState != caffeinate.StateOff {
					opts.Emitter.RecordCaffeinateRound(map[string]string{"cause": cause})
				}
				if prevState == caffeinate.StateArmedCountdown && newState == caffeinate.StateOff && !anyWorking {
					opts.Emitter.RecordCaffeinateGraceExpired(nil)
				}
			}
			updateGauges(opts.Emitter, tree, opts.PlanTier)
			state.setTree(tree)
		}
	}
}

// updateGauges pushes per-state session counts into the emitter gauges.
// nil-safe on emitter.
func updateGauges(e *otel.Emitter, tree *aggregate.Tree, planTier string) {
	if e == nil || tree == nil {
		return
	}
	byState := map[string]int{}
	for _, d := range tree.Dirs {
		byState["working"] += d.WorkingN
		byState["idle"] += d.IdleN
		byState["dormant"] += d.DormantN
	}
	e.RecordSessionsCount(byState, map[string]string{"plan_tier": planTier})
}

// Run is a thin compat wrapper preserving the original signature used by
// lifecycle_test.go and any caller that doesn't need RunOptions yet.
func Run(ctx context.Context, p Paths) error {
	return RunWith(ctx, RunOptions{Paths: p})
}
