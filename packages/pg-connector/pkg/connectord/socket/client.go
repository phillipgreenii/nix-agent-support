package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// DefaultConnectTimeout bounds one connection attempt.
const DefaultConnectTimeout = 2 * time.Second

// DefaultBackoff is the wait before each retry of a refused connection; the
// last entry repeats.
var DefaultBackoff = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}

// maxReplyBytes caps the reply a client reads. The daemon's answer is bounded
// by what its handlers return, not by a protocol cap, so this is only a guard
// against a peer that is not a daemon.
const maxReplyBytes = 256 << 20

// DaemonDownError reports that the daemon could not be reached: nothing
// listens (after retries), the socket is not permitted, the path is over the
// platform limit, or the connection failed. Its message names the socket path
// and carries the platform's restart hint; the caller answers unavailable.
type DaemonDownError struct {
	SocketPath string
	// Hint is the platform's restart hint, possibly empty.
	Hint string
	// Cause is the last underlying error.
	Cause error
	// Waited is how long the client kept retrying.
	Waited time.Duration
}

func (e *DaemonDownError) Error() string {
	msg := fmt.Sprintf("the pg-connector-github daemon is not reachable on socket %s: %v", e.SocketPath, e.Cause)
	if e.Waited > 0 {
		msg += fmt.Sprintf(" (retried for %s)", e.Waited.Round(time.Millisecond))
	}
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
}

func (e *DaemonDownError) Unwrap() error { return e.Cause }

// VersionMismatchError reports a daemon whose socket protocol is outside the
// client's supported range; the caller answers version_mismatch.
type VersionMismatchError struct {
	SocketPath     string
	ClientProtocol int
	DaemonProtocol int
	ClientBuild    string
	DaemonBuild    string
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("the daemon on socket %s speaks socket protocol %d (build %q), outside the range this client (protocol %d, build %q) supports",
		e.SocketPath, e.DaemonProtocol, e.DaemonBuild, e.ClientProtocol, e.ClientBuild)
}

