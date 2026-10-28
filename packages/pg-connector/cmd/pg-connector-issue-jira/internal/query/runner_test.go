package query

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	jqlMine = "assignee = me"
	jqlTeam = "project = TEAM"
)

func TestNewRunnerNeedsItsParts(t *testing.T) {
	h := newHarness(t, 100)
	full := RunnerConfig{
		Cfg: h.cfg, Store: h.st, Persister: h.pers, Fetcher: h.origin, Scheduler: h.sched,
		Chg: h.eng, Summary: h.svc,
	}
	for name, mut := range map[string]func(*RunnerConfig){
		"config":    func(c *RunnerConfig) { c.Cfg = nil },
		"store":     func(c *RunnerConfig) { c.Store = nil },
		"persister": func(c *RunnerConfig) { c.Persister = nil },
		"fetcher":   func(c *RunnerConfig) { c.Fetcher = nil },
		"scheduler": func(c *RunnerConfig) { c.Scheduler = nil },
		"changes":   func(c *RunnerConfig) { c.Chg = nil },
		"summary":   func(c *RunnerConfig) { c.Summary = nil },
	} {
		c := full
		mut(&c)
		if _, err := NewRunner(c); err == nil {
			t.Errorf("NewRunner without %s = nil error", name)
		}
	}
	if _, err := NewRunner(full); err != nil {
		t.Errorf("NewRunner(full) = %v", err)
	}
}

func TestFirstCompleteRunOfAWatchedQueryMarksEveryMemberAndAppendsNoRow(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "new")
	h.fake.list(jqlMine, "A-1", "A-2")

	row := h.fetch("mine")
	if !row.Complete || row.BaselineAt.IsZero() || len(row.IDs) != 2 {
		t.Fatalf("row = %+v, want a complete baseline run of two members", row)
	}
	if got := h.watched(); !eq(got, []string{"A-1", "A-2"}) {
		t.Errorf("watched = %v, want both members", got)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("a baseline run appended rows: %v", rows)
	}
	// One search, with the changelog and comments expanded.
	calls := h.fake.callList()
	if len(calls) != 1 || calls[0][2] != jqlMine || strings.Join(calls[0][3:], " ") != "--all --expand changelog,comments" {
		t.Errorf("origin calls = %v, want one expanded search of the query", calls)
	}
}

func TestNewIssueAfterTheBaselineAppendsExactlyOneAddedRow(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.list(jqlMine, "A-1")
	h.fetch("mine")

	h.fake.setIssue("A-3", "new")
	h.fake.list(jqlMine, "A-1", "A-3")
	h.fetch("mine")
	if rows := h.changeRows(); !eq(rows, []string{"A-3:added"}) {
		t.Errorf("rows = %v, want exactly one added row for the new issue", rows)
	}
	// A further run with nothing new appends nothing.
	h.fetch("mine")
	if rows := h.changeRows(); !eq(rows, []string{"A-3:added"}) {
		t.Errorf("rows after an unchanged run = %v", rows)
	}
	if got := h.watched(); !eq(got, []string{"A-1", "A-3"}) {
		t.Errorf("watched = %v", got)
	}
}

func TestTrackedIssueNewlyListedByABaselinedWatchedQueryIsALateWatch(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.list(jqlMine, "A-1")
	h.fetch("mine")

	// A-9 is already tracked (read through), unwatched.
	h.fake.setIssue("A-9", "indeterminate")
	if _, err := h.svc.Ensure(context.Background(), "A-9", summary.EnsureOptions{}); err != nil {
		t.Fatal(err)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Fatalf("tracking an unwatched issue appended rows: %v", rows)
	}

	h.fake.list(jqlMine, "A-1", "A-9")
	h.fetch("mine")
	if rows := h.changeRows(); !eq(rows, []string{"A-9:added"}) {
		t.Errorf("rows = %v, want exactly one added row (the late watch)", rows)
	}
}

