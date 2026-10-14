package changes

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Membership is an entered_query or left_query event the query packet hands the
// engine.
type Membership struct {
	// Kind is classify.KindEnteredQuery or classify.KindLeftQuery.
	Kind string
	// Query is the query's name; Node the entity key; Ref the wire reference.
	Query, Node, Ref string
	// Extra is appended to the row's fields.
	Extra []classify.Field
}

// AppendMembership records a membership change. It ensures the entity row
// exists (a PR new to the store has none yet when it enters a query), then
// merges the change into the entity's pending row when one exists and leaves
// that row pending (a membership change never waits on the barrier itself and
// never ends someone else's), and otherwise appends one row at once with origin
// "query".
func (e *Engine) AppendMembership(ctx context.Context, tx *store.Tx, m Membership) error {
	if m.Node == "" || m.Ref == "" {
		return errors.New("changes: membership needs a node and a reference")
	}
	if m.Kind != classify.KindEnteredQuery && m.Kind != classify.KindLeftQuery {
		return errors.New("changes: membership kind " + m.Kind + " is neither entered_query nor left_query")
	}
	if err := tx.EnsureEntity(ctx, LogKindPR, m.Node); err != nil {
		return err
	}
	res := classify.Merge(classify.Membership(m.Kind, m.Query), classify.Result{Fields: m.Extra})

	p, has, err := tx.GetPending(ctx, LogKindPR, m.Node)
	if err != nil {
		return err
	}
	if has {
		pres, aw, err := pendingResult(p)
		if err != nil {
			return err
		}
		return savePending(ctx, tx, p, classify.Merge(pres, res), aw)
	}
	var head string
	var originAt time.Time
	if sum, ok, err := storedSummary(ctx, tx, m.Node); err != nil {
		return err
	} else if ok {
		head, originAt = sum.HeadRefOid, originTime(sum)
	}
	_, err = e.appendRow(ctx, tx, m.Node, m.Ref, head, res, groupsvc.OriginQuery, originAt)
	return err
}
