package apply

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// The review head refresh (ruling 16). The review.head-advanced rule moves an
// UNCLAIMED review item to the PR's current head (head_sha, dedup_key suffix
// and title) from the stored view, which can be minutes behind the tracker.
// Like a focus hold, the write is therefore preceded by a live read of the item
// that can only ABANDON it: an item that was claimed (or left the open status)
// in the meantime is never rewritten under its reviewer. The abandoned write is
// skipped-stale, not applied, and the next run decides again from the view.

// The facts the review rule writes on a refresh; apply does not import the
// rules package, so they are restated here.
const (
	reviewFactTransition        = "transition"
	reviewTransitionRefreshHead = "refresh-head"
	reviewStatusOpen            = "open"
)

// isReviewRefresh reports whether a is the review rule's refresh of an
// unclaimed item.
func isReviewRefresh(a action.Action) bool {
	if a.Op != action.OpUpdate || a.Kind != string(workitem.KindReviewPR) {
		return false
	}
	t, _ := a.Facts[reviewFactTransition].(string)
	return t == reviewTransitionRefreshHead
}

// recheckReview is the live conditions of a refresh: the item is exactly open
// and has no assignee, as the plan assumed.
func recheckReview(live liveBead) verdict {
	switch {
	case live.Assignee != "":
		return verdict{abandon: true, reason: fmt.Sprintf("the item is now claimed by %s", live.Assignee)}
	case live.State != reviewStatusOpen:
		return verdict{abandon: true, reason: fmt.Sprintf("the item is now %s, not open", live.State)}
	}
	return verdict{}
}

// refreshReview executes one review refresh: live read, re-check, write.
func (r *runner) refreshReview(ctx context.Context, a action.Action, finish finishFn) Event {
	if a.Target == nil || *a.Target == "" {
		return finish(OutcomeFailed, "", fmt.Errorf("%s action has no target work item", a.Op))
	}
	env, id := r.in.Env, *a.Target
	// abandon writes nothing and no audit comment; the refresh repairs the stale
	// store row so that the next run does not plan the same write again.
	abandon := func(err error) Event {
		r.refresh(ctx, id)
		return finish(OutcomeSkippedStale, id, err)
	}
	live, err := liveShow(ctx, env, id)
	if err != nil {
		return abandon(fmt.Errorf("live read of %s failed, nothing was written: %w", id, err))
	}
	if v := recheckReview(live); v.abandon {
		return abandon(fmt.Errorf("head refresh of %s abandoned, %s since the view was read", id, v.reason))
	}
	if _, err := call(ctx, env, updateArgs(env, id, a.Fields, false)); err != nil {
		return finish(OutcomeFailed, id, err)
	}
	r.refresh(ctx, id)
	return finish(OutcomeApplied, id, nil)
}
