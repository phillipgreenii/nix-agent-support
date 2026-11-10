package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
	"github.com/phillipgreenii/x/cmdrun"
)

// launchLabelEnv overrides the launch agent label the offline status asks
// launchctl about (the module that installs the agent names it).
const launchLabelEnv = "PG_CLAUDE_CODE_RECORDER_LAUNCH_LABEL"

const defaultLaunchLabel = "com.phillipg.pg-claude-code-recorder"

// launchctlState asks launchctl about label through the shared command runner.
func launchctlState(label string) string {
	return launchctlStateVia(cmdrun.Exec, runtime.GOOS, label)
}

// launchctlStateVia is launchctlState with the runner and OS injected so a
// unit test needs no real launchctl.
func launchctlStateVia(run cmdrun.Runner, goos, label string) string {
	if goos != "darwin" {
		return "unknown (no launchctl on " + goos + ")"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := run(ctx, cmdrun.Call{Name: "launchctl", Args: []string{"list", label}})
	if !r.OK() {
		return "not loaded"
	}
	if strings.Contains(string(r.Stdout)+string(r.Stderr), "\"PID\"") {
		return "loaded, running"
	}
	return "loaded, not running"
}

func (a *app) status(args []string) int {
	var c common
	var short, watch bool
	var every time.Duration
	fs := newFlagSet("status", a.errw)
	c.bind(fs)
	fs.BoolVar(&short, "short", false, "print one word: ok, behind, held or down")
	fs.BoolVar(&watch, "watch", false, "poll until the recorder is caught up")
	fs.DurationVar(&every, "interval", 2*time.Second, "poll interval for --watch")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	for {
		code, caught := a.statusOnce(cfg, cl, &c, short)
		if !watch || code == wire.ExitOffline || a.ctx.Err() != nil {
			if watch && code == wire.ExitDegraded && a.ctx.Err() != nil {
				return wire.ExitOK
			}
			return code
		}
		if caught {
			return code
		}
		select {
		case <-a.ctx.Done():
			return wire.ExitOK
		case <-time.After(every):
		}
	}
}

// statusOnce asks the daemon, falling back to the offline answer read from disk.
// It returns the exit code and whether the recorder reports caught up.
func (a *app) statusOnce(cfg config.Config, cl *client.Client, c *common, short bool) (int, bool) {
	var st wire.Status
	if err := cl.Post(a.ctx, "status", nil, &st); err != nil {
		var we *wire.Error
		if errors.As(err, &we) && we.Name == wire.ErrDaemonOffline {
			off := a.offlineStatus(cfg)
			a.printStatus(off, c.jsonOut, short)
			return a.emitWireError(c.jsonOut, err, cfg.Socket), false
		}
		return a.emitWireError(c.jsonOut, err, cfg.Socket), false
	}
	a.addRecent(&st, cl)
	a.printStatus(st, c.jsonOut, short)
	if st.Degraded && !c.partialOK {
		return a.emitErrorFor(c.jsonOut, wire.ErrBehind, st.Verdict, cfg.Socket), st.CaughtUp
	}
	return wire.ExitOK, st.CaughtUp
}

// offlineStatus is what status answers with no daemon: the socket path, the
// launch agent's state, the log path and the state directory's size on disk.
func (a *app) offlineStatus(cfg config.Config) wire.Status {
	label := a.getenv(launchLabelEnv)
	if label == "" {
		label = defaultLaunchLabel
	}
	log := cfg.LogFile
	if log == "" {
		log = "(standard error: see the launch agent's StandardErrorPath)"
	}
	return wire.Status{
		Verdict:     "down: daemon offline",
		Short:       "down",
		Socket:      cfg.Socket,
		LaunchAgent: a.launchState(label),
		LogPath:     log,
		StateBytes:  serve.DirBytes(cfg.StateDir),
		StageCounts: map[string]int{}, ParserVersions: map[string]int{},
		ProtocolVer: wire.ProtocolVersion,
	}
}

func (a *app) printStatus(st wire.Status, jsonOut, short bool) {
	switch {
	case short:
		_, _ = fmt.Fprintln(a.out, st.Short)
	case jsonOut:
		b, _ := json.Marshal(st)
		_, _ = fmt.Fprintln(a.out, string(b))
	default:
		_, _ = fmt.Fprintln(a.out, st.Verdict)
		if st.Short == "down" {
			_, _ = fmt.Fprintf(a.out, "  socket:       %s\n  launch agent: %s\n  log:          %s\n  state size:   %d bytes\n",
				st.Socket, st.LaunchAgent, st.LogPath, st.StateBytes)
			return
		}
		_, _ = fmt.Fprintf(a.out, "  caught up:    %t\n  copy lag:     %.0fs\n  parse lag:    %.0fs\n  held:         %d\n  free bytes:   %d\n  schema:       %d, protocol %d\n",
			st.CaughtUp, st.CopyLagS, st.ParseLagS, st.Held, st.FreeBytes, st.SchemaVersion, st.ProtocolVer)
		if st.CrashedRecent != nil && st.SuspectedRecent != nil {
			_, _ = fmt.Fprintf(a.out, "  crashed (7d): %d, suspected (crashed or unknown with a stale registry file): %d\n", *st.CrashedRecent, *st.SuspectedRecent)
		}
		if b := st.Backfill; b != nil && b.Running {
			_, _ = fmt.Fprintf(a.out, "  backfill:     %d of %d files copied, %d parsed, about %.0fs left\n", b.Copied, b.Total, b.Parsed, b.EtaS)
		}
		a.printStages(st)
	}
}

// stageOrder is the order status lists the stages in.
var stageOrder = []string{"live", "quiet", "held", "settled", "removal_pending", "removed"}

// printStages prints the stage counts, the last settle tick and one line per
// loop. The tick and loop lines are absent from an answer that lacks them.
func (a *app) printStages(st wire.Status) {
	if len(st.StageCounts) > 0 {
		parts := make([]string, 0, len(stageOrder))
		for _, k := range stageOrder {
			if n, ok := st.StageCounts[k]; ok {
				parts = append(parts, fmt.Sprintf("%s %d", k, n))
			}
		}
		_, _ = fmt.Fprintf(a.out, "  stages:       %s\n", strings.Join(parts, ", "))
	}
	if t := st.LastTick; t != nil {
		_, _ = fmt.Fprintf(a.out, "  last tick:    %dms, %d examined, ended %ds ago\n", t.DurationMS, t.Examined, t.AgeS)
	}
	if len(st.Loops) == 0 {
		return
	}
	_, _ = fmt.Fprintln(a.out, "  loops:")
	for _, name := range obs.Loops {
		l, ok := st.Loops[name]
		if !ok {
			continue
		}
		line := "never run"
		if l.LastDurationMS != nil && l.AgeS != nil {
			line = fmt.Sprintf("last %dms %s, ended %ds ago", *l.LastDurationMS, l.LastOutcome, *l.AgeS)
		}
		line += fmt.Sprintf(", in flight %ds", l.InFlightS)
		if l.Stalled {
			line += "  STALLED"
		}
		_, _ = fmt.Fprintf(a.out, "    %-15s %s\n", name, line)
	}
}