func TestTruncatedRunSetsNoBaselineAndAppendsNothing(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2")
	h.fake.setTruncated(jqlMine, true)

	row := h.fetch("mine")
	if row.Complete || !row.BaselineAt.IsZero() {
		t.Fatalf("row = %+v, want an incomplete run with no baseline", row)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("a truncated run appended rows: %v", rows)
	}

	// The baseline takes several runs and stays silent until it completes.
	h.fake.setIssue("A-3", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2", "A-3")
	h.fake.setTruncated(jqlMine, false)
	row = h.fetch("mine")
	if !row.Complete || row.BaselineAt.IsZero() {
		t.Fatalf("row = %+v, want the baseline set by the first complete run", row)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("the first complete run appended rows: %v", rows)
	}
}

func TestUnwatchedQueryTracksItsMembersAndDeliversNothing(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("T-1", "indeterminate")
	h.fake.list(jqlTeam, "T-1")
	h.fetch("team")

	h.fake.setIssue("T-2", "new")
	h.fake.setIssue("T-1", "done") // its content changes
	h.fake.list(jqlTeam, "T-1", "T-2")
	h.fetch("team")

	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("an unwatched query delivered rows: %v", rows)
	}
	if got := h.watched(); len(got) != 0 {
		t.Errorf("watched = %v, want none", got)
	}
	for _, id := range []string{"T-1", "T-2"} {
		g, ok, err := h.st.GetGroup(context.Background(), "issue", id, "summary")
		if err != nil || !ok || len(g.Content) == 0 {
			t.Errorf("%s has no stored summary (ok=%v err=%v): members must be tracked", id, ok, err)
		}
	}
	if m, err := Members(context.Background(), h.st); err != nil || !m["T-1"] || !m["T-2"] {
		t.Errorf("Members = %v, %v", m, err)
	}
}

func TestMemberThatClosedAndLeftTheQueryStillGetsItsClosedRow(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2")
	h.fetch("mine")

	// A-1 closes and no longer matches the query: the departure's confirming
	// read finds it closed, and the watched flag outlives membership.
	h.fake.setIssue("A-1", "done")
	h.fake.list(jqlMine, "A-2")
	row := h.fetch("mine")
	if len(row.IDs) != 1 || row.IDs[0] != "A-2" {
		t.Fatalf("row.IDs = %v", row.IDs)
	}
	// The closed change is held as a pending row until the detail group
	// settles (the shared engine's settle rule); ending the barrier appends it.
	pending, err := h.st.ListPending(context.Background())
	if err != nil || len(pending) != 1 || pending[0].ID != "A-1" || len(pending[0].Kinds) != 1 || pending[0].Kinds[0] != "closed" {
		t.Fatalf("pending = %+v, %v, want one pending closed row of A-1", pending, err)
	}
	h.settle()
	if rows := h.changeRows(); !eq(rows, []string{"A-1:closed"}) {
		t.Errorf("rows = %v, want the closed row of the departed member", rows)
	}
	if got := h.watched(); !eq(got, []string{"A-1", "A-2"}) {
		t.Errorf("watched = %v, want the flag to outlive membership", got)
	}
}

