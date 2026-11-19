package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/blocks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/supervisor"
	"github.com/phillipgreenii/x/exitcode"
	"github.com/phillipgreenii/x/jsonllogger"
	"github.com/phillipgreenii/x/lifecycle"
	"github.com/phillipgreenii/x/pprofserver"
	"github.com/phillipgreenii/x/tmux"
)

const daemonHelp = `pg-assistants daemon: run the supervisor (launchd keeps it alive).

usage: pg-assistants daemon --config PATH [--shutdown-deadline 8s]

Keeps every enabled assistant of the configuration running in its own session
on the tmux server pg-assistants and recycles each session through the nudge,
interrupt, kill ladder (ADR 0128). It writes <state dir>/status.json (read by
pg-assistants status) and logs every action to <state dir>/events.jsonl.
SIGTERM or SIGINT stops it within the shutdown deadline; the sessions keep
running and the next daemon adopts them.

An assistant with a config problem is not started and the others run; a bad
global field falls back to its default. Exit 7 only when the file cannot be
read or parsed or a [paths] entry is invalid, 1 when the state dir, its
tmux.conf or the events log cannot be created, 10 when a shutdown overran its
deadline, 2 on a usage error.

PG_ASSISTANTS_PPROF_ADDR (a socket path or a loopback host:port) opens the
opt-in pprof endpoint.
`

const (
	// defaultShutdownDeadline is the shutdown deadline when the flag is not
	// given; the home-manager module always passes it, and derives launchd's
	// ExitTimeOut from the same number.
	defaultShutdownDeadline = 8 * time.Second
	// eventsMaxBytes caps events.jsonl; past it the file becomes
	// events.jsonl.1 (one backup) and a fresh one starts.
	eventsMaxBytes = 10 << 20
	pprofEnv       = "PG_ASSISTANTS_PPROF_ADDR"
)

// The daemon's own events: its start, the global config problems, and a
// failed final status write (named as the heartbeat's). Every other event is
// the supervisor's or, for the loops and the shutdown, x/lifecycle's.
const (
	evDaemonStart       = "daemon_start"
	evConfigProblem     = "config_problem"
	evStatusWriteFailed = "status_write_failed"
)

