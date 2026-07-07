package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/tui"

	"google.golang.org/grpc"
)

// fakeBridgeStream is an in-memory stand-in for
// pb.PaMonitor_BridgeChannelClient (grpc.BidiStreamingClient[BridgeMsg,
// DaemonMsg]). Recv drains an inbound channel (or unblocks on ctx.Done);
// Send appends to a mutex-guarded slice so a test goroutine can inspect the
// bridge's outbound traffic while the loop runs concurrently.
type fakeBridgeStream struct {
	grpc.ClientStream // embedded interface; only Send/Recv/Context are exercised

	ctx     context.Context
	inbound chan *pb.DaemonMsg

	mu   sync.Mutex
	sent []*pb.BridgeMsg
}

func (f *fakeBridgeStream) Send(m *pb.BridgeMsg) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeBridgeStream) Recv() (*pb.DaemonMsg, error) {
	select {
	case m, ok := <-f.inbound:
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

func (f *fakeBridgeStream) Context() context.Context { return f.ctx }

func (f *fakeBridgeStream) sentMsgs() []*pb.BridgeMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*pb.BridgeMsg, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeBridgeStream) results() []*pb.DeliverResult {
	var out []*pb.DeliverResult
	for _, m := range f.sentMsgs() {
		if r := m.GetResult(); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeBridgeStream) firstRegister() *pb.Register {
	for _, m := range f.sentMsgs() {
		if r := m.GetRegister(); r != nil {
			return r
		}
	}
	return nil
}

// fakeSurf is one (workspace, surface, tty-pids) triple for the cmux fake.
type fakeSurf struct {
	ws      string
	surface string
	pids    []int
}

// fakeCmux is a thread-safe cmux CLI stand-in. It answers
// `cmux --json top --processes` with a synthesized envelope built from
// surfaces and records `cmux send`/`send-key` argv under a mutex, so the
// concurrent deliver handler goroutine and the test goroutine can share it
// under -race.
type fakeCmux struct {
	surfaces []fakeSurf // read-only after construction

	mu    sync.Mutex
	calls []string
}

func (f *fakeCmux) run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "cmux" {
		return nil, fmt.Errorf("unexpected command: %s", name)
	}
	if len(args) >= 3 && args[0] == "--json" && args[1] == "top" && args[2] == "--processes" {
		return f.topJSON(), nil
	}
	if len(args) >= 1 && (args[0] == "send" || args[0] == "send-key") {
		f.mu.Lock()
		f.calls = append(f.calls, "cmux "+strings.Join(args, " "))
		f.mu.Unlock()
		return []byte(""), nil
	}
	return nil, fmt.Errorf("unexpected cmux args: %v", args)
}

func (f *fakeCmux) topJSON() []byte {
	var wsObjs []string
	for i, s := range f.surfaces {
		pidParts := make([]string, len(s.pids))
		for j, p := range s.pids {
			pidParts[j] = fmt.Sprintf("%d", p)
		}
		pane := fmt.Sprintf(
			`{"ref":"pane:%d","surfaces":[{"ref":%q,"type":"terminal","tty":"ttysX","tty_process_pids":[%s]}]}`,
			i, s.surface, strings.Join(pidParts, ","),
		)
		wsObjs = append(wsObjs, fmt.Sprintf(`{"ref":%q,"panes":[%s]}`, s.ws, pane))
	}
	body := fmt.Sprintf(`{"windows":[{"ref":"window:1","workspaces":[%s]}]}`, strings.Join(wsObjs, ","))
	return []byte(body)
}

func (f *fakeCmux) sentCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// fakeReporter records Push snapshots under a mutex.
type fakeReporter struct {
	mu     sync.Mutex
	pushes []cmuxstatus.Snapshot
}

func (r *fakeReporter) Push(s cmuxstatus.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes = append(r.pushes, s)
}
func (r *fakeReporter) Notify(string, string) {}
func (r *fakeReporter) Clear()                {}
func (r *fakeReporter) pushCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pushes)
}

func newTestBridgeLogger(t *testing.T) *bridgeLogger {
	t.Helper()
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "pane-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return &bridgeLogger{
		now:  time.Now,
		file: &tui.ErrorLogger{CacheDir: dir, FileName: "cmux-bridge.log"},
		emit: nil,
		out:  f,
	}
}

