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
			if got := DoneSignal(tc.completion, tc.status, tc.seenClaimed, "", false); got != tc.want {
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
		if !tr.Done(c, Observation{Status: "closed", Labels: esc}, true, false) {
			t.Error("closed must be done")
		}
	})
	t.Run("untouched is not done (still fails)", func(t *testing.T) {
		var tr Tracker
		for i := 0; i < 3; i++ {
			if tr.Done(c, Observation{Status: "open", Labels: esc, Comments: 2}, true, false) {
				t.Fatal("unchanged bead must not be done")
			}
		}
	})
	t.Run("triage comment appended", func(t *testing.T) {
		var tr Tracker
		tr.Done(c, Observation{Status: "open", Labels: esc, Comments: 2}, true, false)
		if !tr.Done(c, Observation{Status: "open", Labels: esc, Comments: 3}, true, false) {
			t.Error("a new comment (Triage) must be done")
		}
	})
	t.Run("escalated removed", func(t *testing.T) {
		var tr Tracker
		tr.Done(c, Observation{Status: "open", Labels: esc}, true, false)
		if !tr.Done(c, Observation{Status: "open", Labels: []string{"human"}}, true, false) {
			t.Error("escalated removed (Escalate) must be done")
		}
	})
	t.Run("failed read is not done", func(t *testing.T) {
		var tr Tracker
		if tr.Done(c, Observation{}, false, false) {
			t.Error("a failed read must not be done")
		}
	})
	t.Run("comments do not complete close-or-handback", func(t *testing.T) {
		var tr Tracker
		tr.Done(roles.CloseOrHandback, Observation{Status: "open", Comments: 1}, true, false)
		if tr.Done(roles.CloseOrHandback, Observation{Status: "open", Comments: 5}, true, false) {
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
		tr.Observe(c, Observation{Status: "open"}, "")
		if tr.SeenClaimed {
			t.Errorf("%s: open must not latch", c)
		}
		tr.Observe(c, Observation{Status: "in_progress"}, "")
		if !tr.SeenClaimed {
			t.Fatalf("%s: in_progress must latch", c)
		}
		if !tr.Done(c, Observation{Status: "open", Labels: []string{"escalated"}}, true, false) {
			t.Errorf("%s: open after claim is a hand-back", c)
		}
	}
	var tr Tracker
	tr.Observe(roles.CloseOnly, Observation{Status: "in_progress"}, "")
	if tr.SeenClaimed {
		t.Error("close-only must never latch")
	}
}

// TestTracker_closeOrSplitTriage covers the split-triage completion
// (pg2-47rsh): done on close or when needs-split-review is gone; a hand-back
// and comment growth are NOT outcomes.
func TestTracker_closeOrSplitTriage(t *testing.T) {
	c := roles.CloseOrSplitTriage
	parked := []string{"needs-split-review", "agent-support"}
	var tr Tracker
	if tr.Done(c, Observation{Status: "open", Labels: parked, Comments: 1}, true, false) {
		t.Error("still parked must not be done")
	}
	if tr.Done(c, Observation{Status: "open", Labels: parked, Comments: 9}, true, false) {
		t.Error("comment growth must not complete split triage")
	}
	tr.Observe(c, Observation{Status: "in_progress"}, "")
	if tr.Done(c, Observation{Status: "open", Labels: parked}, true, false) {
		t.Error("a hand-back (open after claim) with the label still on must not be done")
	}
	if tr.Done(c, Observation{}, false, false) {
		t.Error("a failed read must not be done")
	}
	if !tr.Done(c, Observation{Status: "open", Labels: []string{"was-split"}}, true, false) {
		t.Error("label removed (split) must be done")
	}
	if !tr.Done(c, Observation{Status: "open", Labels: []string{"human"}}, true, false) {
		t.Error("label removed (human) must be done")
	}
	if !tr.Done(c, Observation{Status: "closed", Labels: parked}, true, false) {
		t.Error("closed must be done")
	}
}

const relActor = "drain-actor"

func TestDoneSignal_closeOrRelease(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		seenClaimed bool
		assignee    string
		ended       bool
		want        bool
	}{
		{"closed, session still running", "closed", false, "", false, true},
		{"open+latch+ended+unassigned = hand-back", "open", true, "", true, true},
		{"open+latch+NOT ended (idle but subagent running)", "open", true, "", false, false},
		{"open+no latch+ended is not DoneSignal (strike path)", "open", false, "", true, false},
		{"deferred+latch+ended = stamp-refusal release", "deferred", true, "", true, true},
		{"deferred+no latch+ended = release (latch lost)", "deferred", false, "", true, true},
		{"deferred+NOT ended", "deferred", true, "", false, false},
		{"in_progress still held", "in_progress", true, relActor, true, false},
		{"open but still assigned to the actor", "open", true, relActor, true, false},
		{"open assigned to a peer", "open", true, "peer", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DoneSignal(roles.CloseOrRelease, tc.status, tc.seenClaimed, tc.assignee, tc.ended); got != tc.want {
				t.Errorf("DoneSignal = %v, want %v", got, tc.want)
			}
		})
	}
	// The other modes ignore assignee and sessionEnded.
	for _, c := range []roles.Completion{roles.CloseOnly, roles.CloseOrHandback} {
		if got := DoneSignal(c, "deferred", true, "", true); got {
			t.Errorf("%s: deferred must never be a hand-back outside close-or-release", c)
		}
	}
	if !DoneSignal(roles.CloseOrHandback, "open", true, "peer", false) {
		t.Error("close-or-handback must stay assignee- and session-blind")
	}
}

