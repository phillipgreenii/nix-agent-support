package complete

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

func TestDoneSignal(t *testing.T) {
	cases := []struct {
		name        string
		completion  roles.Completion
		status      string
		seenClaimed bool
		want        bool
	}{
		{"close-only closed", roles.CloseOnly, "closed", false, true},
		{"close-only open not done", roles.CloseOnly, "open", false, false},
		{"close-only in_progress not done", roles.CloseOnly, "in_progress", true, false},
		{"handback closed", roles.CloseOrHandback, "closed", false, true},
		{"handback open after claim = done", roles.CloseOrHandback, "open", true, true},
		{"handback open pre-claim NOT done (startup race)", roles.CloseOrHandback, "open", false, false},
		{"handback in_progress not done", roles.CloseOrHandback, "in_progress", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DoneSignal(tc.completion, tc.status, tc.seenClaimed); got != tc.want {
				t.Errorf("DoneSignal(%q,%q,%v) = %v, want %v", tc.completion, tc.status, tc.seenClaimed, got, tc.want)
			}
		})
	}
}

func TestOnFailure_addHumanNeverUnclaims(t *testing.T) {
	fr := &recRunner{}
	if err := OnFailure(context.Background(), fr, roles.AddHuman, "zr-w1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-w1 --add-label human") {
		t.Errorf("add-human failure must add human; calls=%v", fr.calls)
	}
	if fr.has("--status=open") {
		t.Errorf("add-human failure must NOT unclaim; calls=%v", fr.calls)
	}
}

func TestOnFailure_unclaimNeverAddsHuman(t *testing.T) {
	fr := &recRunner{}
	if err := OnFailure(context.Background(), fr, roles.Unclaim, "zr-c1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-c1 --status=open --assignee=") {
		t.Errorf("unclaim failure must unclaim; calls=%v", fr.calls)
	}
	if fr.has("--add-label human") {
		t.Errorf("unclaim failure must NOT add human; calls=%v", fr.calls)
	}
}

type recRunner struct{ calls []string }

func (r *recRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, join(args))
	return "", nil
}

func (r *recRunner) has(sub string) bool {
	for _, c := range r.calls {
		if c == sub {
			return true
		}
	}
	return false
}

func join(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}

// TestTracker_closeOrTriage covers the escalation triager's completion rule
// (pg2-2grpj): it never claims its bead, so besides closed it is done on
// de-escalation or on a comment appended since the first read.
func TestTracker_closeOrTriage(t *testing.T) {
	esc := []string{"escalated", "agent-support"}
	c := roles.CloseOrTriage

	t.Run("closed", func(t *testing.T) {
		var tr Tracker
		if !tr.Done(c, Observation{Status: "closed", Labels: esc}, true) {
			t.Error("closed must be done")
		}
	})
	t.Run("untouched is not done (still fails)", func(t *testing.T) {
		var tr Tracker
		for i := 0; i < 3; i++ {
			if tr.Done(c, Observation{Status: "open", Labels: esc, Comments: 2}, true) {
				t.Fatal("unchanged bead must not be done")
			}
		}
	})
	t.Run("triage comment appended", func(t *testing.T) {
		var tr Tracker
		tr.Done(c, Observation{Status: "open", Labels: esc, Comments: 2}, true)
		if !tr.Done(c, Observation{Status: "open", Labels: esc, Comments: 3}, true) {
			t.Error("a new comment (Triage) must be done")
		}
	})
	t.Run("escalated removed", func(t *testing.T) {
		var tr Tracker
		tr.Done(c, Observation{Status: "open", Labels: esc}, true)
		if !tr.Done(c, Observation{Status: "open", Labels: []string{"human"}}, true) {
			t.Error("escalated removed (Escalate) must be done")
		}
	})
	t.Run("failed read is not done", func(t *testing.T) {
		var tr Tracker
		if tr.Done(c, Observation{}, false) {
			t.Error("a failed read must not be done")
		}
	})
	t.Run("comments do not complete close-or-handback", func(t *testing.T) {
		var tr Tracker
		tr.Done(roles.CloseOrHandback, Observation{Status: "open", Comments: 1}, true)
		if tr.Done(roles.CloseOrHandback, Observation{Status: "open", Comments: 5}, true) {
			t.Error("comment growth is only a close-or-triage signal")
		}
	})
}

// TestTracker_observeLatchesClaim covers the seenClaimed latch: only
// handback-capable modes latch on in_progress, and the latch then lets an
// open bead read as a hand-back.
func TestTracker_observeLatchesClaim(t *testing.T) {
	for _, c := range []roles.Completion{roles.CloseOrHandback, roles.CloseOrTriage} {
		var tr Tracker
		tr.Observe(c, "open")
		if tr.SeenClaimed {
			t.Errorf("%s: open must not latch", c)
		}
		tr.Observe(c, "in_progress")
		if !tr.SeenClaimed {
			t.Fatalf("%s: in_progress must latch", c)
		}
		if !tr.Done(c, Observation{Status: "open", Labels: []string{"escalated"}}, true) {
			t.Errorf("%s: open after claim is a hand-back", c)
		}
	}
	var tr Tracker
	tr.Observe(roles.CloseOnly, "in_progress")
	if tr.SeenClaimed {
		t.Error("close-only must never latch")
	}
}