func newTestAnnouncer() *connAnnouncer {
	return &connAnnouncer{
		term:   func(string) {},
		detail: func(string, map[string]string) {},
		gauge:  func(bool) {},
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

// TestBridgeChannelSendsRegisterFirst asserts the bridge announces itself with
// a Register carrying its own pid, the resolved server pid, and workspace id
// before any other traffic.
func TestBridgeChannelSendsRegisterFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeCmux{}
	sig := &signal.CmuxSignaler{RunCmd: fc.run}
	stream := &fakeBridgeStream{ctx: ctx, inbound: make(chan *pb.DaemonMsg, 8)}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridgeChannel(ctx, cancel, stream, "workspace:1", 12345, sig, &fakeReporter{}, newTestBridgeLogger(t), newTestAnnouncer())
	}()

	waitFor(t, 2*time.Second, func() bool { return stream.firstRegister() != nil })
	reg := stream.firstRegister()
	if reg.GetServerPid() != 12345 {
		t.Errorf("Register.ServerPid = %d, want 12345", reg.GetServerPid())
	}
	if reg.GetWorkspaceId() != "workspace:1" {
		t.Errorf("Register.WorkspaceId = %q, want workspace:1", reg.GetWorkspaceId())
	}
	if reg.GetBridgePid() != int32(os.Getpid()) {
		t.Errorf("Register.BridgePid = %d, want %d", reg.GetBridgePid(), os.Getpid())
	}

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("runBridgeChannel returned %v", err)
	}
}

// TestBridgeChannelDeliverSuccess feeds a Deliver whose target pid resolves to
// a cmux surface: the bridge must run cmux send + send-key against that surface
// and reply with DeliverResult{ok:true, id}.
func TestBridgeChannelDeliverSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeCmux{surfaces: []fakeSurf{{ws: "workspace:1", surface: "surface:4", pids: []int{4321}}}}
	sig := &signal.CmuxSignaler{RunCmd: fc.run}
	stream := &fakeBridgeStream{ctx: ctx, inbound: make(chan *pb.DaemonMsg, 8)}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridgeChannel(ctx, cancel, stream, "workspace:1", 12345, sig, &fakeReporter{}, newTestBridgeLogger(t), newTestAnnouncer())
	}()

	stream.inbound <- &pb.DaemonMsg{Kind: &pb.DaemonMsg_Deliver{Deliver: &pb.Deliver{Id: "c1", TargetPid: 4321, Text: "continue"}}}

	waitFor(t, 3*time.Second, func() bool {
		return len(fc.sentCalls()) >= 2 && len(stream.results()) >= 1
	})

	calls := fc.sentCalls()
	if len(calls) < 2 {
		t.Fatalf("expected 2 cmux calls (send + send-key), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "send --workspace workspace:1 --surface surface:4 continue") {
		t.Errorf("call[0] = %q, want cmux send to workspace:1/surface:4 with text", calls[0])
	}
	if !strings.Contains(calls[1], "send-key --workspace workspace:1 --surface surface:4 enter") {
		t.Errorf("call[1] = %q, want cmux send-key enter to workspace:1/surface:4", calls[1])
	}

	res := stream.results()
	if len(res) != 1 {
		t.Fatalf("expected exactly 1 DeliverResult, got %d: %v", len(res), res)
	}
	if res[0].GetId() != "c1" || !res[0].GetOk() || res[0].GetError() != "" {
		t.Errorf("DeliverResult = %+v, want {id:c1 ok:true error:\"\"}", res[0])
	}

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("runBridgeChannel returned %v", err)
	}
}

