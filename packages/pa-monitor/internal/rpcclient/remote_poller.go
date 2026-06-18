package rpcclient

import (
	"context"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// RemotePoller satisfies the tui.Poller interface by talking to the
// daemon over gRPC. Reconnects automatically with backoff; serves the
// last-known Tree (with an Offline flag) when the daemon is unreachable.
type RemotePoller struct {
	mu           sync.Mutex
	client       *Client
	socket       string // resolved at construction; reused on reconnect
	lastTree     *aggregate.Tree
	lastFreshAt  time.Time
	backoff      time.Duration
	backoffUntil time.Time

	// lastMeta fields capture view-state from DaemonState that doesn't live
	// on the aggregate.Tree. Populated on every successful Snapshot; exposed
	// via Last* accessors so the TUI can read alongside the tree.
	lastAutoResumeEnabled bool
	lastAutoResumeDelay   time.Duration
	lastCaffeinateActive  bool
	// Two-indicator caffeinate state (D6): MODE (the user toggle) and the
	// PROCESS state (off/on/grace/error) + grace-remaining seconds.
	lastCaffeinateMode    bool
	lastCaffeinateProcess pb.CaffeinateProcess
	lastCaffeinateGraceS  uint32
	lastDaemonVersion     string
	lastDaemonNow         time.Time
}

// NewRemotePoller constructs a poller. The first Snapshot call performs
// the initial dial; subsequent calls reuse the connection.
func NewRemotePoller() (*RemotePoller, error) {
	return &RemotePoller{
		backoff: 1 * time.Second,
	}, nil
}

// NewRemotePollerForSocket pins the poller to an explicit socket path,
// bypassing XDG-based resolution. Tests use this.
func NewRemotePollerForSocket(socket string) *RemotePoller {
	return &RemotePoller{
		backoff: 1 * time.Second,
		socket:  socket,
	}
}

// Snapshot calls GetState and translates the proto to *aggregate.Tree.
// On any RPC failure returns the last-known tree (or empty), the
// derived any-working bool, and the error.
func (r *RemotePoller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Honour backoff while in a failed-reconnect window.
	if time.Now().Before(r.backoffUntil) {
		return r.lastTree, anyWorking(r.lastTree), ErrOffline
	}

	if r.client == nil {
		var c *Client
		var err error
		if r.socket != "" {
			c, err = DialPath(ctx, r.socket)
		} else {
			c, err = Dial(ctx)
		}
		if err != nil {
			r.scheduleBackoff()
			return r.lastTree, anyWorking(r.lastTree), err
		}
		r.client = c
		r.socket = c.Socket
		r.backoff = 1 * time.Second
	}

	state, err := r.client.C.GetState(ctx, &pb.GetStateRequest{})
	if err != nil {
		// Likely connection-died; close + reset for next call.
		_ = r.client.Close()
		r.client = nil
		r.scheduleBackoff()
		return r.lastTree, anyWorking(r.lastTree), err
	}

	tree := pb.ToTree(state)
	r.lastTree = tree
	r.lastFreshAt = time.Now()
	r.lastAutoResumeEnabled = state.GetAutoResumeEnabled()
	r.lastAutoResumeDelay = time.Duration(state.GetAutoResumeDelayS()) * time.Second
	r.lastCaffeinateActive = state.GetCaffeinateActive()
	r.lastCaffeinateMode = state.GetCaffeinateMode()
	r.lastCaffeinateProcess = state.GetCaffeinateProcess()
	r.lastCaffeinateGraceS = state.GetCaffeinateGraceRemainingS()
	r.lastDaemonVersion = state.GetDaemonVersion()
	if ts := state.GetNow(); ts != nil {
		r.lastDaemonNow = ts.AsTime()
	}
	return tree, anyWorking(tree), nil
}

// LastDaemonNow returns the daemon-side wallclock observed on the most
// recent successful GetState. Zero value means "not yet known". Used by
// the TUI to discard stale snapshot data after a user-triggered RPC.
func (r *RemotePoller) LastDaemonNow() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastDaemonNow
}

// LastAutoResumeEnabled returns the auto-resume flag from the most recent
// successful GetState. Zero value (false) means "not yet known".
func (r *RemotePoller) LastAutoResumeEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAutoResumeEnabled
}

// LastAutoResumeDelay returns the auto-resume delay from the most recent
// successful GetState. Zero value means "not yet known".
func (r *RemotePoller) LastAutoResumeDelay() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAutoResumeDelay
}

// LastCaffeinateActive returns the caffeinate flag from the most recent
// successful GetState. Zero value (false) means "not yet known" or "off".
func (r *RemotePoller) LastCaffeinateActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastCaffeinateActive
}

// LastCaffeinateMode returns the auto-caffeinate MODE (the user toggle) from
// the most recent successful GetState.
func (r *RemotePoller) LastCaffeinateMode() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastCaffeinateMode
}

// LastCaffeinateProcess returns the caffeination PROCESS state
// (off/on/grace/error) from the most recent successful GetState.
func (r *RemotePoller) LastCaffeinateProcess() pb.CaffeinateProcess {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastCaffeinateProcess
}

// LastCaffeinateGraceRemaining returns the grace-countdown remaining from the
// most recent successful GetState. Zero unless the process is in grace.
func (r *RemotePoller) LastCaffeinateGraceRemaining() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Duration(r.lastCaffeinateGraceS) * time.Second
}

// LastDaemonVersion returns the daemon's reported version from the most
// recent successful GetState. Empty string means "not yet known".
func (r *RemotePoller) LastDaemonVersion() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastDaemonVersion
}

// IsOffline reports whether the most recent Snapshot returned an error.
func (r *RemotePoller) IsOffline() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client == nil
}

// LastFreshAt returns when Snapshot last succeeded. Zero if never.
func (r *RemotePoller) LastFreshAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastFreshAt
}

func (r *RemotePoller) scheduleBackoff() {
	r.backoffUntil = time.Now().Add(r.backoff)
	r.backoff *= 2
	if r.backoff > 30*time.Second {
		r.backoff = 30 * time.Second
	}
}

func anyWorking(t *aggregate.Tree) bool {
	if t == nil {
		return false
	}
	for _, d := range t.Dirs {
		if d.WorkingN > 0 {
			return true
		}
	}
	return false
}

// ErrOffline is returned when Snapshot is called inside the reconnect
// backoff window. Callers can distinguish "daemon-down, caching" from
// "real RPC error" using this sentinel.
type errOffline struct{}

func (errOffline) Error() string { return "pa-monitor daemon offline; serving cached state" }

// ErrOffline is the sentinel returned during backoff windows.
var ErrOffline error = errOffline{}
