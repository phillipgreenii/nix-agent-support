package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// startFakeSocket starts a minimal, hand-rolled Unix-socket listener that
// decodes exactly one JSON request per connection (a bare map, not
// osx-bridge-api's own wire.Request) and replies with whatever handle
// returns — deliberately NOT importing packages/osx-bridge-api's own
// socketserver/wire/calendarapi packages: this backend's own Contract
// requires this client to be exercised against nothing but its own
// locally-defined mirror types (see client.go's package doc comment), so
// even this test-only fake server must not reach for the real module's
// internal packages.
func startFakeSocket(t *testing.T, handle func(req map[string]any) any) (sockPath string, requests func() []map[string]any) {
	t.Helper()

	// A short, dedicated temp dir rather than t.TempDir(): t.TempDir()
	// embeds the (possibly long) test name in the path, and
	// sockaddr_un.sun_path is capped well under that length on darwin/
	// Linux — mirrors
	// packages/osx-bridge-api/internal/socketserver/server_test.go's
	// identical startServer precedent.
	dir, err := os.MkdirTemp("", "pgcob")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath = filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var seen []map[string]any

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var req map[string]any
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				mu.Lock()
				seen = append(seen, req)
				mu.Unlock()
				resp := handle(req)
				_ = json.NewEncoder(conn).Encode(resp)
			}()
		}
	}()

	return sockPath, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), seen...)
	}
}

func TestSocketClient_Calendars_Success(t *testing.T) {
	sock, requests := startFakeSocket(t, func(req map[string]any) any {
		return map[string]any{
			"protocolVersion": 1,
			"schemaVersion":   1,
			"result": map[string]any{
				"calendars": []map[string]any{
					{"id": "cal-1", "title": "Work", "readOnly": false},
				},
			},
		}
	})
	c := &SocketClient{SocketPath: sock}

	got, err := c.Calendars(context.Background())
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	if len(got) != 1 || got[0].ID != "cal-1" || got[0].Title != "Work" {
		t.Fatalf("got = %+v", got)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(reqs))
	}
	if reqs[0]["service"] != serviceName || reqs[0]["op"] != opCalendars {
		t.Fatalf("request = %+v, want service=%q op=%q", reqs[0], serviceName, opCalendars)
	}
	if pv, _ := reqs[0]["protocolVersion"].(float64); int(pv) != wireProtocolVersion {
		t.Fatalf("request protocolVersion = %v, want %d", reqs[0]["protocolVersion"], wireProtocolVersion)
	}
}

func TestSocketClient_Events_Success_CarriesArgs(t *testing.T) {
	start := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	sock, requests := startFakeSocket(t, func(req map[string]any) any {
		return map[string]any{
			"protocolVersion": 1,
			"schemaVersion":   1,
			"result": map[string]any{
				"events": []map[string]any{
					{"id": "ev-1", "title": "Standup", "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339), "calendarId": "cal-1"},
				},
			},
		}
	})
	c := &SocketClient{SocketPath: sock}

	got, err := c.Events(context.Background(), apiEventsQuery{Start: start, End: end, CalendarIDs: []string{"cal-1"}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ev-1" || got[0].CalendarID != "cal-1" {
		t.Fatalf("got = %+v", got)
	}

	reqs := requests()
	if len(reqs) != 1 || reqs[0]["op"] != opEvents {
		t.Fatalf("request = %+v", reqs)
	}
	args, ok := reqs[0]["args"].(map[string]any)
	if !ok {
		t.Fatalf("args missing/wrong type: %+v", reqs[0])
	}
	ids, ok := args["calendarIds"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "cal-1" {
		t.Fatalf("args.calendarIds = %+v", args["calendarIds"])
	}
}

func TestSocketClient_ErrorCodes_ClassifyToScriptoutSentinels(t *testing.T) {
	cases := []struct {
		wireCode     string
		wantSentinel error
	}{
		{"not_found", scriptout.ErrNotFound},
		{"unauthenticated", scriptout.ErrUnauthenticated},
		{"unavailable", scriptout.ErrUnavailable},
		{"unknown_op", scriptout.ErrUnknownOp},
		{"version_mismatch", scriptout.ErrVersionMismatch},
		{"invalid_argument", scriptout.ErrInvalidArgument},
		{"totally_unrecognized_code", scriptout.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.wireCode, func(t *testing.T) {
			sock, _ := startFakeSocket(t, func(req map[string]any) any {
				return map[string]any{
					"protocolVersion": 1,
					"error":           map[string]any{"code": tc.wireCode, "message": "boom"},
				}
			})
			c := &SocketClient{SocketPath: sock}
			_, err := c.Calendars(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, tc.wantSentinel) {
				t.Fatalf("err = %v, want errors.Is(err, %v)", err, tc.wantSentinel)
			}
		})
	}
}

func TestSocketClient_DialFailure_Unavailable(t *testing.T) {
	dir, err := os.MkdirTemp("", "pgcob")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	c := &SocketClient{SocketPath: filepath.Join(dir, "does-not-exist.sock")}
	_, err = c.Calendars(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestSocketClient_MalformedResponse_Unavailable(t *testing.T) {
	sock, _ := startFakeSocket(t, func(req map[string]any) any {
		return "not an object"
	})
	c := &SocketClient{SocketPath: sock}
	_, err := c.Calendars(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestResolveSocketPath_EnvVarWins(t *testing.T) {
	getenv := func(k string) string {
		if k == socketEnvVar {
			return "/custom/path.sock"
		}
		return ""
	}
	got, err := ResolveSocketPath(getenv)
	if err != nil {
		t.Fatalf("ResolveSocketPath: %v", err)
	}
	if got != "/custom/path.sock" {
		t.Fatalf("got = %q", got)
	}
}

func TestResolveSocketPath_XDGStateHomeDefault(t *testing.T) {
	getenv := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return "/xdg-state"
		}
		return ""
	}
	got, err := ResolveSocketPath(getenv)
	if err != nil {
		t.Fatalf("ResolveSocketPath: %v", err)
	}
	want := filepath.Join("/xdg-state", "osx-bridge-api", "osx-bridge-api.sock")
	if got != want {
		t.Fatalf("got = %q, want %q", got, want)
	}
}

func TestResolveSocketPath_HomeFallbackDefault(t *testing.T) {
	getenv := func(k string) string {
		if k == "HOME" {
			return "/home/phillip"
		}
		return ""
	}
	got, err := ResolveSocketPath(getenv)
	if err != nil {
		t.Fatalf("ResolveSocketPath: %v", err)
	}
	want := filepath.Join("/home/phillip", ".local", "state", "osx-bridge-api", "osx-bridge-api.sock")
	if got != want {
		t.Fatalf("got = %q, want %q", got, want)
	}
}

func TestResolveSocketPath_NeitherSet_Error(t *testing.T) {
	getenv := func(string) string { return "" }
	if _, err := ResolveSocketPath(getenv); err == nil {
		t.Fatal("expected an error when neither env var, XDG_STATE_HOME, nor HOME is set")
	}
}
