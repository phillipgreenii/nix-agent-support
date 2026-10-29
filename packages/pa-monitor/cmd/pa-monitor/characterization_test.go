package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// fakeStatusDaemon serves GetState and GetSessionInfo — the two RPCs
// runStatus calls — from a fixed, in-memory DaemonState.
type fakeStatusDaemon struct {
	pb.UnimplementedPaMonitorServer
	state *pb.DaemonState
}

func (f *fakeStatusDaemon) GetState(_ context.Context, _ *pb.GetStateRequest) (*pb.DaemonState, error) {
	return f.state, nil
}

func (f *fakeStatusDaemon) GetSessionInfo(_ context.Context, req *pb.GetSessionInfoRequest) (*pb.SessionDetail, error) {
	sid := req.GetSelector().GetSessionId()
	for _, d := range f.state.GetDirs() {
		for _, v := range d.GetSessions() {
			if v.GetSessionId() == sid {
				return &pb.SessionDetail{View: v}, nil
			}
		}
	}
	return &pb.SessionDetail{}, nil
}

// characterizationState is the representative DaemonState (one working
// session, one blocked session, an active block, no active week) shared by
// TestRunStatusTextOutputCharacterization.
func characterizationState() *pb.DaemonState {
	fiveHourPct := 22.5
	return &pb.DaemonState{
		DaemonVersion:       "1.2.3",
		DaemonUptimeSeconds: 42,
		PlanTier:            "max20",
		PlanCapUsd:          20,
		FiveHourPct:         &fiveHourPct,
		AutoResumeEnabled:   true,
		Dirs: []*pb.Directory{
			{
				Path:     "/repo/a",
				WorkingN: 1,
				Sessions: []*pb.SessionView{
					{SessionId: "sid-working-1", Name: "feature-x", Status: "working", TerminalHost: "tmux"},
				},
			},
			{
				Path:     "/repo/b",
				BlockedN: 1,
				Sessions: []*pb.SessionView{
					{SessionId: "sid-blocked-1", Status: "blocked", Blocker: "usage_limit", TerminalHost: "cmux"},
				},
			},
		},
		ActiveBlock: &pb.Block{Id: "b1", CostUsd: 4.5},
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String()
}

// TestRunStatusTextOutputCharacterization is the runStatus extract-method
// refactor's regression net (pg2-eezd1.2 Step 0). It captures runStatus's
// text-mode output for a representative state (one working session, one
// blocked session, an active block); this test must be seen to PASS
// against the pre-refactor runStatus (proving it captures, not invents,
// the real output) and must keep passing unchanged after the refactor
// (proving the refactor changed no observable behavior).
func TestRunStatusTextOutputCharacterization(t *testing.T) {
	sock := daemonTestSocket(t)
	serveFakeDaemon(t, sock, &fakeStatusDaemon{state: characterizationState()})

	out := captureStdout(t, func() { runStatus(nil) })

	// Captured verbatim from the pre-refactor runStatus (this test was run
	// and confirmed PASSING against the original cli.go before Step 3's
	// extract-method refactor touched it) — not invented.
	const want = "client:        pa-monitor dev\n" +
		"daemon:        pa-monitor 1.2.3\n" +
		"uptime:        42s\n" +
		"plan_tier:     max20\n" +
		"sessions:      1 working, 1 blocked, 0 idle\n" +
		"block b1:  cost $4.50 / cap $20.00 (22.5%)\n" +
		"caffeinate:    mode off · process off\n" +
		"auto_resume:   true\n"

	if out != want {
		t.Errorf("runStatus text output changed.\ngot:\n%q\nwant:\n%q", out, want)
	}
}

// TestRunStatusJSONOutputIsCleanJSON is the pg2-bx515 regression: runStatus
// used to print its text-summary lines (client/daemon/uptime/plan_tier/
// sessions) UNCONDITIONALLY, before the jsonMode gate later in the
// function — so `pa-monitor status --json` emitted human text followed by
// a JSON object on stdout instead of a clean JSON document, breaking any
// consumer that expects valid JSON on stdout (confirmed live:
// packages/pg-connector-agentsession-pa-monitor's real-daemon contract
// test failed to decode it).
//
// json.Unmarshal runs against the FULL captured stdout, not a substring —
// a substring-based JSON extraction (e.g. finding the first '{') would have
// passed on the old buggy output too, since the JSON object itself was
// always well-formed; only what preceded it on the same stream was wrong.
func TestRunStatusJSONOutputIsCleanJSON(t *testing.T) {
	sock := daemonTestSocket(t)
	serveFakeDaemon(t, sock, &fakeStatusDaemon{state: characterizationState()})

	out := captureStdout(t, func() { runStatus([]string{"--json"}) })

	if !strings.HasPrefix(out, "{") {
		t.Fatalf("status --json stdout has leading non-JSON content before the first '{': %q", out)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("status --json stdout is not valid, single-document JSON: %v\nfull captured stdout:\n%q", err, out)
	}
	if _, ok := doc["sessions"]; !ok {
		t.Errorf("decoded JSON missing expected top-level \"sessions\" key: %+v", doc)
	}
	// "sessions:" is deliberately excluded here: the JSON key renders as
	// `"sessions":...` (no space), which contains this substring even in the
	// fixed output — the leading-'{' check above and json.Unmarshal on the
	// full string are what actually prove no text precedes the JSON.
	for _, text := range []string{"client:", "daemon:", "uptime:", "plan_tier:"} {
		if strings.Contains(out, text) {
			t.Errorf("status --json stdout still contains a text-mode summary line %q: %q", text, out)
		}
	}
}

// daemonTestSocket points every path rpcclient.Dial consults at a fresh temp dir
// and returns the socket path it will resolve to. /tmp rather than t.TempDir()
// because macOS caps unix socket paths at 104 bytes and the Go temp root is well
// past that (the same reason internal/daemon's tests use shortTempDir).
//
// Setting all three of XDG_STATE_HOME / XDG_RUNTIME_DIR / HOME is load-bearing:
// ResolvePaths reads XDG_STATE_HOME on darwin, XDG_RUNTIME_DIR first on linux,
// and falls back to HOME, so leaving any of them alone would let these tests see
// a pa-monitor daemon running on the developer's machine.
func daemonTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pamtest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "pa-monitor"), 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "pa-monitor", "daemon.sock")
}

// serveFakeDaemon serves srv on sock until the returned stop func runs (also
// registered as cleanup).
func serveFakeDaemon(t *testing.T, sock string, srv pb.PaMonitorServer, opts ...grpc.ServerOption) (stop func()) {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(opts...)
	pb.RegisterPaMonitorServer(gs, srv)
	served := make(chan struct{})
	go func() { defer close(served); _ = gs.Serve(ln) }()

	var stopped atomic.Bool
	stop = func() {
		if stopped.Swap(true) {
			return
		}
		gs.Stop()
		<-served
		_ = os.Remove(sock)
	}
	t.Cleanup(stop)
	return stop
}
