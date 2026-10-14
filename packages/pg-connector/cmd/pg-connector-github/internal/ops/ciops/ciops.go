// Package ciops is the ci instance's three ops, served by the daemon:
//
//   - list_runs is a READ served from the store like the pr read ops. Its groups
//     are the stored summary (the PR's head, base and branch) and runs; fresh:
//     true forces both to origin. The result is the []schema.CIRun the old backend
//     returns, newest first, with the read-answer annotations (served_from,
//     stale, age_seconds, groups[]) on every element.
//   - get_logs is a METERED PASS-THROUGH: the daemon performs the call itself, so
//     the governor sees and limits its spend, and stores nothing. It is the old
//     op's porcelain call (gh run view --log --repo <repo> -- <run id>), booked as
//     one core request of class 1, purpose passthrough.
//   - rerun_failed is a WRITE through the daemon. It is serialized per PR, reads
//     origin for its own preconditions outside the cache, picks the newest run
//     with a rerunnable conclusion and asks GitHub to rerun its failed jobs by an
//     explicit-repository REST request (POST /repos/{owner}/{repo}/actions/runs/
//     {run_id}/rerun-failed-jobs; the old backend's `gh run rerun <id> --failed`
//     named no repository and relied on gh's working directory, which a daemon
//     does not have). It is a no-op success when a run of the PR's current head is
//     already queued or in progress, spends as class 2 with purpose write, and
//     after success invalidates the runs group and queues its refresh, so a
//     refresh that STARTED before the write neither overwrites nor reports a
//     change from it (groupsvc.Service.InvalidateAndQueue).
//
// Registration of the handlers is the assembly's.
package ciops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The ops, as the router keys them under the ci instance.
const (
	OpListRuns    = "list_runs"
	OpGetLogs     = "get_logs"
	OpRerunFailed = "rerun_failed"
)

// Ops is the set of ops this package serves, in a fixed order. Their
// auth_status entry is NOT taken: the identity package answers that op.
var Ops = []string{OpListRuns, OpGetLogs, OpRerunFailed}

// Engine is the part of the group refresh engine the ops use.
// *groupsvc.Service satisfies it.
type Engine interface {
	// Ensure is the read-through of the named groups of one PR.
	Ensure(ctx context.Context, id string, want []string, opt groupsvc.EnsureOptions) (groupsvc.EnsureResult, error)
	// Resolve maps a node id or owner/repo#n to the node id and canonical
	// reference (the one first-sighting origin call, at class 1).
	Resolve(ctx context.Context, id string) (string, entity.Ref, error)
	// SerializeWrite runs fn under the in-process lock of the PR.
	SerializeWrite(ctx context.Context, id string, fn func(ctx context.Context) error) error
	// InvalidateAndQueue marks the group stale and queues its refresh at class 2.
	InvalidateAndQueue(ctx context.Context, id, group string) error
}

// Feed is the REST gateway of the daemon: every request is admitted by the
// governor and its spend booked. *ratefeed.Feed satisfies it.
type Feed interface {
	REST(ctx context.Context, call ratefeed.RESTCall) (*ratefeed.Response, error)
}

// Logs reads the log of one run. The ported ci backend satisfies it
// (ciprovider.NewWithDeps(runner, nil)) when its gh runner is a *metered.Runner,
// so the call is admitted, limited and booked by the governor.
type Logs interface {
	GetLogs(ctx context.Context, runID, repo string) ([]byte, error)
}

// Runs fetches the runs group of a PR directly, for rerun_failed's
// precondition: the result is never stored. *runs.Fetcher satisfies it.
type Runs interface {
	Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome
}

// Deps is what the handlers need.
type Deps struct {
	Engine Engine
	Feed   Feed
	Logs   Logs
	Runs   Runs
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers returns the handlers of list_runs, get_logs and rerun_failed, keyed by
// op, each with schema.CISchemaVersion.
func Handlers(deps Deps) map[string]scriptout.OpHandler {
	h := &handler{deps: deps}
	if h.deps.Clock == nil {
		h.deps.Clock = governor.SystemClock{}
	}
	return map[string]scriptout.OpHandler{
		OpListRuns:    {SchemaVersion: schema.CISchemaVersion, Handle: h.listRuns},
		OpGetLogs:     {SchemaVersion: schema.CISchemaVersion, Handle: h.getLogs},
		OpRerunFailed: {SchemaVersion: schema.CISchemaVersion, Handle: h.rerunFailed},
	}
}

type handler struct{ deps Deps }

func (h *handler) now() time.Time { return h.deps.Clock.Now() }

// wireSentinels are the closed wire taxonomy.
var wireSentinels = []error{
	scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrUnavailable, scriptout.ErrUnknownOp,
	scriptout.ErrVersionMismatch, scriptout.ErrInvalidArgument, scriptout.ErrQueryNotRecognized,
}

func isWire(err error) bool {
	for _, s := range wireSentinels {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// wire maps an error of the engine, a fetch or a metered call onto the closed
// wire taxonomy: what is already one passes unchanged; a malformed id is
// invalid_argument; a PR that does not exist is not_found; a governor refusal, a
// rate-limit answer, an authentication failure and a missing resource are
// metered.WireError's; a context that ended is unavailable; anything else is
// unavailable.
func wire(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case isWire(err):
		return err
	case errors.Is(err, groupsvc.ErrBadRef):
		return scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("%s: %v", what, err))
	case errors.Is(err, groupsvc.ErrNoSuchPR), errors.Is(err, groups.ErrGone):
		return scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("%s: %v", what, err))
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("%s: not finished before the deadline (%v)", what, err))
	}
	if w := metered.WireError(err); isWire(w) {
		return w
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("%s: %v", what, err))
}

// blank reports whether s is empty or only white space.
func blank(s string) bool { return strings.TrimSpace(s) == "" }
