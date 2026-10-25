package query

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestConfirmerAnswersClosedOrOpenFromTheStatusCategory(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "done")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.setIssue("A-3", "new")
	c := NewConfirmer(h.svc)
	for id, want := range map[string]string{"A-1": StateClosed, "A-2": StateOpen, "A-3": StateOpen} {
		got, err := c.Confirm(context.Background(), id, id)
		if err != nil || got.Gone || got.State != want {
			t.Errorf("Confirm(%s) = %+v, %v, want state %q", id, got, err, want)
		}
	}
}

func TestConfirmerAnswersGoneForAnIssueTheOriginDoesNotShow(t *testing.T) {
	h := newHarness(t, 100)
	got, err := NewConfirmer(h.svc).Confirm(context.Background(), "Z-9", "Z-9")
	if err != nil || !got.Gone {
		t.Errorf("Confirm(unknown) = %+v, %v, want Gone", got, err)
	}
}

func TestConfirmerReadsFreshEvenWhenAStoredSummaryIsFresh(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	c := NewConfirmer(h.svc)
	if got, err := c.Confirm(context.Background(), "A-1", "A-1"); err != nil || got.State != StateOpen {
		t.Fatalf("first Confirm = %+v, %v", got, err)
	}
	h.fake.setIssue("A-1", "done")
	got, err := c.Confirm(context.Background(), "A-1", "A-1")
	if err != nil || got.State != StateClosed {
		t.Errorf("second Confirm = %+v, %v, want the fresh closed state", got, err)
	}
	if n := len(h.fake.callList()); n != 2 {
		t.Errorf("origin calls = %d, want one per Confirm", n)
	}
}

func TestConfirmerReturnsTheErrorWhenTheReadCannotRun(t *testing.T) {
	h := newHarness(t, 100)
	// Nothing stored: the failed read is unavailable, whatever the cause.
	h.fake.setErr(errors.New("boom"))
	_, err := NewConfirmer(h.svc).Confirm(context.Background(), "A-1", "A-1")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("Confirm with the origin failing = %v, want unavailable", err)
	}
	// A refusal of the governor is returned too (an interactive read of a spent
	// budget).
	h.fake.setErr(nil)
	h.fake.setIssue("A-2", "indeterminate")
	spendAll(t, h)
	_, err = NewConfirmer(h.svc).Confirm(context.Background(), "A-2", "A-2")
	if err == nil {
		t.Error("Confirm with the budget spent = nil error, want the departure retried")
	}
}

func TestConfirmerDoesNotLetAStoredSummaryDecideADeparture(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	c := NewConfirmer(h.svc)
	if _, err := c.Confirm(context.Background(), "A-1", "A-1"); err != nil {
		t.Fatal(err)
	}
	// The stored summary is inside the expiry, so Ensure would serve it stale
	// when the fetch fails: that answer must not confirm a departure.
	h.fake.setErr(errors.New("boom"))
	h.fake.setIssue("A-1", "done")
	got, err := c.Confirm(context.Background(), "A-1", "A-1")
	if err == nil {
		t.Errorf("Confirm served from a stored summary = %+v, want an error", got)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("err = %v, want unavailable", err)
	}
}

func TestConfirmerBooksTheNextSummaryRefreshAfterASuccessfulRead(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	if _, err := NewConfirmer(h.svc).Confirm(context.Background(), "A-1", "A-1"); err != nil {
		t.Fatal(err)
	}
	if c, ok := h.sched.InspectQueued("A-1", groups.Summary); !ok || c != governor.ClassKeepAlive {
		t.Errorf("queued = %v class %v, want the issue's summary refresh booked at class 4", ok, c)
	}
}

// spendAll uses up the harness governor's whole interactive budget in the
// current window.
func spendAll(t *testing.T, h *harness) {
	t.Helper()
	for {
		tk, err := h.gov.Admit(governor.Request{Bucket: "requests", Class: governor.ClassInteractive, Cost: 1})
		if err != nil {
			return
		}
		_ = tk.Settle(governor.Spend{Purpose: governor.PurposeInteractive, Cost: 1, Outcome: "ok"})
	}
}
