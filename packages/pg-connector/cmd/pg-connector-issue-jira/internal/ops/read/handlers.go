package read

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The seven op names.
const (
	OpShow         = "show"
	OpList         = scriptout.OpList
	OpChildren     = "children"
	OpSearch       = "search"
	OpListActivity = scriptout.OpListActivity
	OpDeps         = "deps"
	OpAuthStatus   = scriptout.OpAuthStatus
)

// Ops lists the op names Handlers provides, for the assembly's drift guard.
var Ops = []string{OpShow, OpList, OpChildren, OpSearch, OpListActivity, OpDeps, OpAuthStatus}

// Summary is the summary read-through as show uses it. *summary.Service
// satisfies it.
type Summary interface {
	Ensure(ctx context.Context, key string, opts summary.EnsureOptions) (summary.Result, error)
}

var _ Summary = (*summary.Service)(nil)

// Detail is the detail group's Ensure as show uses it. The shared group service
// satisfies it (detail.Service.Group()), and so does detail.Service itself.
type Detail interface {
	Ensure(ctx context.Context, id string, want []string, opt groupsvc.EnsureOptions) (groupsvc.EnsureResult, error)
}

var _ Detail = (*groupsvc.Service)(nil)

// Queries is the query runner as list uses it. *lq.Runner satisfies it.
type Queries interface {
	Expr(name string) ([]string, bool)
	Row(ctx context.Context, key lq.Key) (store.QueryRow, bool, error)
	Fetch(ctx context.Context, key lq.Key, opts ...lq.FetchOptions) (store.QueryRow, error)
}

var _ Queries = (*lq.Runner)(nil)

// WatchEngine is the change engine as the watch option uses it. *changes.Engine
// satisfies it through the shared engine it embeds.
type WatchEngine interface {
	WatchOne(ctx context.Context, st *store.Store, w engine.WatchRequest) (engine.WatchResult, error)
}

var _ WatchEngine = (*engine.Engine)(nil)

// PauseSource reports the 429 pause in force. *origin.Runner satisfies it.
type PauseSource interface {
	Paused() (until time.Time, reason string, ok bool)
}

var _ PauseSource = (*origin.Runner)(nil)

// Deps wires the handlers. Every field is optional in the sense that an op that
// needs a missing one answers unavailable; the assembly passes them all.
type Deps struct {
	// Auth is the daemon's one authentication tracker: auth_status marks it and
	// the stored-content fallbacks read it.
	Auth *origin.AuthState
	// Summary and Detail are the two group read-throughs behind show.
	Summary Summary
	Detail  Detail
	// Runner is the query runner behind list.
	Runner Queries
	// Settings is the daemon's own query settings; nil means Runner decides
	// which names are defined.
	Settings lq.Settings
	// Chg is the change engine the watch option goes through.
	Chg WatchEngine
	// Store is the daemon store.
	Store *store.Store
	// Backend is the governed backend (internal.New over the *origin.Runner)
	// behind the pass-through ops.
	Backend *internal.Backend
	// Pause names the 429 pause in force in an unavailable answer; nil means the
	// answer carries only the failure's own text.
	Pause PauseSource
	// Expiry is how long stored content may be served stale; zero means
	// config.DefaultExpiry.
	Expiry time.Duration
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers returns exactly the ops of Ops, keyed by op name, each at its
// capability's schema version: schema.IssueSchemaVersion for show, list,
// children, deps and auth_status, schema.SearchSchemaVersion for search and
// schema.ActivitySchemaVersion for list_activity.
func Handlers(deps Deps) map[string]scriptout.OpHandler {
	if deps.Clock == nil {
		deps.Clock = governor.SystemClock{}
	}
	if deps.Expiry <= 0 {
		deps.Expiry = config.DefaultExpiry
	}
	h := &handlers{deps: deps}
	out := map[string]scriptout.OpHandler{
		OpShow: {SchemaVersion: schema.IssueSchemaVersion, Handle: h.show},
		OpList: {SchemaVersion: schema.IssueSchemaVersion, Handle: h.list},
	}
	for op, handler := range h.passthrough() {
		out[op] = handler
	}
	return out
}

type handlers struct{ deps Deps }

func (h *handlers) now() time.Time { return h.deps.Clock.Now() }

// taxonomy is the closed set of wire sentinels (scriptout's seven codes).
var taxonomy = []error{
	scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrUnavailable,
	scriptout.ErrUnknownOp, scriptout.ErrVersionMismatch, scriptout.ErrInvalidArgument,
	scriptout.ErrQueryNotRecognized,
}

func inTaxonomy(err error) bool {
	for _, s := range taxonomy {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

func unavailable(format string, a ...any) error {
	return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf(format, a...))
}

// wire maps a failure onto the closed taxonomy: a wire error passes unchanged, a
// finished context is unavailable naming the deadline, and anything else is
// unavailable with its own text. An unavailable answer is then refined: while
// the daemon is unauthenticated it is unauthenticated (nothing stored served it,
// or the caller would not have an error), and while a 429 pause is in force the
// message names the pause when it does not already.
func (h *handlers) wire(err error) error {
	out := err
	switch {
	case inTaxonomy(err):
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		out = unavailable("the read did not finish before the request ended: %v", err)
	default:
		out = unavailable("%v", err)
	}
	if !errors.Is(out, scriptout.ErrUnavailable) {
		return out
	}
	if a := h.deps.Auth; a != nil {
		if bad, cause := a.Unauthenticated(); bad {
			text := "the daemon is not authenticated with the origin"
			if cause != nil {
				text += ": " + cause.Error()
			}
			return scriptout.WrapError(scriptout.ErrUnauthenticated, text)
		}
	}
	if note := h.pauseNote(); note != "" && !containsPause(out) {
		return scriptout.WrapError(scriptout.ErrUnavailable, out.Error()+" ("+note+")")
	}
	return out
}

// containsPause reports whether err already names a pause.
func containsPause(err error) bool {
	return errors.Is(err, origin.ErrPaused) || strings.Contains(err.Error(), "paused")
}

// pauseNote names the 429 pause in force, or "" when there is none.
func (h *handlers) pauseNote() string {
	if h.deps.Pause == nil {
		return ""
	}
	until, reason, ok := h.deps.Pause.Paused()
	if !ok {
		return ""
	}
	note := "the origin is paused until " + until.UTC().Format(time.RFC3339)
	if reason != "" {
		note += " (" + reason + ")"
	}
	return note
}
