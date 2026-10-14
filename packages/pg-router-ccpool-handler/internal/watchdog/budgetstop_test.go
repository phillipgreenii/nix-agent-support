package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
)

// stopBD is an ordered fake bd: it records every call, keeps the label set a
// `--add-label` writes (set semantics, so a duplicate is a no-op), serves it on
// `show`, and can fail a chosen verb.
type stopBD struct {
	calls  []string
	labels map[string]bool
	failOn string // substring of the joined args that errors
	status string // status `show` reports; "" = in_progress
}

func (b *stopBD) Run(_ context.Context, args ...string) (string, error) {
	j := strings.Join(args, " ")
	b.calls = append(b.calls, j)
	if b.failOn != "" && strings.Contains(j, b.failOn) {
		return "", errors.New("bd down")
	}
	switch args[0] {
	case "update":
		for i, a := range args {
			if a == "--add-label" {
				if b.labels == nil {
					b.labels = map[string]bool{}
				}
				b.labels[args[i+1]] = true
			}
		}
	case "show":
		var ls []string
		for l := range b.labels {
			ls = append(ls, fmt.Sprintf("%q", l))
		}
		st := b.status
		if st == "" {
			st = "in_progress"
		}
		return `{"id":"zr-1","status":"` + st + `","labels":[` + strings.Join(ls, ",") + `]}`, nil
	}
	return "", nil
}

func (b *stopBD) idx(s string) int {
	for i, c := range b.calls {
		if c == s {
			return i
		}
	}
	return -1
}

func newStopWD(bd *stopBD, threshold int) *Watchdog {
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, &fakeCC{list: []ccpool.Session{{ExternalID: "s", CWD: "/repo"}}}, &recBD{}, tokBudget(1000))
	wd.BD = bd
	wd.BudgetStopEscalateAfter = threshold
	return wd
}

const unclaim = "update zr-1 --status=open --assignee="

func TestTerminal_budgetStop_recordsBeforeUnclaimWithExactComment(t *testing.T) {
	bd := &stopBD{}
	newStopWD(bd, 3).terminal(context.Background(), "s", "zr-1", &BudgetError{})
	rec := bd.idx("update zr-1 --add-label budget-stop:s")
	cm := bd.idx("comment zr-1 budget stop 1 of 3 (session s)")
	un := bd.idx(unclaim)
	if rec < 0 || cm < 0 || un < 0 {
		t.Fatalf("missing call(s) rec=%d comment=%d unclaim=%d; calls=%v", rec, cm, un, bd.calls)
	}
	if !(rec < un && cm < un) {
		t.Errorf("record and comment must precede unclaim; calls=%v", bd.calls)
	}
}

func TestTerminal_budgetStop_duplicateSessionCountsOnce(t *testing.T) {
	bd := &stopBD{}
	wd := newStopWD(bd, 3)
	wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
	wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
	if bd.idx("comment zr-1 budget stop 1 of 3 (session s)") < 0 || bd.idx("comment zr-1 budget stop 2 of 3 (session s)") >= 0 {
		t.Errorf("same session id must count once; calls=%v", bd.calls)
	}
	wd.terminal(context.Background(), "s2", "zr-1", &BudgetError{})
	if bd.idx("comment zr-1 budget stop 2 of 3 (session s2)") < 0 {
		t.Errorf("distinct session must make the count 2; calls=%v", bd.calls)
	}
}

func TestTerminal_budgetStop_writeFailureFallsBackToPlainUnclaim(t *testing.T) {
	bd := &stopBD{failOn: "--add-label"}
	newStopWD(bd, 3).terminal(context.Background(), "s", "zr-1", &BudgetError{})
	if bd.idx(unclaim) < 0 {
		t.Fatalf("must still unclaim; calls=%v", bd.calls)
	}
	for _, c := range bd.calls {
		if strings.HasPrefix(c, "comment zr-1 budget stop") {
			t.Errorf("no count comment after a failed record: %q", c)
		}
	}
}

