package summary

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func item(id string, class governor.Class, role scheduler.Role) scheduler.Item {
	return scheduler.Item{Task: scheduler.Task{Kind: groups.Summary, Entity: id, Group: groups.Summary, Class: class}, Role: role}
}

func TestDueNonMemberBatchRunsOneSearchAndPersistsEveryIssue(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, id := range []string{"A-1", "A-2", "A-3"} {
		h.seed(id, "indeterminate")
		h.fake.set(id, "indeterminate", "2026-10-04T10:00:00.000+0000")
	}
	if n := h.rebuild(); n != 3 {
		t.Fatalf("Rebuild queued %d, want 3", n)
	}
	h.clk.Advance(2 * time.Minute)
	h.drain()

	if h.fake.count() != 1 {
		t.Fatalf("searches = %d, want ONE for the whole batch", h.fake.count())
	}
	args := h.fake.call(0)
	if args[0] != "search" || args[1] != "--jql" || args[3] != "--all" || args[4] != "--expand" || args[5] != "changelog,comments" {
		t.Errorf("args = %q, want search --jql <jql> --all --expand changelog,comments", args)
	}
	if want := `key in ("A-1", "A-2", "A-3")`; args[2] != want {
		t.Errorf("jql = %q, want %q", args[2], want)
	}
	sp := h.spendList()
	if len(sp) != 1 || sp[0].Class != governor.ClassKeepAlive || sp[0].Purpose != governor.PurposeRefresh ||
		sp[0].Group != groups.Summary || sp[0].BatchSize != 3 || sp[0].Cost != 1 {
		t.Errorf("spend = %+v, want one class 4 refresh of group summary carrying 3", sp)
	}
	for _, id := range []string{"A-1", "A-2", "A-3"} {
		g := h.groupRow(id)
		if !g.FetchedAt.Equal(h.clk.Now()) {
			t.Errorf("%s fetched_at = %v, want %v (persisted by the refresh)", id, g.FetchedAt, h.clk.Now())
		}
		// The comment summary came from the expanded search.
		if got := string(g.Content); !strings.Contains(got, `"comment_count":1`) {
			t.Errorf("%s content %s lacks the comment count", id, got)
		}
	}
}

