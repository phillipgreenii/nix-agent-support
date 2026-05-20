package daemon

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/phillipgreenii/claude-agents-tui/internal/proto"
)

type server struct {
	pb.UnimplementedPaMonitorServer
	started time.Time
}

func newServer() *server {
	return &server{started: time.Now()}
}

func (s *server) Ping(ctx context.Context, _ *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{Ts: timestamppb.Now()}, nil
}

func (s *server) GetState(ctx context.Context, _ *pb.GetStateRequest) (*pb.DaemonState, error) {
	return s.currentState(), nil
}

func (s *server) WatchState(req *pb.WatchStateRequest, stream pb.PaMonitor_WatchStateServer) error {
	if err := stream.Send(&pb.WatchStateEvent{
		Payload: &pb.WatchStateEvent_State{
			State: s.currentState(),
		},
	}); err != nil {
		return err
	}

	interval := time.Duration(req.GetHeartbeatIntervalMs()) * time.Millisecond
	if interval < 50*time.Millisecond {
		interval = 2 * time.Second // server default
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

func (s *server) currentState() *pb.DaemonState {
	return &pb.DaemonState{
		Now:                 timestamppb.Now(),
		DaemonUptimeSeconds: uint64(time.Since(s.started).Seconds()),
		DaemonVersion:       "0.0.0-dev",
	}
}

// serve runs the gRPC server on the given listener in a background
// goroutine. Returns the server (to allow stopping) and a stop func.
func serve(lis net.Listener) (*grpc.Server, func()) {
	gs := grpc.NewServer()
	pb.RegisterPaMonitorServer(gs, newServer())

	go func() {
		_ = gs.Serve(lis)
	}()

	return gs, func() { gs.GracefulStop() }
}
