package socketserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/osx-bridge-api/internal/calendarapi"
	"github.com/phillipgreenii/osx-bridge-api/internal/wire"
)

// fakeProvider is the same style of FAKE/stub calendar provider AC #1
// requires, used here to exercise the daemon's service-routing/dispatch
// logic and JSON envelope end-to-end over a real Unix domain socket,
// without ever touching EventKit/TCC.
type fakeProvider struct {
	calendars []calendarapi.Calendar
}

func (f *fakeProvider) Calendars() ([]calendarapi.Calendar, error) { return f.calendars, nil }
func (f *fakeProvider) Events(calendarapi.EventsQuery) ([]calendarapi.Event, error) {
	return nil, nil
}

// call dials sock, writes req, reads back one wire.Response, and closes the
// connection — a minimal client mirroring how a real caller (the future
// pg-connector-calendar-osx-bridge backend) would speak this protocol.
func call(t *testing.T, sock string, req wire.Request) wire.Response {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer func() { _ = conn.Close() }()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp wire.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// startServer registers a calendar service backed by a fake provider and
// serves it on a Unix socket under a fresh temp dir, returning the socket
// path and a stop func.
func startServer(t *testing.T) (sock string, stop func()) {
	t.Helper()

	srv := New()
	srv.Register(calendarapi.ServiceName, calendarapi.New(&fakeProvider{
		calendars: []calendarapi.Calendar{{ID: "cal-1", Title: "Test Calendar"}},
	}))

	// A short, dedicated temp dir rather than t.TempDir(): t.TempDir()
	// embeds the (possibly long) test name in the path, and
	// sockaddr_un.sun_path is capped at 104 bytes on darwin / 108 on
	// Linux — a long subtest name overflows that limit ("bind: invalid
	// argument"), which t.TempDir()'s own path does not protect against.
	dir, err := os.MkdirTemp("", "osxb")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock = filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	return sock, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned an error after shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Serve did not return within 2s of ctx cancellation")
		}
	}
}

func TestServeRoutesToRegisteredService(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	resp := call(t, sock, wire.Request{ProtocolVersion: wire.ProtocolVersion, Service: calendarapi.ServiceName, Op: calendarapi.OpCalendars})
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
	if resp.ProtocolVersion != wire.ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", resp.ProtocolVersion, wire.ProtocolVersion)
	}
	if resp.SchemaVersion != calendarapi.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", resp.SchemaVersion, calendarapi.SchemaVersion)
	}

	var got struct {
		Calendars []calendarapi.Calendar `json:"calendars"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got.Calendars) != 1 || got.Calendars[0].ID != "cal-1" {
		t.Errorf("unexpected calendars result: %+v", got.Calendars)
	}
}

func TestServeUnknownService(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	resp := call(t, sock, wire.Request{Service: "mail", Op: "list"})
	requireErrorCode(t, resp, "unknown_op")
}

func TestServeUnknownOpWithinKnownService(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	resp := call(t, sock, wire.Request{Service: calendarapi.ServiceName, Op: "delete-everything"})
	requireErrorCode(t, resp, "unknown_op")
}

func TestServeMissingService(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	resp := call(t, sock, wire.Request{Op: calendarapi.OpCalendars})
	requireErrorCode(t, resp, "invalid_argument")
}

func TestServeMissingOp(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	resp := call(t, sock, wire.Request{Service: calendarapi.ServiceName})
	requireErrorCode(t, resp, "invalid_argument")
}

func TestServeVersionMismatch(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	resp := call(t, sock, wire.Request{ProtocolVersion: wire.ProtocolVersion + 1, Service: calendarapi.ServiceName, Op: calendarapi.OpCalendars})
	requireErrorCode(t, resp, "version_mismatch")
}

func TestServeMalformedRequest(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("{not valid json")); err != nil {
		t.Fatalf("write malformed request: %v", err)
	}
	// Signal EOF so the decoder's Decode call (which needs a full JSON
	// value) returns rather than blocking forever on more bytes.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}

	var resp wire.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	requireErrorCode(t, resp, "invalid_argument")
}

func TestServeInvalidEventsArgs(t *testing.T) {
	sock, stop := startServer(t)
	defer stop()

	args, _ := json.Marshal(map[string]any{})
	resp := call(t, sock, wire.Request{Service: calendarapi.ServiceName, Op: calendarapi.OpEvents, Args: args})
	requireErrorCode(t, resp, "invalid_argument")
}

func requireErrorCode(t *testing.T, resp wire.Response, wantCode string) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("expected an error response with code %q, got a success response: %+v", wantCode, resp)
	}
	if resp.Error.Code != wantCode {
		t.Errorf("Error.Code = %q, want %q (message: %s)", resp.Error.Code, wantCode, resp.Error.Message)
	}
}

// TestDispatchDirect exercises Server.Dispatch without any socket I/O, so
// the routing/dispatch logic itself (as distinct from the transport) has a
// unit-level test independent of net.Conn framing concerns.
func TestDispatchDirect(t *testing.T) {
	srv := New()
	srv.Register(calendarapi.ServiceName, calendarapi.New(&fakeProvider{}))

	resp := srv.Dispatch(wire.Request{Service: "unregistered", Op: "whatever"})
	if resp.Error == nil || resp.Error.Code != "unknown_op" {
		t.Fatalf("unexpected response for an unregistered service: %+v", resp)
	}

	if !errors.Is(wire.WrapError(wire.ErrUnknownOp, "x"), wire.ErrUnknownOp) {
		t.Fatalf("sanity check on wire sentinel wrapping failed")
	}
}
