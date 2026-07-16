package daemon

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
)

// caffeinateProcessToProto maps the caffeinate.Manager's internal State onto
// the wire CaffeinateProcess enum. StateOff → OFF, StateArmedRunning → ON
// (holding the assertion), StateArmedCountdown → GRACE, StateError → ERROR.
func caffeinateProcessToProto(st caffeinate.State) pb.CaffeinateProcess {
	switch st {
	case caffeinate.StateArmedRunning:
		return pb.CaffeinateProcess_CAFFEINATE_PROCESS_ON
	case caffeinate.StateArmedCountdown:
		return pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE
	case caffeinate.StateError:
		return pb.CaffeinateProcess_CAFFEINATE_PROCESS_ERROR
	default:
		return pb.CaffeinateProcess_CAFFEINATE_PROCESS_OFF
	}
}

// caffeinateProcessLabel maps the caffeinate.Manager's State to the
// off/on/grace/error process label used as the OTel `state` attribute and the
// CLI process indicator.
func caffeinateProcessLabel(st caffeinate.State) string {
	switch st {
	case caffeinate.StateArmedRunning:
		return "on"
	case caffeinate.StateArmedCountdown:
		return "grace"
	case caffeinate.StateError:
		return "error"
	default:
		return "off"
	}
}

type server struct {
	pb.UnimplementedPaMonitorServer
	started time.Time
	state   *sharedState
	// version is the build identifier reported on DaemonState. Set by serve().
	version string
	// planTier is the configured plan tier (e.g. "max_5x"). Reported on
	// DaemonState.PlanTier so CLI clients can show it without re-parsing config.
	planTier string
	// autoResumeMessage is the configured default text used when NudgeQueue
	// is called without a body. Threaded in from opts.AutoResumeMessage so
	// the server doesn't have to reach into config; empty falls back to the
	// hardcoded "continue" sentinel inside the handler.
	autoResumeMessage string
	// writeService persists toggle changes (Caffeinate, SetAutoResume) to
	// the ToggleStore. Set by serve().
	writeService *service.WriteService
	// bridges tracks cmux-bridge registrations (self-reported via
	// BridgeChannel's Register message) so the poller can refine "cmux"
	// terminal-host labels with bridge status, and so the delivery path can
	// look up a live stream to push a Deliver over.
	bridges *bridge.Registry
	// onDeliverResult, when non-nil, is invoked by the BridgeChannel handler
	// when a cmux-bridge reports the outcome of a Deliver (DeliverResult). A
	// later task wires this to the delivery tracker; it defaults nil so the
	// handler is a no-op for results until then.
	onDeliverResult func(id string, ok bool, errStr, reason string, timedOut bool)
	// onStreamClosed, when non-nil, is invoked by the BridgeChannel handler
	// after a registered stream is deregistered on teardown, keyed by the
	// stream's cmux server PID. A later task wires this to fail any in-flight
	// deliveries for that stream; it defaults nil.
	onStreamClosed func(serverPID int)
	// bridgeSnapshotInterval overrides the per-stream roster push cadence used
	// by BridgeChannel. Zero selects the default (defaultBridgeSnapshotInterval);
	// tests set a small value to exercise the ticker quickly.
	bridgeSnapshotInterval time.Duration
	// shutdown is closed by serve()'s stop func to tell the long-lived
	// server-stream handlers (WatchState, BridgeChannel) to return. GracefulStop
	// waits for in-flight handlers but does NOT cancel their stream contexts, so
	// without this signal a graceful stop blocks until every streaming client
	// disconnects (bead pg2-fcjpr). A nil channel — the state newServer leaves it
	// in for unit tests that construct the server without serve() — is never
	// selected, so those handlers keep their prior ctx-only teardown behaviour.
	shutdown <-chan struct{}
}