// TestBridgeChannelDeliverFailure feeds a Deliver whose target pid resolves to
// no surface: the bridge must reply DeliverResult{ok:false} with a non-empty
// error and must not have run any cmux send.
func TestBridgeChannelDeliverFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeCmux{surfaces: []fakeSurf{{ws: "workspace:1", surface: "surface:4", pids: []int{100, 200}}}}
	sig := &signal.CmuxSignaler{RunCmd: fc.run}
	stream := &fakeBridgeStream{ctx: ctx, inbound: make(chan *pb.DaemonMsg, 8)}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridgeChannel(ctx, cancel, stream, "workspace:1", 12345, sig, &fakeReporter{}, newTestBridgeLogger(t), newTestAnnouncer())
	}()

	stream.inbound <- &pb.DaemonMsg{Kind: &pb.DaemonMsg_Deliver{Deliver: &pb.Deliver{Id: "c2", TargetPid: 9999, Text: "continue"}}}

	waitFor(t, 3*time.Second, func() bool { return len(stream.results()) >= 1 })

	res := stream.results()
	if len(res) != 1 {
		t.Fatalf("expected exactly 1 DeliverResult, got %d: %v", len(res), res)
	}
	if res[0].GetId() != "c2" {
		t.Errorf("DeliverResult.Id = %q, want c2", res[0].GetId())
	}
	if res[0].GetOk() {
		t.Errorf("DeliverResult.Ok = true, want false for unresolved pid")
	}
	if res[0].GetError() == "" {
		t.Errorf("DeliverResult.Error is empty, want a resolution error")
	}
	if calls := fc.sentCalls(); len(calls) != 0 {
		t.Errorf("expected no cmux send calls on resolution failure, got %v", calls)
	}

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("runBridgeChannel returned %v", err)
	}
}

// TestBridgeChannelSnapshotDrivesReporterPush asserts a DaemonMsg.snapshot
// still drives reporter.Push exactly as the old WatchState path did.
func TestBridgeChannelSnapshotDrivesReporterPush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeCmux{}
	sig := &signal.CmuxSignaler{RunCmd: fc.run}
	rep := &fakeReporter{}
	stream := &fakeBridgeStream{ctx: ctx, inbound: make(chan *pb.DaemonMsg, 8)}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridgeChannel(ctx, cancel, stream, "workspace:1", 12345, sig, rep, newTestBridgeLogger(t), newTestAnnouncer())
	}()

	stream.inbound <- &pb.DaemonMsg{Kind: &pb.DaemonMsg_Snapshot{Snapshot: &pb.DaemonState{
		CaffeinateActive:  true,
		AutoResumeEnabled: true,
	}}}

	waitFor(t, 3*time.Second, func() bool { return rep.pushCount() >= 1 })

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("runBridgeChannel returned %v", err)
	}
}

// TestBridgeChannelWatchdogTeardownReturns is the regression test for the
// teardown deadlock: on the watchdog-timeout path (healthy connection, no
// snapshot within the push budget) runBridgeChannel MUST tear down and return.
//
// The parent ctx stays alive for the whole test — it is NEVER cancelled here —
// so the ONLY thing that can cancel the stream's context is the cancel func
// runBridgeChannel invokes on teardown. The fake stream's Recv blocks until
// that context is cancelled, faithful to real gRPC where Recv is bound to the
// stream's creation context. Before the cancel-propagation fix, teardown
// cancelled a fresh child context instead, leaving Recv (bound to the parent)
// blocked forever; runBridgeChannel then hung on <-recvDone and this test timed
// out. With the fix, teardown cancels the stream's own context, Recv returns,
// and runBridgeChannel returns the watchdog error within the budget.
func TestBridgeChannelWatchdogTeardownReturns(t *testing.T) {
	// Live parent that outlives the call: we deliberately do NOT cancel it, so
	// only the teardown cancel can unblock the stream's Recv.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeCmux{}
	sig := &signal.CmuxSignaler{RunCmd: fc.run}
	// Never fed: with no inbound message, the ~4s push-budget watchdog trips
	// and drives teardown. Recv blocks on ctx.Done until teardown cancels ctx.
	stream := &fakeBridgeStream{ctx: ctx, inbound: make(chan *pb.DaemonMsg)}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridgeChannel(ctx, cancel, stream, "workspace:1", 12345, sig, &fakeReporter{}, newTestBridgeLogger(t), newTestAnnouncer())
	}()

	// The watchdog budget is ~4s; give generous margin under -race. A hang here
	// (rather than a returned error) is the pre-fix deadlock.
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("runBridgeChannel returned nil error; want a watchdog teardown error")
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("runBridgeChannel returned %v; want the watchdog push-missed error, not a bare cancel", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runBridgeChannel did not return after watchdog fired: teardown deadlock")
	}
}
