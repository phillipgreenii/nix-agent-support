package summary

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestRebuildBooksAClass4TaskForEveryRetainedNonMemberOnly(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	h.seed("O-1", "indeterminate") // retained non-member
	h.seed("M-1", "indeterminate") // member of a configured query
	h.setMember("M-1", true)
	h.seed("G-1", "indeterminate") // tombstoned
	if err := h.pers.MarkGone(ctx, "G-1", governor.PurposeQuery); err != nil {
		t.Fatal(err)
	}
	if err := h.st.TouchEntity(ctx, groups.EntityKind, "N-1", t0); err != nil { // tracked, no summary yet
		t.Fatal(err)
	}

	n, err := h.svc.Rebuild(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Rebuild = %d, %v, want 2 (O-1 and N-1)", n, err)
	}
	for _, id := range []string{"O-1", "N-1"} {
		if c, ok := h.queuedClass(id); !ok || c != governor.ClassKeepAlive {
			t.Errorf("%s queued = %v class %v, want class 4", id, ok, c)
		}
	}
	for _, id := range []string{"M-1", "G-1"} {
		if _, ok := h.queuedClass(id); ok {
			t.Errorf("a task was booked for %s", id)
		}
	}
}

func TestRebuildDueTimeIsTheStoredFreshUntil(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	st := h.seed("O-1", "indeterminate")
	h.fake.set("O-1", "indeterminate")
	h.rebuild()
	h.clk.Set(st.FreshUntil.Add(-time.Second))
	h.drain()
	if h.fake.count() != 0 {
		t.Fatal("refreshed before the stored fresh_until")
	}
	h.clk.Set(st.FreshUntil)
	h.drain()
	if h.fake.count() != 1 {
		t.Errorf("calls at fresh_until = %d, want 1", h.fake.count())
	}
}

func TestRebuildTaskWithNoStoredSummaryIsDueNow(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if err := h.st.TouchEntity(context.Background(), groups.EntityKind, "N-1", t0); err != nil {
		t.Fatal(err)
	}
	h.fake.set("N-1", "new")
	h.rebuild()
	h.drain()
	if h.fake.count() != 1 {
		t.Errorf("calls = %d, want 1: an issue with no summary is fetched at once", h.fake.count())
	}
}

func TestRebuildQueuesNothingForAnIssue24HoursPastClosingAndRefreshesOneClosedAnHourAgo(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("OLD-1", "done") // closed at t0
	h.clk.Advance(24 * time.Hour)
	h.seed("NEW-1", "done") // closed at t0+24h
	h.clk.Advance(time.Hour)
	h.fake.set("OLD-1", "done")
	h.fake.set("NEW-1", "done")

	n, err := h.svc.Rebuild(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Rebuild = %d, %v, want 1 (the issue closed 1 hour ago)", n, err)
	}
	if _, ok := h.queuedClass("OLD-1"); ok {
		t.Error("a task was queued for the issue closed 25 hours ago")
	}
	h.drain()
	if h.fake.count() != 1 {
		t.Fatalf("calls = %d, want 1", h.fake.count())
	}
	if jql := h.fake.call(0)[2]; !strings.Contains(jql, "NEW-1") || strings.Contains(jql, "OLD-1") {
		t.Errorf("jql = %q, want only the issue closed 1 hour ago", jql)
	}
	// It is re-booked on its normal schedule.
	h.clk.Advance(time.Minute)
	h.drain()
	if h.fake.count() != 2 {
		t.Errorf("calls = %d, want the closed-an-hour-ago issue refreshed again", h.fake.count())
	}
}
