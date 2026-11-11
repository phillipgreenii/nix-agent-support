package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Dispatcher answers one decoded call; *router.Router is the production one.
type Dispatcher interface {
	Dispatch(ctx context.Context, c router.Call) scriptout.Response
}

// ServerConfig configures Handler.
type ServerConfig struct {
	// Build is this daemon's build id, sent as daemon_build.
	Build string
	// Protocol is the daemon's socket protocol N; zero uses ProtocolVersion.
	Protocol int
	// Logger receives one line per rejected frame; nil discards them.
	Logger connectord.Logger
	// MaxFrameBytes overrides the request cap (tests); zero uses MaxFrameBytes.
	MaxFrameBytes int
	// ReadTimeout bounds how long a client may take to send its frame; zero
	// uses DefaultReadTimeout. It does not bound the handler.
	ReadTimeout time.Duration
	// WriteTimeout bounds the reply write so a client that stopped reading
	// cannot pin the connection goroutine; zero uses DefaultWriteTimeout.
	WriteTimeout time.Duration
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// DefaultWriteTimeout bounds the write of a reply frame.
const DefaultWriteTimeout = 10 * time.Second

// DefaultReadTimeout bounds the read of a request frame.
const DefaultReadTimeout = 10 * time.Second

// drainWindow is how long, after an early reply, the server keeps reading and
// discarding what the client is still sending, so the close does not reset the
// connection and lose the reply.
const drainWindow = 2 * time.Second

// Handler returns the proc.Handler that serves one connection: read one
// request frame (capped), check the protocol window, dispatch, write one reply
// frame. The shell closes the connection when it returns. The socket layer
// never cancels a running handler on account of the call's deadline: it waits
// for the dispatcher's answer and writes it.
func Handler(d Dispatcher, cfg ServerConfig) proc.Handler {
	if cfg.Protocol == 0 {
		cfg.Protocol = ProtocolVersion
	}
	if cfg.Logger == nil {
		cfg.Logger = connectord.NopLogger
	}
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = MaxFrameBytes
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = DefaultReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return func(ctx context.Context, conn net.Conn, _ proc.Peer) {
		received := cfg.Now()
		_ = conn.SetReadDeadline(received.Add(cfg.ReadTimeout))
		raw, err := ReadFrame(bufio.NewReader(conn), cfg.MaxFrameBytes)
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil {
			serveReadFailure(conn, cfg, err)
			return
		}

		var frame RequestFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			cfg.Logger.Log("frame_malformed", "error", err.Error())
			reply(conn, cfg, errorResponse(scriptout.ErrInvalidArgument, fmt.Sprintf("malformed request frame: %v", err)))
			return
		}
		if !Accepts(cfg.Protocol, frame.SocketProtocol) {
			cfg.Logger.Log("socket_protocol_rejected", "client_protocol", frame.SocketProtocol, "daemon_protocol", cfg.Protocol, "client_build", frame.ClientBuild)
			reply(conn, cfg, errorResponse(scriptout.ErrVersionMismatch, fmt.Sprintf(
				"socket protocol %d is outside the daemon's supported window (%d and %d); client build %q, daemon build %q",
				frame.SocketProtocol, cfg.Protocol, cfg.Protocol-1, frame.ClientBuild, cfg.Build,
			)))
			return
		}
		if frame.Request == nil || string(frame.Request) == "null" {
			reply(conn, cfg, errorResponse(scriptout.ErrInvalidArgument, `request frame has no "request"`))
			return
		}
		var req scriptout.Request
		if err := json.Unmarshal(frame.Request, &req); err != nil {
			reply(conn, cfg, errorResponse(scriptout.ErrInvalidArgument, fmt.Sprintf("malformed request: %v", err)))
			return
		}
		if req.Op == "" {
			reply(conn, cfg, errorResponse(scriptout.ErrInvalidArgument, `request is missing required field "op"`))
			return
		}

		// The protocol is one frame per connection and the client does not
		// half-close, so any read result after the frame (EOF or an error)
		// means the peer left: cancel the handler instead of running it to
		// its deadline.
		dctx, cancel := context.WithCancel(ctx)
		watchDone := watchPeer(conn, cancel)
		resp := dispatchSafely(dctx, d, cfg, router.Call{
			Instance:    frame.Instance,
			Request:     req,
			Deadline:    time.Duration(frame.DeadlineMS) * time.Millisecond,
			Received:    received,
			ClientBuild: frame.ClientBuild,
		})
		cancel()
		watchDone()
		reply(conn, cfg, resp)
	}
}

// watchPeer reads conn in the background and calls onGone when the read ends.
// The returned stop function unblocks the reader and waits for it, leaving the
// connection with no read deadline.
func watchPeer(conn net.Conn, onGone func()) (stop func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, conn)
		onGone()
	}()
	return func() {
		_ = conn.SetReadDeadline(time.Now())
		<-done
		_ = conn.SetReadDeadline(time.Time{})
	}
}

// dispatchSafely runs the dispatcher and turns a panic into an unavailable
// response, logging the panic value and stack, so one bad request cannot end
// the daemon.
func dispatchSafely(ctx context.Context, d Dispatcher, cfg ServerConfig, c router.Call) (resp scriptout.Response) {
	defer func() {
		if r := recover(); r != nil {
			cfg.Logger.Log("handler_panic", "op", c.Request.Op, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			resp = errorResponse(scriptout.ErrUnavailable, "internal error: handler panicked")
		}
	}()
	return d.Dispatch(ctx, c)
}

// serveReadFailure answers a frame that could not be read: over the cap, or a
// connection that closed or stalled first.
func serveReadFailure(conn net.Conn, cfg ServerConfig, err error) {
	switch {
	case errors.Is(err, ErrFrameTooLarge):
		cfg.Logger.Log("frame_too_large", "limit_bytes", cfg.MaxFrameBytes)
		reply(conn, cfg, errorResponse(scriptout.ErrInvalidArgument, fmt.Sprintf("request frame exceeds the %d byte cap", cfg.MaxFrameBytes)))
		drainAfterReply(conn)
	case errors.Is(err, io.EOF):
		// The client connected and left without a frame: nothing to answer.
	default:
		cfg.Logger.Log("frame_read_failed", "error", err.Error())
	}
}

// drainAfterReply half-closes the write side and discards what the client is
// still sending, for a bounded time.
func drainAfterReply(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = conn.SetReadDeadline(time.Now().Add(drainWindow))
	_, _ = io.Copy(io.Discard, conn)
}

// reply writes one reply frame. A client that already left is not an error
// worth more than a log line, and the shell closes the connection anyway.
func reply(conn net.Conn, cfg ServerConfig, resp scriptout.Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		cfg.Logger.Log("reply_marshal_failed", "error", err.Error())
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(cfg.WriteTimeout))
	if err := WriteFrame(conn, ReplyFrame{SocketProtocol: cfg.Protocol, DaemonBuild: cfg.Build, Response: raw}); err != nil {
		cfg.Logger.Log("reply_write_failed", "error", err.Error())
	}
}

// errorResponse builds an error Response in the closed taxonomy.
func errorResponse(sentinel error, message string) scriptout.Response {
	return *scriptout.ErrorResponse(scriptout.WrapError(sentinel, message))
}
