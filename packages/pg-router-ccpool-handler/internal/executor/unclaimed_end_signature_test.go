package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
)

// An unclaimed end carries the failure signature of the dying session, and an
// end caused by a usage or spend limit or an API outage is exempt from the
// two-strike policy (INV-CCH-28, INV-CCH-9). These tests drive the real
// unclaimed-end path (waitDone, then finishWait) against a real transcript file
// and a real event log; only bd and ccpool are scripted.

// spendLimitText is the status-line wording recorded on 2026-10-14 when the
// account hit its individual spend limit (pg2-e0imq).
const spendLimitText = "You've hit your individual spend limit · run /usage-credits to ask your admin for a higher limit · your session limit resets 4:30am (America/New_York)"

// noCapacityCC fails the test if the capacity usage-limit reading is consulted:
// the exemption MUST NOT depend on it.
type noCapacityCC struct {
	*dtest.FakeCC
	t *testing.T
}

func (c noCapacityCC) Capacity(context.Context) (ccpool.Capacity, error) {
	c.t.Helper()
	c.t.Error("the unclaimed-end path read the capacity usage-limit reading")
	return ccpool.Capacity{}, nil
}

// unclaimedOutcome is what one unclaimed end left behind.
type unclaimedOutcome struct {
	err    error
	result report.Result
	bd     *dtest.ScriptBD
	raw    string // the event log
}

// runUnclaimedEnd runs one close-or-release dispatch whose session ended
// without ever claiming the bead (open, unassigned, no release mark), with the
// given transcript lines and ccpool row state, on a bead that already carries
// labels. A fresh run each call, as each real dispatch is.
func runUnclaimedEnd(t *testing.T, state ccpool.SessionState, live bool, labels []string, lines ...string) unclaimedOutcome {
	t.Helper()
	cfg := fastCfg()
	transcript := ""
	if len(lines) > 0 {
		transcript = writeTranscript(t, lines...)
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}}
	if labels != nil {
		bd.Labels = map[string][]string{relBead: labels}
	}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	row := ccpool.Session{ExternalID: relSession, Live: live, State: state, TranscriptPath: transcript}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}
	e := newExec(cc, bd, cfg)
	e.deps.CC = noCapacityCC{FakeCC: cc, t: t}
	e.deps.Log = w
	e.deps.LatestActivity = func(string) (time.Time, bool) { return time.Time{}, false }
	d := releaseDispatch(cfg)
	werr := e.waitDone(context.Background(), nil, d, relSession)
	res, ferr := e.finishWait(context.Background(), d.Role.CCPool, d, relSession, "", werr)
	if (werr == nil) != (ferr == nil) {
		t.Fatalf("finishWait changed the outcome: waitDone=%v finishWait=%v", werr, ferr)
	}
	_ = w.Close()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return unclaimedOutcome{err: ferr, result: res, bd: bd, raw: string(raw)}
}

func assistantText(t *testing.T, text string) string {
	t.Helper()
	return jsonlRecord(t, "assistant", []map[string]any{{"type": "text", "text": text}})
}

// The recorded spend-limit text, through the real unclaimed-end path, twice on
// one bead (a fresh dispatch each time, as in the 08:02Z burst): neither end
// strikes. Nothing is written to the bead, with no strike label present and
// with one already present.
func TestUnclaimedEnd_spendLimitTwiceLandsNoStrike(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []string
	}{
		{"no prior strike", nil},
		{"a prior strike is left as it was", []string{unclaimedEndLabel}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 1; i <= 2; i++ {
				out := runUnclaimedEnd(t, ccpool.StateErrored, false, tc.labels, assistantText(t, spendLimitText))
				if !errors.Is(out.err, ErrUnclaimedEnd) {
					t.Fatalf("end %d: got %v, want an unclaimed end", i, out.err)
				}
				if len(out.bd.Updates) != 0 || len(out.bd.Comments) != 0 {
					t.Errorf("end %d: a limit death must write nothing to the bead; updates=%v comments=%v", i, out.bd.Updates, out.bd.Comments)
				}
				if hasUpdateContaining(out.bd, unclaimedEndLabel) || hasUpdateContaining(out.bd, "human") {
					t.Errorf("end %d: neither strike may land; updates=%v", i, out.bd.Updates)
				}
				var ue *unclaimedEnd
				if !errors.As(out.err, &ue) || !ue.exempt || ue.escalated {
					t.Errorf("end %d: got %+v, want an exempt, non-escalated end", i, ue)
				}
				if len(out.result.Actions) != 0 {
					t.Errorf("end %d: no bead write means no verb, got %+v", i, out.result)
				}
				// The documented stable substring the router classifies on.
				if !strings.Contains(out.err.Error(), UsageLimitEndSentinel) {
					t.Errorf("end %d: error %q lacks the documented substring %q", i, out.err, UsageLimitEndSentinel)
				}
				rec := findKind(t, out.raw, "unclaimed_end")
				if rec["failure_signature"] != "api-rate-limit" || rec["escalated"] != false {
					t.Errorf("end %d: unclaimed_end record = %v", i, rec)
				}
			}
		})
	}
}

