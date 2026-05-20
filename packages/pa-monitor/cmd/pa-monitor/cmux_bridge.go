package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// runCmuxBridge runs inside a cmux pane, streams DaemonState from the
// daemon, and drives the cmux sidebar. The bridge filters daemon state
// to the workspace identified by $CMUX_WORKSPACE_ID, then derives a
// cmuxstatus.Snapshot for it.
//
// Errors (daemon unreachable, missing env) are best-effort: the
// sidebar shows "daemon offline" rather than crashing the host pane.
func runCmuxBridge(args []string) {
	ws := os.Getenv("CMUX_WORKSPACE_ID")
	if ws == "" {
		fmt.Fprintln(os.Stderr, "cmux-bridge: CMUX_WORKSPACE_ID not set; nothing to bridge")
		os.Exit(2)
	}
	_ = args

	reporter := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable: true,
		Logf:   func(s string) { fmt.Fprintln(os.Stderr, "cmux-bridge:", s) },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer reporter.Clear()

	for {
		if err := streamOnce(ctx, ws, reporter); err != nil {
			if ctx.Err() != nil {
				return
			}
			reporter.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateUnknown})
			fmt.Fprintf(os.Stderr, "cmux-bridge: stream lost: %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}
		return
	}
}

func streamOnce(ctx context.Context, ws string, reporter cmuxstatus.Reporter) error {
	client, err := rpcclient.Dial(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	stream, err := client.C.WatchState(ctx, &pb.WatchStateRequest{HeartbeatIntervalMs: 2000})
	if err != nil {
		return err
	}

	// Heartbeat-miss watchdog: client requires 2s heartbeats; 4s budget.
	const hbBudget = 4 * time.Second
	type recvResult struct {
		msg *pb.WatchStateEvent
		err error
	}
	recvCh := make(chan recvResult, 1)
	next := func() {
		go func() {
			m, e := stream.Recv()
			recvCh <- recvResult{m, e}
		}()
	}
	next()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(hbBudget):
			return fmt.Errorf("heartbeat missed: no message in %s", hbBudget)
		case r := <-recvCh:
			if r.err != nil {
				return r.err
			}
			next()
			state := r.msg.GetState()
			if state == nil {
				continue
			}
			snap := snapshotForWorkspace(state, ws)
			reporter.Push(snap)
		}
	}
}

// snapshotForWorkspace filters daemon state down to sessions whose
// CMUX_WORKSPACE_ID matches the bridge's workspace and rolls them up
// into a cmuxstatus.Snapshot.
//
// Today's DaemonState doesn't carry per-session env on the wire, so the
// bridge approximates by treating the daemon's global aggregate state
// as the workspace's. Plan-3 follow-up: enrich the proto SessionView
// with the per-session env-lookup result for the workspace key.
func snapshotForWorkspace(state *pb.DaemonState, ws string) cmuxstatus.Snapshot {
	_ = ws

	var working, idle, dormant int
	for _, d := range state.GetDirs() {
		working += int(d.GetWorkingN())
		idle += int(d.GetIdleN())
		dormant += int(d.GetDormantN())
	}

	out := cmuxstatus.Snapshot{
		CaffeinateOn: state.GetCaffeinateActive(),
	}
	switch {
	case working > 0:
		out.State = cmuxstatus.StateWorking
	case idle > 0:
		out.State = cmuxstatus.StateIdle
	case dormant > 0:
		out.State = cmuxstatus.StateDormant
	default:
		out.State = cmuxstatus.StateUnknown
	}

	if state.GetWindowResetsAt() != nil {
		out.State = cmuxstatus.StatePaused
		out.PausedResetAt = state.GetWindowResetsAt().AsTime()
	}

	if b := state.GetActiveBlock(); b != nil && state.GetPlanCapUsd() > 0 {
		out.HasProgress = true
		out.Progress = b.GetWindowPct()
		out.ProgressLabel = fmt.Sprintf("block %.0f%%", b.GetWindowPct()*100)
	}

	return out
}
