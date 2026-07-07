package daemon

import (
	"errors"
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
	// stall must not block the delivery dispatcher (Deliver) nor the snapshot
	// ticker: the send hook is non-blocking and applies backpressure instead.
	bridgeSendBuffer = 16
)

// errBridgeSendQueueFull is returned by the send hook when a Deliver cannot be
// enqueued because the bounded outbound queue is full. Snapshots are dropped
// silently in this case rather than surfacing an error.
var errBridgeSendQueueFull = errors.New("bridge send queue full")

// errBridgeStreamClosed is returned by the send hook when the stream is tearing
// down and no further messages can be enqueued.
var errBridgeStreamClosed = errors.New("bridge stream closed")

// BridgeChannel is the daemon side of the cmux-bridge bidirectional stream.
//
// Concurrency model (single auxiliary goroutine):
//
//   - The MAIN goroutine owns the writer: it is the sole caller of
//     stream.Send. It selects over the bounded outbound queue (out), the
//     snapshot ticker, the reader's exit, and the stream context. This keeps
//     Send single-goroutine as gRPC requires.
//   - A READER goroutine loops on stream.Recv and dispatches inbound BridgeMsgs
//     (Register / Heartbeat / DeliverResult). Recv is interruptible by context
//     cancellation, so the reader always terminates on teardown — unlike Send,
//     which may block on a slow client until the handler returns and gRPC
//     cancels the context.
//   - The send hook (handed to Registry.AttachStream and used by the delivery
//     dispatcher) is non-blocking: it enqueues onto out, returning an error for
//     a full-queue Deliver and dropping a full-queue snapshot. It never sends
//     on a closed channel — out is never closed; a done signal only makes the
//     hook stop enqueuing.
//
// Teardown ordering: main breaks its select on ctx.Done() or the reader's exit,
// stops the ticker, closes done (so the hook stops enqueuing), joins the reader
// (guaranteed to exit because Recv is ctx-interruptible), then — if the stream
// had registered — deregisters the bridge and invokes onStreamClosed.
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
		return &pb.DaemonMsg{Kind: &pb.DaemonMsg_Snapshot{Snapshot: s.buildState()}}
	}

	// send enqueues a DaemonMsg for delivery. Non-blocking: snapshots are
	// dropped when the queue is full (a fresher one follows on the next tick);
	// deliveries return an error so the dispatcher can react. A closed done
	// makes both cases stop enqueuing.
	send := func(msg *pb.DaemonMsg) error {
		if _, isSnapshot := msg.GetKind().(*pb.DaemonMsg_Snapshot); isSnapshot {
			select {
			case out <- msg:
			case <-done:
			default:
				// Queue full — drop this snapshot.
			}
			return nil
		}
		select {
		case out <- msg:
			return nil
		case <-done:
			return errBridgeStreamClosed
		default:
			return errBridgeSendQueueFull
		}
	}

	// Reader goroutine: dispatch inbound messages. It records the stream's
	// registration so teardown can deregister it. That registration state is
	// read by the main goroutine only after joining this goroutine
	// (readerDone), so the channel close provides the happens-before edge.
	var (
		registered   bool
		regServerPID int
		regBridgePID int
	)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			switch k := msg.GetKind().(type) {
			case *pb.BridgeMsg_Register:
				r := k.Register
				serverPID := int(r.GetServerPid())
				if serverPID < 1 {
					// A Register without a resolvable cmux server PID cannot be
					// keyed; ignore it.
					continue
				}
				bridgePID := int(r.GetBridgePid())
				// AttachStream replaces any prior send hook for this
				// (serverPID, bridgePID) key, so a re-register on the same key
				// swaps the writer in place; distinct bridge PIDs on one server
				// PID coexist.
				s.bridges.AttachStream(serverPID, bridgePID, send)
				registered = true
				regServerPID = serverPID
				regBridgePID = bridgePID
				// Push an immediate snapshot so the bridge renders the roster
				// without waiting a full tick.
				_ = send(snapshotMsg())
			case *pb.BridgeMsg_Heartbeat:
				if regServerPID < 1 {
					// No registration yet — nothing to refresh.
					continue
				}
				// Refresh the bridge this stream actually registered
				// (regBridgePID), not whatever bridge PID the heartbeat
				// message claims, so stream identity stays consistent with
				// teardown's Deregister(regServerPID, regBridgePID).
				s.bridges.Heartbeat(regServerPID, regBridgePID, time.Now())
			case *pb.BridgeMsg_Result:
				if s.onDeliverResult != nil {
					res := k.Result
					s.onDeliverResult(res.GetId(), res.GetOk(), res.GetError())
				}
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
		case <-readerDone:
			break loop
		case <-ticker.C:
			// Enqueue a snapshot via the same non-blocking hook so its
			// drop-on-full policy applies uniformly; the queued copy is drained
			// by the out case below.
			_ = send(snapshotMsg())
		case msg := <-out:
			if err := stream.Send(msg); err != nil {
				sendErr = err
				break loop
			}
		}
	}

	// Teardown. Order: stop the ticker, close done (the hook stops enqueuing),
	// join the reader (Recv is ctx-interruptible, so it always exits), then
	// deregister and notify.
	ticker.Stop()
	close(done)
	<-readerDone

	if registered {
		s.bridges.Deregister(regServerPID, regBridgePID)
		if s.onStreamClosed != nil {
			s.onStreamClosed(regServerPID)
		}
	}
	return sendErr
}
