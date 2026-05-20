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
	return nil, status.Error(codes.Unimplemented, "Caffeinate not yet wired; pending daemon-side manager")
}

func (s *server) Nudge(ctx context.Context, req *pb.NudgeRequest) (*pb.NudgeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "Nudge not yet wired; pending signal-layer integration")
}

func (s *server) GetSessionInfo(ctx context.Context, req *pb.GetSessionInfoRequest) (*pb.SessionDetail, error) {
	return nil, status.Error(codes.Unimplemented, "GetSessionInfo not yet wired")
}

func (s *server) GetPathInfo(ctx context.Context, req *pb.GetPathInfoRequest) (*pb.PathRollup, error) {
	return nil, status.Error(codes.Unimplemented, "GetPathInfo not yet wired")
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
	state.CaffeinateActive = s.state.isCaffeinateOn()
	return state
}

// serve runs the gRPC server on the given listener. Caller owns the
// returned stop func.
func serve(lis net.Listener, state *sharedState) (*grpc.Server, func()) {
	gs := grpc.NewServer()
	pb.RegisterPaMonitorServer(gs, newServer(state))

	go func() {
		_ = gs.Serve(lis)
	}()

	return gs, func() { gs.GracefulStop() }
}

// avoid "imported and not used" until other consumers land
var _ = aggregate.Tree{}
