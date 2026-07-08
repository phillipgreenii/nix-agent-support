package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/versioncmp"
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
	daemonVersion     string
}

// stateFromDaemon extracts a bridgeState from a DaemonState message and
// marks it initialized.
func stateFromDaemon(s *pb.DaemonState) bridgeState {
	return bridgeState{
		initialized:       true,
		caffeinateActive:  s.GetCaffeinateActive(),
		autoResumeEnabled: s.GetAutoResumeEnabled(),
		daemonVersion:     s.GetDaemonVersion(),
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
// selfVersion is the bridge's own build id (the package-main `version` global).
// On the initial tick, if it differs from the daemon's reported version (both
// non-empty), diffAndLog emits a warning after the summary — surfacing the
// stale-daemon case (rebuild+activate without restarting the launchd daemon).
// There is intentionally no diff-across-ticks version check: a daemon's version
// is fixed for its process lifetime, so such a branch would be unreachable. The
// initial-state branch fires on the first snapshot of every (re)connection
// because prev resets per runBridgeChannel call.
//
// Returns curr so callers can `prev = diffAndLog(prev, curr, selfVersion, log)`.
func diffAndLog(prev, curr bridgeState, selfVersion string, log func(string)) bridgeState {
	if !prev.initialized {
		log(fmt.Sprintf("initial state: %s, %s",
			caffeinatePhrase(curr.caffeinateActive),
			autoNudgePhrase(curr.autoResumeEnabled)))
		if versioncmp.Mismatch(selfVersion, curr.daemonVersion) {
			log("⚠ daemon version differs from this bridge — restart daemon")
		}
		return curr
	}
	if prev.caffeinateActive != curr.caffeinateActive {
		log(caffeinatePhrase(curr.caffeinateActive))
	}
	if prev.autoResumeEnabled != curr.autoResumeEnabled {
		log(autoNudgePhrase(curr.autoResumeEnabled))
	}
	return curr
}

// formatBridgeLine renders one operator-facing terminal line: a local
// date+time stamp and the message, with no "cmux-bridge:" prefix.
func formatBridgeLine(ts time.Time, msg string) string {
	return ts.Format("2006-01-02 15:04:05") + " " + msg
}

func caffeinatePhrase(on bool) string {
	if on {
		return "Caffeinated enabled"
	}
	return "Caffeinated disabled"
}

func autoNudgePhrase(on bool) string {
	if on {
		return "Auto Nudge enabled"
	}
	return "Auto Nudge disabled"
}

// bridgeSessionInfo is the display tuple the bridge logs for one session.
// SessionID is the stable identifier (always present); Name is the
// human-set label (may be empty).
type bridgeSessionInfo struct {
	SessionID string
	Name      string
}

// bridgeSessions is the set of sessions visible in this bridge's cmux
// workspace at one moment in time. Keyed by PID.
//
// initialized matches the bridgeState convention: false → emit the full
// list once; true → only emit add/remove deltas thereafter.
type bridgeSessions struct {
	initialized bool
	byPID       map[int]bridgeSessionInfo
}

// sessionsFromDaemon extracts the per-workspace session set from a
// DaemonState message.
func sessionsFromDaemon(s *pb.DaemonState, ws string) bridgeSessions {
	out := bridgeSessions{initialized: true, byPID: map[int]bridgeSessionInfo{}}
	for _, d := range s.GetDirs() {
		for _, sv := range d.GetSessions() {
			if sv.GetCmuxWorkspaceId() != ws {
				continue
			}
			out.byPID[int(sv.GetPid())] = bridgeSessionInfo{
				SessionID: sv.GetSessionId(),
				Name:      sv.GetName(),
			}
		}
	}
	return out
}

// formatSessionEntry renders the per-session log line content used by both
// initial-roster and add/remove deltas. Format: "<pid> <sessionid>/<name>"
// when both fields are non-empty; falls back to whichever is present.
func formatSessionEntry(pid int, info bridgeSessionInfo) string {
	switch {
	case info.SessionID != "" && info.Name != "":
		return fmt.Sprintf("%d %s/%s", pid, info.SessionID, info.Name)
	case info.SessionID != "":
		return fmt.Sprintf("%d %s", pid, info.SessionID)
	case info.Name != "":
		return fmt.Sprintf("%d %s", pid, info.Name)
	default:
		return fmt.Sprintf("%d <unknown>", pid)
	}
}

// diffSessionsAndLog emits "+<pid> <sessionid>/<name>" for each new session
// and "-<pid> <sessionid>/<name>" for each session that closed since the
// last tick. On the initial tick (prev.initialized == false) it emits a
// "+" line for every session it currently sees so pane operators get a
// full roster on bridge startup.
//
// Returns curr so callers can `prev = diffSessionsAndLog(prev, curr, log)`.
func diffSessionsAndLog(prev, curr bridgeSessions, log func(string)) bridgeSessions {
	if !prev.initialized {
		for pid, info := range curr.byPID {
			log("+" + formatSessionEntry(pid, info))
		}
		return curr
	}
	for pid, info := range curr.byPID {
		if _, ok := prev.byPID[pid]; !ok {
			log("+" + formatSessionEntry(pid, info))
		}
	}
	for pid, info := range prev.byPID {
		if _, ok := curr.byPID[pid]; !ok {
			log("-" + formatSessionEntry(pid, info))
		}
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
		fmt.Fprintln(os.Stderr, "CMUX_WORKSPACE_ID not set; nothing to bridge")
		os.Exit(2)
	}
	_ = args

	cfg, _ := config.Load(config.DefaultPath())
	config.ApplyOTelEnv(cfg.OTel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emit, err := otel.NewConnectionEmitter(ctx, otel.ConnOptions{
		ServiceName:    "pa-monitor",
		ServiceVersion: version,
		Component:      "cmux-bridge",
	})
	if err != nil {
		emit = nil // best-effort; never block the sidebar on OTel
	}
	defer emit.Shutdown(ctx)

	home, _ := os.UserHomeDir()
	log := newBridgeLogger(filepath.Join(home, ".cache", "pa-monitor"), emit)

	reporter := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable: true,
		Logf:   func(s string) { log.Detail("cmux.reporter", map[string]string{"msg": s}) },
	})
	defer reporter.Clear()

	announcer := &connAnnouncer{
		term:   log.Term,
		detail: log.Detail,
		gauge:  emit.RecordDaemonConnected,
	}

	logBridgeVersions(ctx, log)

	// One CmuxSignaler for the bridge's lifetime: its ps/surface caches are
	// reused across the server-PID probe and every local delivery. The bridge
	// is a cmux descendant, so its FindCmuxServerAncestor and Send calls both
	// resolve against the same in-tree cmux server.
	cmuxSig := &signal.CmuxSignaler{}

	// Resolve the cmux server PID once, retrying until non-zero: the Register
	// message keys the daemon-side bridge registry on (serverPID, bridgePID),
	// so a serverPID of 0 would be silently dropped by the daemon. Block here
	// (best-effort, ctx-cancellable) rather than freeze a bad value.
	serverPID := resolveServerPID(ctx, cmuxSig, log)
	if serverPID == 0 {
		return // ctx cancelled before a cmux server ancestor was found
	}

	for {
		if err := streamOnce(ctx, ws, serverPID, cmuxSig, reporter, log, announcer); err != nil {
			if ctx.Err() != nil {
				return
			}
			reporter.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateUnknown})
			announcer.disconnected(map[string]string{"error": err.Error()})
			time.Sleep(2 * time.Second)
			continue
		}
		return
	}
}