func TestTracker_closeOrReleaseLatchIsAssigneeAware(t *testing.T) {
	c := roles.CloseOrRelease
	var tr Tracker
	tr.Observe(c, Observation{Status: "in_progress", Assignee: "peer"}, relActor)
	if tr.SeenClaimed {
		t.Fatal("a peer-held in_progress must NOT set the latch")
	}
	tr.Observe(c, Observation{Status: "in_progress", Assignee: ""}, relActor)
	if tr.SeenClaimed {
		t.Fatal("an unassigned in_progress must NOT set the latch")
	}
	tr.Observe(c, Observation{Status: "open", Assignee: relActor}, relActor)
	if tr.SeenClaimed {
		t.Fatal("open must not set the latch")
	}
	tr.Observe(c, Observation{Status: "in_progress", Assignee: relActor}, "")
	if tr.SeenClaimed {
		t.Fatal("an empty role actor must never match")
	}
	tr.Observe(c, Observation{Status: "in_progress", Assignee: relActor}, relActor)
	if !tr.SeenClaimed {
		t.Fatal("own-actor in_progress must set the latch")
	}
}

// TestTracker_otherModesStillLatchOnPeerHeld is the regression the plan names:
// only close-or-release is assignee-aware.
func TestTracker_otherModesStillLatchOnPeerHeld(t *testing.T) {
	for _, c := range []roles.Completion{roles.CloseOrHandback, roles.CloseOrTriage} {
		var tr Tracker
		tr.Observe(c, Observation{Status: "in_progress", Assignee: "peer"}, relActor)
		if !tr.SeenClaimed {
			t.Errorf("%s: must still latch on a peer-held in_progress (unchanged behavior)", c)
		}
	}
}

func TestTracker_closeOrReleaseDone(t *testing.T) {
	c := roles.CloseOrRelease
	future := Observation{Status: "open", FutureDefer: true}
	blocked := Observation{Status: "open", Blocked: true}
	human := Observation{Status: "open", Labels: []string{"human"}}
	cases := []struct {
		name  string
		latch bool
		obs   Observation
		ok    bool
		ended bool
		want  bool
	}{
		{"closed", false, Observation{Status: "closed"}, true, false, true},
		{"open+latch+ended", true, Observation{Status: "open"}, true, true, true},
		{"open+latch+not ended", true, Observation{Status: "open"}, true, false, false},
		{"open+no latch+ended, no mark", false, Observation{Status: "open"}, true, true, false},
		{"deferred+latch+ended", true, Observation{Status: "deferred"}, true, true, true},
		{"fresh tracker: park (human)", false, human, true, true, true},
		{"fresh tracker: DEFER-ON-EVENT", false, future, true, true, true},
		{"fresh tracker: CONVERT", false, blocked, true, true, true},
		{"fresh-tracker marks need the session to have ended", false, human, true, false, false},
		{"fresh-tracker marks need an unassigned bead", false, Observation{Status: "open", Labels: []string{"human"}, Assignee: relActor}, true, true, false},
		{"read failure", true, Observation{}, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := Tracker{SeenClaimed: tc.latch}
			if got := tr.Done(c, tc.obs, tc.ok, tc.ended); got != tc.want {
				t.Errorf("Done = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTracker_closeOrReleaseEndKind is the classification matrix of INV-CCH-28
// for a session that ended and was not Done.
func TestTracker_closeOrReleaseEndKind(t *testing.T) {
	c := roles.CloseOrRelease
	cases := []struct {
		name  string
		latch bool
		obs   Observation
		ok    bool
		want  End
	}{
		{"peer-held in_progress, no latch", false, Observation{Status: "in_progress", Assignee: "peer"}, true, EndPeer},
		{"peer-held in_progress, latched earlier", true, Observation{Status: "in_progress", Assignee: "peer"}, true, EndPeer},
		{"in_progress with no assignee", false, Observation{Status: "in_progress"}, true, EndPeer},
		{"open assigned to a peer", false, Observation{Status: "open", Assignee: "peer"}, true, EndPeer},
		{"own-actor in_progress: died holding the claim", true, Observation{Status: "in_progress", Assignee: relActor}, true, EndFailed},
		{"open still assigned to the actor", true, Observation{Status: "open", Assignee: relActor}, true, EndFailed},
		{"unclaimed end: open, unassigned, no latch, no mark", false, Observation{Status: "open"}, true, EndUnclaimed},
		{"open+human is a release, not an unclaimed end", false, Observation{Status: "open", Labels: []string{"human"}}, true, EndFailed},
		{"future defer is a release, not an unclaimed end", false, Observation{Status: "open", FutureDefer: true}, true, EndFailed},
		{"blocker edge is a release, not an unclaimed end", false, Observation{Status: "open", Blocked: true}, true, EndFailed},
		{"latched open bead is Done, never an unclaimed end", true, Observation{Status: "open"}, true, EndFailed},
		{"unknown status", false, Observation{Status: "blocked"}, true, EndFailed},
		{"unreadable bead", false, Observation{}, false, EndFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := Tracker{SeenClaimed: tc.latch}
			if got := tr.EndKind(c, tc.obs, tc.ok, relActor); got != tc.want {
				t.Errorf("EndKind = %v, want %v", got, tc.want)
			}
		})
	}
	var tr Tracker
	if got := tr.EndKind(roles.CloseOrHandback, Observation{Status: "open"}, true, relActor); got != EndFailed {
		t.Errorf("only close-or-release classifies an end, got %v", got)
	}
}
