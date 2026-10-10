package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// The focus apply path (daily-focus design section 8). The focus.item rule
// decides a hold or a release from the stored view, which can be minutes
// behind the tracker, so the apply step reads the bead LIVE first and
// re-checks the conditions the plan used. That read is a declared second input
// of the rule that may only ABANDON a write (skipped-stale); it never chooses
// a different one (INV-DECIDER-8 and -9).

// The focus transitions named in an action's facts.transition, and the two
// facts the apply step reads. They are the strings the focus.item rule writes;
// apply does not import the rules package, so they are restated here.
const (
	focusTransitionHold         = "hold"
	focusTransitionHoldTerminal = "hold_terminal"
	focusTransitionRelease      = "release"
	focusFactTransition         = "transition"
	focusFactMarkerOnly         = "marker_only"

	focusMarkerKey      = "focus_hold"
	focusMarkerStruck   = "struck"
	focusStatusOpen     = "open"
	focusStatusDeferred = "deferred"
	focusStatusProgress = "in_progress"
	servedFromOrigin    = "origin"
)

// focusTransition reports the transition of a focus bead's hold or release
// update, and false for every other action (including a mint, whose create
// goes through the dedup lookup instead).
func focusTransition(a action.Action) (string, bool) {
	if a.Op != action.OpUpdate || a.Kind != string(workitem.KindFocusItem) {
		return "", false
	}
	t, _ := a.Facts[focusFactTransition].(string)
	switch t {
	case focusTransitionHold, focusTransitionHoldTerminal, focusTransitionRelease:
		return t, true
	}
	return "", false
}

func isHold(t string) bool { return t == focusTransitionHold || t == focusTransitionHoldTerminal }

// liveBead is what a live `issue show <id> --fresh` answers, reduced to the
// members the re-check reads.
type liveBead struct {
	ID       string            `json:"id"`
	State    string            `json:"state"`
	Assignee string            `json:"assignee"`
	Metadata map[string]string `json:"metadata"`

	ServedFrom string `json:"served_from"`
	Stale      bool   `json:"stale"`
}

// showArgs is the argv of a live read of one bead. --fresh is mandatory: a
// plain show is read-through and answers from the entity cache for up to
// cache_read_ttl, so a claim that landed minutes ago would be invisible.
func showArgs(env Env, id string) []string {
	return append([]string{"issue", "show", id, "--fresh"}, backendFlag(env)...)
}

// childrenArgs is the argv of the live read of a bead's non-closed children.
func childrenArgs(env Env, id string) []string {
	return append([]string{"issue", "children", id}, backendFlag(env)...)
}

// liveShow reads the bead live. A failed call, an undecodable answer, an answer
// not served from the origin and an answer marked stale are all the same
// thing: a read that cannot be trusted, which fails closed.
func liveShow(ctx context.Context, env Env, id string) (liveBead, error) {
	args := showArgs(env, id)
	out, err := call(ctx, env, args)
	if err != nil {
		return liveBead{}, err
	}
	var w wireEnvelope
	if err := json.Unmarshal(out, &w); err != nil {
		return liveBead{}, fmt.Errorf("decode pg-connector %s stdout: %w", strings.Join(args, " "), err)
	}
	var live liveBead
	if len(w.Result) == 0 || json.Unmarshal(w.Result, &live) != nil {
		return liveBead{}, fmt.Errorf("decode pg-connector %s result: not a work item", strings.Join(args, " "))
	}
	if live.ServedFrom != servedFromOrigin {
		return liveBead{}, fmt.Errorf("pg-connector %s was served from %q, not from the origin", strings.Join(args, " "), live.ServedFrom)
	}
	if live.Stale {
		return liveBead{}, fmt.Errorf("pg-connector %s answered stale", strings.Join(args, " "))
	}
	return live, nil
}