// Every API-limit or API-outage shape is exempt, whatever the row state: the
// bead stays as it was and the error names the documented substring.
func TestUnclaimedEnd_limitAndOutageShapesAreExempt(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"spend limit, curly apostrophe", strings.Replace(spendLimitText, "'", "’", 1), "api-rate-limit"},
		{"spend limit, have", "You have hit your individual spend limit", "api-rate-limit"},
		{"usage limit", "You've hit your limit · resets 3:30pm (America/New_York)", "api-rate-limit"},
		{"429", "API Error: 429 Too Many Requests", "api-rate-limit"},
		{"529 overloaded", "API Error: 529 Overloaded", "api-transient"},
		{"500", "API Error: 500 Internal server error. This is a server-side issue, usually temporary.", "api-transient"},
		{"socket closed", "API Error: The socket connection was closed unexpectedly.", "api-transient"},
		{"unable to connect", "Unable to connect to API (ConnectionRefused)", "api-transient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A strike already present: the exempt end neither adds human nor clears it.
			out := runUnclaimedEnd(t, ccpool.StateIdle, true, []string{unclaimedEndLabel}, assistantText(t, tc.text))
			if !errors.Is(out.err, ErrUnclaimedEnd) || !strings.Contains(out.err.Error(), UsageLimitEndSentinel) {
				t.Fatalf("got %v, want an exempt unclaimed end", out.err)
			}
			if len(out.bd.Updates) != 0 {
				t.Errorf("an exempt end must write nothing; updates=%v", out.bd.Updates)
			}
			if rec := findKind(t, out.raw, "unclaimed_end"); rec["failure_signature"] != tc.want {
				t.Errorf("failure_signature = %v, want %s; rec=%v", rec["failure_signature"], tc.want, rec)
			}
		})
	}
}

// The exemption keys on the failure signature alone. The capacity usage-limit
// reading (usage_limit_present) was false at 08:02Z inside a blocked window, so
// a death with that reading false and a spend-limit transcript is still
// exempt: runUnclaimedEnd's ccpool fails the test if Capacity is read at all.
func TestUnclaimedEnd_exemptionDoesNotReadTheCapacityUsageLimit(t *testing.T) {
	out := runUnclaimedEnd(t, ccpool.StateErrored, false, nil, assistantText(t, spendLimitText))
	var ue *unclaimedEnd
	if !errors.As(out.err, &ue) || !ue.exempt {
		t.Fatalf("got %v, want an exempt end", out.err)
	}
	if strings.Contains(out.raw, "usage_limit_present") {
		t.Errorf("the unclaimed end must not log a capacity reading: %s", out.raw)
	}
}

// Every other signature keeps today's two-strike policy exactly: the first end
// adds drain-unclaimed-end, the second consecutive end adds human, and neither
// carries the usage-limit substring.
func TestUnclaimedEnd_otherSignaturesKeepTwoStrikes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state ccpool.SessionState
		live  bool
		lines func(t *testing.T) []string
		want  failsig.Signature
	}{
		{"api-auth", ccpool.StateErrored, false, func(t *testing.T) []string {
			return []string{assistantText(t, "Please run /login · API Error: 401 Invalid authentication credentials")}
		}, failsig.APIAuth},
		{"context-limit", ccpool.StateErrored, false, func(t *testing.T) []string {
			return []string{assistantText(t, "Prompt is too long")}
		}, failsig.ContextLimit},
		{"session-idle", ccpool.StateIdle, true, func(t *testing.T) []string {
			return []string{assistantText(t, "I believe the bead is done.")}
		}, failsig.SessionIdle},
		{"session-errored", ccpool.StateErrored, false, func(t *testing.T) []string {
			return []string{assistantText(t, "working")}
		}, failsig.SessionErrored},
		{"session-gone", ccpool.StateWorking, false, func(t *testing.T) []string {
			return []string{assistantText(t, "working")}
		}, failsig.SessionGone},
		{"git-auth", ccpool.StateIdle, true, func(t *testing.T) []string {
			return []string{toolResult(t, oauthFailure)}
		}, failsig.GitAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			one := runUnclaimedEnd(t, tc.state, tc.live, nil, tc.lines(t)...)
			var ue *unclaimedEnd
			if !errors.As(one.err, &ue) || ue.exempt || ue.escalated {
				t.Fatalf("strike one: got %v, want a plain first strike", one.err)
			}
			if strings.Contains(one.err.Error(), UsageLimitEndSentinel) {
				t.Errorf("strike one must not carry the usage-limit substring: %v", one.err)
			}
			if !dtest.HasUpdate(one.bd, "update zr-d --add-label drain-unclaimed-end") || hasUpdateContaining(one.bd, "--add-label human") {
				t.Errorf("strike one must add only the strike label; updates=%v", one.bd.Updates)
			}
			if got := findKind(t, one.raw, "unclaimed_end"); got["failure_signature"] != string(tc.want) {
				t.Errorf("failure_signature = %v, want %s", got["failure_signature"], tc.want)
			}

			two := runUnclaimedEnd(t, tc.state, tc.live, []string{unclaimedEndLabel}, tc.lines(t)...)
			if !errors.As(two.err, &ue) || ue.exempt || !ue.escalated {
				t.Fatalf("strike two: got %v, want an escalated end", two.err)
			}
			if !dtest.HasUpdate(two.bd, "update zr-d --add-label human") {
				t.Errorf("strike two must add human; updates=%v", two.bd.Updates)
			}
			if len(two.result.Actions) != 1 || two.result.Actions[0].Verb != report.Escalated {
				t.Errorf("strike two reports %+v, want Escalated", two.result)
			}
		})
	}
}

