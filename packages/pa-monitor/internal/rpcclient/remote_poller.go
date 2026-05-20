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
}

// NewRemotePoller constructs a poller. The first Snapshot call performs
// the initial dial; subsequent calls reuse the connection.
func NewRemotePoller() (*RemotePoller, error) {
	return &RemotePoller{
		backoff: 1 * time.Second,
	}, nil
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
		c, err := Dial(ctx)
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
	return tree, anyWorking(tree), nil
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
