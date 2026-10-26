package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/eventlog"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/report"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// A session that dies on a usage or spend limit or an API outage is not a
// failure of the work, whatever kind of role it ran (pg2-jzttp, widening
// pg2-e0imq's ruling of 2026-10-14 from the unclaimed end of a drain role to the
// death path every other role takes; INV-CCH-28). These tests drive the real
// death path (waitDone, then finishWait) against a real transcript and event
// log; only bd and ccpool are scripted.

const (
	triBead    = "pg2-x"
	triSession = "pg-router-pg2-escalation-triager-pg2-x"
)

type deathOutcome struct {
	err    error
	result report.Result
	bd     *dtest.ScriptBD
	raw    string // the event log
}

// runTriagerDeath runs one close-or-triage dispatch (on_failure add-human) whose
// session died with the given row state and transcript lines, on a bead that
// still carries `escalated` and was never claimed (a triager works a bead it
// does not claim).
func runTriagerDeath(t *testing.T, state ccpool.SessionState, live bool, lines ...string) deathOutcome {
	t.Helper()
	cfg := fastCfg()
	transcript := ""
	if len(lines) > 0 {
		transcript = writeTranscript(t, lines...)
	}
	bd := &dtest.ScriptBD{
		StatusSeq: map[string][]string{triBead: {"open"}},
		Labels:    map[string][]string{triBead: {"escalated"}},
	}
	return runDeath(t, cfg, bd, triagerRole(cfg), triBead, triSession, state, live, transcript)
}

func runDeath(t *testing.T, cfg config.Config, bd *dtest.ScriptBD, role roles.Role, bead, session string, state ccpool.SessionState, live bool, transcript string) deathOutcome {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	row := ccpool.Session{ExternalID: session, Live: live, State: state, TranscriptPath: transcript}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}
	e := newExec(cc, bd, cfg)
	e.deps.Log = w
	e.deps.LatestActivity = func(string) (time.Time, bool) { return time.Time{}, false }
	d := DispatchContext{Role: role, Item: item.Item{ID: bead}}
	werr := e.waitDone(context.Background(), nil, d, session)
	res, ferr := e.finishWait(context.Background(), d.Role.CCPool, d, session, "", werr)
	_ = w.Close()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return deathOutcome{err: ferr, result: res, bd: bd, raw: string(raw)}
}

// A triager that dies on a limit or an outage leaves the bead exactly as it
// found it: released (the same guarded release every death makes), NO human
// label, no park note, no escalated verb. The error carries the documented
// stable substring so the router can count it, and the dispatch is still
// recorded as a failure with the signature.
func TestDeathOnLimit_triagerIsNotParkedHuman(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"spend limit", spendLimitText, "api-rate-limit"},
		{"usage limit", "You've hit your limit · resets 3:30pm (America/New_York)", "api-rate-limit"},
		{"429", "API Error: 429 Too Many Requests", "api-rate-limit"},
		{"529 overloaded", "API Error: 529 Overloaded", "api-transient"},
		{"unable to connect", "Unable to connect to API (ConnectionRefused)", "api-transient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runTriagerDeath(t, ccpool.StateErrored, true, assistantText(t, tc.text))
			if !errors.Is(out.err, ErrSessionDied) {
				t.Fatalf("got %v, want a session death", out.err)
			}
			if !strings.Contains(out.err.Error(), UsageLimitEndSentinel) {
				t.Errorf("error %q lacks the documented substring %q", out.err, UsageLimitEndSentinel)
			}
			if hasUpdateContaining(out.bd, "human") {
				t.Errorf("a limit death must not park the bead human; updates=%v", out.bd.Updates)
			}
			if !dtest.HasUpdate(out.bd, "update "+triBead+" --status=open --assignee=") {
				t.Errorf("the death's guarded release must still be made; updates=%v", out.bd.Updates)
			}
			if len(out.bd.Comments) != 0 {
				t.Errorf("a limit death must not leave a park note; comments=%v", out.bd.Comments)
			}
			for _, a := range out.result.Actions {
				if a.Verb == report.Escalated {
					t.Errorf("a limit death must not report an escalation; actions=%v", verbsOf(out.result))
				}
			}
			if rec := findKind(t, out.raw, dispatchResultKind); rec["failure_signature"] != tc.want {
				t.Errorf("dispatch_result failure_signature = %v, want %s; rec=%v", rec["failure_signature"], tc.want, rec)
			}
		})
	}
}

// Every other cause of a triager's death keeps today's behaviour exactly: the
// release rides the single add-human update, the park note is written, the
// escalated verb is reported, and the error text is unchanged.
func TestDeathOnOtherCause_triagerIsStillParkedHuman(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state ccpool.SessionState
		live  bool
		lines []string
	}{
		{"api-auth", ccpool.StateErrored, true, []string{assistantText(t, "Please run /login · API Error: 401 Invalid authentication credentials")}},
		{"context-limit", ccpool.StateErrored, true, []string{assistantText(t, "Prompt is too long")}},
		{"session-errored", ccpool.StateErrored, true, []string{assistantText(t, "working")}},
		{"session-gone", ccpool.StateWorking, false, []string{assistantText(t, "working")}},
		{"no transcript", ccpool.StateErrored, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runTriagerDeath(t, tc.state, tc.live, tc.lines...)
			if out.err == nil || out.err.Error() != triBead+": session exited before completing" {
				t.Fatalf("the failure text must be unchanged; err=%v", out.err)
			}
			if !dtest.HasUpdate(out.bd, "update "+triBead+" --add-label human --status=open --assignee=") {
				t.Errorf("on_failure=add-human must apply in one update; updates=%v", out.bd.Updates)
			}
			if len(out.bd.Comments) == 0 {
				t.Errorf("the park note must be written; comments=%v", out.bd.Comments)
			}
			got := verbsOf(out.result)
			if len(got) != 2 || got[0] != report.Escalated || got[1] != report.Unclaimed {
				t.Errorf("result actions = %v, want [escalated unclaimed]", got)
			}
		})
	}
}

// A drain worker that claimed its bead and then died on a limit (the death path,
// not the unclaimed end) is released without human too: the same ruling, the
// same rule for every role whose on_failure is add-human.
func TestDeathOnLimit_claimedDrainWorkerIsNotParkedHuman(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{relBead: {"in_progress"}},
		AssigneeSeq: map[string][]string{relBead: {relActor}},
	}
	out := runDeath(t, cfg, bd, releaseRole(cfg), relBead, relSession, ccpool.StateErrored, false,
		writeTranscript(t, assistantText(t, spendLimitText)))
	if !errors.Is(out.err, ErrSessionDied) || !strings.Contains(out.err.Error(), UsageLimitEndSentinel) {
		t.Fatalf("got %v, want an exempt session death", out.err)
	}
	if hasUpdateContaining(out.bd, "human") {
		t.Errorf("a limit death must not park the bead human; updates=%v", out.bd.Updates)
	}
	if !hasUpdateContaining(out.bd, "update "+relBead+" --status=open --assignee= --if-assignee "+relActor) {
		t.Errorf("the claim must be released; updates=%v", out.bd.Updates)
	}
}
