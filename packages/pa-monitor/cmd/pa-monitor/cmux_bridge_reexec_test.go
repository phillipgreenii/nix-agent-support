package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/otel"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/reexec"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

type reexecCall struct {
	attempt int
	outcome string
}

// reexecOpts builds a bridgeStreamOpts with the self-restart wiring populated:
// a spy recordReexec (appends to *calls) plus caller-owned base/gaveUp pointers
// that persist across the (fake) stream, mirroring runCmuxBridge's ownership.
func reexecOpts(self string, autoRestart bool, base *int, gaveUp *bool, calls *[]reexecCall) bridgeStreamOpts {
	return bridgeStreamOpts{
		heartbeat:    10 * time.Second,
		pushBudget:   10 * time.Second,
		selfVersion:  self,
		prev:         &bridgeState{},
		prevSessions: &bridgeSessions{},
		autoRestart:  autoRestart,
		gaveUp:       gaveUp,
		attemptBase:  base,
		recordReexec: func(a int, o string) { *calls = append(*calls, reexecCall{a, o}) },
	}
}

func TestEvalDaemonVersionMatchResetsAttemptBase(t *testing.T) {
	gaveUp, base := false, 2
	var calls []reexecCall
	opts := reexecOpts("v1", true, &base, &gaveUp, &calls)

	if err := evalDaemonVersion("v1", newTestBridgeLogger(t), opts); err != nil {
		t.Fatalf("matching version must not request reexec, got %v", err)
	}
	if base != 0 {
		t.Errorf("attemptBase = %d, want reset to 0 on convergence", base)
	}
	if len(calls) != 0 {
		t.Errorf("no metric expected on match, got %v", calls)
	}
}

func TestEvalDaemonVersionMismatchAutoRestartRequestsReexec(t *testing.T) {
	gaveUp, base := false, 0
	var calls []reexecCall
	opts := reexecOpts("old", true, &base, &gaveUp, &calls)

	err := evalDaemonVersion("new", newTestBridgeLogger(t), opts)
	if !errors.Is(err, errReexecRequested) {
		t.Fatalf("want errReexecRequested, got %v", err)
	}
	if gaveUp {
		t.Error("must not give up while attempt budget remains")
	}
	// The attempt metric belongs to the OUTER loop (just before the exec), not here.
	if len(calls) != 0 {
		t.Errorf("evalDaemonVersion must not emit the attempt metric, got %v", calls)
	}
}

func TestEvalDaemonVersionMismatchDisabledWarnsOnly(t *testing.T) {
	gaveUp, base := false, 0
	var calls []reexecCall
	opts := reexecOpts("old", false /* autoRestart off */, &base, &gaveUp, &calls)

	if err := evalDaemonVersion("new", newTestBridgeLogger(t), opts); err != nil {
		t.Fatalf("disabled path must warn-only (nil), got %v", err)
	}
	if gaveUp || len(calls) != 0 {
		t.Errorf("disabled path must not touch gaveUp (%v) or metrics (%v)", gaveUp, calls)
	}
}

func TestEvalDaemonVersionMismatchExhaustedGivesUp(t *testing.T) {
	gaveUp, base := false, reexec.MaxAttempts
	var calls []reexecCall
	opts := reexecOpts("old", true, &base, &gaveUp, &calls)

	if err := evalDaemonVersion("new", newTestBridgeLogger(t), opts); err != nil {
		t.Fatalf("exhausted budget must keep serving (nil), got %v", err)
	}
	if !gaveUp {
		t.Error("exhausted budget must set gaveUp")
	}
	if len(calls) != 1 || calls[0].outcome != otel.ReexecOutcomeExhausted || calls[0].attempt != reexec.MaxAttempts {
		t.Errorf("want one exhausted metric at attempt=%d, got %v", reexec.MaxAttempts, calls)
	}
}

