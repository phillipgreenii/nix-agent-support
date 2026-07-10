package cmuxstatus

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// State enumerates the aggregate TUI state surfaced to the cmux sidebar.
type State int

const (
	StateUnknown State = iota
	StateDormant
	StateIdle
	StateWorking
	StatePaused
)

// Snapshot is the full sidebar state. Push always receives one of these.
type Snapshot struct {
	CaffeinateOn  bool
	NudgeOn       bool
	State         State
	PausedResetAt time.Time
	Progress      float64
	ProgressLabel string
	HasProgress   bool
	// WorkspaceColor is the cmux workspace accent color hex; "" means clear.
	// Only consulted when HasWorkspaceColor is true.
	WorkspaceColor string
	// HasWorkspaceColor false means "leave the workspace color unchanged" (no
	// opinion — e.g. daemon disconnected). true means WorkspaceColor is authoritative.
	HasWorkspaceColor bool
}

// Reporter pushes sidebar updates and notifications to cmux when invoked from
// inside a cmux workspace. The Noop implementation is used outside cmux or when
// the feature is disabled by config.
type Reporter interface {
	Push(s Snapshot)
	Notify(title, body string)
	Clear()
}

// Options configures NewReporter. RunCmd and LookupEnv are injectable for tests;
// nil falls back to exec.CommandContext and os.LookupEnv. Logf receives one line
// per cmux subprocess failure (typically wired to the TUI's signal-errors.log).
type Options struct {
	Enable    bool
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)
	Logf      func(string)
}

// NewReporter returns a Cmux reporter when pa-monitor is itself running
// inside cmux and Enable is true; a Noop otherwise.
func NewReporter(o Options) Reporter {
	if !o.Enable {
		return noop{}
	}
	lookup := o.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if v, _ := lookup("CMUX_WORKSPACE_ID"); v == "" {
		return noop{}
	}
	return &cmuxReporter{
		runCmd: o.RunCmd,
		logf:   o.Logf,
	}
}

type noop struct{}

func (noop) Push(Snapshot)         {}
func (noop) Notify(string, string) {}
func (noop) Clear()                {}

// cmuxReporter speaks to cmux via subprocesses. Method implementations land in
// Tasks 3 and 4; for now they are empty stubs so Task 2's gating tests can pass.
type cmuxReporter struct {
	runCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
	logf   func(string)

	// colorEmitted / lastWorkspaceColor track the last workspace color this
	// reporter told cmux about so Push can skip redundant CLI calls. Written
	// only from the bridge's single-threaded push path (single-writer), so no
	// synchronization is needed.
	colorEmitted       bool
	lastWorkspaceColor string
}

func (c *cmuxReporter) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.runCmd != nil {
		return c.runCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

func (c *cmuxReporter) log(msg string) {
	if c.logf != nil {
		c.logf(msg)
	}
}

// Push issues 1 cmux set-status call (single pill, key="claude-agents"),
// optionally one cmux set-progress call (when HasProgress), and — when the
// snapshot has an opinion on the workspace color (HasWorkspaceColor) and it
// differs from the last emitted one — one cmux workspace-action call. All share
// one 5-second context. Errors per call route to logf but do not short-circuit
// subsequent calls.
func (c *cmuxReporter) Push(s Snapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	value, icon, color := pillContent(s)
	if _, err := c.run(ctx, "cmux", "set-status", "claude-agents", value,
		"--icon", icon, "--color", color); err != nil {
		c.log(fmt.Sprintf("cmux set-status claude-agents: %v", err))
	}

	if s.HasProgress {
		v := s.Progress
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		args := []string{"set-progress", fmt.Sprintf("%.2f", v)}
		if s.ProgressLabel != "" {
			args = append(args, "--label", s.ProgressLabel)
		}
		if _, err := c.run(ctx, "cmux", args...); err != nil {
			c.log(fmt.Sprintf("cmux set-progress: %v", err))
		}
	}

	// Workspace accent color. HasWorkspaceColor false means "no opinion" — leave
	// whatever cmux currently shows. When it changes (or on the first emit — the
	// stale-color-on-crash-startup fix), push it once. No --workspace: the cmux
	// CLI defaults it to $CMUX_WORKSPACE_ID, matching the set-status/set-progress
	// calls above.
	if s.HasWorkspaceColor && (!c.colorEmitted || s.WorkspaceColor != c.lastWorkspaceColor) {
		if s.WorkspaceColor == "" {
			if _, err := c.run(ctx, "cmux", "workspace-action", "--action", "clear-color"); err != nil {
				c.log(fmt.Sprintf("cmux workspace-action clear-color: %v", err))
			}
		} else {
			if _, err := c.run(ctx, "cmux", "workspace-action", "--action", "set-color", "--color", s.WorkspaceColor); err != nil {
				c.log(fmt.Sprintf("cmux workspace-action set-color: %v", err))
			}
		}
		c.colorEmitted = true
		c.lastWorkspaceColor = s.WorkspaceColor
	}
}

// pillContent collapses Snapshot into the single-pill value, icon, and color.
// State leads ("working", "paused (resets ...)", "idle", "dormant"); the
// caffeinate and nudge toggles each contribute a "• caff"/"• nudge" suffix
// only when on. Icon and color follow state.
func pillContent(s Snapshot) (value, icon, color string) {
	value, icon, color = stateAttrs(s.State, s.PausedResetAt)
	if s.CaffeinateOn {
		value += " • caff"
	}
	if s.NudgeOn {
		value += " • nudge"
	}
	return value, icon, color
}

func stateAttrs(s State, resetAt time.Time) (value, icon, color string) {
	switch s {
	case StateWorking:
		return "working", "play", "#008f47"
	case StateIdle:
		return "idle", "pause", "#888888"
	case StatePaused:
		v := "paused"
		if !resetAt.IsZero() {
			v = fmt.Sprintf("paused (resets %s)", resetAt.Format("15:04"))
		}
		return v, "clock", "#ff8800"
	case StateDormant:
		return "dormant", "moon", "#555555"
	default:
		return "unknown", "circle", "#888888"
	}
}

// Notify issues one cmux notify call. Failures log but do not panic.
func (c *cmuxReporter) Notify(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.run(ctx, "cmux", "notify", "--title", title, "--body", body); err != nil {
		c.log(fmt.Sprintf("cmux notify: %v", err))
	}
}

// Clear removes the single sidebar entry this reporter owns plus the progress
// bar. Best-effort; partial failures are logged and ignored.
func (c *cmuxReporter) Clear() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.run(ctx, "cmux", "clear-status", "claude-agents"); err != nil {
		c.log(fmt.Sprintf("cmux clear-status claude-agents: %v", err))
	}
	if _, err := c.run(ctx, "cmux", "clear-progress"); err != nil {
		c.log(fmt.Sprintf("cmux clear-progress: %v", err))
	}
	if _, err := c.run(ctx, "cmux", "workspace-action", "--action", "clear-color"); err != nil {
		c.log(fmt.Sprintf("cmux workspace-action clear-color: %v", err))
	}
	c.colorEmitted = true
	c.lastWorkspaceColor = ""
}
