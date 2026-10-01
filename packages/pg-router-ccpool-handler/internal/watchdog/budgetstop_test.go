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
		if strings.Contains(c, "budget-stop") || strings.HasPrefix(c, "show") || strings.HasPrefix(c, "comment zr-1 budget stop") {
			t.Errorf("threshold 0 must do nothing extra, saw %q", c)
		}
	}
	if bd.idx(unclaim) < 0 {
		t.Errorf("must still unclaim; calls=%v", bd.calls)
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

const splitComment = "comment zr-1 budget stops reached threshold 2; queued for split review"

func TestEscalate_belowThresholdDoesNothing(t *testing.T) {
	bd := seeded()
	newEscWD(bd, "worker", 3).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.has("update zr-1 --add-label needs-split-review") || bd.has("update zr-1 --add-label human") {
		t.Errorf("below threshold must not escalate; calls=%v", bd.calls)
	}
}

func TestEscalate_atThresholdQueuesSplitReviewBeforeUnclaim(t *testing.T) {
	bd := seeded("budget-stop:s0")
	newEscWD(bd, "worker", 2).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	lab := bd.idx("update zr-1 --add-label needs-split-review")
	cm := bd.idx(splitComment)
	un := bd.idx(unclaim)
	if lab < 0 || cm < 0 || un < 0 || !(lab < un && cm < un) {
		t.Fatalf("want label+comment before unclaim; lab=%d cm=%d un=%d calls=%v", lab, cm, un, bd.calls)
	}
	if bd.has("update zr-1 --add-label human") {
		t.Errorf("must not add human on the split path; calls=%v", bd.calls)
	}
}

func TestEscalate_humanBranches(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		labels     []string
		reason     string
	}{
		{"review role", "review", nil, "review role"},
		{"triage role", "pg2-escalation-triager", nil, "triage role"},
		{"split-triage role", "split-triage", nil, "triage role"},
		{"was-split", "worker", []string{"was-split"}, "already split"},
		{"split-from child", "worker", []string{"split-from:pg2-abc"}, "split child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bd := seeded(append([]string{"budget-stop:s0"}, tc.labels...)...)
			newEscWD(bd, tc.role, 2).terminal(context.Background(), "s", "zr-1", stopBE(tc.role))
			exact := "comment zr-1 budget stops reached threshold 2; escalated to human (" + tc.reason +
				"; not split). stop sessions: s, s0. last stop: session=s limit=tokens used=1000 cap=1000"
			lab, cm, un := bd.idx("update zr-1 --add-label human"), bd.idx(exact), bd.idx(unclaim)
			if lab < 0 || cm < 0 || un < 0 || !(lab < un && cm < un) {
				t.Fatalf("lab=%d cm=%d un=%d; calls=%v", lab, cm, un, bd.calls)
			}
			if bd.has("update zr-1 --add-label needs-split-review") {
				t.Errorf("human path must not queue split review; calls=%v", bd.calls)
			}
			// pg2-47rsh: a triage session that hit its own budget drops
			// needs-split-review (human instead); other human routes leave it.
			rm := bd.idx("update zr-1 --remove-label needs-split-review")
			if (tc.reason == "triage role") != (rm >= 0) || (rm >= 0 && rm > un) {
				t.Errorf("remove-label needs-split-review at %d (unclaim %d); calls=%v", rm, un, bd.calls)
			}
		})
	}
}

func TestEscalate_killSwitchNeverEscalates(t *testing.T) {
	bd := seeded("budget-stop:a", "budget-stop:b", "budget-stop:c")
	newEscWD(bd, "worker", 0).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.has("update zr-1 --add-label needs-split-review") || bd.has("update zr-1 --add-label human") {
		t.Errorf("threshold 0 must never escalate; calls=%v", bd.calls)
	}
}

// Manual removal of human / needs-split-review keeps the stop history, so the
// next stop re-evaluates and escalates again.
func TestEscalate_manualLabelRemovalReEscalates(t *testing.T) {
	bd := seeded("budget-stop:s0")
	wd := newEscWD(bd, "worker", 2)
	wd.terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	delete(bd.labels, "needs-split-review") // operator removes it
	bd.calls = nil
	wd.terminal(context.Background(), "s2", "zr-1", stopBE("worker"))
	if !bd.has("update zr-1 --add-label needs-split-review") || bd.idx(unclaim) < 0 {
		t.Errorf("history preserved: next stop must re-escalate; calls=%v", bd.calls)
	}
}

// A reopened bead keeps its budget-stop history, so its next stop escalates.
func TestEscalate_reopenedBeadKeepsHistory(t *testing.T) {
	bd := seeded("budget-stop:a", "budget-stop:b")
	newEscWD(bd, "worker", 3).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.idx("comment zr-1 budget stops reached threshold 3; queued for split review") < 0 {
		t.Errorf("calls=%v", bd.calls)
	}
}

// A bead closed mid-flight gets no escalation write and is not unclaimed
// (that would reopen it).
func TestEscalate_closedMidFlightWritesNothing(t *testing.T) {
	bd := seeded("budget-stop:s0")
	bd.status = "closed"
	newEscWD(bd, "worker", 2).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.has("update zr-1 --add-label needs-split-review") || bd.has("update zr-1 --add-label human") || bd.has(splitComment[:len("comment zr-1 budget stops")]) {
		t.Errorf("no escalation write on a closed bead; calls=%v", bd.calls)
	}
	if bd.idx(unclaim) >= 0 {
		t.Errorf("must not unclaim (reopen) a closed bead; calls=%v", bd.calls)
	}
}

// A failed escalation label write falls back to the plain unclaim.
func TestEscalate_labelWriteFailureFallsBackToUnclaim(t *testing.T) {
	bd := seeded("budget-stop:s0")
	bd.failOn = "needs-split-review"
	newEscWD(bd, "worker", 2).terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	if bd.idx(unclaim) < 0 || bd.idx(splitComment) >= 0 {
		t.Errorf("want plain unclaim and no split comment; calls=%v", bd.calls)
	}
}

func TestEscalate_eventlogExact(t *testing.T) {
	logPath := t.TempDir() + "/events.jsonl"
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	wd := newEscWD(seeded("budget-stop:s0"), "worker", 2)
	wd.Log = lw
	wd.terminal(context.Background(), "s", "zr-1", stopBE("worker"))
	_ = lw.Close()
	raw, _ := os.ReadFile(logPath)
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
			gotEsc = r["msg"] == "budget stops reached threshold; escalated to split-review" ||
				r["message"] == "budget stops reached threshold; escalated to split-review"
			if f["outcome"] != "split-review" || f["threshold"] != float64(2) || f["budget_stops"] != float64(2) {
				t.Errorf("escalation fields: %v", f)
			}
		case "hard_stop":
			gotStop = f["escalation"] == "split-review"
		}
	}
	if !gotEsc || !gotStop {
		t.Errorf("want budget_escalation (msg exact) and hard_stop.escalation; esc=%v stop=%v\n%s", gotEsc, gotStop, raw)
	}
}
