package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// close-or-release (INV-CCH-28): a role whose session claims its bead and MAY
// hand it back unfinished. These tests drive waitDone and finishWait with
// scripted bead reads (status and assignee advance together, one entry per bead
// read) and one scripted session row.

const (
	relActor   = "pgii-pool__drain"
	relSession = "pg-router-drain-zr-d"
	relBead    = "zr-d"
)

func releaseRole(cfg config.Config) roles.Role {
	return roles.Role{
		Name: "drain", Type: "ccpool",
		CCPool: &roles.CCPoolConfig{
			Actor:      relActor,
			Completion: roles.CloseOrRelease, OnFailure: roles.AddHuman, OnDispatchFail: roles.DispatchLeave,
			PromptBody: workerPromptBody, Prompt: mustParsePrompt("drain", workerPromptBody),
			Budget: cfg.WorkerBudget(),
		},
	}
}

// idleRow is a session that is live but idle: ccpool's `Stop` hook state, which
// nothing moves back to working.
func idleRow() ccpool.Session {
	return ccpool.Session{ExternalID: relSession, Live: true, State: ccpool.StateIdle}
}

func releaseDispatch(cfg config.Config) DispatchContext {
	return DispatchContext{Role: releaseRole(cfg), Item: item.Item{ID: relBead}}
}

func hasUpdateContaining(bd *dtest.ScriptBD, sub string) bool {
	for _, u := range bd.Updates {
		if strings.Contains(u, sub) {
			return true
		}
	}
	return false
}

func TestWaitDoneRelease_closedIsDone(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"in_progress", "closed"}}, AssigneeSeq: map[string][]string{relBead: {relActor, relActor}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: relSession, Live: true, State: ccpool.StateWorking}}}}
	e := newExec(cc, bd, cfg)
	if err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession); err != nil {
		t.Fatalf("a closed bead is done: %v", err)
	}
}

func TestWaitDoneRelease_openLatchedAndEndedIsAHandBack(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress", "open"}},
		AssigneeSeq: map[string][]string{relBead: {relActor, ""}},
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{idleRow()}}}
	e := newExec(cc, bd, cfg)
	if err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession); err != nil {
		t.Fatalf("latched + ended + open + unassigned is a hand-back, got %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("a hand-back writes nothing; updates=%v", bd.Updates)
	}
}

func TestWaitDoneRelease_deferredLatchedAndEndedIsAStampRefusalRelease(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress", "deferred"}},
		AssigneeSeq: map[string][]string{relBead: {relActor, ""}},
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{idleRow()}}}
	e := newExec(cc, bd, cfg)
	if err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession); err != nil {
		t.Fatalf("deferred + unassigned + ended must be done, not turned into human: %v", err)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("no bead write expected; updates=%v", bd.Updates)
	}
}

// TestWaitDoneRelease_freshTrackerReleaseMarks is the fresh-Tracker hole: after
// a daemon restart the latch is lost, and a worker's PARK, DEFER-ON-EVENT or
// CONVERT must still read as done, never as an unclaimed end or a failure.
func TestWaitDoneRelease_freshTrackerReleaseMarks(t *testing.T) {
	future := time.Unix(0, 0).Add(24 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		name string
		bd   func() *dtest.ScriptBD
	}{
		{"park: open + human", func() *dtest.ScriptBD {
			return &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}, Labels: map[string][]string{relBead: {"human"}}}
		}},
		{"DEFER-ON-EVENT: open + future defer_until", func() *dtest.ScriptBD {
			return &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}, DeferUntil: map[string]string{relBead: future}}
		}},
		{"CONVERT: open + open blocks dependency", func() *dtest.ScriptBD {
			return &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}, BlockerStatus: map[string]string{relBead: "open"}}
		}},
		{"stamp refusal: deferred, no latch", func() *dtest.ScriptBD {
			return &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"deferred"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastCfg()
			bd := tc.bd()
			e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{idleRow()}}}, bd, cfg)
			if err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession); err != nil {
				t.Fatalf("a release mark without the latch must still be done: %v", err)
			}
			if len(bd.Updates) != 0 {
				t.Errorf("no strike and no write expected; updates=%v", bd.Updates)
			}
		})
	}
}

