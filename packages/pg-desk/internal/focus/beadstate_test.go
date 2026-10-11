package focus

import (
	"testing"
	"time"
)

// The bead column: one test row per row of the state-by-outcome table, the
// vocabulary pinned against the behavior doc (TestSection83TableMatchesCode, in
// doc_conformance_test.go), and the classification order.

type beadCase struct {
	name  string
	state BeadState
	link  LinkedBead
	src   SourceFlags
	shown string
}

// Traces INV-FOCUS-18: the bead column uses only the table words.
func TestClassifyBeadOneCasePerTableRow(t *testing.T) {
	cases := []beadCase{
		{"no bead yet", StatePending, LinkedBead{}, SourceFlags{}, "pending"},
		{"no bead yet, waiting 12m", StatePending, LinkedBead{PendingAge: 12 * time.Minute}, SourceFlags{}, "pending 12m"},
		{"no bead yet, waiting 3h", StatePending, LinkedBead{PendingAge: 3 * time.Hour}, SourceFlags{}, "pending 3h"},
		{"no bead yet, hidden", StatePendingHidden, LinkedBead{}, SourceFlags{Hidden: true}, "pending (hidden)"},
		{"no bead yet, suppressed", StatePendingSuppress, LinkedBead{}, SourceFlags{SuppressedFocusKind: true}, "pending (suppressed)"},
		{"no bead yet, source done", StateSourceDoneNoBead, LinkedBead{}, SourceFlags{Terminal: true}, "source done (no bead)"},
		{"source is a bead", StateNotApplicable, LinkedBead{Present: true, Status: "open"}, SourceFlags{IsBeadItself: true}, "n/a (epic or bead)"},
		{"open", StateOpen, LinkedBead{Present: true, Status: "open"}, SourceFlags{}, "open"},
		{"open, marker released", StateOpen, LinkedBead{Present: true, Status: "open", FocusHold: "released"}, SourceFlags{}, "open"},
		{"open with children", StateOpenChildren, LinkedBead{Present: true, Status: "open", OpenChildren: true}, SourceFlags{}, "open (has children)"},
		{"open, marker struck", StateOpenMarkerStale, LinkedBead{Present: true, Status: "open", FocusHold: "struck"}, SourceFlags{}, "open (marker stale)"},
		{"in progress", StateClaimed, LinkedBead{Present: true, Status: "in_progress"}, SourceFlags{}, "claimed"},
		{"assigned", StateClaimed, LinkedBead{Present: true, Status: "open", Assignee: "worker-1"}, SourceFlags{}, "claimed by worker-1"},
		{"held", StateHeld, LinkedBead{Present: true, Status: "deferred", FocusHold: "struck"}, SourceFlags{}, "held"},
		{"deferred by someone else", StateDeferredOther, LinkedBead{Present: true, Status: "deferred"}, SourceFlags{}, "deferred by someone else"},
		{"blocked", StateStatusWord, LinkedBead{Present: true, Status: "blocked"}, SourceFlags{}, "blocked"},
		{"pinned", StateStatusWord, LinkedBead{Present: true, Status: "pinned"}, SourceFlags{}, "pinned"},
		{"hooked", StateStatusWord, LinkedBead{Present: true, Status: "hooked"}, SourceFlags{}, "hooked"},
		{"closed", StateClosed, LinkedBead{Present: true, Status: "closed"}, SourceFlags{}, "closed"},
		{"source done, bead unclaimed", StateSourceDone, LinkedBead{Present: true, Status: "open"}, SourceFlags{Terminal: true}, "source done"},
		{"hold stale", StateHoldStale, LinkedBead{Present: true, Status: "open"}, SourceFlags{HoldStale: true}, "hold stale"},
		{"escalated", StateEscalated, LinkedBead{Present: true, Status: "open"}, SourceFlags{Escalated: true}, "escalated"},
		{"unwatched is added to the row", StateOpen, LinkedBead{Present: true, Status: "open"}, SourceFlags{Unwatched: true}, "open unwatched"},
		{"unwatched pending", StatePending, LinkedBead{}, SourceFlags{Unwatched: true}, "pending unwatched"},
	}
	covered := map[BeadState]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyBead(c.link, c.src)
			if got != c.state {
				t.Fatalf("ClassifyBead = %q, want %q", got, c.state)
			}
			if shown := ShownAs(got, c.link, c.src); shown != c.shown {
				t.Errorf("ShownAs = %q, want %q", shown, c.shown)
			}
			covered[got] = true
		})
	}
	// Unwatched has no classification of its own: it is added to the row above.
	covered[StateUnwatched] = true
	for _, row := range BeadStates {
		if !covered[row.State] {
			t.Errorf("table row %q has no test case", row.State)
		}
	}
	if got := ShownAs(StateUnwatched, LinkedBead{}, SourceFlags{}); got != "unwatched" {
		t.Errorf("ShownAs(unwatched) = %q", got)
	}
}