const (
	// defaultPushInterval is the WatchState push cadence used when a client
	// requests push_interval_ms == 0 ("use server default").
	defaultPushInterval = 2 * time.Second
	// minPushInterval is the server-side floor for WatchState pushes. Positive
	// client requests below this are clamped up to it. Each push is a full DB
	// materialization (snapshot() -> ReadService.GetState), so the floor bounds
	// the worst-case snapshot rate a single misbehaving client can force
	// (<= 4 pushes/sec at 250ms, vs. 20/sec at the earlier 50ms floor). No real
	// client requests faster than 1s (see cmd/pa-monitor/wait.go); the TUI polls
	// via GetState, not WatchState.
	minPushInterval = 250 * time.Millisecond
	// gracefulStopTimeout bounds how long serve()'s stop func waits for in-flight
	// RPC handlers to drain after signalling shutdown, before forcing a hard
	// gs.Stop(). The shutdown signal makes the long-lived stream handlers return
	// at once, so GracefulStop normally completes far inside this budget; the
	// timeout is only a safety net for a handler wedged in stream.Send to a
	// stalled client. It MUST stay well under launchd's ExitTimeOut (default 20s)
	// so a graceful daemon restart never falls back to SIGKILL (bead pg2-fcjpr).
	gracefulStopTimeout = 5 * time.Second
)

func newServer(s *sharedState) *server {
	return &server{started: time.Now(), state: s}
}

func (s *server) Ping(ctx context.Context, _ *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{Ts: timestamppb.Now()}, nil
}

func (s *server) GetState(ctx context.Context, _ *pb.GetStateRequest) (*pb.DaemonState, error) {
	return s.buildState(), nil
}

func (s *server) WatchState(req *pb.WatchStateRequest, stream pb.PaMonitor_WatchStateServer) error {
	if err := stream.Send(s.buildState()); err != nil {
		return err
	}

	interval := time.Duration(req.GetPushIntervalMs()) * time.Millisecond
	switch {
	case interval == 0:
		interval = defaultPushInterval
	case interval < minPushInterval:
		interval = minPushInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.shutdown:
			// Daemon is shutting down. End the stream cleanly (nil, a normal EOF
			// the client redials on) so GracefulStop is not blocked waiting on
			// this long-lived handler (bead pg2-fcjpr).
			return nil
		case <-ticker.C:
			// Push the current state on every tick so subscribers see
			// RPC-driven changes (Caffeinate, SetAutoResume) immediately.
			if err := stream.Send(s.buildState()); err != nil {
				return err
			}
		}
	}
}

func (s *server) IsAnyBusy(ctx context.Context, _ *pb.IsAnyBusyRequest) (*pb.IsAnyBusyResponse, error) {
	t := s.state.snapshot()
	if t == nil {
		return &pb.IsAnyBusyResponse{}, nil
	}
	var busy uint32
	for _, d := range t.Dirs {
		busy += uint32(d.WorkingN)
	}
	return &pb.IsAnyBusyResponse{AnyBusy: busy > 0, BusyCount: busy}, nil
}

