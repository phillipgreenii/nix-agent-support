package apply

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// The terminal sweep (pg2-w4as2e). all.closed is a pure rule of the view, so
// it plans closes only for the children the view links to the PR. A child filed
// under the anchor by a review session, or by the decider's own escalation
// path, carries no repo/pr_number and is not in the view; bd then refuses the
// anchor (open children) and any planned child that such a bead blocks. Rather
// than give the rule a tracker call, the apply layer, which already makes live
// reads for the focus hold, reads the anchor's live open children once, at the
// first all.closed close of the run, and closes every one of them itself, a
// blocker before the bead it blocks (bd refuses to close a blocked bead). The
// planned closes then find their target already closed and report applied
// without a second call. A failed live read falls back to the planned closes
// alone (the pre-sweep behavior); a child the sweep could not close is left to
// its planned close or to the anchor close, which report the bd refusal.
const anchorCloseRule = "all.closed"

// isTerminalClose reports whether a is one of all.closed's closes.
func isTerminalClose(a action.Action) bool {
	return a.Op == action.OpClose && a.Rule == anchorCloseRule
}

// terminalAnchor finds the anchor close among the run's actions.
func (r *runner) terminalAnchor() (string, bool) {
	for _, a := range r.in.Actions {
		if isTerminalClose(a) && a.Kind == string(workitem.KindAnchor) && a.Target != nil && *a.Target != "" {
			return *a.Target, true
		}
	}
	return "", false
}

// sweepAnchorChildren runs once per run, before the first all.closed close.
func (r *runner) sweepAnchorChildren(ctx context.Context, a action.Action) {
	if r.swept != nil {
		return
	}
	r.swept = map[string]bool{}
	anchorID, ok := r.terminalAnchor()
	if !ok {
		return
	}
	env := r.in.Env
	kids, err := liveChildren(ctx, env, anchorID)
	if err != nil {
		fmt.Fprintf(env.stderr(), "pg-decider: all.closed: live read of the children of %s failed, closing only the planned items: %v\n", anchorID, err)
		return
	}
	skip := map[string]bool{}
	if r.in.View != nil { // a focus bead is not the anchor's child, whatever its parent field says
		for _, it := range workitem.BuildIndex(r.in.View).ByKind(workitem.KindFocusItem) {
			skip[it.ID] = true
		}
	}
	var pending []string
	for _, id := range kids {
		if id != anchorID && !skip[id] {
			pending = append(pending, id)
		}
	}
	// Each round closes what bd accepts; a bead refused because it is blocked
	// is retried after its blocker closed. It ends when a round closes nothing.
	for len(pending) > 0 {
		var left []string
		for _, id := range pending {
			c := action.Action{Op: action.OpClose, Rule: anchorCloseRule, Target: &id, Fields: action.Fields{Description: a.Fields.Description}}
			if _, err := call(ctx, env, closeArgs(env, id, c)); err != nil {
				left = append(left, id)
				continue
			}
			r.swept[id] = true
			r.refresh(ctx, id)
		}
		if len(left) == len(pending) {
			return
		}
		pending = left
	}
}
