package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// errDeliverTimeout is returned by bridgeDeliverer.Deliver when no
// DeliverResult arrives for a pending id before its timeout elapses.
var errDeliverTimeout = errors.New("delivery timed out waiting for bridge ack")

// deliverOutcome is the terminal result of a single tracked delivery,
// correlated by id via tracker.
type deliverOutcome struct {
	ok  bool
	err string
}

// pendingDelivery is what tracker keeps per in-flight id: which cmux server
// PID it was sent to (so failServer can target the right subset) and the
// channel its outcome is delivered on.
type pendingDelivery struct {
	serverPID int
	ch        chan deliverOutcome
}

// tracker correlates delivery command ids to result channels, tagged by the
// cmux server PID each id was sent to. It is the bridge between the
// BridgeChannel handler's inbound hooks (onDeliverResult / onStreamClosed,
// wired in a later task) and bridgeDeliverer's blocking Deliver call.
//
// Concurrency: every access to the pending map is serialized by mu. resolve,
// cancel, and failServer all delete-then-send (or delete-only), so at most
// one outcome is ever sent per id and a given pending channel is never sent
// to twice. Channels are buffered(1) and never closed, so a send race after
// Deliver has already returned (e.g. failServer racing a slow send) can
// never block or panic on a closed channel.
type tracker struct {
	mu      sync.Mutex
	pending map[string]pendingDelivery
	counter atomic.Uint64
}

// newTracker constructs an empty tracker.
func newTracker() *tracker {
	return &tracker{pending: map[string]pendingDelivery{}}
}

// nextID returns a unique, monotonically increasing delivery id. Ids are
// derived from an atomic counter rather than time-based randomness, so they
// stay unique under concurrent callers without needing an entropy source.
func (t *tracker) nextID() string {
	return "d-" + strconv.FormatUint(t.counter.Add(1), 10)
}

// add registers a pending id tagged with serverPID and returns a buffered
// (1) channel that will receive exactly one outcome — via resolve, cancel
// (which sends none), or failServer.
func (t *tracker) add(id string, serverPID int) <-chan deliverOutcome {
	ch := make(chan deliverOutcome, 1)
	t.mu.Lock()
	t.pending[id] = pendingDelivery{serverPID: serverPID, ch: ch}
	t.mu.Unlock()
	return ch
}

// resolve completes a pending id with the given outcome. A no-op if id is
// not (or no longer) pending — e.g. it already timed out, was cancelled, or
// failServer already cleared it — so a late or duplicate resolve can never
// double-send or send on a channel nobody holds anymore.
func (t *tracker) resolve(id string, ok bool, errStr string) {
	t.mu.Lock()
	p, found := t.pending[id]
	if found {
		delete(t.pending, id)
	}
	t.mu.Unlock()
	if !found {
		return
	}
	p.ch <- deliverOutcome{ok: ok, err: errStr}
}

// cancel removes a pending id without sending an outcome. Used by
// bridgeDeliverer's send-failed, timeout, and ctx-cancelled paths, where no
// reader remains listening on the channel (Deliver has already returned or
// is about to), so a later resolve/failServer racing in must be a safe
// no-op rather than leaking the map entry.
func (t *tracker) cancel(id string) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

// failServer fails and clears every pending id tagged with serverPID,
// without touching pending ids for any other server. Used when that
// server's bridge stream drops, so in-flight Deliver calls awaiting it
// don't have to wait out their full timeout.
func (t *tracker) failServer(serverPID int) {
	t.mu.Lock()
	var toFail []pendingDelivery
	for id, p := range t.pending {
		if p.serverPID == serverPID {
			toFail = append(toFail, p)
			delete(t.pending, id)
		}
	}
	t.mu.Unlock()
	for _, p := range toFail {
		p.ch <- deliverOutcome{ok: false, err: "bridge stream closed"}
	}
}

// bridgeDeliverer delivers nudge text to a target PID via that target's
// cmux server's live bridge stream: it resolves the target's cmux server
// ancestor, pushes a Deliver over that server's live bridge entry, and
// blocks until a correlated DeliverResult arrives (via tracker), ctx is
// done, or timeout elapses.
type bridgeDeliverer struct {
	reg      *bridge.Registry
	ancestor func(pid int) (serverPID int, ok bool)
	tr       *tracker
	timeout  time.Duration
}

var _ nudger.Deliverer = (*bridgeDeliverer)(nil)

func (d *bridgeDeliverer) Deliver(ctx context.Context, targetPID int, text string) error {
	serverPID, ok := d.ancestor(targetPID)
	if !ok {
		return nudger.ErrNoBridge
	}
	entry, ok := d.reg.LiveBridge(serverPID)
	if !ok {
		return nudger.ErrNoBridge
	}

	id := d.tr.nextID()
	ch := d.tr.add(id, serverPID)

	msg := &pb.DaemonMsg{
		Kind: &pb.DaemonMsg_Deliver{
			Deliver: &pb.Deliver{
				Id:        id,
				TargetPid: int32(targetPID),
				Text:      text,
			},
		},
	}

	if err := entry.Send(msg); err != nil {
		d.tr.cancel(id)
		return err
	}

	select {
	case o := <-ch:
		if o.ok {
			return nil
		}
		if o.err == "" {
			return errors.New("delivery failed")
		}
		return fmt.Errorf("delivery failed: %s", o.err)
	case <-ctx.Done():
		d.tr.cancel(id)
		return ctx.Err()
	case <-time.After(d.timeout):
		d.tr.cancel(id)
		return errDeliverTimeout
	}
}

// inDaemonDeliverer wraps the existing synchronous in-daemon signal path
// (tmux/ghostty/vscode, resolved via nudger.Signaler) behind the async
// Deliverer interface.
type inDaemonDeliverer struct {
	sig nudger.Signaler
}

var _ nudger.Deliverer = (*inDaemonDeliverer)(nil)

func (d *inDaemonDeliverer) Deliver(_ context.Context, targetPID int, text string) error {
	return d.sig.Send(targetPID, text)
}

// compositeDeliverer routes each Deliver call to the bridge path when the
// target PID resolves to a cmux server ancestor, and to the in-daemon path
// otherwise.
type compositeDeliverer struct {
	ancestor func(pid int) (serverPID int, ok bool)
	bridge   nudger.Deliverer
	inDaemon nudger.Deliverer
}

var _ nudger.Deliverer = (*compositeDeliverer)(nil)

func (d *compositeDeliverer) Deliver(ctx context.Context, targetPID int, text string) error {
	if _, ok := d.ancestor(targetPID); ok {
		return d.bridge.Deliver(ctx, targetPID, text)
	}
	return d.inDaemon.Deliver(ctx, targetPID, text)
}