func TestTerminal_budgetStop_disabledRecordsNothing(t *testing.T) {
	bd := &stopBD{}
	newStopWD(bd, 0).terminal(context.Background(), "s", "zr-1", &BudgetError{})
	for _, c := range bd.calls {
		// The one read allowed with counting off is the pre-release status
		// check (bead pg2-sx246): it must not be mistaken for a count read.
		if strings.Contains(c, "budget-stop") || strings.HasPrefix(c, "comment zr-1 budget stop") {
			t.Errorf("threshold 0 must do nothing extra, saw %q", c)
		}
	}
	if bd.idx(unclaim) < 0 {
		t.Errorf("must still unclaim; calls=%v", bd.calls)
	}
}

// Bead pg2-sx246: a bead closed before the hard stop is handled stays closed
// on every path -- below the threshold, counting disabled, and at the
// threshold -- so the release (which would reopen it) is skipped and logged.
func TestTerminal_closedBeadIsNeverUnclaimed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		threshold int
		labels    []string
	}{
		{"below threshold", 3, nil},
		{"counting disabled", 0, nil},
		{"at threshold", 2, []string{"budget-stop:s0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bd := seeded(tc.labels...)
			bd.status = "closed"
			logPath := t.TempDir() + "/events.jsonl"
			lw, err := eventlog.New(logPath)
			if err != nil {
				t.Fatal(err)
			}
			wd := newEscWD(bd, "worker", tc.threshold)
			wd.Log = lw
			wd.terminal(context.Background(), "s", "zr-1", stopBE("worker"))
			_ = lw.Close()
			if bd.idx(unclaim) >= 0 || bd.has("update zr-1 --status=open") {
				t.Errorf("closed bead must not be released/reopened; calls=%v", bd.calls)
			}
			if bd.hasAnyEscalationLabel() {
				t.Errorf("closed bead must not be relabeled for escalation; calls=%v", bd.calls)
			}
			raw, _ := os.ReadFile(logPath)
			if !strings.Contains(string(raw), "budget_unclaim_skipped") {
				t.Errorf("skipped release must be logged; log=%s", raw)
			}
			if !strings.Contains(string(raw), "unclaim_skipped") {
				t.Errorf("hard_stop must report unclaim_skipped; log=%s", raw)
			}
		})
	}
}

// A failed status read falls back to the plain unclaim (the pre-existing
// safe direction): the release is not silently dropped.
func TestTerminal_statusReadFailureStillUnclaims(t *testing.T) {
	bd := &stopBD{failOn: "show"}
	newStopWD(bd, 0).terminal(context.Background(), "s", "zr-1", &BudgetError{})
	if bd.idx(unclaim) < 0 {
		t.Errorf("must still unclaim when the status read fails; calls=%v", bd.calls)
	}
}

// Losing the terminal claim (waitDone owns the outcome) records nothing.
func TestRun_budgetStop_lostTerminalClaimDoesNotCount(t *testing.T) {
	bd := &stopBD{}
	wd := newStopWD(bd, 3)
	wd.Reader = &fakeReader{seq: []usage.Snapshot{{OutputTokens: 1000}}}
	ctx, cancel := context.WithCancel(context.Background())
	wd.ClaimTerminal = func() bool { cancel(); return false } // lose, then let Run exit
	_ = wd.Run(ctx, "s", "zr-1")
	if len(bd.calls) != 0 {
		t.Errorf("lost claim must make no bd call; calls=%v", bd.calls)
	}
}

// hard_stop carries budget_stops=<n> when counting is enabled, and omits it when disabled.
func TestTerminal_budgetStop_eventlogField(t *testing.T) {
	for _, tc := range []struct {
		threshold int
		want      bool
	}{{3, true}, {0, false}} {
		logPath := t.TempDir() + "/events.jsonl"
		lw, err := eventlog.New(logPath)
		if err != nil {
			t.Fatal(err)
		}
		wd := newStopWD(&stopBD{}, tc.threshold)
		wd.Log = lw
		wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
		_ = lw.Close()
		raw, _ := os.ReadFile(logPath)
		var rec map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var r map[string]any
			if json.Unmarshal([]byte(line), &r) == nil && r["kind"] == "hard_stop" {
				rec = r
			}
		}
		if rec == nil {
			t.Fatalf("no hard_stop in %s", raw)
		}
		fields, _ := rec["fields"].(map[string]any)
		if fields == nil {
			fields = rec
		}
		v, has := fields["budget_stops"]
		if has != tc.want || (has && v != float64(1)) {
			t.Errorf("threshold %d: budget_stops=%v present=%v, want present=%v (1)", tc.threshold, v, has, tc.want)
		}
	}
}