func (s *server) Caffeinate(ctx context.Context, req *pb.CaffeinateRequest) (*pb.CaffeinateResponse, error) {
	current := s.state.isCaffeinateOn()
	var target bool
	switch req.GetAction() {
	case "on":
		target = true
	case "off":
		target = false
	case "toggle":
		target = !current
	default:
		return nil, status.Errorf(codes.InvalidArgument, "Caffeinate: action must be on|off|toggle, got %q", req.GetAction())
	}
	s.state.setCaffeinateOn(target)
	// Synchronously update the operational gauge (caffeinateActive) so the
	// RPC response and any subsequent buildState read reflect the intent
	// immediately. Without this the response and any pollResultMsg that
	// arrives before the next tick still report the OLD operational state
	// (caffeinateActive is otherwise only refreshed by the tick loop), which
	// caused the TUI toggle to flap on press. The tick loop will reconcile
	// against the actual caffeinate.Manager state on its next pass; if the
	// manager fails to spawn the proc, that reconciliation flips the gauge
	// back -- but the common path is consistent.
	cause := ""
	if target {
		cause = "manual"
	}
	s.state.setCaffeinateActive(target, cause)
	// Persist to ToggleStore (primary persistence since migration to SQLite).
	if s.writeService != nil {
		_ = s.writeService.SetToggle(ctx, "caffeinate_on", target)
	}
	// Build the two-indicator response. The manager hasn't re-ticked yet, so
	// the PROCESS state reflects the last tick's value; the MODE reflects the
	// just-committed toggle. `until` (grace expiry) is set only while the
	// process is in its grace countdown — previously this field was defined
	// but never populated.
	_, _, process, graceRemaining, _ := s.state.caffeinateIndicators()
	procEnum := caffeinateProcessToProto(process)
	resp := &pb.CaffeinateResponse{
		Active:  target,
		Cause:   cause,
		Mode:    target,
		Process: procEnum,
	}
	if procEnum == pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE && graceRemaining > 0 {
		resp.GraceRemainingS = uint32(graceRemaining.Seconds())
		resp.Until = timestamppb.New(time.Now().Add(graceRemaining))
	}
	return resp, nil
}

func (s *server) GetSessionInfo(ctx context.Context, req *pb.GetSessionInfoRequest) (*pb.SessionDetail, error) {
	t := s.state.snapshot()
	if t == nil {
		return nil, status.Error(codes.NotFound, "no session data available")
	}
	sel := req.GetSelector()
	if sel == nil {
		return nil, status.Error(codes.InvalidArgument, "GetSessionInfo: selector required")
	}
	for _, sv := range t.Sessions() {
		if matchesSelector(sv, sel) {
			out := pb.SessionDetailFromView(sv)
			if out == nil {
				out = &pb.SessionDetail{}
			}
			if sv.Env != nil {
				for k, v := range sv.Env {
					if v != "" {
						out.LabelPairs = append(out.LabelPairs, k+"="+v)
					}
				}
			}
			return out, nil
		}
	}
	return nil, status.Error(codes.NotFound, "session not found")
}

