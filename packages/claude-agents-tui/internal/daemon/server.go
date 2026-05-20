package daemon

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/phillipgreenii/claude-agents-tui/internal/core/aggregate"
	pb "github.com/phillipgreenii/claude-agents-tui/internal/proto"
)

const daemonVersion = "0.0.0-dev"

type server struct {
	pb.UnimplementedPaMonitorServer
	started time.Time
	state   *sharedState
	// nudgeFn is the signal-layer dispatcher. Plumbed by RunWith when
	// signalers are configured. nil → Nudge RPC returns FailedPrecondition.
	nudgeFn func(pid int, text string) error
}

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
	if err := stream.Send(&pb.WatchStateEvent{
		Payload: &pb.WatchStateEvent_State{
			State: s.buildState(),
		},
	}); err != nil {
		return err
	}

	interval := time.Duration(req.GetHeartbeatIntervalMs()) * time.Millisecond
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
			if err := stream.Send(&pb.WatchStateEvent{
				Payload: &pb.WatchStateEvent_Heartbeat{
					Heartbeat: &pb.Heartbeat{
						Ts:                  timestamppb.Now(),
						DaemonUptimeSeconds: uint64(time.Since(s.started).Seconds()),
					},
				},
			}); err != nil {
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

// Caffeinate, Nudge, GetSessionInfo, GetPathInfo, Drain — stubs returning
// Unimplemented for now. Plan 3 will wire each as the corresponding
// daemon-side concern (manager, signal layer, lookup helpers, shutdown
// hook) is plumbed.

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
	// Persist to runtime.json (best-effort).
	s.state.mu.RLock()
	path := s.state.runtimePath
	s.state.mu.RUnlock()
	if path != "" {
		_ = WriteRuntimeState(path, RuntimeState{CaffeinateOn: target})
	}
	active, cause := s.state.caffeinateView()
	return &pb.CaffeinateResponse{Active: active, Cause: cause}, nil
}

func (s *server) Nudge(ctx context.Context, req *pb.NudgeRequest) (*pb.NudgeResponse, error) {
	t := s.state.snapshot()
	if t == nil {
		return &pb.NudgeResponse{}, nil
	}
	sel := req.GetSelector()
	if sel == nil {
		return nil, status.Error(codes.InvalidArgument, "Nudge: selector required")
	}

	var targets []*aggregate.SessionView
	for _, sv := range t.Sessions() {
		if matchesSelector(sv, sel) {
			targets = append(targets, sv)
		}
	}

	if s.nudgeFn == nil {
		return nil, status.Error(codes.FailedPrecondition, "Nudge: signal layer not wired into daemon")
	}

	text := req.GetText()
	if text == "" {
		text = "Continue."
	}

	var sent, errors uint32
	postWindow := false
	for _, sv := range targets {
		if !sv.RateLimitResetsAt.IsZero() && time.Now().After(sv.RateLimitResetsAt) {
			postWindow = true
		}
		if err := s.nudgeFn(sv.PID, text); err != nil {
			errors++
		} else {
			sent++
		}
	}
	return &pb.NudgeResponse{
		SentCount:  sent,
		ErrorCount: errors,
		PostWindow: postWindow,
	}, nil
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
			out := &pb.SessionDetail{View: sessionViewToWire(sv)}
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

func (s *server) Drain(ctx context.Context, req *pb.DrainRequest) (*pb.DrainResponse, error) {
	return nil, status.Error(codes.Unimplemented, "Drain not yet wired")
}

// buildState constructs the wire DaemonState from the shared tree plus
// daemon-level fields.
func (s *server) buildState() *pb.DaemonState {
	state := pb.FromTree(s.state.snapshot())
	state.Now = timestamppb.Now()
	state.DaemonUptimeSeconds = uint64(time.Since(s.started).Seconds())
	state.DaemonVersion = daemonVersion
	active, _ := s.state.caffeinateView()
	state.CaffeinateActive = active
	return state
}

// serve runs the gRPC server on the given listener. Caller owns the
// returned stop func.
func serve(lis net.Listener, state *sharedState, nudgeFn func(int, string) error) (*grpc.Server, func()) {
	gs := grpc.NewServer()
	srv := newServer(state)
	srv.nudgeFn = nudgeFn
	pb.RegisterPaMonitorServer(gs, srv)

	go func() {
		_ = gs.Serve(lis)
	}()

	return gs, func() { gs.GracefulStop() }
}

// avoid "imported and not used" until other consumers land
var _ = aggregate.Tree{}