func TestClassifyBeadPrecedence(t *testing.T) {
	open := LinkedBead{Present: true, Status: "open"}
	// A claimed bead stays claimed whatever the source or the marker says.
	claimed := LinkedBead{Present: true, Status: "in_progress", FocusHold: "struck"}
	if got := ClassifyBead(claimed, SourceFlags{Terminal: true}); got != StateClaimed {
		t.Errorf("claimed + terminal = %q, want claimed", got)
	}
	// A closed bead is closed even when claimed-looking.
	if got := ClassifyBead(LinkedBead{Present: true, Status: "closed", Assignee: "w"}, SourceFlags{Terminal: true}); got != StateClosed {
		t.Errorf("closed = %q", got)
	}
	// A deferred bead with the marker but an assignee is claimed, not held.
	if got := ClassifyBead(LinkedBead{Present: true, Status: "deferred", FocusHold: "struck", Assignee: "w"}, SourceFlags{}); got != StateClaimed {
		t.Errorf("assigned deferred = %q, want claimed", got)
	}
	// An escalation and a bead-as-source win over the bead's status.
	if got := ClassifyBead(open, SourceFlags{Escalated: true, Terminal: true}); got != StateEscalated {
		t.Errorf("escalated = %q", got)
	}
	if got := ClassifyBead(LinkedBead{}, SourceFlags{IsBeadItself: true, Hidden: true}); got != StateNotApplicable {
		t.Errorf("bead itself = %q", got)
	}
	// Hidden wins over suppressed wins over terminal when there is no bead.
	if got := ClassifyBead(LinkedBead{}, SourceFlags{Hidden: true, SuppressedFocusKind: true, Terminal: true}); got != StatePendingHidden {
		t.Errorf("hidden first = %q", got)
	}
	if got := ClassifyBead(LinkedBead{}, SourceFlags{SuppressedFocusKind: true, Terminal: true}); got != StatePendingSuppress {
		t.Errorf("suppressed before terminal = %q", got)
	}
	// A terminal source with an open unclaimed bead is source done even with children.
	if got := ClassifyBead(LinkedBead{Present: true, Status: "open", OpenChildren: true}, SourceFlags{Terminal: true}); got != StateSourceDone {
		t.Errorf("terminal with children = %q", got)
	}
}

func TestBeadStateTableShape(t *testing.T) {
	if len(BeadStates) != 17 {
		t.Fatalf("BeadStates has %d rows, want the 17 rows of the spec table", len(BeadStates))
	}
	seen := map[BeadState]bool{}
	for _, r := range BeadStates {
		if seen[r.State] {
			t.Errorf("duplicate row %q", r.State)
		}
		seen[r.State] = true
		if r.Shown == "" || r.SkipReason == "" || r.StrikeEcho == "" || r.SelectEcho == "" || r.Description == "" {
			t.Errorf("row %q has an empty column: %+v", r.State, r)
		}
		switch r.StrikeEffect {
		case "mint", "hold", "release", "none":
		default:
			t.Errorf("row %q strike effect %q", r.State, r.StrikeEffect)
		}
		switch r.SelectEffect {
		case "mint", "hold", "release", "none":
		default:
			t.Errorf("row %q select effect %q", r.State, r.SelectEffect)
		}
	}
	if RowOf("nope").State != "" {
		t.Errorf("RowOf of an unknown state must be the zero row")
	}
}
