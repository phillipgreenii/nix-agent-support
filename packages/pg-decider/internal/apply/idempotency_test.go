package apply

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// lazyAnchor is a test-only rule: it creates the anchor unless the view
// already links one, so re-running it on the state it produced yields nothing.
type lazyAnchor struct{}

func (lazyAnchor) ID() string          { return "test.anchor-lazy" }
func (lazyAnchor) Kind() workitem.Kind { return workitem.KindAnchor }
func (lazyAnchor) Evaluate(in decide.Input) decide.Result {
	if _, ok := in.Items.Anchor(); ok {
		return decide.Result{Skip: &action.Skip{Reason: action.ReasonAlreadyHandled}}
	}
	return decide.Result{Actions: []action.Action{{
		Op: action.OpCreate, Kind: string(workitem.KindAnchor), Rule: "test.anchor-lazy",
		Fields: action.Fields{
			Title: prID + ": anchor", IssueType: "epic",
			Metadata: map[string]string{"dedup_key": "pr:" + prID + ":anchor"},
		},
	}}}
}

func decideAndApply(t *testing.T, entityType string, v *view.View) (*double, Result) {
	t.Helper()
	d := newDouble(t)
	plan := decide.Decide(v, entityType)
	r := Run(context.Background(), Input{Type: "pr", ID: prID, View: v, Actions: plan.Actions, Env: testEnv(d, nil)})
	return d, r
}

func TestHiddenEntityProducesZeroWrites(t *testing.T) {
	et := t.Name()
	decide.Register(et, decide.OrdinalAnchorLazy, lazyAnchor{})
	v := prView()
	v.Annotations.Hidden.Value = true
	d, r := decideAndApply(t, et, v)
	if len(d.calls()) != 0 || r.ExitCode != 0 || len(r.Events) != 0 {
		t.Fatalf("hidden entity wrote: %q %+v", d.lines(), r)
	}
}

func TestUnchangedStateProducesZeroWrites(t *testing.T) {
	et := t.Name()
	decide.Register(et, decide.OrdinalAnchorLazy, lazyAnchor{})

	// First run on a view with no anchor writes (so the test proves the rule
	// is live and the zero below is idempotency, not a dead rule).
	d, r := decideAndApply(t, et, prView())
	if r.ExitCode != 0 || len(r.Events) != 1 || len(d.calls()) == 0 {
		t.Fatalf("first run should create the anchor: %q %+v", d.lines(), r)
	}

	// The state that run produced: the anchor is linked from the view.
	v := prView()
	v.Links = []view.Link{{
		Type: "issue", ID: "wb-1", State: "open",
		Title:    prID + ": anchor",
		Metadata: map[string]string{"dedup_key": "pr:" + prID + ":anchor"},
	}}
	d, r = decideAndApply(t, et, v)
	if len(d.calls()) != 0 || r.ExitCode != 0 || len(r.Events) != 0 {
		t.Fatalf("unchanged state wrote: %q %+v", d.lines(), r)
	}
}