// Budgets stay fixed: a hard stop never mutates the Budget.
func TestTerminal_budgetStop_doesNotMutateBudget(t *testing.T) {
	bd := &stopBD{}
	wd := newStopWD(bd, 3)
	before := wd.Budget
	wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
	if !reflect.DeepEqual(wd.Budget, before) {
		t.Errorf("budget mutated: %+v -> %+v", before, wd.Budget)
	}
}

// --- escalation at the threshold (bead pg2-mab1w) ---

func seeded(labels ...string) *stopBD {
	b := &stopBD{labels: map[string]bool{}}
	for _, l := range labels {
		b.labels[l] = true
	}
	return b
}

func stopBE(role string) *BudgetError {
	return &BudgetError{Role: role, Pool: "p", Limit: "tokens", Used: 1000, Cap: 1000}
}

func newEscWD(bd *stopBD, role string, threshold int) *Watchdog {
	wd := newStopWD(bd, threshold)
	wd.Role = role
	return wd
}

func (b *stopBD) has(prefix string) bool {
	for _, c := range b.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// hasAnyEscalationLabel reports whether any escalation label (escalated,
// human, needs-split-review) was added.
func (b *stopBD) hasAnyEscalationLabel() bool {
	return b.has("update zr-1 --add-label escalated") || b.has("update zr-1 --add-label human") ||
		b.has("update zr-1 --add-label needs-split-review")
}

const escalatedComment = "comment zr-1 budget stops reached threshold"

// escScenario is the stop history an escalation test starts from. The drain
// roles run at threshold 1 (bead pg2-izvr8: a session that fails on time
// escalates at once), so a SINGLE stop with no prior history must escalate
// exactly as the second stop does at threshold 2.
type escScenario struct {
	name      string
	threshold int
	prior     []string // budget-stop labels already on the bead
	sessions  string   // expected "stop sessions:" list in the escalation comment
}

var escScenarios = []escScenario{
	{"second stop at threshold 2", 2, []string{"budget-stop:s0"}, "s, s0"},
	{"first stop at threshold 1", 1, nil, "s"},
}

func TestEscalate_belowThresholdDoesNothing(t *testing.T) {
	bd := seeded()
	newEscWD(bd, "worker", 3).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.hasAnyEscalationLabel() {
		t.Errorf("below threshold must not escalate; calls=%v", bd.calls)
	}
}

// Bead pg2-cty80: a budget stop at the threshold puts the bead on the escalated
// path (the label the *-escalated-work queries select), for an implementation
// bead and for a review bead alike. It is neither queued for split review nor
// parked for a human, the label and the comment precede the unclaim, and nothing
// is removed from the bead.
func TestEscalate_atThresholdLabelsEscalatedBeforeUnclaim(t *testing.T) {
	for _, role := range []string{"worker", "drain-pg2", "drain-zr", "review"} {
		for _, sc := range escScenarios {
			t.Run(role+"/"+sc.name, func(t *testing.T) {
				bd := seeded(sc.prior...)
				newEscWD(bd, role, sc.threshold).terminal(context.Background(), "s", "zr-1", stopBE(role))
				hint := ""
				if role == "review" {
					hint = " (review role; not eligible for split review)"
				}
				exact := fmt.Sprintf("comment zr-1 budget stops reached threshold %d; labelled escalated for the escalation triager%s"+
					". stop sessions: %s. last stop: session=s limit=tokens used=1000 cap=1000", sc.threshold, hint, sc.sessions)
				lab, cm, un := bd.idx("update zr-1 --add-label escalated"), bd.idx(exact), bd.idx(unclaim)
				if lab < 0 || cm < 0 || un < 0 || !(lab < un && cm < un) {
					t.Fatalf("want escalated label+comment before unclaim; lab=%d cm=%d un=%d calls=%v", lab, cm, un, bd.calls)
				}
				if bd.has("update zr-1 --add-label needs-split-review") || bd.has("update zr-1 --add-label human") {
					t.Errorf("an at-threshold stop must be escalated, not split-review or human; calls=%v", bd.calls)
				}
				if bd.has("update zr-1 --remove-label") {
					t.Errorf("the escalated path removes no label; calls=%v", bd.calls)
				}
			})
		}
	}
}

// A bead that must not go on to split review (was-split, a split child) is
// still escalated; the comment tells the triager why it is not eligible.
func TestEscalate_splitIneligibleBeadsAreEscalatedWithReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []string
		reason string
	}{
		{"was-split", []string{"was-split"}, "already split"},
		{"split-from child", []string{"split-from:pg2-abc"}, "split child"},
	} {
		for _, sc := range escScenarios {
			t.Run(tc.name+"/"+sc.name, func(t *testing.T) {
				bd := seeded(append(append([]string{}, sc.prior...), tc.labels...)...)
				newEscWD(bd, "worker", sc.threshold).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
				exact := fmt.Sprintf("comment zr-1 budget stops reached threshold %d; labelled escalated for the escalation triager (%s"+
					"; not eligible for split review). stop sessions: %s. last stop: session=s limit=tokens used=1000 cap=1000",
					sc.threshold, tc.reason, sc.sessions)
				lab, cm, un := bd.idx("update zr-1 --add-label escalated"), bd.idx(exact), bd.idx(unclaim)
				if lab < 0 || cm < 0 || un < 0 || !(lab < un && cm < un) {
					t.Fatalf("lab=%d cm=%d un=%d; calls=%v", lab, cm, un, bd.calls)
				}
				if bd.has("update zr-1 --add-label human") || bd.has("update zr-1 --add-label needs-split-review") {
					t.Errorf("must be escalated only; calls=%v", bd.calls)
				}
			})
		}
	}
}

