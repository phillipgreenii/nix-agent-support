package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

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
	sock := waitTestSocket(t)
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
