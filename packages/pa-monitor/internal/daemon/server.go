package daemon

import (
	"context"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

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
	// bridges tracks cmux-bridge registrations so RegisterBridge handlers
	// can update last-seen and the poller can refine "cmux" terminal-host
	// labels with bridge status.
	bridges *bridge.Registry
	// cmuxAncestor is consulted in RegisterBridge to walk the caller-supplied
	// bridge PID's ancestry to its cmux server PID, which is the registry
	// key.
	cmuxAncestor cmuxAncestryFn
}

// cmuxAncestryFn is the minimal slice of CmuxSignaler used by the
// RegisterBridge handler: walk an arbitrary PID's ancestry to find its
// cmux server PID. Function-shaped so tests can inject a fake without
// constructing a CmuxSignaler.
type cmuxAncestryFn func(pid int) (int, bool)

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
		interval = 2 * time.Second
	case interval < 50*time.Millisecond:
		interval = 50 * time.Millisecond
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
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
	return &pb.CaffeinateResponse{Active: target, Cause: cause}, nil
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

// RegisterBridge records or refreshes a cmux-bridge entry. The bridge
// provides its workspace_id (for display/logging) and its own PID; the
// daemon walks bridge_pid's ancestry to find the cmux server PID, which is
// the actual registry key consulted by the poller's TerminalHost refinement.
//
// If the bridge PID has no cmux server ancestor (e.g. the bridge is running
// outside cmux somehow, or the cmuxAncestor walker is unconfigured), the
// call is a no-op success — the caller doesn't need to know how the daemon
// resolves cmux membership.
func (s *server) RegisterBridge(ctx context.Context, req *pb.RegisterBridgeRequest) (*pb.RegisterBridgeResponse, error) {
	if s.bridges == nil || s.cmuxAncestor == nil {
		return &pb.RegisterBridgeResponse{}, nil
	}
	bridgePID := int(req.GetBridgePid())
	if bridgePID < 1 {
		return nil, status.Error(codes.InvalidArgument, "bridge_pid must be > 0")
	}
	serverPID, ok := s.cmuxAncestor(bridgePID)
	if !ok {
		// No cmux server ancestor — caller isn't actually inside cmux. Silent
		// success; the bridge's TerminalHost won't be refined but no harm done.
		return &pb.RegisterBridgeResponse{}, nil
	}
	s.bridges.Register(serverPID)
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

// sessionViewToWire / dirToWire — bridges into the proto translator
// without exposing internal-only functions outside the daemon package.
// (The proto package itself owns the public conversion.)
func sessionViewToWire(sv *aggregate.SessionView) *pb.SessionView {
	d := pb.FromTree(&aggregate.Tree{Dirs: []*aggregate.Directory{
		{Sessions: []*aggregate.SessionView{sv}},
	}})
	if len(d.GetDirs()) > 0 && len(d.GetDirs()[0].GetSessions()) > 0 {
		return d.GetDirs()[0].GetSessions()[0]
	}
	return nil
}

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
	state := pb.FromTree(s.state.snapshot())
	state.Now = timestamppb.Now()
	state.DaemonUptimeSeconds = uint64(time.Since(s.started).Seconds())
	state.DaemonVersion = s.version
	state.PlanTier = s.planTier
	active, _ := s.state.caffeinateView()
	state.CaffeinateActive = active
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
// bridges + cmuxAncestor are both optional; when nil the RegisterBridge
// handler becomes a no-op success and poller-side cmux refinement falls
// back to a bare "cmux" label.
func serve(lis net.Listener, state *sharedState, version, planTier, autoResumeMessage string, writeService *service.WriteService, bridges *bridge.Registry, cmuxAncestor cmuxAncestryFn) (*grpc.Server, func()) {
	gs := grpc.NewServer()
	srv := newServer(state)
	srv.version = version
	srv.planTier = planTier
	srv.autoResumeMessage = autoResumeMessage
	srv.writeService = writeService
	srv.bridges = bridges
	srv.cmuxAncestor = cmuxAncestor
	pb.RegisterPaMonitorServer(gs, srv)

	go func() {
		_ = gs.Serve(lis)
	}()

	return gs, func() { gs.GracefulStop() }
}
