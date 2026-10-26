package beads

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fnRunner func(args ...string) (string, error)

func (f fnRunner) Run(_ context.Context, args ...string) (string, error) { return f(args...) }

func TestRecordBudgetStop_writesSessionLabel(t *testing.T) {
	var got []string
	r := fnRunner(func(a ...string) (string, error) { got = a; return "", nil })
	if err := RecordBudgetStop(context.Background(), r, "zr-1", "sess-a"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "update zr-1 --add-label budget-stop:sess-a" {
		t.Errorf("got %v", got)
	}
	if err := RecordBudgetStop(context.Background(), r, "zr-1", ""); err == nil {
		t.Error("empty session id must error")
	}
}

func TestBudgetStops_countsDistinctSessions(t *testing.T) {
	r := fnRunner(func(a ...string) (string, error) {
		return `{"data":[{"id":"zr-1","status":"open","labels":["x","budget-stop:a","budget-stop:b","budget-stop:a"]}]}`, nil
	})
	n, err := BudgetStops(context.Background(), r, "zr-1")
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2", n, err)
	}
}

func TestBudgetStops_error(t *testing.T) {
	r := fnRunner(func(a ...string) (string, error) { return "", errors.New("boom") })
	if _, err := BudgetStops(context.Background(), r, "zr-1"); err == nil {
		t.Fatal("want error")
	}
}

func TestClearBudgetStops_onlyWhenClosed(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   []string
	}{
		{"open", nil},
		{"closed", []string{"update zr-1 --remove-label budget-stop:a", "update zr-1 --remove-label budget-stop:b"}},
	} {
		var calls []string
		r := fnRunner(func(a ...string) (string, error) {
			if a[0] == "show" {
				return `{"id":"zr-1","status":"` + tc.status + `","labels":["keep","budget-stop:a","budget-stop:b"]}`, nil
			}
			calls = append(calls, strings.Join(a, " "))
			return "", nil
		})
		if err := ClearBudgetStops(context.Background(), r, "zr-1"); err != nil {
			t.Fatal(err)
		}
		if strings.Join(calls, "|") != strings.Join(tc.want, "|") {
			t.Errorf("status %s: calls=%v want %v", tc.status, calls, tc.want)
		}
	}
}