// A PAST defer_until and a CLOSED blocker are not release marks.
func TestWaitDoneRelease_pastDeferAndClosedBlockerAreNotReleaseMarks(t *testing.T) {
	past := time.Unix(0, 0).Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq:     map[string][]string{relBead: {"open"}},
		DeferUntil:    map[string]string{relBead: past},
		BlockerStatus: map[string]string{relBead: "closed"},
	}
	e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{idleRow()}}}, bd, cfg)
	err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
	if !errors.Is(err, ErrUnclaimedEnd) {
		t.Fatalf("got %v, want an unclaimed end", err)
	}
}

func TestWaitDoneRelease_unclaimedEndTwoStrikes(t *testing.T) {
	cfg := fastCfg()
	// Strike one: no latch, open, unassigned, no marks.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}}
	e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{idleRow()}}}, bd, cfg)
	err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
	var ue *unclaimedEnd
	if !errors.Is(err, ErrUnclaimedEnd) || !errors.As(err, &ue) || ue.escalated {
		t.Fatalf("strike one: got %v, want an unclaimed end that is not escalated", err)
	}
	if !dtest.HasUpdate(bd, "update zr-d --add-label drain-unclaimed-end") {
		t.Errorf("strike one must add the drain-unclaimed-end label; updates=%v", bd.Updates)
	}
	if hasUpdateContaining(bd, "--add-label human") {
		t.Errorf("strike one must NOT add human; updates=%v", bd.Updates)
	}
	if got := e.waitFailureResult(releaseRole(cfg).CCPool, relBead, err); got.Actions[0].Verb != report.Unclaimed {
		t.Errorf("strike one reports %+v, want Unclaimed", got)
	}

	// Strike two: the label is already there.
	bd = &dtest.ScriptBD{
		StatusSeq: map[string][]string{relBead: {"open"}},
		Labels:    map[string][]string{relBead: {"drain-unclaimed-end"}},
	}
	e = newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{idleRow()}}}, bd, cfg)
	err = e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
	if !errors.Is(err, ErrUnclaimedEnd) || !errors.As(err, &ue) || !ue.escalated {
		t.Fatalf("strike two: got %v, want an escalated unclaimed end", err)
	}
	if !dtest.HasUpdate(bd, "update zr-d --add-label human") {
		t.Errorf("strike two must add human; updates=%v", bd.Updates)
	}
	if got := e.waitFailureResult(releaseRole(cfg).CCPool, relBead, err); got.Actions[0].Verb != report.Escalated {
		t.Errorf("strike two reports %+v, want Escalated", got)
	}
}

// The strike label is not the budget-stop prefix INV-CCH-11 owns.
func TestUnclaimedEndLabelIsNotABudgetStop(t *testing.T) {
	if strings.HasPrefix(unclaimedEndLabel, beads.BudgetStopLabelPrefix) {
		t.Fatalf("%q must not use the budget-stop: prefix", unclaimedEndLabel)
	}
}

// A peer that holds the bead: no write, no strike, no human, and the end is
// classified without on_failure (the role's on_failure is add-human).
func TestWaitDoneRelease_peerHeldWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		assignee string
	}{
		{"peer in_progress", "in_progress", "someone-else"},
		{"peer open assigned", "open", "someone-else"},
		{"in_progress, no assignee", "in_progress", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastCfg()
			bd := &dtest.ScriptBD{
				StatusSeq:   map[string][]string{relBead: {tc.status}},
				AssigneeSeq: map[string][]string{relBead: {tc.assignee}},
			}
			e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{idleRow()}}}, bd, cfg)
			err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
			if !errors.Is(err, ErrPeerHeld) {
				t.Fatalf("got %v, want ErrPeerHeld", err)
			}
			if len(bd.Updates) != 0 || len(bd.Comments) != 0 {
				t.Errorf("a peer-held bead must get no write; updates=%v comments=%v", bd.Updates, bd.Comments)
			}
			if got := e.waitFailureResult(releaseRole(cfg).CCPool, relBead, err); len(got.Actions) != 0 {
				t.Errorf("a peer-held end reports no verb, got %+v", got)
			}
		})
	}
}

