// Package rpcclient is the shared gRPC-over-unix-socket client used by
// every CLI subcommand and by the TUI. Handles socket-path resolution,
// dial, and consistent error messaging on daemon-unavailable.
package rpcclient

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/phillipgreenii/pa-monitor/internal/daemon"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// Client wraps a gRPC connection.
type Client struct {
	conn   *grpc.ClientConn
	C      pb.PaMonitorClient
	Socket string
}

// Dial returns a connected client. The caller closes via Close.
func Dial(ctx context.Context) (*Client, error) {
	paths, err := daemon.ResolvePaths(daemon.PathOverrides{})
	if err != nil {
		return nil, fmt.Errorf("resolve socket: %w", err)
	}
	return DialPath(ctx, paths.Socket)
}

// DialPath is the explicit-socket form. Tests pass an arbitrary path.
func DialPath(ctx context.Context, socket string) (*Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		dialCtx, "unix:"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socket, err)
	}
	return &Client{
		conn:   conn,
		C:      pb.NewPaMonitorClient(conn),
		Socket: socket,
	}, nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// DaemonUnavailableMessage is the standard stderr line used by all
// one-shot CLI subcommands when the daemon socket can't be reached.
func DaemonUnavailableMessage(socket string) string {
	return fmt.Sprintf("pa-monitor daemon not running (socket: %s missing or unresponsive). "+
		"start it via launchctl or `pa-monitor daemon`.", socket)
}