// A classification of unknown keeps the two strikes too. A real unclaimed end
// cannot reach it through the session facts (an ended session is errored, idle
// or gone), so the captured classification is seeded directly.
func TestUnclaimedEnd_unknownSignatureKeepsTwoStrikes(t *testing.T) {
	cfg := fastCfg()
	for _, labels := range [][]string{nil, {unclaimedEndLabel}} {
		bd := &dtest.ScriptBD{StatusSeq: map[string][]string{relBead: {"open"}}}
		if labels != nil {
			bd.Labels = map[string][]string{relBead: labels}
		}
		e := newExec(&dtest.FakeCC{}, bd, cfg)
		e.captured = &failsig.Result{Signature: failsig.Unknown, Evidence: "ccpool-session: not-observed"}
		err := e.unclaimedEndResult(context.Background(), releaseDispatch(cfg), relSession)
		var ue *unclaimedEnd
		if !errors.As(err, &ue) || ue.exempt || ue.escalated != (labels != nil) {
			t.Fatalf("labels=%v: got %v", labels, err)
		}
		want := "update zr-d --add-label drain-unclaimed-end"
		if labels != nil {
			want = "update zr-d --add-label human"
		}
		if !dtest.HasUpdate(bd, want) {
			t.Errorf("labels=%v: want %q; updates=%v", labels, want, bd.Updates)
		}
	}
}

// The substring is a contract with pg-router (interfaces.md, "Usage-limit
// sentinel"); pg-router's own test pins the same literal on its side, so a
// reworded constant on either side fails a test instead of silently
// reclassifying the series.
func TestUsageLimitEndSentinelIsTheDocumentedContract(t *testing.T) {
	if UsageLimitEndSentinel != "session ended on a usage limit or API outage" {
		t.Errorf("UsageLimitEndSentinel = %q; the documented contract is %q", UsageLimitEndSentinel, "session ended on a usage limit or API outage")
	}
}

// Every unclaimed_end record carries failure_signature and signature_evidence
// (INV-CCH-9 shape: closed set, redacted, at most 300 characters, captured
// before the session is closed or purged), keeps its kind and existing fields,
// and no dispatch_result is added for an unclaimed end.
func TestUnclaimedEnd_recordCarriesSignatureAndEvidence(t *testing.T) {
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)
	long := strings.Repeat("x ", 400)
	for _, tc := range []struct {
		name  string
		state ccpool.SessionState
		live  bool
		lines []string
	}{
		{"limit death", ccpool.StateErrored, false, []string{assistantText(t, spendLimitText+" "+token)}},
		{"idle, nothing recognizable", ccpool.StateIdle, true, []string{assistantText(t, long)}},
		{"no transcript at all", ccpool.StateIdle, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runUnclaimedEnd(t, tc.state, tc.live, nil, tc.lines...)
			rec := findKind(t, out.raw, "unclaimed_end")
			sig, _ := rec["failure_signature"].(string)
			if !failsig.Signature(sig).Valid() {
				t.Errorf("failure_signature %q is not in the closed set; rec=%v", sig, rec)
			}
			ev, ok := rec["signature_evidence"].(string)
			if !ok || ev == "" || len(ev) > failsig.MaxEvidenceLen {
				t.Errorf("signature_evidence = %q (present=%v), want non-empty and <= %d bytes", ev, ok, failsig.MaxEvidenceLen)
			}
			if strings.Contains(out.raw, token) {
				t.Errorf("the event log leaked the credential: %s", out.raw)
			}
			for _, k := range []string{"role", "bead", "session", "escalated"} {
				if _, ok := rec[k]; !ok {
					t.Errorf("existing field %q is gone from the record: %v", k, rec)
				}
			}
			if rec["bead"] != relBead || rec["session"] != relSession {
				t.Errorf("bead/session = %v/%v", rec["bead"], rec["session"])
			}
			if strings.Contains(out.raw, dispatchResultKind) {
				t.Errorf("an unclaimed end must not add a dispatch_result: %s", out.raw)
			}
		})
	}
}
