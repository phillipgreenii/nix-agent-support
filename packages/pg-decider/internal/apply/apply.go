// Package apply executes a decider's action list against the tracker (through
// `pg-connector issue ...`) and against pg-desk's annotations (through
// `pg-desk <type> annotate ...` and the force-review clear path), entity-
// change-flow spec 7.4 and 9.7.
//
// Actions apply in order, best-effort: each error is captured and later
// actions continue unless they depend on a failed one. Two dependencies
// exist: children whose parent is action.AnchorParent depend on the anchor
// create, and an action with RequiresPrior depends on every earlier action of
// the same Rule. The tracker is the source of truth; apply never rolls a
// tracker write back, and a failed post-write refresh is retryable staleness,
// not an apply failure.
package apply

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// anchorState tracks the anchor create of the current run.
type anchorState int

const (
	anchorUnseen anchorState = iota
	anchorResolved
	anchorBroken
)

type runner struct {
	in         Input
	ref        workitem.EntityRef
	anchor     anchorState
	anchorID   string
	ruleBroken map[string]bool // a rule with a failed or skipped action so far
}

// Run executes in.Actions in order and reports every outcome to in.Hooks.
func Run(ctx context.Context, in Input) Result {
	r := &runner{in: in, ruleBroken: map[string]bool{}}
	r.ref = workitem.EntityRef{Type: in.Type, ID: in.ID}
	if in.View != nil {
		r.ref = workitem.EntityRefFrom(in.View)
	}
	env := in.Env
	hookFailed := false
	reportHook := func(what string, err error) {
		hookFailed = true
		fmt.Fprintf(env.stderr(), "pg-decider: %s: %v\n", what, err)
	}

	var res Result
	for _, a := range in.Actions {
		ev := r.step(ctx, a)
		if in.Item != nil {
			ev.Seq, ev.HasSeq = in.Item.Metadata.Seq, true
		}
		res.Events = append(res.Events, ev)
		for _, h := range in.Hooks {
			if err := h.After(ctx, env, ev); err != nil {
				reportHook("hook after "+string(a.Op)+" "+a.Rule, err)
			}
		}
	}
	for _, h := range in.Hooks {
		if err := h.Finish(ctx, env, res.Events); err != nil {
			reportHook("hook finish", err)
		}
	}

	res.ExitCode = 0
	if hookFailed {
		res.ExitCode = 2
	}
	for _, ev := range res.Events {
		if ev.Outcome == OutcomeFailed || ev.Outcome == OutcomeSkippedDependency {
			res.ExitCode = 2
		}
	}
	return res
}

func isAnchorCreate(a action.Action) bool {
	return a.Op == action.OpCreate && a.Kind == string(workitem.KindAnchor)
}

// step runs one action, applying the dependency rules, and updates the run
// state from its outcome.
func (r *runner) step(ctx context.Context, a action.Action) Event {
	ev := Event{Action: a}
	finish := func(o Outcome, id string, err error) Event {
		ev.Outcome, ev.WorkItemID, ev.Err = o, id, err
		if o == OutcomeFailed || o == OutcomeSkippedDependency {
			r.ruleBroken[a.Rule] = true
		}
		if isAnchorCreate(a) {
			if o == OutcomeApplied || o == OutcomeDeduped {
				r.anchor, r.anchorID = anchorResolved, id
			} else {
				r.anchor = anchorBroken
			}
		}
		return ev
	}

	if a.RequiresPrior && r.ruleBroken[a.Rule] {
		return finish(OutcomeSkippedDependency, "", fmt.Errorf("an earlier action of rule %s did not apply", a.Rule))
	}
	if a.Fields.Parent == action.AnchorParent {
		switch r.anchor {
		case anchorBroken:
			return finish(OutcomeSkippedDependency, "", fmt.Errorf("the anchor create did not apply"))
		case anchorUnseen:
			return finish(OutcomeFailed, "", fmt.Errorf("parent %s names an anchor create that is not earlier in the action list", action.AnchorParent))
		}
		a.Fields.Parent = r.anchorID
	}

	switch a.Op {
	case action.OpCreate:
		return r.create(ctx, a, finish)
	case action.OpUpdate, action.OpReopen, action.OpClose:
		return r.write(ctx, a, finish)
	case action.OpAnnotate:
		return r.annotate(ctx, a, finish)
	}
	return finish(OutcomeFailed, "", fmt.Errorf("unknown action op %q", a.Op))
}

type finishFn func(Outcome, string, error) Event

func (r *runner) create(ctx context.Context, a action.Action, finish finishFn) Event {
	env := r.in.Env
	if key := a.Fields.Metadata["dedup_key"]; key != "" {
		id, found, err := lookupDedup(ctx, env, r.ref, key)
		if err != nil {
			return finish(OutcomeFailed, "", err)
		}
		if found {
			return finish(OutcomeDeduped, id, nil)
		}
	}
	id, err := CreateIssue(ctx, env, a.Fields)
	if err != nil {
		return finish(OutcomeFailed, "", err)
	}
	r.refresh(ctx, id)
	return finish(OutcomeApplied, id, nil)
}

func (r *runner) write(ctx context.Context, a action.Action, finish finishFn) Event {
	if a.Target == nil || *a.Target == "" {
		return finish(OutcomeFailed, "", fmt.Errorf("%s action has no target work item", a.Op))
	}
	id := *a.Target
	var args []string
	switch a.Op {
	case action.OpClose:
		args = closeArgs(r.in.Env, id, a)
	default:
		args = updateArgs(r.in.Env, id, a.Fields, a.Op == action.OpReopen)
	}
	if _, err := call(ctx, r.in.Env, args); err != nil {
		return finish(OutcomeFailed, id, err)
	}
	r.refresh(ctx, id)
	return finish(OutcomeApplied, id, nil)
}

func (r *runner) annotate(ctx context.Context, a action.Action, finish finishFn) Event {
	if a.Target == nil || *a.Target == "" {
		return finish(OutcomeFailed, "", fmt.Errorf("annotate action has no key"))
	}
	key := *a.Target
	var err error
	switch {
	case a.Fields.Clear:
		err = clearAnnotation(ctx, r.in.Env, r.in.Type, r.in.ID, key)
	case a.Fields.Value == nil:
		err = fmt.Errorf("annotate %q has neither a value nor clear", key)
	default:
		err = Annotate(ctx, r.in.Env, r.in.Type, r.in.ID, key, *a.Fields.Value)
	}
	if err != nil {
		return finish(OutcomeFailed, "", err)
	}
	// Annotations go through pg-desk, so no follow-up refresh is run.
	return finish(OutcomeApplied, "", nil)
}

// refresh re-hydrates a work item after an external tracker write. A failure
// is retryable staleness: it is logged and nothing is rolled back.
func (r *runner) refresh(ctx context.Context, id string) {
	if err := refresh(ctx, r.in.Env, id); err != nil {
		fmt.Fprintf(r.in.Env.stderr(), "pg-decider: refresh of work item %s failed (retryable staleness, the tracker write stands): %v\n", id, err)
	}
}