// The one exception (bead pg2-cty80): a triage role's own stop must not put the
// bead back on the escalated path it works, so it goes to human and loses
// needs-split-review (bead pg2-47rsh), before the unclaim.
func TestEscalate_triageRoleStopGoesToHuman(t *testing.T) {
	for _, role := range []string{"pg2-escalation-triager", "zr-escalation-triager", "pg2-split-triager", "split-triage"} {
		for _, sc := range escScenarios {
			t.Run(role+"/"+sc.name, func(t *testing.T) {
				bd := seeded(append(append([]string{}, sc.prior...), "needs-split-review")...)
				newEscWD(bd, role, sc.threshold).terminal(context.Background(), "s", "zr-1", stopBE(role))
				exact := fmt.Sprintf("comment zr-1 budget stops reached threshold %d; escalated to human (triage role"+
					"; not escalated again). stop sessions: %s. last stop: session=s limit=tokens used=1000 cap=1000",
					sc.threshold, sc.sessions)
				lab, cm, un := bd.idx("update zr-1 --add-label human"), bd.idx(exact), bd.idx(unclaim)
				if lab < 0 || cm < 0 || un < 0 || !(lab < un && cm < un) {
					t.Fatalf("lab=%d cm=%d un=%d; calls=%v", lab, cm, un, bd.calls)
				}
				if bd.has("update zr-1 --add-label escalated") || bd.has("update zr-1 --add-label needs-split-review") {
					t.Errorf("a triage role must never put its own bead back on the escalated path; calls=%v", bd.calls)
				}
				rm := bd.idx("update zr-1 --remove-label needs-split-review")
				if rm < 0 || rm > un {
					t.Errorf("needs-split-review must be removed before the unclaim; rm=%d unclaim=%d calls=%v", rm, un, bd.calls)
				}
			})
		}
	}
}

