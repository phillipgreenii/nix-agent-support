package feed

import (
	"context"
	"slices"
	"testing"
	"time"

	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestQueries_KnownNamesAndWatchedComeFromTheConfiguredQueries(t *testing.T) {
	r := newRig(t)
	q := r.queries
	if !q.Known("mine") || !q.Known("team") || q.Known("nope") {
		t.Errorf("Known: mine=%v team=%v nope=%v", q.Known("mine"), q.Known("team"), q.Known("nope"))
	}
	if got := q.Names(); !slices.Equal(got, []string{"mine", "team"}) {
		t.Errorf("Names = %v", got)
	}
	if !q.Watched("mine") || q.Watched("team") || q.Watched("nope") {
		t.Errorf("Watched: mine=%v team=%v nope=%v", q.Watched("mine"), q.Watched("team"), q.Watched("nope"))
	}
}

func TestQueries_MembersAreTheStoredRowWithItsBaselineFlag(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m, err := r.queries.Members(ctx, "mine")
	if err != nil || len(m.Refs) != 0 || m.Baseline {
		t.Fatalf("before any run: %+v, %v, want no members and no baseline", m, err)
	}
	r.seed()
	m, err = r.queries.Members(ctx, "mine")
	if err != nil || !m.Baseline || !slices.Equal(m.Refs, []string{"A-1", "A-2"}) {
		t.Errorf("after the baseline run: %+v, %v", m, err)
	}
	if m, err := r.queries.Members(ctx, "team"); err != nil || !m.Baseline || !slices.Equal(m.Refs, []string{"T-1"}) {
		t.Errorf("team: %+v, %v", m, err)
	}
}

func TestQueries_RunReRunsOnlyAStaleResult(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.seed()
	before := r.fake.callCount()

	if err := r.queries.Run(ctx, "mine"); err != nil {
		t.Fatal(err)
	}
	if got := r.fake.callCount(); got != before {
		t.Errorf("Run of a fresh result called the origin %d more times", got-before)
	}
	r.clk.Advance(48 * time.Hour)
	if err := r.queries.Run(ctx, "mine"); err != nil {
		t.Fatal(err)
	}
	if got := r.fake.callCount(); got == before {
		t.Error("Run of a stale result did not call the origin")
	}
}

func TestQueries_RunWithoutAStoredRowRunsTheQueryAndReturnsItsError(t *testing.T) {
	r := newRig(t)
	r.fake.list(jqlMine, "A-1")
	r.fake.setIssue("A-1", "new")
	if err := r.queries.Run(context.Background(), "mine"); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.queries.Members(context.Background(), "mine"); !slices.Equal(m.Refs, []string{"A-1"}) {
		t.Errorf("members after Run = %v", m.Refs)
	}
	r.clk.Advance(48 * time.Hour)
	r.fake.setErr(errBoom)
	if err := r.queries.Run(context.Background(), "mine"); err == nil {
		t.Error("a failed run must be returned")
	}
}

func TestQueries_WatchedRefsListWatchedVisibleIssuesOnly(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.seed()
	refs, err := r.queries.WatchedRefs(ctx)
	if err != nil || !slices.Equal(refs, []string{"A-1", "A-2"}) {
		t.Fatalf("WatchedRefs = %v, %v, want the watched members and not the unwatched T-1", refs, err)
	}
	if err := r.pers.MarkGone(ctx, "A-2", governor.PurposeRefresh); err != nil {
		t.Fatal(err)
	}
	refs, err = r.queries.WatchedRefs(ctx)
	if err != nil || !slices.Equal(refs, []string{"A-1"}) {
		t.Errorf("WatchedRefs after A-2 is gone = %v, %v", refs, err)
	}
}

func TestWithClockIgnoresNil(t *testing.T) {
	r := newRig(t)
	q := newQueries(r.runner, r.st, WithClock(nil))
	if _, ok := q.clk.(governor.SystemClock); !ok {
		t.Errorf("clock = %T, want the system clock", q.clk)
	}
}

var _ libfeed.Queries = (*queries)(nil)

type boom struct{}

func (boom) Error() string { return "boom" }

var errBoom error = boom{}
