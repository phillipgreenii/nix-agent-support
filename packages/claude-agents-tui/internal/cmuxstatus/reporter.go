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

// NewReporter returns a Cmux reporter when claude-agents-tui is itself running
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

// Push issues 3 cmux set-status calls (caffeinate, nudge, state) and, when
// HasProgress is true, one cmux set-progress call. All four share one 5-second
// context. Errors per call route to logf but do not short-circuit subsequent
// calls — partial-success beats all-or-nothing for sidebar UX.
func (c *cmuxReporter) Push(s Snapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c.runStatus(ctx, "caffeinate", onOff(s.CaffeinateOn), "bolt", caffeinateColor(s.CaffeinateOn))
	c.runStatus(ctx, "nudge", onOff(s.NudgeOn), "bell", nudgeColor(s.NudgeOn))

	stateVal, stateIcon, stateColor := stateAttrs(s.State, s.PausedResetAt)
	c.runStatus(ctx, "state", stateVal, stateIcon, stateColor)

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
}

// runStatus issues one cmux set-status with icon and color; logs failures.
func (c *cmuxReporter) runStatus(ctx context.Context, key, value, icon, color string) {
	_, err := c.run(ctx, "cmux", "set-status", key, value, "--icon", icon, "--color", color)
	if err != nil {
		c.log(fmt.Sprintf("cmux set-status %s: %v", key, err))
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func caffeinateColor(on bool) string {
	if on {
		return "#ffcc00"
	}
	return "#888888"
}

func nudgeColor(on bool) string {
	if on {
		return "#00aaff"
	}
	return "#888888"
}

func stateAttrs(s State, resetAt time.Time) (value, icon, color string) {
	switch s {
	case StateWorking:
		return "working", "play", "#00cc66"
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

// Clear removes every sidebar entry this reporter owns. Best-effort; partial
// failures are logged and ignored.
func (c *cmuxReporter) Clear() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range []string{"caffeinate", "nudge", "state"} {
		if _, err := c.run(ctx, "cmux", "clear-status", key); err != nil {
			c.log(fmt.Sprintf("cmux clear-status %s: %v", key, err))
		}
	}
	if _, err := c.run(ctx, "cmux", "clear-progress"); err != nil {
		c.log(fmt.Sprintf("cmux clear-progress: %v", err))
	}
}
