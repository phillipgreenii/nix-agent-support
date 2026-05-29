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

// bridgeState captures the small slice of DaemonState whose flips the
// bridge surfaces as log lines. It is the input to diffAndLog and is
// updated once per successful daemon tick.
//
// initialized distinguishes the very first observed state (where we want
// a single "initial state" summary line) from subsequent ticks (where we
// only log on diff).
type bridgeState struct {
	initialized       bool
	caffeinateActive  bool
	autoResumeEnabled bool
}

// stateFromDaemon extracts a bridgeState from a DaemonState message and
// marks it initialized.
func stateFromDaemon(s *pb.DaemonState) bridgeState {
	return bridgeState{
		initialized:       true,
		caffeinateActive:  s.GetCaffeinateActive(),
		autoResumeEnabled: s.GetAutoResumeEnabled(),
	}
}

// diffAndLog compares prev and curr and emits one log line per observable
// state-change event. On the very first tick (prev.initialized == false)
// it emits a single "initial state" summary line instead of synthesizing
// flips against the zero value — this avoids spurious "caffeinate -> false"
// noise at startup.
//
// Nudge dispatch is intentionally NOT diffed here: nudges are RPC-level
// one-shot events (NudgeQueue/NudgeCancel) that the DaemonState message
// does not expose as a counter or per-tick event list, so there is nothing
// for the bridge to diff against. If a future proto change adds a
// nudge-event signal to DaemonState, extend bridgeState + diffAndLog here.
//
// Returns curr so callers can `prev = diffAndLog(prev, curr, log)`.
func diffAndLog(prev, curr bridgeState, log func(string)) bridgeState {
	if !prev.initialized {
		log(fmt.Sprintf("initial state: caffeinate=%v auto_resume=%v",
			curr.caffeinateActive, curr.autoResumeEnabled))
		return curr
	}
	if prev.caffeinateActive != curr.caffeinateActive {
		log(fmt.Sprintf("caffeinate -> %v", curr.caffeinateActive))
	}
	if prev.autoResumeEnabled != curr.autoResumeEnabled {
		log(fmt.Sprintf("auto_resume -> %v", curr.autoResumeEnabled))
	}
	return curr
}

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

	logBridgeVersions(ctx)

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

// logBridgeVersions does a best-effort one-shot GetState to learn the daemon
// version, then prints both the bridge's own version and the daemon's. Stays
// silent if the daemon is unreachable — the main watch loop already handles
// reconnect/backoff with its own diagnostics.
func logBridgeVersions(ctx context.Context) {
	dialCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	client, err := rpcclient.Dial(dialCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux-bridge: version=%s (daemon unreachable)\n", version)
		return
	}
	defer client.Close()
	state, err := client.C.GetState(dialCtx, &pb.GetStateRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux-bridge: version=%s (daemon GetState: %v)\n", version, err)
		return
	}
	fmt.Fprintf(os.Stderr, "cmux-bridge: version=%s daemon=%s\n", version, state.GetDaemonVersion())
}

// bridgeHeartbeatInterval is how often the bridge re-calls RegisterBridge
// on the daemon to refresh its lastSeen timestamp. Must be shorter than the
// daemon's bridge.Registry staleAfter window (30s) by enough margin to
// survive a missed call. ~10s gives 3 attempts before the daemon flags us
// as disconnected.
const bridgeHeartbeatInterval = 10 * time.Second

// registerBridge calls the daemon's RegisterBridge RPC on the given client.
// Failures are non-fatal — the bridge keeps streaming state; the only
// effect of a failed registration is that sessions in this cmux workspace
// will surface as "cmux (no bridge)" until a later attempt succeeds.
func registerBridge(ctx context.Context, client *rpcclient.Client, ws string) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := client.C.RegisterBridge(cctx, &pb.RegisterBridgeRequest{
		WorkspaceId: ws,
		BridgePid:   int32(os.Getpid()),
	}); err != nil {
		// Older daemons (pre-RPC) will return Unimplemented; that's fine,
		// just log at debug level.
		fmt.Fprintf(os.Stderr, "cmux-bridge: RegisterBridge: %v\n", err)
	}
}

func streamOnce(ctx context.Context, ws string, reporter cmuxstatus.Reporter) error {
	client, err := rpcclient.Dial(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	// Announce ourselves to the daemon so it can refine "cmux" terminal-host
	// labels for sessions in our workspace. Then start a goroutine that
	// re-registers every bridgeHeartbeatInterval as a liveness heartbeat.
	registerBridge(ctx, client, ws)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go func() {
		t := time.NewTicker(bridgeHeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-t.C:
				registerBridge(heartbeatCtx, client, ws)
			}
		}
	}()

	stream, err := client.C.WatchState(ctx, &pb.WatchStateRequest{PushIntervalMs: 2000})
	if err != nil {
		return err
	}

	// Watchdog: client requires 2s pushes from the server; 4s budget.
	const pushBudget = 4 * time.Second
	type recvResult struct {
		msg *pb.DaemonState
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

	// Per-stream diff state: tracks observable toggles (caffeinate,
	// auto_resume) across ticks so the bridge can emit human-readable
	// change events on stderr instead of being a silent mirror. Reset
	// per streamOnce call: a reconnect re-emits the "initial state" line,
	// which is desirable since pane operators care about state across
	// reconnects.
	var prev bridgeState
	logChange := func(msg string) {
		fmt.Fprintln(os.Stderr, "cmux-bridge:", msg)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pushBudget):
			return fmt.Errorf("push missed: no message in %s", pushBudget)
		case r := <-recvCh:
			if r.err != nil {
				return r.err
			}
			next()
			if r.msg == nil {
				continue
			}
			prev = diffAndLog(prev, stateFromDaemon(r.msg), logChange)
			snap := snapshotForWorkspace(r.msg, ws)
			reporter.Push(snap)
		}
	}
}

// snapshotForWorkspace filters daemon state down to sessions whose
// CMUX_WORKSPACE_ID matches the bridge's workspace and rolls them up
// into a cmuxstatus.Snapshot.
//
// When no session in the daemon's state carries the bridge's workspace
// id, the snapshot reflects the global aggregate so the sidebar still
// shows something useful (e.g. caffeinate state).
func snapshotForWorkspace(state *pb.DaemonState, ws string) cmuxstatus.Snapshot {
	var working, idle, dormant int
	matched := false
	for _, d := range state.GetDirs() {
		for _, sv := range d.GetSessions() {
			if sv.GetCmuxWorkspaceId() != ws {
				continue
			}
			matched = true
			switch sv.GetStatus() {
			case "working":
				working++
			case "idle":
				idle++
			default:
				dormant++
			}
		}
	}
	if !matched {
		for _, d := range state.GetDirs() {
			working += int(d.GetWorkingN())
			idle += int(d.GetIdleN())
			dormant += int(d.GetDormantN())
		}
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