// resolveServerPID probes for the cmux server in the bridge's own ancestry,
// retrying with a short backoff until it finds one or ctx is cancelled.
// Returns 0 only when ctx is cancelled first. The bridge always runs as a
// descendant of a cmux server, so in steady state this returns on the first
// probe; the retry loop covers a race where the bridge starts before its
// ancestry is fully observable via ps.
func resolveServerPID(ctx context.Context, cmuxSig *signal.CmuxSignaler, log *bridgeLogger) int {
	const probeBackoff = 2 * time.Second
	for {
		if pid, ok := cmuxSig.FindCmuxServerAncestor(os.Getpid()); ok && pid > 0 {
			return pid
		}
		log.Detail("bridge.server_pid_probe", map[string]string{"result": "no cmux server ancestor yet"})
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(probeBackoff):
		}
	}
}

// logBridgeVersions prints the startup banner only when the daemon is
// reachable. If unreachable it stays silent on the pane (detail to log) — the
// reconnect loop will surface "Lost connection to daemon" instead.
func logBridgeVersions(ctx context.Context, log *bridgeLogger) {
	dialCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	client, err := rpcclient.Dial(dialCtx)
	if err != nil {
		log.Detail("bridge.version_probe", map[string]string{"version": version, "error": err.Error()})
		return
	}
	defer client.Close()
	state, err := client.C.GetState(dialCtx, &pb.GetStateRequest{})
	if err != nil {
		log.Detail("bridge.version_probe", map[string]string{"version": version, "error": err.Error()})
		return
	}
	log.Term(fmt.Sprintf("pa-monitor bridge v%s (daemon v%s)", version, state.GetDaemonVersion()))
}

