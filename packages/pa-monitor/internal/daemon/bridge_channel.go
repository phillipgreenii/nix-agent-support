package daemon

import (
	"fmt"
	"os"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// defaultBridgeSnapshotInterval is the per-stream roster push cadence used
	// when the server's bridgeSnapshotInterval is unset. It replaces the old
	// WatchState push cadence for cmux-bridge subscribers.
	defaultBridgeSnapshotInterval = 2 * time.Second
	// bridgeSendBuffer bounds the per-stream outbound queue. A short bridge
	// stall must not block the snapshot ticker: the send hook is non-blocking
	// and drops a snapshot when the queue is full.
	bridgeSendBuffer = 16
)

// BridgeChannel is the daemon side of the cmux-bridge bidirectional stream.
//
// Concurrency model (single auxiliary goroutine):
//
//   - The MAIN goroutine owns the writer: it is the sole caller of
//     stream.Send. It selects over the bounded outbound queue (out), the
//     snapshot ticker, the reader's exit, and the stream context. This keeps
//     Send single-goroutine as gRPC requires.
//   - A READER goroutine loops on stream.Recv and dispatches inbound BridgeMsgs
//     (Register / Heartbeat). Recv is interruptible by context
//     cancellation, so the reader always terminates on teardown — unlike Send,
//     which may block on a slow client until the handler returns and gRPC
//     cancels the context.
//   - The send hook is non-blocking: it enqueues a snapshot onto out and drops
//     it when the queue is full. It never sends on a closed channel — out is
//     never closed; a done signal only makes the hook stop enqueuing.
//
// Teardown ordering: main breaks its select on ctx.Done(), the server-shutdown
// signal (s.shutdown), the reader's exit, or a Send error; it stops the ticker,
// closes done (so the hook stops enqueuing), and returns. It does NOT join the
// reader — the reader owns the registration lifecycle and deregisters the
// bridge in its own defer, which runs when its
// ctx-interruptible Recv unblocks (on client disconnect, or when the handler's
// return cancels the stream context on a server-shutdown exit). Not joining is
// what lets the handler return promptly on shutdown (bead pg2-fcjpr): the
// reader is parked in Recv, which GracefulStop never cancels, so joining here
// would re-block the handler until a client disconnected.
func (s *server) BridgeChannel(stream pb.PaMonitor_BridgeChannelServer) error {
	if s.bridges == nil {
		return status.Error(codes.FailedPrecondition, "bridge registry not configured")
	}

	ctx := stream.Context()

	interval := s.bridgeSnapshotInterval
	if interval <= 0 {
		interval = defaultBridgeSnapshotInterval
	}

	// out is the bounded outbound queue drained by the main writer loop. It is
	// never closed; teardown is signalled by closing done instead, so the send
	// hook can never send on a closed channel.
	out := make(chan *pb.DaemonMsg, bridgeSendBuffer)
	done := make(chan struct{})

	snapshotMsg := func() *pb.DaemonMsg {
		return &pb.DaemonMsg{Kind: &pb.DaemonMsg_Snapshot{Snapshot: s.buildPushState(ctx)}}
	}

	// send enqueues a snapshot. Non-blocking: a snapshot is dropped when the
	// queue is full (a fresher one follows on the next tick). A closed done
	// stops enqueuing.
	send := func(msg *pb.DaemonMsg) {
		select {
		case out <- msg:
		case <-done:
		default:
			// Queue full — drop this snapshot.
		}
	}

	// Reader goroutine: dispatch inbound messages and own the stream's
	// registration lifecycle. The registration state is reader-local and the
	// bridge is deregistered in this goroutine's defer, so the main writer loop
	// does NOT join the reader on teardown. That lets the handler return promptly
	// on a server-shutdown signal (bead pg2-fcjpr); the reader — parked in the
	// ctx-interruptible Recv — then unwinds when the stream context is cancelled
	// (on client disconnect, or as the handler returns on shutdown) and its defer
	// deregisters and notifies.
	readerDone := make(chan struct{})
	go func() {
		var (
			registered   bool
			regWorkspace string
			regBridgePID int
		)
		defer func() {
			if registered {
				s.bridges.Deregister(regWorkspace, regBridgePID)
			}
			close(readerDone)
		}()
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			switch k := msg.GetKind().(type) {
			case *pb.BridgeMsg_Register:
				r := k.Register
				workspaceID := r.GetWorkspaceId()
				if workspaceID == "" {
					// A Register without a workspace id cannot be keyed; reject
					// it and keep the stream unregistered.
					fmt.Fprintf(os.Stderr, "daemon: bridge channel: rejecting Register from bridge pid %d: empty workspace_id\n", r.GetBridgePid())
					continue
				}
				bridgePID := int(r.GetBridgePid())
				// AttachStream refreshes an existing (workspaceID, bridgePID)
				// member in place; distinct bridge PIDs on one workspace id
				// coexist.
				s.bridges.AttachStream(workspaceID, int(r.GetServerPid()), bridgePID)
				registered = true
				regWorkspace = workspaceID
				regBridgePID = bridgePID
				// Push an immediate snapshot so the bridge renders the roster
				// without waiting a full tick.
				send(snapshotMsg())
			case *pb.BridgeMsg_Heartbeat:
				if regWorkspace == "" {
					// No registration yet — nothing to refresh.
					continue
				}
				// Refresh the bridge this stream actually registered
				// (regBridgePID), not whatever bridge PID the heartbeat
				// message claims, so stream identity stays consistent with
				// teardown's Deregister(regWorkspace, regBridgePID).
				s.bridges.Heartbeat(regWorkspace, regBridgePID, time.Now())
			}
		}
	}()

	ticker := time.NewTicker(interval)

	// Main writer loop: sole caller of stream.Send.
	var sendErr error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-s.shutdown:
			// Daemon is shutting down; break so the handler returns instead of
			// blocking GracefulStop until the (often always-connected) bridge
			// client disconnects (bead pg2-fcjpr). A nil s.shutdown — the state
			// newServer leaves it in for unit tests — is never selected.
			break loop
		case <-readerDone:
			break loop
		case <-ticker.C:
			// Enqueue a snapshot via the same non-blocking hook so its
			// drop-on-full policy applies uniformly; the queued copy is drained
			// by the out case below.
			send(snapshotMsg())
		case msg := <-out:
			if err := stream.Send(msg); err != nil {
				sendErr = err
				break loop
			}
		}
	}

	// Teardown: stop the ticker and signal the send hook to stop enqueuing, then
	// return. We do NOT join the reader — returning cancels the stream context,
	// which unblocks the reader's ctx-interruptible Recv, and the reader's defer
	// then deregisters the bridge (bead pg2-fcjpr).
	ticker.Stop()
	close(done)
	return sendErr
}
