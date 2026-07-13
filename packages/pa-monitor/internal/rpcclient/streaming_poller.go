package rpcclient

import (
	"context"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// StreamingPoller satisfies tui.Poller/tui.MetaPoller by subscribing to the
// daemon's server-streaming WatchState RPC. A background goroutine holds one
// long-lived stream and stores each pushed DaemonState; Snapshot is a
// non-blocking read of the latest push (no per-call RPC). This replaces the
// unary-GetState-per-tick RemotePoller.
type StreamingPoller struct {
	mu                    sync.Mutex
	lastTree              *aggregate.Tree
	lastFreshAt           time.Time
	connected             bool
	lastAutoResumeEnabled bool
	lastAutoResumeDelay   time.Duration
	lastCaffeinateActive  bool
	lastCaffeinateMode    bool
	lastCaffeinateProcess pb.CaffeinateProcess
	lastCaffeinateGraceS  uint32
	lastDaemonVersion     string
	lastDaemonNow         time.Time

	socket       string        // "" => XDG-resolved daemon socket
	pushInterval time.Duration // WatchState push cadence requested of the daemon

	// streaming machinery (set by start()/tests before the loop runs).
	watch          watchFunc          // opens a WatchState subscription; injectable for tests
	watchdogBudget time.Duration      // 0 => derive 2x the effective push interval
	reconnectPause time.Duration      // 0 => streamReconnectPause
	initialBackoff time.Duration      // 0 => streamInitialBackoff
	cancel         context.CancelFunc // cancels the receiver goroutine
	done           chan struct{}      // closed when the receiver goroutine exits
}

// watchStream is the minimal view of the WatchState client stream the poller
// consumes. grpc.ServerStreamingClient[DaemonState] satisfies it.
type watchStream interface {
	Recv() (*pb.DaemonState, error)
}

// watchFunc opens a WatchState subscription, returning the stream and a closer
// that tears down the underlying connection. Injectable so tests can drive the
// receiver loop without a live daemon.
type watchFunc func(ctx context.Context, pushIntervalMs uint32) (watchStream, func(), error)

const (
	// streamPushFloor mirrors the daemon's minPushInterval (server.go): WatchState
	// clamps requests below this up to it, so the effective cadence is never faster.
	streamPushFloor = 250 * time.Millisecond
	// streamMaxBackoff caps the redial backoff. Mirrors the old RemotePoller
	// maxBackoff: a local socket recovers fast, so a long backoff only strands
	// stale state well after the daemon is reachable again.
	streamMaxBackoff = 5 * time.Second
	// streamReconnectPause is the brief settle between a stream drop and redial.
	streamReconnectPause = 500 * time.Millisecond
	// streamInitialBackoff is the first backoff after a failed dial.
	streamInitialBackoff = 1 * time.Second
)

// newStreamingPoller builds an unstarted poller. Public constructors start the
// background receiver; tests use this directly to exercise the seam offline.
func newStreamingPoller(socket string, pushInterval time.Duration) *StreamingPoller {
	return &StreamingPoller{socket: socket, pushInterval: pushInterval}
}

// NewStreamingPoller constructs a poller subscribed to the daemon's WatchState
// stream at the given push cadence and starts its background receiver. Close it
// when done to stop the goroutine.
func NewStreamingPoller(pushInterval time.Duration) (*StreamingPoller, error) {
	p := newStreamingPoller("", pushInterval)
	p.start()
	return p, nil
}

// NewStreamingPollerForSocket pins the poller to an explicit socket path,
// bypassing XDG-based resolution. Tests use this.
func NewStreamingPollerForSocket(socket string, pushInterval time.Duration) *StreamingPoller {
	p := newStreamingPoller(socket, pushInterval)
	p.start()
	return p
}

// start launches the background receiver goroutine. Call once.
func (p *StreamingPoller) start() {
	if p.watch == nil {
		p.watch = p.dialWatch
	}
	if p.reconnectPause == 0 {
		p.reconnectPause = streamReconnectPause
	}
	if p.initialBackoff == 0 {
		p.initialBackoff = streamInitialBackoff
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go p.run(ctx)
}

// run maintains a single long-lived WatchState subscription: (re)dial, consume
// pushes until the stream drops or the watchdog trips, then redial with backoff.
func (p *StreamingPoller) run(ctx context.Context) {
	defer close(p.done)
	backoff := p.initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		stream, closer, err := p.watch(ctx, p.requestedPushMs())
		if err != nil {
			p.setDisconnected()
			if sleepCtx(ctx, backoff) {
				return
			}
			backoff = capBackoff(backoff * 2)
			continue
		}
		backoff = p.initialBackoff
		p.consume(ctx, stream)
		closer()
		p.setDisconnected()
		if ctx.Err() != nil {
			return
		}
		if sleepCtx(ctx, p.reconnectPause) {
			return
		}
	}
}

// consume applies each pushed state until the stream errors, the context is
// cancelled, or no push arrives within the watchdog budget. Recv runs on its
// own goroutine (like wait.go) so the watchdog can't be starved by a hung Recv.
func (p *StreamingPoller) consume(ctx context.Context, stream watchStream) {
	type recvResult struct {
		st  *pb.DaemonState
		err error
	}
	recvCh := make(chan recvResult, 1)
	next := func() {
		go func() {
			st, err := stream.Recv()
			recvCh <- recvResult{st, err}
		}()
	}
	next()
	budget := p.watchdog()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(budget):
			return // push missed -> treat as hung; caller redials
		case r := <-recvCh:
			if r.err != nil {
				return
			}
			next()
			if r.st != nil {
				p.apply(r.st)
			}
		}
	}
}