func TestEscalate_killSwitchNeverEscalates(t *testing.T) {
	bd := seeded("budget-stop:a", "budget-stop:b", "budget-stop:c")
	newEscWD(bd, "worker", 0).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.hasAnyEscalationLabel() {
		t.Errorf("threshold 0 must never escalate; calls=%v", bd.calls)
	}
}

// Manual removal of escalated keeps the stop history, so the next stop
// re-evaluates and escalates again.
func TestEscalate_manualLabelRemovalReEscalates(t *testing.T) {
	bd := seeded("budget-stop:s0")
	wd := newEscWD(bd, "worker", 2)
	wd.terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	delete(bd.labels, "escalated") // operator or triager removes it
	bd.calls = nil
	wd.terminal(context.Background(), "s2", "zr-1", stopBE("worker"))
	if !bd.has("update zr-1 --add-label escalated") || bd.idx(unclaim) < 0 {
		t.Errorf("history preserved: next stop must re-escalate; calls=%v", bd.calls)
	}
}

// A reopened bead keeps its budget-stop history, so its next stop escalates.
func TestEscalate_reopenedBeadKeepsHistory(t *testing.T) {
	bd := seeded("budget-stop:a", "budget-stop:b")
	newEscWD(bd, "worker", 3).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if !bd.has("update zr-1 --add-label escalated") || !bd.has("comment zr-1 budget stops reached threshold 3; labelled escalated") {
		t.Errorf("calls=%v", bd.calls)
	}
}

// A bead closed mid-flight gets no escalation write and is not unclaimed
// (that would reopen it).
func TestEscalate_closedMidFlightWritesNothing(t *testing.T) {
	bd := seeded("budget-stop:s0")
	bd.status = "closed"
	newEscWD(bd, "worker", 2).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.hasAnyEscalationLabel() || bd.has(escalatedComment) {
		t.Errorf("no escalation write on a closed bead; calls=%v", bd.calls)
	}
	if bd.idx(unclaim) >= 0 {
		t.Errorf("must not unclaim (reopen) a closed bead; calls=%v", bd.calls)
	}
}

// A failed escalation label write falls back to the plain unclaim.
func TestEscalate_labelWriteFailureFallsBackToUnclaim(t *testing.T) {
	bd := seeded("budget-stop:s0")
	bd.failOn = "--add-label escalated"
	newEscWD(bd, "worker", 2).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.idx(unclaim) < 0 || bd.has(escalatedComment) {
		t.Errorf("want plain unclaim and no escalation comment; calls=%v", bd.calls)
	}
}

func TestEscalate_eventlogExact(t *testing.T) {
	for _, tc := range []struct{ role, outcome string }{{"worker", "escalated"}, {"pg2-escalation-triager", "human"}} {
		t.Run(tc.role, func(t *testing.T) {
			logPath := t.TempDir() + "/events.jsonl"
			lw, err := eventlog.New(logPath)
			if err != nil {
				t.Fatal(err)
			}
			wd := newEscWD(seeded("budget-stop:s0"), tc.role, 2)
			wd.Log = lw
			wd.terminal(context.Background(), "s", "zr-1", stopBE(tc.role))
			_ = lw.Close()
			raw, _ := os.ReadFile(logPath)
			wantMsg := "budget stops reached threshold; escalated to " + tc.outcome
			var gotEsc, gotStop bool
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var r map[string]any
				if json.Unmarshal([]byte(line), &r) != nil {
					continue
				}
				f, _ := r["fields"].(map[string]any)
				if f == nil {
					f = r
				}
				switch r["kind"] {
				case "budget_escalation":
					gotEsc = r["msg"] == wantMsg || r["message"] == wantMsg
					if f["outcome"] != tc.outcome || f["threshold"] != float64(2) || f["budget_stops"] != float64(2) {
						t.Errorf("escalation fields: %v", f)
					}
				case "hard_stop":
					gotStop = f["escalation"] == tc.outcome
				}
			}
			if !gotEsc || !gotStop {
				t.Errorf("want budget_escalation (msg exact) and hard_stop.escalation=%s; esc=%v stop=%v\n%s", tc.outcome, gotEsc, gotStop, raw)
			}
		})
	}
}