func (s *server) GetPathInfo(ctx context.Context, req *pb.GetPathInfoRequest) (*pb.PathRollup, error) {
	t := s.state.snapshot()
	if t == nil {
		return nil, status.Error(codes.NotFound, "no path data available")
	}
	target := req.GetPath()
	for _, d := range t.Dirs {
		if d.Path == target {
			return &pb.PathRollup{Directory: dirToWire(d)}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "path not found")
}

// expandStringSelector resolves a plain-text selector string to a slice of
// session IDs present in the current tree snapshot. The selector format is:
//
//	session:<sid>   — exact session ID
//	path:<dir>      — sessions whose cwd equals dir
//	cmux:<id>       — sessions whose CMUX_WORKSPACE_ID equals id
//	<bare-value>    — treated as a session ID
//
// Returns an error only when the tree is completely unavailable. An empty
// result (no matching sessions) is not an error.
func (s *server) expandStringSelector(sel string) ([]string, error) {
	t := s.state.snapshot()
	if t == nil {
		return nil, status.Error(codes.FailedPrecondition, "no session data available")
	}
	var sids []string
	for _, sv := range t.Sessions() {
		if sv == nil || sv.Session == nil {
			continue
		}
		var match bool
		switch {
		case strings.HasPrefix(sel, "session:"):
			match = sv.SessionID == strings.TrimPrefix(sel, "session:")
		case strings.HasPrefix(sel, "path:"):
			match = sv.Cwd == strings.TrimPrefix(sel, "path:")
		case strings.HasPrefix(sel, "cmux:"):
			match = sv.Env != nil && sv.Env["CMUX_WORKSPACE_ID"] == strings.TrimPrefix(sel, "cmux:")
		default:
			match = sv.SessionID == sel
		}
		if match {
			sids = append(sids, sv.SessionID)
		}
	}
	return sids, nil
}

func (s *server) NudgeQueue(ctx context.Context, req *pb.NudgeQueueRequest) (*pb.NudgeQueueResponse, error) {
	sel := req.GetSelector()
	if sel == "" {
		return nil, status.Error(codes.InvalidArgument, "NudgeQueue: selector required")
	}
	n := s.state.Nudger()
	if n == nil {
		return nil, status.Error(codes.FailedPrecondition, "NudgeQueue: nudger not configured")
	}
	sids, err := s.expandStringSelector(sel)
	if err != nil {
		return nil, err
	}
	text := req.GetText()
	if text == "" {
		text = s.autoResumeMessage
	}
	if text == "" {
		// Final fallback when the configured default is empty (e.g. running
		// outside RunWith). Keeps NudgeQueue useful in tests that wire the
		// server directly.
		text = "continue"
	}
	now := time.Now()
	var queued, already []string
	for _, sid := range sids {
		if n.PendingForSource(sid, nudger.SourceManual) {
			already = append(already, sid)
			continue
		}
		n.QueueManual([]string{sid}, text, now)
		queued = append(queued, sid)
	}
	return &pb.NudgeQueueResponse{
		QueuedSessionIds:        queued,
		AlreadyQueuedSessionIds: already,
	}, nil
}

func (s *server) NudgeCancel(ctx context.Context, req *pb.NudgeCancelRequest) (*pb.NudgeCancelResponse, error) {
	sel := req.GetSelector()
	if sel == "" {
		return nil, status.Error(codes.InvalidArgument, "NudgeCancel: selector required")
	}
	n := s.state.Nudger()
	if n == nil {
		return nil, status.Error(codes.FailedPrecondition, "NudgeCancel: nudger not configured")
	}
	sids, err := s.expandStringSelector(sel)
	if err != nil {
		return nil, err
	}
	n.CancelManual(sids)
	return &pb.NudgeCancelResponse{CancelledSessionIds: sids}, nil
}

// RegisterBridge is a no-op shim. Bridges self-report their cmux server PID
// via the Register message on the BridgeChannel stream (see
// bridge_channel.go), so the daemon no longer needs to resolve cmux ancestry
// or record a display-only registry entry here. The RPC is kept (rather than
// removed via a proto-codegen pass) purely for wire back-compat with any
// caller still invoking it; it always succeeds trivially.
func (s *server) RegisterBridge(ctx context.Context, req *pb.RegisterBridgeRequest) (*pb.RegisterBridgeResponse, error) {
	return &pb.RegisterBridgeResponse{}, nil
}

func (s *server) SetAutoResume(ctx context.Context, req *pb.SetAutoResumeRequest) (*pb.SetAutoResumeResponse, error) {
	w := s.state.Watermarks()
	if w == nil {
		return nil, status.Error(codes.FailedPrecondition, "SetAutoResume: nudger not configured")
	}
	w.SetAutoResumeEnabled(req.GetEnabled())
	// Persist to ToggleStore.
	if s.writeService != nil {
		_ = s.writeService.SetToggle(ctx, "auto_resume_enabled", req.GetEnabled())
	}
	return &pb.SetAutoResumeResponse{Enabled: req.GetEnabled()}, nil
}

// matchesSelector reports whether sv satisfies sel.
func matchesSelector(sv *aggregate.SessionView, sel *pb.Selector) bool {
	if sv == nil || sv.Session == nil || sel == nil {
		return false
	}
	switch t := sel.GetTarget().(type) {
	case *pb.Selector_SessionId:
		return sv.SessionID == t.SessionId
	case *pb.Selector_Path:
		// Matches when the session's cwd equals or is under the target path.
		return sv.Cwd == t.Path
	case *pb.Selector_CmuxWorkspaceId:
		if sv.Env == nil {
			return false
		}
		return sv.Env["CMUX_WORKSPACE_ID"] == t.CmuxWorkspaceId
	}
	return false
}

// dirToWire bridges into the proto translator without exposing internal-only
// functions outside the daemon package. (The proto package itself owns the
// public conversion.)
func dirToWire(d *aggregate.Directory) *pb.Directory {
	t := pb.FromTree(&aggregate.Tree{Dirs: []*aggregate.Directory{d}})
	if len(t.GetDirs()) > 0 {
		return t.GetDirs()[0]
	}
	return nil
}

// buildState constructs the wire DaemonState from the shared tree plus
// daemon-level fields.
func (s *server) buildState() *pb.DaemonState {
	// Serve the tick-refreshed cache so this (a gRPC handler goroutine, e.g. the
	// BridgeChannel writer) never does a synchronous SQLite read. Fall back to a
	// live snapshot only on cold start, before the first tick refresh populates
	// the cache. See sharedState.refreshSnapshot.
	tree := s.state.cachedSnapshot()
	if tree == nil {
		tree = s.state.snapshot()
	}
	state := pb.FromTree(tree)
	state.Now = timestamppb.Now()
	state.DaemonUptimeSeconds = uint64(time.Since(s.started).Seconds())
	state.DaemonVersion = s.version
	state.PlanTier = s.planTier
	mode, active, process, graceRemaining, cause := s.state.caffeinateIndicators()
	state.CaffeinateActive = active
	state.CaffeinateMode = mode
	state.CaffeinateProcess = caffeinateProcessToProto(process)
	state.CaffeinateGraceRemainingS = uint32(graceRemaining.Seconds())
	state.CaffeinateCause = cause
	s.state.mu.RLock()
	delay := s.state.autoResumeDelay
	wm := s.state.watermarks
	s.state.mu.RUnlock()
	if wm != nil {
		state.AutoResumeEnabled = wm.AutoResumeEnabled()
	}
	if delay > 0 {
		state.AutoResumeDelayS = uint32(delay.Seconds())
	}
	return state
}

// serve runs the gRPC server on the given listener. Caller owns the
// returned stop func.
//
// bridges is optional; when nil, BridgeChannel rejects with
// FailedPrecondition and poller-side cmux refinement falls back to a bare
// "cmux" label. onDeliverResult/onStreamClosed are the BridgeChannel
// handler's inbound hooks into the delivery tracker (see delivery.go); both
// are nil-safe (BridgeChannel checks before calling), so callers that don't
// wire the delivery path may pass nil for either.
func serve(lis net.Listener, state *sharedState, version, planTier, autoResumeMessage string, writeService *service.WriteService, bridges *bridge.Registry, snapshotInterval time.Duration, onDeliverResult func(id string, ok bool, errStr, reason string, timedOut bool), onStreamClosed func(serverPID int)) (*grpc.Server, func()) {
	gs := grpc.NewServer()
	srv := newServer(state)
	srv.version = version
	srv.planTier = planTier
	srv.autoResumeMessage = autoResumeMessage
	srv.writeService = writeService
	srv.bridges = bridges
	srv.bridgeSnapshotInterval = snapshotInterval
	srv.onDeliverResult = onDeliverResult
	srv.onStreamClosed = onStreamClosed
	// shutdown tells the long-lived stream handlers (WatchState, BridgeChannel)
	// to return when stop() runs, so GracefulStop does not block on a live
	// client (bead pg2-fcjpr).
	shutdown := make(chan struct{})
	srv.shutdown = shutdown
	pb.RegisterPaMonitorServer(gs, srv)

	go func() {
		_ = gs.Serve(lis)
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			// Signal the stream handlers to return, then wait for in-flight
			// handlers to drain. The signal normally makes GracefulStop complete
			// promptly; gracefulStopTimeout then forces a hard gs.Stop() (which
			// cancels handler contexts and closes transports) as a bounded safety
			// net, so a graceful daemon restart never depends on launchd's
			// SIGKILL fallback (bead pg2-fcjpr).
			close(shutdown)
			done := make(chan struct{})
			go func() {
				gs.GracefulStop()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(gracefulStopTimeout):
				gs.Stop()
				<-done
			}
		})
	}
	return gs, stop
}