// The run confirms a departure BEFORE it stores the new row, when the issue is
// still counted as a member, so the confirmer's own booking is a no-op there.
// The runner books the departed issue again after the run commits.
func TestIssueThatLeftTheQueryButIsStillOpenIsBookedForRefreshAfterTheRun(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2")
	h.fetch("mine")
	if _, ok := h.sched.InspectQueued("A-1", groups.Summary); ok {
		t.Fatal("a member's summary refresh is booked, but its query run refreshes it")
	}

	// A-1 stays open but no longer matches the query.
	h.fake.list(jqlMine, "A-2")
	h.fetch("mine")
	if m, err := Members(context.Background(), h.st); err != nil || m["A-1"] {
		t.Fatalf("Members = %v, %v, want A-1 to have left", m, err)
	}
	if c, ok := h.sched.InspectQueued("A-1", groups.Summary); !ok || c != governor.ClassKeepAlive {
		t.Fatalf("queued = %v class %v, want the departed issue's summary refresh booked at class 4", ok, c)
	}
	if _, ok := h.sched.InspectQueued("A-2", groups.Summary); ok {
		t.Error("the remaining member's summary refresh is booked")
	}

	// The booking is carried out: the issue's summary is refreshed from the
	// origin once its stored freshness lapses.
	before := len(h.fake.callList())
	h.fake.setIssue("A-1", "in progress")
	h.clk.Advance(settingsRules.SummaryTTL + 2*time.Minute)
	h.sched.Drain(context.Background())
	refreshed := false
	for _, c := range h.fake.callList()[before:] {
		if strings.Contains(strings.Join(c, " "), `key in ("A-1")`) {
			refreshed = true
		}
	}
	if !refreshed {
		t.Errorf("origin calls after the lapse = %v, want a key search for A-1", h.fake.callList()[before:])
	}
}

func TestFlippingABaselinedQueryToWatchedIsALateWatchOfEachKnownMember(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("T-1", "indeterminate")
	h.fake.setIssue("T-2", "new")
	h.fake.list(jqlTeam, "T-1", "T-2")
	h.fetch("team") // baselined, unwatched
	if rows := h.changeRows(); len(rows) != 0 {
		t.Fatalf("rows = %v", rows)
	}

	def := h.cfg.Queries["team"]
	def.Watched = true
	h.cfg.Queries["team"] = def
	h.fetch("team")
	if rows := h.changeRows(); !eq(rows, []string{"T-1:added", "T-2:added"}) {
		t.Errorf("rows = %v, want one added row per known member", rows)
	}
	if got := h.watched(); !eq(got, []string{"T-1", "T-2"}) {
		t.Errorf("watched = %v", got)
	}
}

func TestQueryOutsideTheConfigIsRefusedByTheRunner(t *testing.T) {
	h := newHarness(t, 100)
	if _, ok := h.runner.Expr("nope"); ok {
		t.Error("Expr(nope) = ok")
	}
	_, err := h.runner.Fetch(context.Background(), lq.Key{Name: "nope"})
	if err == nil || !strings.Contains(err.Error(), "not a configured query") {
		t.Errorf("Fetch(nope) = %v, want a not-configured error", err)
	}
	if calls := h.fake.callList(); len(calls) != 0 {
		t.Errorf("origin calls = %v, want none", calls)
	}
}

func TestFetchWithTheWatchOptionIsRefused(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("T-1", "indeterminate")
	h.fake.list(jqlTeam, "T-1")
	_, err := h.runner.Fetch(context.Background(), lq.Key{Name: "team"}, lq.FetchOptions{Watch: true})
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("Fetch with the watch option = %v, want invalid_argument", err)
	}
	if got := h.watched(); len(got) != 0 {
		t.Errorf("watched = %v, want none", got)
	}
	if calls := h.fake.callList(); len(calls) != 0 {
		t.Errorf("origin calls = %v, want none", calls)
	}
}

func TestRunnerIsBuiltOverTheJiraRegistration(t *testing.T) {
	spec := changes.Spec()
	if spec.AdHocQueryWatch {
		t.Error("the registration allows the ad-hoc watch option")
	}
}

type unauth struct{ bad bool }

func (u unauth) Unauthenticated() (bool, error) { return u.bad, errors.New("login required") }

