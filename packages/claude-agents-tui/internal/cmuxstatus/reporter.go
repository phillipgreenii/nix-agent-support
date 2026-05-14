package cmuxstatus

import (
	"context"
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

// Stubs filled in by Tasks 3-4.
func (c *cmuxReporter) Push(Snapshot)         {}
func (c *cmuxReporter) Notify(string, string) {}
func (c *cmuxReporter) Clear()                {}