// Close stops the background receiver and waits for it to exit. Idempotent.
func (p *StreamingPoller) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		<-p.done
	}
	return nil
}

// dialWatch is the production watchFunc: dial the daemon socket and open a
// WatchState stream.
func (p *StreamingPoller) dialWatch(ctx context.Context, pushMs uint32) (watchStream, func(), error) {
	var c *Client
	var err error
	if p.socket != "" {
		c, err = DialPath(ctx, p.socket)
	} else {
		c, err = Dial(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	stream, err := c.C.WatchState(ctx, &pb.WatchStateRequest{PushIntervalMs: pushMs})
	if err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	return stream, func() { _ = c.Close() }, nil
}

// requestedPushMs is the push cadence sent to the daemon (clamped server-side to
// the minPushInterval floor).
func (p *StreamingPoller) requestedPushMs() uint32 {
	return uint32(p.pushInterval / time.Millisecond)
}

// watchdog is the no-push budget before the stream is treated as hung: twice the
// effective push interval (mirrors wait.go's 2x pushBudget), or an injected value.
func (p *StreamingPoller) watchdog() time.Duration {
	if p.watchdogBudget > 0 {
		return p.watchdogBudget
	}
	eff := p.pushInterval
	if eff < streamPushFloor {
		eff = streamPushFloor
	}
	return 2 * eff
}

// sleepCtx sleeps for d, returning true if the context was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() != nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

// capBackoff clamps the redial backoff to streamMaxBackoff.
func capBackoff(d time.Duration) time.Duration {
	if d > streamMaxBackoff {
		return streamMaxBackoff
	}
	return d
}

// apply translates one pushed DaemonState onto the cached view-state under a
// single lock acquisition, marking the poller connected. Field mapping mirrors
// the old RemotePoller.Snapshot post-GetState assignment.
func (p *StreamingPoller) apply(st *pb.DaemonState) {
	tree := pb.ToTree(st)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastTree = tree
	p.lastFreshAt = time.Now()
	p.connected = true
	p.lastAutoResumeEnabled = st.GetAutoResumeEnabled()
	p.lastAutoResumeDelay = time.Duration(st.GetAutoResumeDelayS()) * time.Second
	p.lastCaffeinateActive = st.GetCaffeinateActive()
	p.lastCaffeinateMode = st.GetCaffeinateMode()
	p.lastCaffeinateProcess = st.GetCaffeinateProcess()
	p.lastCaffeinateGraceS = st.GetCaffeinateGraceRemainingS()
	p.lastDaemonVersion = st.GetDaemonVersion()
	if ts := st.GetNow(); ts != nil {
		p.lastDaemonNow = ts.AsTime()
	}
}

// setDisconnected marks the stream down. Snapshot then returns ErrOffline
// while still serving the last-known tree, until the next apply reconnects.
func (p *StreamingPoller) setDisconnected() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connected = false
}

// Snapshot returns the last-known tree without performing an RPC. While the
// stream is disconnected it returns ErrOffline alongside the cached tree, so
// callers (update.go) treat "serving cached state" distinctly from a live push.
func (p *StreamingPoller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.connected {
		return p.lastTree, anyWorking(p.lastTree), ErrOffline
	}
	return p.lastTree, anyWorking(p.lastTree), nil
}

// IsOffline reports whether the stream is currently disconnected.
func (p *StreamingPoller) IsOffline() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.connected
}

// LastFreshAt returns when the last push was applied. Zero if never.
func (p *StreamingPoller) LastFreshAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastFreshAt
}

func (p *StreamingPoller) LastDaemonNow() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastDaemonNow
}

func (p *StreamingPoller) LastAutoResumeEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAutoResumeEnabled
}

func (p *StreamingPoller) LastAutoResumeDelay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAutoResumeDelay
}

func (p *StreamingPoller) LastCaffeinateActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastCaffeinateActive
}

func (p *StreamingPoller) LastCaffeinateMode() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastCaffeinateMode
}

func (p *StreamingPoller) LastCaffeinateProcess() pb.CaffeinateProcess {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastCaffeinateProcess
}

func (p *StreamingPoller) LastCaffeinateGraceRemaining() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Duration(p.lastCaffeinateGraceS) * time.Second
}

func (p *StreamingPoller) LastDaemonVersion() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastDaemonVersion
}
