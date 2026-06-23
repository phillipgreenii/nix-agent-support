package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
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
		log(fmt.Sprintf("initial state: %s, %s",
			caffeinatePhrase(curr.caffeinateActive),
			autoNudgePhrase(curr.autoResumeEnabled)))
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

	for {
		if err := streamOnce(ctx, ws, reporter, log, announcer); err != nil {
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
func registerBridge(ctx context.Context, client *rpcclient.Client, ws string, log *bridgeLogger) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := client.C.RegisterBridge(cctx, &pb.RegisterBridgeRequest{
		WorkspaceId: ws,
		BridgePid:   int32(os.Getpid()),
	}); err != nil {
		log.Detail("bridge.register_failed", map[string]string{"error": err.Error()})
	}
}

func streamOnce(ctx context.Context, ws string, reporter cmuxstatus.Reporter, log *bridgeLogger, announcer *connAnnouncer) error {
	client, err := rpcclient.Dial(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	// Announce ourselves to the daemon so it can refine "cmux" terminal-host
	// labels for sessions in our workspace. Then start a goroutine that
	// re-registers every bridgeHeartbeatInterval as a liveness heartbeat.
	registerBridge(ctx, client, ws, log)
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
				registerBridge(heartbeatCtx, client, ws, log)
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
	// change events on the pane instead of being a silent mirror. Reset
	// per streamOnce call: a reconnect re-emits the "initial state" line,
	// which is desirable since pane operators care about state across
	// reconnects.
	var prev bridgeState
	var prevSessions bridgeSessions

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pushBudget):
			log.Detail("bridge.push_missed", map[string]string{"budget": pushBudget.String()})
			return fmt.Errorf("push missed: no message in %s", pushBudget)
		case r := <-recvCh:
			if r.err != nil {
				return r.err
			}
			next()
			if r.msg == nil {
				continue
			}
			announcer.connected()
			prev = diffAndLog(prev, stateFromDaemon(r.msg), log.Term)
			prevSessions = diffSessionsAndLog(prevSessions, sessionsFromDaemon(r.msg, ws), log.Term)
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