// liveChildren reads the bead's non-closed direct children through
// `pg-connector issue children`, a live read that bypasses the entity cache. A
// failure (unknown_op included) means "unknown", never "no children".
func liveChildren(ctx context.Context, env Env, id string) ([]string, error) {
	args := childrenArgs(env, id)
	out, err := call(ctx, env, args)
	if err != nil {
		return nil, err
	}
	var w wireEnvelope
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, fmt.Errorf("decode pg-connector %s stdout: %w", strings.Join(args, " "), err)
	}
	var res struct {
		Children *[]struct {
			ID string `json:"id"`
		} `json:"children"`
	}
	if len(w.Result) == 0 || json.Unmarshal(w.Result, &res) != nil || res.Children == nil {
		return nil, fmt.Errorf("decode pg-connector %s result: no children member", strings.Join(args, " "))
	}
	ids := make([]string, 0, len(*res.Children))
	for _, c := range *res.Children {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// verdict is the outcome of re-checking a planned write against the live bead.
type verdict struct {
	abandon bool
	reason  string
}

// recheck re-checks the conditions the plan used against the live bead. It is
// pure: it can only abandon the planned write, never pick another one.
//
//   - a hold (and a terminal hold) was planned for a bead that is exactly open
//     and unclaimed; it still needs the bead open, with no assignee;
//   - a full release was planned for a HELD bead (deferred, marker struck, no
//     assignee); it still needs all three;
//   - a marker-only release was planned for an open, unclaimed bead carrying a
//     stale struck marker; it still needs exactly that.
func recheck(a action.Action, live liveBead) verdict {
	t, _ := focusTransition(a)
	stale := func(format string, args ...any) verdict {
		return verdict{abandon: true, reason: fmt.Sprintf(format, args...)}
	}
	if live.Assignee != "" {
		return stale("the bead is now claimed by %s", live.Assignee)
	}
	if live.State == focusStatusProgress {
		return stale("the bead is now in_progress")
	}
	marker := live.Metadata[focusMarkerKey]
	switch {
	case isHold(t):
		if live.State != focusStatusOpen {
			return stale("the bead is now %s, not open", live.State)
		}
	case a.Facts[focusFactMarkerOnly] == true:
		if live.State != focusStatusOpen {
			return stale("the bead is now %s, not open", live.State)
		}
		if marker != focusMarkerStruck {
			return stale("the bead's %s marker is now %q, not %q", focusMarkerKey, marker, focusMarkerStruck)
		}
	default: // a full release
		if live.State != focusStatusDeferred {
			return stale("the bead is now %s, not deferred", live.State)
		}
		if marker != focusMarkerStruck {
			return stale("the bead's %s marker is now %q, not %q", focusMarkerKey, marker, focusMarkerStruck)
		}
	}
	return verdict{}
}

// holdOrRelease executes one focus hold or release: live read, re-check, the
// live children read for a hold, the write, and for a hold the post-write read
// that restores a claim that landed in the window.
func (r *runner) holdOrRelease(ctx context.Context, a action.Action, finish finishFn) Event {
	if a.Target == nil || *a.Target == "" {
		return finish(OutcomeFailed, "", fmt.Errorf("%s action has no target work item", a.Op))
	}
	env, id := r.in.Env, *a.Target
	t, _ := focusTransition(a)

	// abandon writes nothing and no audit comment; the refresh repairs the
	// stale store row, because without it every later run would re-plan the
	// same write and abandon it forever.
	abandon := func(err error) Event {
		r.refresh(ctx, id)
		return finish(OutcomeSkippedStale, id, err)
	}

	live, err := liveShow(ctx, env, id)
	if err != nil {
		return abandon(fmt.Errorf("live read of %s failed, nothing was written: %w", id, err))
	}
	if v := recheck(a, live); v.abandon {
		return abandon(fmt.Errorf("%s of %s abandoned, %s since the view was read", t, id, v.reason))
	}
	if isHold(t) {
		kids, err := liveChildren(ctx, env, id)
		if err != nil {
			return finish(OutcomeFailed, id, fmt.Errorf("live read of the children of %s failed, the hold was not written: %w", id, err))
		}
		if len(kids) > 0 {
			return abandon(fmt.Errorf("hold of %s abandoned, the bead has open children (%s)", id, strings.Join(kids, ", ")))
		}
	}

	if _, err := call(ctx, env, updateArgs(env, id, a.Fields, false)); err != nil {
		return finish(OutcomeFailed, id, err)
	}
	if isHold(t) {
		if err := r.restoreClaim(ctx, id); err != nil {
			r.refresh(ctx, id)
			return finish(OutcomeFailed, id, err)
		}
	}
	r.refresh(ctx, id)
	return finish(OutcomeApplied, id, nil)
}

// restoreClaim is the compensation for the window between the live read and a
// hold's write. It reads the bead once more; if a claim landed in the window
// (the bead is deferred AND has an assignee, the stranded shape) it issues ONE
// update back to in_progress, leaves the marker alone and reports "claimed,
// left running". A post-write read that fails is only reported: the hold stands
// and the next run reads the view again.
func (r *runner) restoreClaim(ctx context.Context, id string) error {
	env := r.in.Env
	live, err := liveShow(ctx, env, id)
	if err != nil {
		fmt.Fprintf(env.stderr(), "pg-decider: focus hold of %s: the read after the write failed, a claim in the window would not be restored: %v\n", id, err)
		return nil
	}
	if live.State != focusStatusDeferred || live.Assignee == "" {
		return nil
	}
	args := append([]string{"issue", "update", id, "--status", focusStatusProgress}, backendFlag(env)...)
	if _, err := call(ctx, env, args); err != nil {
		return fmt.Errorf("focus hold of %s stranded a claim by %s (deferred and assigned) and the restore to in_progress failed: %w", id, live.Assignee, err)
	}
	fmt.Fprintf(env.stderr(), "pg-decider: focus hold of %s: claimed, left running (%s claimed it in the window; restored to %s, the %s marker is left alone)\n", id, live.Assignee, focusStatusProgress, focusMarkerKey)
	return nil
}

// refreshFocusHit handles a dedup HIT on a focus-item create: the source's view
// showed no linked bead (the rule mints only then) although the bead exists, so
// the store row is missing or stale. The hit is refreshed in pg-desk, and
// nothing else is written. A view that does show a linked bead needs none.
func (r *runner) refreshFocusHit(ctx context.Context, id string) {
	if v := r.in.View; v != nil && len(workitem.BuildIndex(v).ByKind(workitem.KindFocusItem)) > 0 {
		return
	}
	r.refresh(ctx, id)
}