func TestEvalDaemonVersionMismatchAlreadyGaveUp(t *testing.T) {
	gaveUp, base := true, 0
	var calls []reexecCall
	opts := reexecOpts("old", true, &base, &gaveUp, &calls)

	if err := evalDaemonVersion("new", newTestBridgeLogger(t), opts); err != nil {
		t.Fatalf("already-gave-up must keep serving (nil), got %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("no new metric once already gave up, got %v", calls)
	}
}

// TestBridgeChannelReexecSentinelPropagates drives runBridgeChannel with a
// NEW-vs-OLD snapshot and asserts the reexec sentinel survives the teardown
// precedence block (cmux_bridge.go send-error handling) and reaches the caller.
func TestBridgeChannelReexecSentinelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := &signal.CmuxSignaler{RunCmd: (&fakeCmux{}).run}
	stream := &fakeBridgeStream{ctx: ctx, inbound: make(chan *pb.DaemonMsg, 8)}

	gaveUp, base := false, 0
	var calls []reexecCall
	opts := reexecOpts("OLD", true, &base, &gaveUp, &calls)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridgeChannel(ctx, cancel, stream, "workspace:1", 12345, sig, &fakeReporter{}, newTestBridgeLogger(t), newTestAnnouncer(), 10*time.Minute, opts)
	}()

	stream.inbound <- &pb.DaemonMsg{Kind: &pb.DaemonMsg_Snapshot{Snapshot: &pb.DaemonState{DaemonVersion: "NEW"}}}

	select {
	case err := <-errCh:
		if !errors.Is(err, errReexecRequested) {
			t.Fatalf("runBridgeChannel returned %v, want errReexecRequested", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runBridgeChannel did not return the reexec sentinel")
	}
	// Safe to read after <-errCh: the loop goroutine has fully returned.
	if gaveUp {
		t.Error("must not give up on the first mismatch with budget remaining")
	}
}

// TestBridgeChannelReexecExhaustedKeepsServing asserts the exhausted give-up
// does NOT tear down: the snapshot is still served (reporter.Push) and the
// returned error is NOT the reexec sentinel.
func TestBridgeChannelReexecExhaustedKeepsServing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := &signal.CmuxSignaler{RunCmd: (&fakeCmux{}).run}
	rep := &fakeReporter{}
	stream := &fakeBridgeStream{ctx: ctx, inbound: make(chan *pb.DaemonMsg, 8)}

	gaveUp, base := false, reexec.MaxAttempts // already exhausted
	var calls []reexecCall
	opts := reexecOpts("OLD", true, &base, &gaveUp, &calls)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridgeChannel(ctx, cancel, stream, "workspace:1", 12345, sig, rep, newTestBridgeLogger(t), newTestAnnouncer(), 10*time.Minute, opts)
	}()

	stream.inbound <- &pb.DaemonMsg{Kind: &pb.DaemonMsg_Snapshot{Snapshot: &pb.DaemonState{DaemonVersion: "NEW"}}}

	// The snapshot is still served despite give-up (reporter.Push runs), proving
	// the stream was not torn down.
	waitFor(t, 3*time.Second, func() bool { return rep.pushCount() >= 1 })

	cancel()
	err := <-errCh
	if errors.Is(err, errReexecRequested) {
		t.Fatalf("give-up must NOT return the reexec sentinel, got %v", err)
	}
	if !gaveUp {
		t.Error("exhausted path must set gaveUp")
	}
	if len(calls) != 1 || calls[0].outcome != otel.ReexecOutcomeExhausted {
		t.Errorf("want one exhausted metric, got %v", calls)
	}
}

func TestClassifyBridgeResult(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		ctxErr error
		want   bridgeLoopVerdict
	}{
		{"nil -> stop", nil, nil, bridgeStop},
		{"ctx cancelled wins over sentinel -> stop", errReexecRequested, context.Canceled, bridgeStop},
		{"sentinel intercepted before disconnect -> reexec", errReexecRequested, nil, bridgeReexecNow},
		{"wrapped sentinel -> reexec", fmt.Errorf("stream: %w", errReexecRequested), nil, bridgeReexecNow},
		{"generic error -> disconnect", errors.New("connection reset"), nil, bridgeDisconnect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyBridgeResult(tt.err, tt.ctxErr); got != tt.want {
				t.Fatalf("classifyBridgeResult(%v, %v) = %v, want %v", tt.err, tt.ctxErr, got, tt.want)
			}
		})
	}
}