// A peer-held bead with a ccpool-closed session must not be unclaimed by the
// external-close branch (that would write to the peer's bead).
func TestWaitDoneRelease_peerHeldBeatsExternalClose(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress"}},
		AssigneeSeq: map[string][]string{relBead: {"someone-else"}},
	}
	row := ccpool.Session{ExternalID: relSession, Live: false, State: ccpool.StateWorking, CloseReason: "idle_ttl"}
	e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}, bd, cfg)
	err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
	if !errors.Is(err, ErrPeerHeld) {
		t.Fatalf("got %v, want ErrPeerHeld", err)
	}
	if len(bd.Updates) != 0 || len(bd.Comments) != 0 {
		t.Errorf("no write to the peer's bead; updates=%v comments=%v", bd.Updates, bd.Comments)
	}
}

// An external close of a session that never claimed keeps INV-CCH-7's behavior
// (release + pool-evicted strike), ahead of the unclaimed-end strike.
func TestWaitDoneRelease_externalCloseBeatsUnclaimedEnd(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}}
	row := ccpool.Session{ExternalID: relSession, Live: false, State: ccpool.StateWorking, CloseReason: "cap_eviction"}
	e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}, bd, cfg)
	err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
	if !errors.Is(err, ErrExternallyClosed) {
		t.Fatalf("got %v, want ErrExternallyClosed", err)
	}
	if hasUpdateContaining(bd, "drain-unclaimed-end") {
		t.Errorf("no unclaimed-end strike for an external close; updates=%v", bd.Updates)
	}
}

// The session died holding its own claim: on_failure applies, and the earlier
// unclaimed-end strike is cleared (the worker did claim).
func TestWaitDoneRelease_diedHoldingClaimAppliesOnFailureAndClearsStrike(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress"}},
		AssigneeSeq: map[string][]string{relBead: {relActor}},
	}
	row := ccpool.Session{ExternalID: relSession, Live: false, State: ccpool.StateErrored}
	e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}, bd, cfg)
	err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
	if err == nil || errors.Is(err, ErrPeerHeld) || errors.Is(err, ErrUnclaimedEnd) {
		t.Fatalf("got %v, want a plain failure", err)
	}
	if !dtest.HasUpdate(bd, "update zr-d --add-label human --status=open --assignee=") {
		t.Errorf("on_failure add-human must apply; updates=%v", bd.Updates)
	}
	if !dtest.HasUpdate(bd, "update zr-d --remove-label drain-unclaimed-end") {
		t.Errorf("a latched end clears the unclaimed-end strike; updates=%v", bd.Updates)
	}
}

// idle-but-subagent-running is NOT ended: while the transcript or a subagent
// transcript is still being written, an idle session with the bead handed back
// must not complete.
func TestWaitDoneRelease_idleButSubagentRunningIsNotEnded(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeQuietWindow = 10 * time.Millisecond
	cfg.WorktreeQuietMax = 20 * time.Millisecond
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress", "open"}},
		AssigneeSeq: map[string][]string{relBead: {relActor, ""}},
	}
	row := idleRow()
	row.TranscriptPath = "/scratch/session.jsonl"
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}
	e := newExec(cc, bd, cfg)
	// A subagent keeps writing: the newest activity is always "now".
	e.deps.LatestActivity = func(string) (time.Time, bool) { return e.deps.Now(), true }
	err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession)
	if err == nil {
		t.Fatal("a bare idle with a subagent still writing must not complete a close-or-release dispatch")
	}
	if errors.Is(err, ErrPeerHeld) || errors.Is(err, ErrUnclaimedEnd) {
		t.Fatalf("not ended means no end classification, got %v", err)
	}
	// And with the same bead, once the transcripts go quiet, it is a hand-back.
	bd = &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress", "open"}},
		AssigneeSeq: map[string][]string{relBead: {relActor, ""}},
	}
	e = newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}, bd, cfg)
	e.deps.LatestActivity = func(string) (time.Time, bool) { return time.Unix(0, 0).Add(-time.Hour), true }
	if err := e.waitDone(context.Background(), nil, releaseDispatch(cfg), relSession); err != nil {
		t.Fatalf("a quiet idle session with the bead handed back is done: %v", err)
	}
}