func TestUnauthenticatedRunAnswersUnauthenticated(t *testing.T) {
	h := newHarness(t, 100)
	r, err := NewRunner(RunnerConfig{
		Cfg: h.cfg, Store: h.st, Persister: h.pers, Fetcher: h.origin, Scheduler: h.sched,
		Chg: h.eng, Summary: h.svc, Auth: unauth{bad: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Fetch(context.Background(), lq.Key{Name: "team"})
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Errorf("Fetch while unauthenticated = %v, want unauthenticated", err)
	}
}

// A closed issue that left every query keeps its watched flag until eviction,
// so a reopen inside the seven-day window is a change of a watched entity and
// is delivered, after the closed row and with no second added row.
func TestReopenInsideTheWindowIsDeliveredAfterTheClosedRow(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2")
	h.fetch("mine")

	h.fake.setIssue("A-1", "done")
	h.fake.list(jqlMine, "A-2")
	h.fetch("mine")
	h.settle()
	if rows := h.changeRows(); !eq(rows, []string{"A-1:closed"}) {
		t.Fatalf("rows = %v, want the closed row", rows)
	}

	h.clk.Advance(24 * time.Hour) // well inside the seven-day window
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2")
	h.fetch("mine")
	h.settle()
	rows := h.changeRows()
	if len(rows) != 2 || rows[0] != "A-1:closed" || !strings.HasPrefix(rows[1], "A-1:reopened") {
		t.Errorf("rows = %v, want the closed row then one reopened row of A-1 and no added row", rows)
	}
	if got := h.watched(); !eq(got, []string{"A-1", "A-2"}) {
		t.Errorf("watched = %v", got)
	}
}

// After the eviction age the closed issue and its flag are gone, so a query
// that lists the issue again meets a first observation: exactly one added row.
func TestEvictedIssueListedAgainIsAFirstObservationWithOneAddedRow(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2")
	h.fetch("mine")

	h.fake.setIssue("A-1", "done")
	h.fake.list(jqlMine, "A-2")
	h.fetch("mine")
	h.settle()
	if rows := h.changeRows(); !eq(rows, []string{"A-1:closed"}) {
		t.Fatalf("rows = %v, want the closed row", rows)
	}

	h.clk.Advance(7 * 24 * time.Hour)
	if n, err := summary.Sweep(context.Background(), h.st, 7*24*time.Hour, h.clk.Now()); err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v, want the closed issue evicted", n, err)
	}
	if got := h.watched(); !eq(got, []string{"A-2"}) {
		t.Fatalf("watched after the eviction = %v, want the flag gone with the row", got)
	}

	h.fake.setIssue("A-1", "indeterminate")
	h.fake.list(jqlMine, "A-1", "A-2")
	h.fetch("mine")
	h.settle()
	if rows := h.changeRows(); !eq(rows, []string{"A-1:added", "A-1:closed"}) {
		t.Errorf("rows = %v, want the earlier closed row and exactly one added row", rows)
	}
	if got := h.watched(); !eq(got, []string{"A-1", "A-2"}) {
		t.Errorf("watched = %v", got)
	}
}

// A watched query added to the configuration after the others has no marker:
// its first complete run is a baseline, silent for every member, the ones the
// store already tracks included.
func TestWatchedQueryAddedLaterRunsABaselineWithZeroRowsForItsMembers(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.list(jqlMine, "A-1")
	h.fetch("mine")
	h.fake.setIssue("T-1", "indeterminate")
	h.fake.list(jqlTeam, "T-1")
	h.fetch("team") // T-1 is tracked, unwatched

	const jqlLate = "project = LATE"
	h.cfg.Queries["late"] = schema.QueryDef{Expr: schema.QueryExpr{jqlLate}, Watched: true}
	h.fake.setIssue("L-1", "new")
	h.fake.setIssue("L-2", "indeterminate")
	h.fake.list(jqlLate, "A-1", "T-1", "L-1", "L-2")

	row := h.fetch("late")
	if !row.Complete || row.BaselineAt.IsZero() || len(row.IDs) != 4 {
		t.Fatalf("row = %+v, want a complete baseline run of four members", row)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("the baseline of the later query appended rows: %v", rows)
	}
	if got := h.watched(); !eq(got, []string{"A-1", "L-1", "L-2", "T-1"}) {
		t.Errorf("watched = %v, want every member of the later query marked", got)
	}
}
