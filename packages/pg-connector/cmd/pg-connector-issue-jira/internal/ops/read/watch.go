package read

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The watch option of show, as a one-off watch of that one issue. It is the only
// read that accepts it: a query run, a listing and a feed read refuse it. It is
// applied at one of two moments because of when the daemon can name the issue:
//
//   - for an issue the store already holds, BEFORE the read. A change the read's
//     own refresh then finds is a row of a watched issue, and the issue gets one
//     added row so its consumer hydrates it (engine.WatchLate).
//   - for an issue the daemon had never seen, AFTER the read stored it. That
//     first fetch appended no row, so the watch only sets the flag
//     (engine.WatchBaseline): the issue's next change is its first row.
//
// A read without the option never reaches either and never clears a watch.

// errNoWatch answers a read that carries the option when the daemon has no
// change engine or store to apply it with.
func errNoWatch() error {
	return scriptout.WrapError(scriptout.ErrUnavailable, "the watch option is not available")
}

// canWatch reports whether the watch option can be applied.
func (h *handlers) canWatch() bool { return h.deps.Chg != nil && h.deps.Store != nil }

// isKnown reports whether the daemon holds the issue: its entity row exists, is
// not tombstoned and has stored summary content.
func (h *handlers) isKnown(ctx context.Context, id string) (bool, error) {
	ctx = context.WithoutCancel(ctx)
	ent, ok, err := h.deps.Store.GetEntity(ctx, groups.EntityKind, id)
	if err != nil || !ok || !ent.TombstonedAt.IsZero() {
		return false, err
	}
	g, ok, err := h.deps.Store.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	if err != nil {
		return false, err
	}
	return ok && len(g.Content) > 0, nil
}

func (h *handlers) watchOne(ctx context.Context, id string, mode engine.WatchMode) error {
	_, err := h.deps.Chg.WatchOne(context.WithoutCancel(ctx), h.deps.Store, engine.WatchRequest{
		ID: id, Ref: id, Via: registry.ViaDirect, Mode: mode,
	})
	return h.watchErr(id, err)
}

func (h *handlers) watchErr(id string, err error) error {
	if err == nil {
		return nil
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("watch %s: %v", id, err))
}
