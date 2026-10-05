package apply

import (
	"context"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/item"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// CmdFactory constructs the *exec.Cmd used to invoke pg-connector or pg-desk.
// Production code uses exec.CommandContext; tests swap in a factory spawning a
// reentrant test-helper process.
type CmdFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// Env is everything an apply run (and the hook packets built on it) needs to
// reach the tracker and pg-desk.
type Env struct {
	Command CmdFactory
	Config  *config.Config
	Clock   func() time.Time // injectable; production time.Now
	// Stderr receives diagnostics that do not change an outcome (a failed
	// refresh, a hook error). nil means os.Stderr. Added after the pinned
	// shape; additive.
	Stderr io.Writer
}

func (e Env) command() CmdFactory {
	if e.Command == nil {
		return func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, name, args...)
		}
	}
	return e.Command
}

func (e Env) stderr() io.Writer {
	if e.Stderr == nil {
		return os.Stderr
	}
	return e.Stderr
}

func (e Env) now() time.Time {
	if e.Clock == nil {
		return time.Now()
	}
	return e.Clock()
}

// Outcome is what happened to one action.
type Outcome string

const (
	OutcomeApplied           Outcome = "applied"
	OutcomeDeduped           Outcome = "deduped"
	OutcomeFailed            Outcome = "failed"
	OutcomeSkippedDependency Outcome = "skipped-dependency"
)

// Event is one action's outcome, handed to every Hook.
type Event struct {
	Action     action.Action
	Outcome    Outcome
	WorkItemID string // created or touched work-item id ("" for annotations)
	Err        error
	Seq        int64 // routed item's metadata.seq
	HasSeq     bool  // false when apply ran without --from-item
}

// Hook observes a run. The audit and failure packets implement it.
type Hook interface {
	// After is called synchronously, in order, after every action. An error is
	// reported on stderr and makes the run exit 2.
	After(ctx context.Context, env Env, ev Event) error
	// Finish is called once after the last action; same error rule.
	Finish(ctx context.Context, env Env, evs []Event) error
}

// Input is one apply run.
type Input struct {
	Type, ID string
	View     *view.View
	Actions  []action.Action
	Item     *item.Routed // nil without --from-item
	Env      Env
	Hooks    []Hook
}

// Result is a run's outcome.
type Result struct {
	Events   []Event
	ExitCode int // 0 all applied/deduped or none needed; 2 some failed
}
