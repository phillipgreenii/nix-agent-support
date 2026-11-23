package failure

// The escalation of a still-failing all.closed rule must outlive the terminal
// sweep. The tracker is a stateful fake that enforces bd's two close refusals
// (an open child, an open blocker) and answers `issue children` from its state,
// so a sweep that reached the escalation bead would close it here.

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/config"
)

type stateBD struct {
	open      map[string]bool
	parent    map[string]string
	blockedBy map[string][]string
	nextEsc   int
}

func (b *stateBD) runner() apply.Runner {
	return func(_ context.Context, c apply.Call) apply.CallResult {
		a := c.Args
		ok := func(s string) apply.CallResult { return apply.CallResult{Stdout: []byte(s)} }
		refuse := func(m string) apply.CallResult {
			return apply.CallResult{Code: 1, Stdout: []byte(`{"error":{"code":"refused","message":"` + m + `"}}`)}
		}
		if c.Name != "pg-connector" || len(a) < 2 || a[0] != "issue" {
			return ok(`{"result":{}}`)
		}
		switch a[1] {
		case "children":
			var kids []string
			for id, p := range b.parent {
				if p == a[2] && b.open[id] {
					kids = append(kids, fmt.Sprintf(`{"id":%q}`, id))
				}
			}
			sort.Strings(kids)
			return ok(`{"result":{"children":[` + strings.Join(kids, ",") + `]}}`)
		case "create":
			b.nextEsc++
			id := fmt.Sprintf("esc-%d", b.nextEsc)
			b.open[id] = true
			for i, s := range a {
				if s == "--parent" && i+1 < len(a) {
					b.parent[id] = a[i+1]
				}
			}
			return ok(`{"result":{"id":"` + id + `"}}`)
		case "close":
			id := a[2]
			if !b.open[id] {
				return refuse(id + " is already closed")
			}
			for _, bl := range b.blockedBy[id] {
				if b.open[bl] {
					return refuse("cannot close blocked issue: " + id)
				}
			}
			for k, p := range b.parent {
				if p == id && b.open[k] {
					return refuse("cannot close " + id + ": open child issue(s)")
				}
			}
			b.open[id] = false
		}
		return ok(`{"result":{}}`)
	}
}

func (b *stateBD) escalations() []string {
	var out []string
	for id := range b.open {
		if strings.HasPrefix(id, "esc-") {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func closeRun(t *testing.T, b *stateBD, state map[string]string) apply.Result {
	t.Helper()
	k := 1
	cl := func(kind, id string) action.Action {
		return action.Action{Op: action.OpClose, Kind: kind, Target: &id, Rule: "all.closed", Fields: action.Fields{Description: "PR merged"}}
	}
	return apply.Run(context.Background(), apply.Input{
		Type: "pr", ID: entity,
		View:    viewWith(state),
		Actions: []action.Action{cl("review-pr", "a.1"), cl("anchor", "a")},
		Env:     apply.Env{Runner: b.runner(), Config: &config.Config{EscalateAfter: &k}, Clock: time.Now, Stderr: &bytes.Buffer{}},
		Hooks:   []apply.Hook{New(viewWith(state), "pr", entity)},
	})
}

func TestEscalationOfAFailingAllClosedSurvivesTheTerminalSweep(t *testing.T) {
	// a.1 is blocked by x, a bead outside the anchor: the close keeps failing.
	b := &stateBD{
		open:      map[string]bool{"a": true, "a.1": true, "x": true},
		parent:    map[string]string{"a.1": "a"},
		blockedBy: map[string][]string{"a.1": {"x"}},
	}
	closeRun(t, b, nil)
	if got := b.escalations(); len(got) != 1 {
		t.Fatalf("the failing rule must escalate once, got %v", got)
	}
	if _, parented := b.parent["esc-1"]; parented {
		t.Fatalf("the escalation must not be filed under the anchor")
	}

	// A later run: still failing, escalated=1 suppresses a new escalation, and
	// the sweep (which reads the anchor's children) leaves the escalation open.
	escalated := map[string]string{"failures.all.closed": "1", "escalated.all.closed": "1"}
	closeRun(t, b, escalated)
	if got := b.escalations(); len(got) != 1 || !b.open["esc-1"] {
		t.Fatalf("the sweep must leave the escalation open and not file another: %v open=%v", got, b.open)
	}

	// The blocker clears: the PR's beads close, the escalation stays open for a
	// person (a successful run resets the annotations and never closes it).
	b.open["x"] = false
	r := closeRun(t, b, escalated)
	if r.ExitCode != 0 || b.open["a"] || b.open["a.1"] {
		t.Fatalf("the anchor and its child must close once unblocked: exit %d open=%v", r.ExitCode, b.open)
	}
	if !b.open["esc-1"] {
		t.Fatalf("a successful run must not close the escalation: %v", b.open)
	}
}