func TestAbsentIssueIsTombstonedAndAWatchedOneGetsARemovedRow(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	h.seed("W-1", "indeterminate") // watched
	h.seed("U-1", "indeterminate") // not watched
	h.seed("K-1", "indeterminate") // still there
	h.fake.set("K-1", "indeterminate")
	if err := h.st.InTx(ctx, func(tx *store.Tx) error {
		_, err := tx.SetWatched(ctx, changes.LogKindIssue, "W-1", registry.ViaDirect, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.rebuild()
	h.clk.Advance(2 * time.Minute)
	h.drain()

	if h.fake.count() != 1 {
		t.Fatalf("searches = %d, want 1", h.fake.count())
	}
	for _, id := range []string{"W-1", "U-1"} {
		if e, _ := h.entityRow(id); e.TombstonedAt.IsZero() {
			t.Errorf("%s was not tombstoned though the search did not return it", id)
		}
	}
	if e, _ := h.entityRow("K-1"); !e.TombstonedAt.IsZero() {
		t.Error("an issue the search returned was tombstoned")
	}
	rows, err := h.st.ChangesAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, r := range rows {
		if slices.Contains(r.Kinds, classify.KindRemoved) {
			removed = append(removed, r.ID)
		}
	}
	if !slices.Equal(removed, []string{"W-1"}) {
		t.Errorf("removed rows for %v, want only the watched W-1", removed)
	}
	if !slices.Equal(h.obs.gone, []string{"U-1", "W-1"}) && !slices.Equal(h.obs.gone, []string{"W-1", "U-1"}) {
		t.Errorf("observer gone = %v", h.obs.gone)
	}
	// A tombstoned issue is not refreshed again.
	h.clk.Advance(time.Hour)
	h.drain()
	if _, ok := h.queuedClass("W-1"); ok {
		t.Error("a tombstoned issue was re-booked")
	}
}

func TestTruncatedSearchFailsTheWholeBatch(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("A-1", "indeterminate")
	h.seed("A-2", "indeterminate")
	h.fake.set("A-1", "indeterminate")
	h.fake.truncated = true
	before := h.groupRow("A-1")
	h.clk.Advance(2 * time.Minute)

	res := executor{h.svc}.Execute(context.Background(), []scheduler.Item{
		item("A-1", governor.ClassKeepAlive, scheduler.RoleDue),
		item("A-2", governor.ClassKeepAlive, scheduler.RoleDue),
	})
	for i, r := range res {
		if r.Err == nil || r.Skipped {
			t.Errorf("result %d = %+v, want the batch error", i, r)
		}
	}
	if e, _ := h.entityRow("A-2"); !e.TombstonedAt.IsZero() {
		t.Error("an issue missing from a truncated search was tombstoned: absence is not proven")
	}
	if after := h.groupRow("A-1"); !after.FetchedAt.Equal(before.FetchedAt) {
		t.Error("a truncated search stored an issue")
	}
}

func TestGovernorRefusalIsReturnedAsTheItemsError(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("A-1", "indeterminate")
	h.fake.set("A-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)
	if err := h.gov.Pause(origin.Bucket, time.Hour, "test pause"); err != nil {
		t.Fatal(err)
	}
	res := executor{h.svc}.Execute(context.Background(), []scheduler.Item{item("A-1", governor.ClassKeepAlive, scheduler.RoleDue)})
	var refusal *governor.Refusal
	if len(res) != 1 || !errors.As(res[0].Err, &refusal) {
		t.Fatalf("result = %+v, want a *governor.Refusal", res)
	}
	if h.fake.count() != 0 {
		t.Error("pjira ran under a refusal")
	}

	// Through the scheduler the batch is held, then goes once the pause ends.
	h.rebuild()
	h.drain()
	if h.fake.count() != 0 {
		t.Fatal("a batch went out during the pause")
	}
	h.clk.Advance(2 * time.Hour)
	h.drain()
	if h.fake.count() != 1 {
		t.Errorf("calls after the pause = %d, want 1", h.fake.count())
	}
}

func TestItemsNotRetainedFreshOrNotKeyShapedAreSkippedWithoutACall(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	h.seed("C-1", "done")
	h.clk.Advance(25 * time.Hour) // closed 25 hours ago: no longer retained
	h.seed("F-1", "indeterminate")
	h.fake.set("C-1", "done")
	h.fake.set("F-1", "indeterminate")

	res := executor{h.svc}.Execute(ctx, []scheduler.Item{
		item("C-1", governor.ClassKeepAlive, scheduler.RoleDue),
		item("F-1", governor.ClassKeepAlive, scheduler.RoleDue),
		item("x\") OR project = Y OR key in (\"Z-1", governor.ClassKeepAlive, scheduler.RoleDue),
		item("N-1", governor.ClassKeepAlive, scheduler.RoleDue), // no entity at all
	})
	for i, r := range res {
		if !r.Skipped || r.Err != nil {
			t.Errorf("result %d = %+v, want Skipped", i, r)
		}
	}
	if h.fake.count() != 0 {
		t.Errorf("calls = %d, want none", h.fake.count())
	}
	// The fresh one is booked for its next cycle instead.
	if c, ok := h.queuedClass("F-1"); !ok || c != governor.ClassKeepAlive {
		t.Errorf("F-1 queued = %v class %v, want a class 4 task booked", ok, c)
	}
	if _, ok := h.queuedClass("C-1"); ok {
		t.Error("a task was booked for an issue past the keep-alive cutoff")
	}
}

func TestRefreshBooksTheNextTaskAtFreshUntilForANonMemberOnly(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("N-1", "indeterminate")
	h.seed("M-1", "indeterminate")
	h.setMember("M-1", true)
	h.fake.set("N-1", "indeterminate")
	h.fake.set("M-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)

	// A member's task (a write re-pull) is refreshed but not re-booked.
	if err := h.svc.Queue(context.Background(), "M-1", governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Queue(context.Background(), "N-1", governor.ClassKeepAlive, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h.drain()
	if h.fake.count() != 1 || h.fake.call(0)[2] != `key in ("M-1", "N-1")` {
		t.Fatalf("calls = %d %q, want ONE search for both", h.fake.count(), h.fake.calls)
	}
	if _, ok := h.queuedClass("M-1"); ok {
		t.Error("a query member was re-booked by the summary service")
	}
	if c, ok := h.queuedClass("N-1"); !ok || c != governor.ClassKeepAlive {
		t.Fatalf("N-1 queued = %v class %v, want class 4", ok, c)
	}
	calls := h.fake.count()
	h.clk.Advance(59 * time.Second)
	h.drain()
	if h.fake.count() != calls {
		t.Fatal("refreshed again before fresh_until")
	}
	h.clk.Advance(time.Second)
	h.drain()
	if h.fake.count() != calls+1 {
		t.Errorf("calls at fresh_until = %d, want %d", h.fake.count(), calls+1)
	}
}

func TestLostGenerationRaceStoresNothingAndBooksNothing(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("A-1", "indeterminate")
	h.fake.set("A-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)
	// A write lands between the executor's token and its commit.
	h.fake.beforeReturn = func() {
		persist(t, h.pers, issue("A-1", "indeterminate", "written meanwhile"))
	}
	res := executor{h.svc}.Execute(context.Background(), []scheduler.Item{item("A-1", governor.ClassKeepAlive, scheduler.RoleDue)})
	if res[0].Err != nil || res[0].Skipped {
		t.Fatalf("result = %+v, want a clean no-op", res[0])
	}
	if got := string(h.groupRow("A-1").Content); !strings.Contains(got, "written meanwhile") {
		t.Errorf("content = %s, want the interleaved write to stand", got)
	}
	if _, ok := h.queuedClass("A-1"); ok {
		t.Error("the executor re-booked after losing the race: the write notice's re-pull owns it")
	}
}

func TestNotYetDueTaskRidesABatchAsFill(t *testing.T) {
	h := newHarness(t, harnessOpts{fillHorizon: time.Hour})
	h.seed("D-1", "indeterminate") // fresh until t0+1m
	h.clk.Advance(30 * time.Second)
	h.seed("F-2", "indeterminate") // fresh until t0+1m30s: not due when D-1 is
	h.fake.set("D-1", "indeterminate")
	h.fake.set("F-2", "indeterminate")
	h.rebuild()
	h.clk.Set(t0.Add(time.Minute))
	h.drain()

	if h.fake.count() != 1 {
		t.Fatalf("searches = %d, want ONE carrying the due task and the fill", h.fake.count())
	}
	if want := `key in ("D-1", "F-2")`; h.fake.call(0)[2] != want {
		t.Errorf("jql = %q, want %q", h.fake.call(0)[2], want)
	}
	if len(h.obs.due) != 1 || !slices.Equal(h.obs.due[0], []string{"D-1"}) || len(h.obs.fill) != 1 || !slices.Equal(h.obs.fill[0], []string{"F-2"}) {
		t.Errorf("observer due=%v fill=%v", h.obs.due, h.obs.fill)
	}
}

func TestOpenNonMemberIssueIsRefreshedAt24HoursSevenDaysAndThirtyDays(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("O-1", "indeterminate")
	h.fake.set("O-1", "indeterminate")
	if n := h.rebuild(); n != 1 {
		t.Fatalf("Rebuild queued %d, want 1", n)
	}
	for i, d := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour} {
		h.clk.Set(t0.Add(d))
		h.drain()
		if got := h.fake.count(); got != i+1 {
			t.Fatalf("after +%s the searches = %d, want %d: an open issue's refresh must not decay", d, got, i+1)
		}
		if !h.groupRow("O-1").FetchedAt.Equal(t0.Add(d)) {
			t.Errorf("after +%s the summary was not refreshed", d)
		}
	}
}

func TestClosedIssueIsRefreshedUntil24HoursAfterClosingThenNotAtAll(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("C-1", "done") // closed at t0
	h.fake.set("C-1", "done")
	// Still inside the 24 hours: refreshed on the normal schedule.
	h.clk.Set(t0.Add(23 * time.Hour))
	if n := h.rebuild(); n != 1 {
		t.Fatalf("Rebuild at +23h queued %d, want 1", n)
	}
	h.drain()
	if h.fake.count() != 1 {
		t.Fatalf("a closed issue 23h after closing: calls = %d, want 1", h.fake.count())
	}
	// 24 hours or more after closing: served from the store, never refreshed.
	h.clk.Set(t0.Add(25 * time.Hour))
	h.drain()
	if h.fake.count() != 1 {
		t.Errorf("an issue closed 25h ago was refreshed: calls = %d, want 1", h.fake.count())
	}
}
