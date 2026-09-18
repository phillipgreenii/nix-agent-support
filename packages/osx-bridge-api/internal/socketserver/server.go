// Package socketserver is osx-bridge-api's transport-agnostic
// service-routing/dispatch layer: it accepts a wire.Request, looks up the
// registered Service named by the request's Service field, hands the
// request's Op/Args to it, and turns the result (or error) into a
// wire.Response — the part of the daemon design's "one daemon/one
// transport can host multiple OS integrations" claim that is actually
// exercised by AC #1's automated unit tests, independent of any real
// socket I/O.
//
// Serve/ServeConn additionally drive this dispatch over a real
// net.Listener/net.Conn (a Unix domain socket in production), so the same
// logic is exercised end-to-end in tests without requiring EventKit/TCC —
// only a FAKE/stub Service (see internal/calendarapi's tests) is needed.
package socketserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/phillipgreenii/osx-bridge-api/internal/wire"
)

// Service is what a registered service (e.g. "calendar") implements to
// answer requests routed to it.
type Service interface {
	// Handle executes op with the given (possibly nil) args and returns the
	// JSON result payload, or an error wrapping one of wire's Err*
	// sentinels (internal/wire/errors.go) via wire.WrapError. An op this
	// service does not implement MUST return wire.ErrUnknownOp.
	Handle(op string, args json.RawMessage) (json.RawMessage, error)

	// SchemaVersion is this service's own current schema version, stamped
	// onto every Response this service answers (wire.Response.SchemaVersion)
	// — independent of the transport-level wire.ProtocolVersion, mirroring
	// ADR 0062 principle 3's per-capability schemaVersion.
	SchemaVersion() int
}

// Server routes a wire.Request to a registered Service by its Service
// field and drives that routing over a net.Listener. It is safe for
// concurrent Register calls made before Serve starts; Register after Serve
// has started is also safe (guarded by mu) but registering while requests
// are already flowing in is not a pattern this daemon uses today (services
// are all registered once at startup, in cmd/osx-bridge-api/main.go).
type Server struct {
	mu       sync.RWMutex
	services map[string]Service
}

// New returns a Server with no services registered.
func New() *Server {
	return &Server{services: make(map[string]Service)}
}

// Register adds svc under name, replacing any prior registration of that
// name.
func (s *Server) Register(name string, svc Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.services[name] = svc
}

// Dispatch routes req to its named service and returns the resulting
// wire.Response. It never returns an error itself — every failure
// (malformed request, unknown service, a service's own handler error) is
// reported as a wire.Response carrying a wire.ErrorBody, which is exactly
// what a real client receives over the wire.
func (s *Server) Dispatch(req wire.Request) *wire.Response {
	if req.ProtocolVersion != 0 && req.ProtocolVersion != wire.ProtocolVersion {
		return wire.ErrorResponse(wire.WrapError(wire.ErrVersionMismatch,
			fmt.Sprintf("daemon speaks protocolVersion %d, request carried %d", wire.ProtocolVersion, req.ProtocolVersion)))
	}
	if req.Service == "" {
		return wire.ErrorResponse(wire.WrapError(wire.ErrInvalidArgument, "request carries no service"))
	}
	if req.Op == "" {
		return wire.ErrorResponse(wire.WrapError(wire.ErrInvalidArgument, "request carries no op"))
	}

	s.mu.RLock()
	svc, ok := s.services[req.Service]
	s.mu.RUnlock()
	if !ok {
		return wire.ErrorResponse(wire.WrapError(wire.ErrUnknownOp, fmt.Sprintf("unknown service %q", req.Service)))
	}

	result, err := svc.Handle(req.Op, req.Args)
	if err != nil {
		resp := wire.ErrorResponse(err)
		resp.SchemaVersion = svc.SchemaVersion()
		return resp
	}
	return &wire.Response{
		ProtocolVersion: wire.ProtocolVersion,
		SchemaVersion:   svc.SchemaVersion(),
		Result:          result,
	}
}

// ServeConn reads exactly one wire.Request from conn, dispatches it, writes
// exactly one wire.Response back, and closes conn — the one-request/
// one-response/one-connection framing this daemon's socket protocol uses
// (mirroring the request/response shape of pg-connector's own stdio
// envelope, ADR 0062, just carried over a socket).
//
// A malformed request (unreadable JSON, EOF before a full request arrives)
// is reported as an invalid_argument wire.Response rather than silently
// dropping the connection, so a caller always gets a well-formed reply.
func (s *Server) ServeConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	var req wire.Request
	dec := json.NewDecoder(conn)
	var resp *wire.Response
	if err := dec.Decode(&req); err != nil {
		resp = wire.ErrorResponse(wire.WrapError(wire.ErrInvalidArgument, fmt.Sprintf("malformed request: %v", err)))
	} else {
		resp = s.Dispatch(req)
	}

	// A write failure has no client left to report it to; it is the
	// caller's (Serve's) job to log it if it wants to.
	_ = json.NewEncoder(conn).Encode(resp)
}

// Serve accepts connections on ln until ctx is done or ln.Accept fails for
// a reason other than ctx being done, handling each connection via
// ServeConn in its own goroutine (each connection carries exactly one
// request, so there is no shared per-connection state to race on).
//
// Serve returns nil on a clean shutdown (ctx done, or ln closed as part of
// that shutdown) and a non-nil error only for an Accept failure unrelated
// to ctx cancellation.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("socketserver: accept: %w", err)
		}
		go s.ServeConn(conn)
	}
}
