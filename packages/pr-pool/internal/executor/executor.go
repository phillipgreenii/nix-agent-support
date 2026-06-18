// Package executor runs one (role, item) dispatch. It owns the ccpool
// ensure→send→wait path (including the pg2-c1vp watchdog/single-terminal race)
// and the command path. The orchestrator selects an Executor by role.Type via
// For and hands it a Deps seam bag; Dispatch returns the failure action the
// executor itself performed on the bead (empty on success) — the orchestrator
// merges the observed created/closed/handed-back actions around it.
package executor

import (
	"context"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/report"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// Executor dispatches one item for a role and reports the failure action it took.
type Executor interface {
	Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error)
}

// Deps is the explicit seam bag the executor needs from the orchestrator. Fields
// are exported because the orchestrator builds Deps from a different package. The
// per-attempt ExternalID is resolved ONCE by the orchestrator (so the same id is
// reused for the deferred teardown Close); the executor never re-stamps.
type Deps struct {
	CC          ccpool.Runner
	BD          beads.Runner
	Cmd         query.Commander
	Log         *eventlog.Writer // may be nil (no-op)
	Cfg         config.Config
	Now         func() time.Time                           // clock seam; nil ⇒ time.Now
	Tick        func(context.Context, time.Duration) error // cancellable wait; nil ⇒ select poll
	UsageReader usage.Reader                               // nil ⇒ usage.NewTranscriptReader()
	ExternalID  string                                     // resolved once by the orchestrator
}

func (d Deps) clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) reader() usage.Reader {
	if d.UsageReader != nil {
		return d.UsageReader
	}
	return usage.NewTranscriptReader()
}

func (d Deps) commander() query.Commander {
	if d.Cmd != nil {
		return d.Cmd
	}
	return query.OSCommander{}
}

func (d Deps) waitPoll(ctx context.Context, dur time.Duration) error {
	if d.Tick != nil {
		return d.Tick(ctx, dur)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(dur):
		return nil
	}
}

// For returns the concrete executor for a role type. Any non-"command" type
// takes the ccpool path (matches today's orchestrator.workOneWithID switch).
func For(roleType string) Executor {
	if roleType == "command" {
		return commandExecutor{}
	}
	return ccpoolExecutor{}
}

// failureAction builds a single-bead Result for one failure verb.
func failureAction(verb report.Verb, beadID string) report.Result {
	return report.Result{Actions: []report.Action{{Verb: verb, Refs: []report.Ref{{Type: "bead", ID: beadID}}}}}
}