// close-or-handback keeps reading a bare idle as the end (unchanged behavior).
func TestWaitDoneHandback_bareIdleStillEnds(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeQuietWindow = 10 * time.Millisecond
	cfg.WorktreeQuietMax = 20 * time.Millisecond
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateIdle, TranscriptPath: "/scratch/s.jsonl"}
	e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}, bd, cfg)
	e.deps.LatestActivity = func(string) (time.Time, bool) { return e.deps.Now(), true }
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err != nil {
		t.Fatalf("close-or-handback must still treat idle as the end: %v", err)
	}
}

// close-or-handback and close-or-triage STILL latch on a peer-held in_progress
// bead, end to end: the peer-held bead then handed back reads as done.
func TestWaitDoneHandback_stillLatchesOnPeerHeldInProgress(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{"zr-w": {"in_progress", "open"}},
		AssigneeSeq: map[string][]string{"zr-w": {"someone-else", ""}},
	}
	row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateIdle}
	e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err != nil {
		t.Fatalf("close-or-handback latches on any in_progress: %v", err)
	}
}

// finishWait: a peer-held end is a success with no verb, writes nothing and
// clears nothing; an unclaimed end carries its verb; a completed dispatch clears
// the strike label.
func TestFinishWaitRelease(t *testing.T) {
	cfg := fastCfg()
	cc := releaseRole(cfg).CCPool
	d := releaseDispatch(cfg)

	t.Run("peer held", func(t *testing.T) {
		bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"in_progress"}}}
		e := newExec(&dtest.FakeCC{}, bd, cfg)
		res, err := e.finishWait(context.Background(), cc, d, relSession, "", errors.Join(ErrPeerHeld))
		if err != nil || len(res.Actions) != 0 {
			t.Fatalf("got %+v, %v; want an empty result and no error", res, err)
		}
		if len(bd.Updates) != 0 {
			t.Errorf("no write to a peer's bead; updates=%v", bd.Updates)
		}
	})
	t.Run("unclaimed end", func(t *testing.T) {
		bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}}
		e := newExec(&dtest.FakeCC{}, bd, cfg)
		res, err := e.finishWait(context.Background(), cc, d, relSession, "", &unclaimedEnd{escalated: true})
		if !errors.Is(err, ErrUnclaimedEnd) || len(res.Actions) != 1 || res.Actions[0].Verb != report.Escalated {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("completed dispatch clears the strike", func(t *testing.T) {
		bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"closed"}}}
		e := newExec(&dtest.FakeCC{}, bd, cfg)
		if _, err := e.finishWait(context.Background(), cc, d, relSession, "", nil); err != nil {
			t.Fatal(err)
		}
		if !dtest.HasUpdate(bd, "update zr-d --remove-label drain-unclaimed-end") {
			t.Errorf("updates=%v", bd.Updates)
		}
	})
	t.Run("other modes do not touch the label", func(t *testing.T) {
		bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"closed"}}}
		e := newExec(&dtest.FakeCC{}, bd, cfg)
		w := workerRole(cfg)
		if _, err := e.finishWait(context.Background(), w.CCPool, DispatchContext{Role: w, Item: item.Item{ID: "zr-w"}}, "s", "", nil); err != nil {
			t.Fatal(err)
		}
		if hasUpdateContaining(bd, "drain-unclaimed-end") {
			t.Errorf("updates=%v", bd.Updates)
		}
	})
}