// bridgeHeartbeatInterval is how often the bridge sends a Heartbeat over the
// BridgeChannel stream to refresh its lastSeen timestamp on the daemon. Must
// be shorter than the daemon's bridge.Registry staleAfter window (30s) by
// enough margin to survive a missed message. ~10s gives 3 attempts before the
// daemon flags us as disconnected.
const bridgeHeartbeatInterval = 10 * time.Second

// streamOnce establishes one BridgeChannel session and drives it to
// completion, returning an error the outer reconnect loop uses to retry. It is
// intentionally thin: it owns dial + stream open + context lifetime and
// delegates the message loop to runBridgeChannel (which is unit-tested with a
// fake stream).
//
// The stream is created from ctx, so a blocked stream.Recv only unwinds when
// THAT context is cancelled. streamOnce therefore threads its own cancel into
// runBridgeChannel and lets the message loop invoke it on teardown; that cancel
// unblocks Recv on every exit path (watchdog, Send-error, Recv-error, or an
// outer ctx cancel). The defer here is the backstop for the dial/open error
// returns above, which never reach runBridgeChannel.
func streamOnce(ctx context.Context, ws string, serverPID int, cmuxSig *signal.CmuxSignaler, reporter cmuxstatus.Reporter, log *bridgeLogger, announcer *connAnnouncer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	stream, err := client.C.BridgeChannel(ctx)
	if err != nil {
		return err
	}

	return runBridgeChannel(ctx, cancel, stream, ws, serverPID, cmuxSig, reporter, log, announcer)
}