// daemon is `pg-assistants daemon`.
func (c *cli) daemon(ctx context.Context, args []string) int {
	fs := c.newFlagSet("daemon")
	path := fs.String("config", "", "the configuration file (required)")
	deadline := lifecycle.DeadlineFlag(fs, "shutdown-deadline", defaultShutdownDeadline)
	pos, code, done := c.parse(fs, args, daemonHelp)
	if done {
		return code
	}
	const usage = "usage: pg-assistants daemon --config PATH [--shutdown-deadline 8s]"
	if len(pos) != 0 || *path == "" {
		fmt.Fprintln(c.stderr, usage)
		return exitcode.Usage
	}
	if *deadline <= 0 {
		fmt.Fprintln(c.stderr, "pg-assistants daemon: --shutdown-deadline must be positive")
		return exitcode.Usage
	}
	cfg, raw, code, ok := c.loadConfigRaw("daemon", *path)
	if !ok {
		return code
	}
	sum := sha256.Sum256(raw)
	configPath := *path
	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	p := cfg.Paths

	// The state dir is private: create it 0700 before anything is opened in
	// it, and make an existing one 0700 too (MkdirAll leaves its mode alone),
	// because the events log and the status file are created world-readable.
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants daemon: state dir: %v\n", err)
		return exitcode.Error
	}
	if err := os.Chmod(p.StateDir, 0o700); err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants daemon: state dir: %v\n", err)
		return exitcode.Error
	}
	tc := tmux.Client{Bin: p.Tmux, Socket: tmuxSocket, Conf: tmuxConfPath(p.StateDir), Run: c.run}
	if err := tc.WriteConf(supervisor.TmuxConfLine); err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants daemon: %v\n", err)
		return exitcode.Error
	}
	logFile, err := jsonllogger.OpenRotatingFile(eventsPath(p.StateDir), eventsMaxBytes)
	if err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants daemon: events log: %v\n", err)
		return exitcode.Error
	}
	defer func() { _ = logFile.Close() }()
	events := jsonllogger.NewLogger(logFile, supervisor.EventLogOptions()...)

	now := time.Now()
	ds := supervisor.DaemonStatus{
		PID:             c.pid,
		Version:         Version,
		StartedAt:       now,
		ConfigPath:      configPath,
		ConfigHash:      hex.EncodeToString(sum[:]),
		ControlStateDir: c.getenv("PG_CLAUDE_CONTROL_STATE_DIR"),
		ClaudeConfigDir: c.getenv("CLAUDE_CONFIG_DIR"),
	}
	// The daemon runs under the login shell's -l, not -i, so these two could
	// differ from the sessions'; the start event and the status record them.
	events.Log(ctx, slog.LevelInfo, evDaemonStart,
		"pid", ds.PID, "version", ds.Version, "config_path", ds.ConfigPath, "config_hash", ds.ConfigHash,
		"state_dir", p.StateDir, "pg_claude_control_state_dir", ds.ControlStateDir,
		"claude_config_dir", ds.ClaudeConfigDir, "shutdown_deadline_s", deadline.Seconds())
	for _, pr := range cfg.Problems {
		events.Log(ctx, slog.LevelWarn, evConfigProblem, "field", pr.Field, "error", pr.Msg)
		ds.ConfigProblems = append(ds.ConfigProblems, pr.Error())
	}

	// The start hold survives a restart: seed the block readings from the
	// previous status file. A missing or unreadable one seeds nothing. The
	// seeded readings go on the board at once: only an assistant loop's tick
	// publishes them, so with no valid assistant the next restart would
	// otherwise find them gone.
	tracker := blocks.NewTracker(cfg.Blocks)
	if prev, err := supervisor.ReadStatus(statusPath(p.StateDir)); err == nil {
		tracker.Seed(prev.Blocks, now)
	}
	board := supervisor.NewStatusBoard(statusPath(p.StateDir), ds)
	board.SetBlocks(tracker.Snapshot(now))

	stopPprof := pprofserver.StartFromEnv(pprofEnv, c.getenv, c.stderr)
	defer stopPprof()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	onEvent, signalled := signalWatch()
	g := lifecycle.New(runCtx, lifecycle.Options{
		OnEvent:  onEvent,
		Deadline: *deadline,
		ExitCode: exitcode.ShutdownOverrun,
		Log:      events,
		Notify:   c.notify,
		Exit:     c.exit,
	})
	deps := supervisor.Deps{
		Tmux:    tc,
		Control: control.Client{Bin: p.PgClaudeControl, Run: c.run},
		Trust:   newTrustFile(p.ClaudeJSON),
		Blocks:  tracker,
		Status:  board,
		Events:  events,
		Go:      g.Go,
		NewID:   c.newID,
		Stat:    os.Stat,
	}
	// Neither error below happens today (Run and every loop return nil); they
	// go to launchd's stderr log and fail the exit status.
	failed := false
	// Run returns once the group's context ends (a signal, a loop error).
	// g.Context() is derived from ctx (lifecycle.New(runCtx, ...)), which
	// contextcheck cannot see through the Group.
	if err := supervisor.Run(g.Context(), cfg, deps); err != nil { //nolint:contextcheck // derived from ctx via lifecycle.New
		fmt.Fprintf(c.stderr, "pg-assistants daemon: %v\n", err)
		failed = true
		cancel()
	}
	// Workaround for an x/lifecycle race: when every loop returns before the
	// context's AfterFunc runs, g.Run skips shutdown_complete. Waiting for the
	// shutdown_signal event first makes the order deterministic. The wait
	// always ends: Run returns only after the context is done. Remove it when
	// the x/lifecycle fix is adopted.
	<-signalled
	g.SetPhase("stop_loops")
	if err := g.Run(); err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants daemon: %v\n", err)
		failed = true
	}
	// The heartbeat returns with the context, so the loops' last ticks reach
	// the file only through this final flush, after every loop has returned.
	// g.Run has disarmed the watchdog by now, so the flush runs outside the
	// shutdown deadline, bounded only by launchd's ExitTimeOut: it is one
	// local file write, with no retry.
	g.SetPhase("flush_status")
	if err := board.Flush(time.Now()); err != nil {
		events.Log(ctx, slog.LevelError, evStatusWriteFailed, "error", err.Error())
	}
	if failed {
		return exitcode.Error
	}
	return exitcode.OK
}

// signalWatch returns a lifecycle.Options.OnEvent hook and a channel that the
// hook closes (once) when it sees the shutdown_signal event.
func signalWatch() (func(lifecycle.Event), <-chan struct{}) {
	ch := make(chan struct{})
	var once sync.Once
	return func(ev lifecycle.Event) {
		if ev.Name == lifecycle.EvSignal {
			once.Do(func() { close(ch) })
		}
	}, ch
}

// newUUID returns a random (version 4) UUID, a new session's id.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])  // crypto/rand.Read never fails: on failure it crashes the program
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
