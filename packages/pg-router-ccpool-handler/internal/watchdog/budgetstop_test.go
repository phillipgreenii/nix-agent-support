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
		return `{"id":"zr-1","status":"in_progress","labels":[` + strings.Join(ls, ",") + `]}`, nil
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