// runBridgeChannel runs the BridgeChannel message loop over an established
// bidi stream until the stream fails or ctx is cancelled, returning the error
// that ended it (so the caller reconnects).
//
// Concurrency model (mirrors the daemon side in
// internal/daemon/bridge_channel.go):
//
//   - A single SENDER goroutine is the sole caller of stream.Send. It sends the
//     initial Register, then funnels periodic Heartbeats and per-delivery
//     DeliverResults — the latter arriving over the outbound channel — so
//     stream.Send is never called concurrently, as gRPC requires.
//   - A single RECEIVER goroutine is the sole caller of stream.Recv, forwarding
//     each (msg, err) to the main loop. Recv is bound to the context the stream
//     was created from, so it only unblocks when THAT context is cancelled;
//     teardown does so by invoking the cancel func the caller (streamOnce)
//     threaded in for exactly this purpose.
//   - The MAIN loop consumes received messages: a snapshot drives reporter.Push
//     (exactly as the old WatchState path did); a Deliver is dispatched to its
//     own HANDLER goroutine so a slow cmux send never head-of-line-blocks
//     snapshot handling or other deliveries.
//   - HANDLER goroutines resolve + send locally via cmuxSig, then enqueue their
//     DeliverResult onto the outbound channel.
//
// Teardown: the main loop breaks, then calls cancel — the CancelFunc for the
// context the stream was created from — which stops the sender, unblocks the
// receiver's Recv, and releases any handler blocked enqueuing a result. Every
// goroutine is joined before returning. The outbound channel is never closed,
// so no goroutine can send on a closed channel. cancel MUST be the stream's
// cancel (not a fresh child): a child cancel would leave Recv, bound to the
// parent, blocked forever and deadlock the join on recvDone.
func runBridgeChannel(
	ctx context.Context,
	cancel context.CancelFunc,
	stream pb.PaMonitor_BridgeChannelClient,
	ws string,
	serverPID int,
	cmuxSig *signal.CmuxSignaler,
	reporter cmuxstatus.Reporter,
	log *bridgeLogger,
	announcer *connAnnouncer,
) error {
	// outbound funnels every non-Register stream.Send through the sole sender
	// goroutine. Buffered so a delivery handler rarely blocks enqueuing a
	// result. Never closed — teardown is signalled by cancelling ctx.
	outbound := make(chan *pb.BridgeMsg, 16)

	// sendErrCh reports the first stream.Send failure (buffered, non-blocking
	// write) so the main loop can surface it as the return error.
	sendErrCh := make(chan error, 1)
	reportSendErr := func(err error) {
		select {
		case sendErrCh <- err:
		default:
		}
	}

	// Sender goroutine: sole stream.Send caller.
	var senderWG sync.WaitGroup
	senderWG.Add(1)
	go func() {
		defer senderWG.Done()
		reg := &pb.BridgeMsg{Kind: &pb.BridgeMsg_Register{Register: &pb.Register{
			BridgePid:   int32(os.Getpid()),
			ServerPid:   int32(serverPID),
			WorkspaceId: ws,
		}}}
		if err := stream.Send(reg); err != nil {
			reportSendErr(err)
			cancel()
			return
		}
		t := time.NewTicker(bridgeHeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				hb := &pb.BridgeMsg{Kind: &pb.BridgeMsg_Heartbeat{Heartbeat: &pb.Heartbeat{
					BridgePid: int32(os.Getpid()),
				}}}
				if err := stream.Send(hb); err != nil {
					reportSendErr(err)
					cancel()
					return
				}
			case msg := <-outbound:
				if err := stream.Send(msg); err != nil {
					reportSendErr(err)
					cancel()
					return
				}
			}
		}
	}()

	// Receiver goroutine: sole stream.Recv caller.
	type recvResult struct {
		msg *pb.DaemonMsg
		err error
	}
	recvCh := make(chan recvResult, 1)
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		for {
			m, e := stream.Recv()
			select {
			case recvCh <- recvResult{m, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()

	// Delivery handlers run concurrently; joined at teardown.
	var handlerWG sync.WaitGroup

	// Per-stream diff state: tracks observable toggles (caffeinate,
	// auto_resume) across snapshots so the bridge can emit human-readable
	// change events on the pane instead of being a silent mirror. Reset per
	// call: a reconnect re-emits the "initial state" line, which is desirable
	// since pane operators care about state across reconnects.
	var prev bridgeState
	var prevSessions bridgeSessions

	// Watchdog: the daemon pushes a snapshot every ~2s; a 4s budget on any
	// received message flags a stalled stream. A single reusable timer (reset
	// on each received message) avoids allocating a fresh time.After channel
	// every loop iteration. Timer.Reset is safe here without draining timer.C
	// because Go 1.23+ timers deliver no stale value after Reset.
	const pushBudget = 4 * time.Second
	timer := time.NewTimer(pushBudget)
	defer timer.Stop()

	var loopErr error
loop:
	for {
		select {
		case <-ctx.Done():
			loopErr = ctx.Err()
			break loop
		case <-timer.C:
			log.Detail("bridge.push_missed", map[string]string{"budget": pushBudget.String()})
			loopErr = fmt.Errorf("push missed: no message in %s", pushBudget)
			break loop
		case r := <-recvCh:
			if r.err != nil {
				loopErr = r.err
				break loop
			}
			// Refresh the budget: we just heard from the stream.
			timer.Reset(pushBudget)
			if r.msg == nil {
				continue
			}
			announcer.connected()
			if snap := r.msg.GetSnapshot(); snap != nil {
				prev = diffAndLog(prev, stateFromDaemon(snap), version, log.Term)
				prevSessions = diffSessionsAndLog(prevSessions, sessionsFromDaemon(snap, ws), log.Term)
				reporter.Push(snapshotForWorkspace(snap, ws))
				continue
			}
			if d := r.msg.GetDeliver(); d != nil {
				handlerWG.Add(1)
				go func(d *pb.Deliver) {
					defer handlerWG.Done()
					deliverLocally(ctx, cmuxSig, d, outbound, log)
				}(d)
			}
		}
	}

	// Teardown: signal all goroutines, then join before returning so no
	// goroutine outlives this call. Handlers blocked enqueuing a result unblock
	// via ctx.Done; the sender exits on ctx.Done; the receiver's Recv is
	// unblocked by the cancelled stream context.
	cancel()
	handlerWG.Wait()
	senderWG.Wait()
	<-recvDone

	// A stream.Send failure is the more actionable cause; prefer it over a
	// bare context.Canceled from teardown.
	select {
	case e := <-sendErrCh:
		if loopErr == nil || loopErr == context.Canceled {
			loopErr = e
		}
	default:
	}
	return loopErr
}

// deliverLocally resolves the cmux surface hosting d.TargetPid and injects
// d.Text, then enqueues a DeliverResult reflecting the outcome. It runs on a
// per-delivery handler goroutine; the outbound enqueue is abandoned if ctx is
// cancelled (teardown) so it never blocks on a drained sender.
func deliverLocally(ctx context.Context, cmuxSig *signal.CmuxSignaler, d *pb.Deliver, outbound chan<- *pb.BridgeMsg, log *bridgeLogger) {
	err := cmuxSig.Send(int(d.GetTargetPid()), d.GetText())
	errStr := ""
	if err != nil {
		errStr = err.Error()
		log.Detail("bridge.deliver_failed", map[string]string{
			"id":         d.GetId(),
			"target_pid": fmt.Sprintf("%d", d.GetTargetPid()),
			"error":      errStr,
		})
	}
	res := &pb.BridgeMsg{Kind: &pb.BridgeMsg_Result{Result: &pb.DeliverResult{
		Id:    d.GetId(),
		Ok:    err == nil,
		Error: errStr,
	}}}
	select {
	case outbound <- res:
	case <-ctx.Done():
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
		NudgeOn:      state.GetAutoResumeEnabled(),
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