// Client dials the daemon's socket for one call.
type Client struct {
	// SocketPath is the daemon's absolute socket path (REQUIRED).
	SocketPath string
	// Build is this client's build id, sent as client_build.
	Build string
	// Protocol is the client's socket protocol; zero uses ProtocolVersion.
	Protocol int
	// RestartHint is the platform's restart hint, named in a DaemonDownError.
	RestartHint string
	// ConnectTimeout bounds one attempt; zero uses DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// Backoff is the retry schedule; nil uses DefaultBackoff.
	Backoff []time.Duration
	// Logger receives one line per retry and per failure; nil discards them.
	Logger connectord.Logger
	// MaxSocketPathBytes overrides the platform's sun_path limit (tests); zero
	// uses proc.MaxSocketPathBytes.
	MaxSocketPathBytes int
	// Now and Sleep are the clock and the waiter; nil uses the real ones.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Reply is a daemon's answer to Call.
type Reply struct {
	// Response is the unmodified scriptout response.
	Response    json.RawMessage
	DaemonBuild string
	// DaemonProtocol is the socket protocol the daemon stamped on its reply.
	DaemonProtocol int
}

// Call sends req to the daemon for instance and returns its reply.
//
// budget is the whole time the caller allows itself, measured from now: a
// refused (or absent) socket is retried with backoff until budget/2 has
// passed, which covers a supervisor restart, and the reply is awaited until
// budget has passed. deadline is what the daemon is told (deadline_ms), taken
// down by the time already spent connecting; a non-positive deadline sends
// none.
//
// The error is nil, a *DaemonDownError (unreachable, over-long path, a
// permission refusal, no reply by budget) or a *VersionMismatchError. A reply
// that is itself an error response (the daemon refusing the request, for
// example version_mismatch for a client outside ITS window) is a Reply, not
// an error: the caller relays it.
func (c *Client) Call(ctx context.Context, instance string, req scriptout.Request, budget, deadline time.Duration) (*Reply, error) {
	log := c.Logger
	if log == nil {
		log = connectord.NopLogger
	}
	now, sleep := c.Now, c.Sleep
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	protocol := c.Protocol
	if protocol == 0 {
		protocol = ProtocolVersion
	}
	connectTimeout := c.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = DefaultConnectTimeout
	}
	backoff := c.Backoff
	if len(backoff) == 0 {
		backoff = DefaultBackoff
	}
	down := func(cause error, waited time.Duration) error {
		return &DaemonDownError{SocketPath: c.SocketPath, Hint: c.RestartHint, Cause: cause, Waited: waited}
	}

	limit := c.MaxSocketPathBytes
	if limit == 0 {
		limit = proc.MaxSocketPathBytes()
	}
	if err := proc.CheckSocketPath(c.SocketPath, limit); err != nil {
		log.Log("socket_path_too_long", "socket_path", c.SocketPath, "bytes", len(c.SocketPath), "limit", limit)
		return nil, &DaemonDownError{SocketPath: c.SocketPath, Cause: err}
	}

	// start and the retry window use the injectable clock; the context and the
	// connection deadline use the real one, which is what they run on.
	start := now()
	retryUntil := start.Add(budget / 2)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	var conn net.Conn
	for attempt := 0; ; attempt++ {
		d := net.Dialer{Timeout: connectTimeout}
		var err error
		conn, err = d.DialContext(ctx, "unix", c.SocketPath)
		if err == nil {
			break
		}
		waited := now().Sub(start)
		if !retryable(err) || ctx.Err() != nil {
			log.Log("connect_failed", "socket_path", c.SocketPath, "error", err.Error())
			return nil, down(err, waited)
		}
		// The last wait is cut short so one final attempt lands at the end of
		// the window rather than the retries stopping a step early.
		wait := min(backoff[min(attempt, len(backoff)-1)], retryUntil.Sub(now()))
		if wait <= 0 {
			log.Log("connect_gave_up", "socket_path", c.SocketPath, "waited", waited.String(), "error", err.Error())
			return nil, down(err, waited)
		}
		log.Log("connect_retry", "socket_path", c.SocketPath, "attempt", attempt+1, "error", err.Error())
		if err := sleep(ctx, wait); err != nil {
			return nil, down(err, now().Sub(start))
		}
	}
	defer conn.Close()
	if left := budget - now().Sub(start); left > 0 {
		_ = conn.SetDeadline(time.Now().Add(left))
	}

	rawReq, err := json.Marshal(req)
	if err != nil {
		return nil, down(fmt.Errorf("encode request: %w", err), now().Sub(start))
	}
	frame := RequestFrame{SocketProtocol: protocol, ClientBuild: c.Build, Instance: instance, Request: rawReq}
	if deadline > 0 {
		if left := deadline - now().Sub(start); left > 0 {
			frame.DeadlineMS = left.Milliseconds()
			if frame.DeadlineMS == 0 {
				frame.DeadlineMS = 1
			}
		} else {
			frame.DeadlineMS = 1
		}
	}
	if err := WriteFrame(conn, frame); err != nil {
		log.Log("request_write_failed", "socket_path", c.SocketPath, "error", err.Error())
		return nil, down(fmt.Errorf("send request: %w", err), now().Sub(start))
	}

	rawReply, err := ReadFrame(bufio.NewReaderSize(conn, 64<<10), maxReplyBytes)
	if err != nil {
		log.Log("reply_read_failed", "socket_path", c.SocketPath, "error", err.Error())
		return nil, down(fmt.Errorf("read the daemon's reply: %w", err), now().Sub(start))
	}
	var rf ReplyFrame
	if err := json.Unmarshal(rawReply, &rf); err != nil {
		return nil, down(fmt.Errorf("the daemon's reply is not a frame: %w", err), now().Sub(start))
	}
	if !ReplyCompatible(protocol, rf.SocketProtocol) {
		log.Log("daemon_protocol_outside_range", "client_protocol", protocol, "daemon_protocol", rf.SocketProtocol, "daemon_build", rf.DaemonBuild)
		return nil, &VersionMismatchError{SocketPath: c.SocketPath, ClientProtocol: protocol, DaemonProtocol: rf.SocketProtocol, ClientBuild: c.Build, DaemonBuild: rf.DaemonBuild}
	}
	if len(rf.Response) == 0 {
		return nil, down(errors.New("the daemon's reply frame carries no response"), now().Sub(start))
	}
	return &Reply{Response: rf.Response, DaemonBuild: rf.DaemonBuild, DaemonProtocol: rf.SocketProtocol}, nil
}

// retryable reports whether a failed connect may succeed shortly: nothing
// listening yet (refused, or the socket file absent while a supervisor
// restarts the daemon) or a connect that timed out. A permission or any other
// error is final.
func retryable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
