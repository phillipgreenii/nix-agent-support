// Package rpcclient is the shared gRPC-over-unix-socket client used by
// every CLI subcommand and by the TUI. Handles socket-path resolution,
// dial, and consistent error messaging on daemon-unavailable.
package rpcclient

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
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

// MaxRecvMsgSize is the client's gRPC receive cap (256 MiB; the default is 4 MiB).
const MaxRecvMsgSize = 256 << 20

// DefaultDialTimeout is the production deadline for the blocking connect
// handshake in DialPath.
const DefaultDialTimeout = 2 * time.Second

// DialTimeoutEnv names the environment variable that overrides the dial
// deadline with a Go duration (for example "5ms"). It exists so tests that
// exercise the daemon-unavailable path do not wait out the full default; an
// unset, unparsable or non-positive value keeps DefaultDialTimeout.
const DialTimeoutEnv = "PA_MONITOR_DIAL_TIMEOUT"

// DialTimeout returns the effective dial deadline.
func DialTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(DialTimeoutEnv)); err == nil && d > 0 {
		return d
	}
	return DefaultDialTimeout
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
	dialCtx, cancel := context.WithTimeout(ctx, DialTimeout())
	defer cancel()

	conn, err := grpc.NewClient(
		"unix:"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// The default 4 MiB receive cap made GetState fail once the daemon held
		// a few hundred sessions; the daemon is local, so allow a generous cap.
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxRecvMsgSize)),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socket, err)
	}
	// grpc.NewClient connects lazily. The prior grpc.DialContext used
	// grpc.WithBlock so a returned client always meant the daemon answered;
	// every caller relies on Dial failing fast when the socket is unreachable.
	// Reproduce that blocking handshake: kick off the connect and wait for
	// READY within dialCtx, tearing the conn down and erroring on timeout.
	conn.Connect()
	for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
		if !conn.WaitForStateChange(dialCtx, state) {
			_ = conn.Close()
			return nil, fmt.Errorf("dial %s: %w", socket, dialCtx.Err())
		}
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
