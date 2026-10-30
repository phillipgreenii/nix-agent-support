package apply

// Tests of the terminal sweep (pg2-w4as2e): a merged or closed PR whose anchor
// has an open child the view does not link must still end with the anchor and
// every child closed in ONE delivery. The tracker is a stateful fake that
// enforces bd's two refusals (a bead with an open child, and a blocked bead,
// cannot be closed), so a sweep that closes in the wrong order fails here the
// way it fails against bd.

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
)

type sweepBD struct {
	open       map[string]bool
	parent     map[string]string   // child -> parent
	blockedBy  map[string][]string // bead -> its blockers
	closes     []string            // successful closes, in order
	noChildren bool                // answer `issue children` with an error
}

func (b *sweepBD) runner() Runner {
	return func(_ context.Context, c Call) CallResult {
		args := c.Args
		out := func(s string) CallResult { return CallResult{Stdout: []byte(s)} }
		fail := func(msg string) CallResult {
			return CallResult{Code: 1, Stdout: []byte(`{"error":{"code":"refused","message":"` + msg + `"}}`)}
		}
		switch {
		case c.Name == "pg-connector" && len(args) >= 3 && args[0] == "issue" && args[1] == "children":
			if b.noChildren {
				return out(`{"result":{}}`)
			}
			var kids []string
			for id, p := range b.parent {
				if p == args[2] && b.open[id] {
					kids = append(kids, fmt.Sprintf(`{"id":%q}`, id))
				}
			}
			sort.Strings(kids)
			return out(`{"result":{"children":[` + strings.Join(kids, ",") + `]}}`)
		case c.Name == "pg-connector" && len(args) >= 3 && args[0] == "issue" && args[1] == "close":
			id := args[2]
			if !b.open[id] {
				return fail(id + " is already closed")
			}
			for _, bl := range b.blockedBy[id] {
				if b.open[bl] {
					return fail("cannot close blocked issue: " + id + " is blocked by [" + bl + "]")
				}
			}
			for k, p := range b.parent {
				if p == id && b.open[k] {
					return fail("cannot close " + id + ": open child issue(s)")
				}
			}
			b.open[id] = false
			b.closes = append(b.closes, id)
			return out(`{"result":{}}`)
		}
		return out(`{"result":{}}`)
	}
}

// sweepFixture is the shape of the 2026-10-29 incident: the anchor has the
// review-pr item (.1, linked to the PR), a human bead (.2, unlinked) that
// blocks .1, and the decider's own escalation bead (.3, unlinked).
func sweepFixture() *sweepBD {
	return &sweepBD{
		open:      map[string]bool{"a": true, "a.1": true, "a.2": true, "a.3": true},
		parent:    map[string]string{"a.1": "a", "a.2": "a", "a.3": "a"},
		blockedBy: map[string][]string{"a.1": {"a.2"}},
	}
}

// sweepPlanned is what all.closed plans from a view that links only the anchor
// and the review item: children first, the anchor last.
func sweepPlanned() []action.Action {
	cl := func(kind, id string) action.Action {
		return action.Action{
			Op: action.OpClose, Kind: kind, Target: str(id), Rule: "all.closed",
			Fields: action.Fields{Description: "PR merged"},
		}
	}
	return []action.Action{cl("review-pr", "a.1"), cl("anchor", "a")}
}

func sweepRun(b *sweepBD, errBuf *bytes.Buffer) Result {
	env := Env{Runner: b.runner(), Config: &config.Config{}}
	if errBuf != nil {
		env.Stderr = errBuf
	}
	return Run(context.Background(), Input{Type: "pr", ID: prID, View: prView(), Actions: sweepPlanned(), Env: env})
}

func TestTerminalSweepClosesUnlinkedAndBlockingChildrenInOneDelivery(t *testing.T) {
	b := sweepFixture()
	r := sweepRun(b, nil)
	for id, open := range b.open {
		if open {
			t.Errorf("%s is still open after one delivery (closes: %v)", id, b.closes)
		}
	}
	if r.ExitCode != 0 {
		t.Errorf("exit code %d, want 0: %+v", r.ExitCode, r.Events)
	}
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied, OutcomeApplied}) {
		t.Errorf("outcomes: %v", outcomes(r))
	}
	// The blocker closes before the bead it blocks, and the anchor is last.
	if want := []string{"a.2", "a.3", "a.1", "a"}; !reflect.DeepEqual(b.closes, want) {
		t.Errorf("close order %v, want %v", b.closes, want)
	}
}

func TestTerminalSweepFallsBackToPlannedClosesWhenTheChildrenReadFails(t *testing.T) {
	b := sweepFixture()
	b.noChildren = true
	var errBuf bytes.Buffer
	r := sweepRun(b, &errBuf)
	if !strings.Contains(errBuf.String(), "closing only the planned items") {
		t.Errorf("stderr: %q", errBuf.String())
	}
	if r.ExitCode != 2 || len(b.closes) != 0 {
		t.Errorf("without the live read the pre-sweep behavior stands (bd refuses both): exit %d closes %v", r.ExitCode, b.closes)
	}
}
