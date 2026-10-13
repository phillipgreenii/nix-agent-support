package groupsvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
)

func TestWithNoSummaryAndAFailingOriginEveryGroupIsUnavailableNamingTheFailure(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	r.Origin.FailWith(errors.New("gh: HTTP 502: bad gateway"))
	res := ensure(t, r, node, []string{groups.Summary, groups.Detail}, groupsvc.EnsureOptions{})
	if res.Summary.NodeID != "" {
		t.Errorf("Summary = %+v, want none", res.Summary)
	}
	for _, g := range res.Groups {
		if g.Status != groupsvc.Unavailable || g.Err == nil || !strings.Contains(g.Err.Error(), "502") {
			t.Errorf("%s = %+v, want unavailable naming the origin failure", g.Group, g)
		}
	}
	if f.total() != 0 {
		t.Error("a group was fetched without a summary to build its target from")
	}
}

func TestAStaleSummaryThatCannotBeRefreshedServesTheStoredRowsStale(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	seed(r, node, freshRow(r, node, groups.Detail, "d"))
	r.Clock.Advance(2 * time.Minute) // the summary (60 s) is stale; detail (10 min) is not
	r.Origin.FailWith(errors.New("gh: HTTP 502: bad gateway"))

	res := ensure(t, r, node, []string{groups.Summary, groups.Detail}, groupsvc.EnsureOptions{})
	if res.Summary.NodeID != node {
		t.Errorf("Summary = %+v, want the stored one", res.Summary)
	}
	for _, g := range res.Groups {
		if g.Status != groupsvc.Stale || g.Err == nil || !strings.Contains(g.Err.Error(), "502") || g.FromOrigin {
			t.Errorf("%s = %+v, want stale with the origin failure", g.Group, g)
		}
	}
	if f.total() != 0 {
		t.Error("detail was fetched against a summary that could not be refreshed")
	}
}

func TestASummaryStillRunningAtTheDeadlineLeavesEveryGroupPending(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	register(r, groups.Detail)
	seed(r, node, freshRow(r, node, groups.Detail, "d"))
	r.Clock.Advance(2 * time.Minute)
	release, entered := r.Origin.Block()
	t.Cleanup(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, err := r.Svc.Ensure(ctx, node, []string{groups.Summary, groups.Detail}, groupsvc.EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	for _, g := range res.Groups {
		if g.Status != groupsvc.Stale || !g.Pending {
			t.Errorf("%s = %+v, want the stale row, pending", g.Group, g)
		}
	}
	// With no stored summary at all the deadline is what the groups name.
	r2 := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r2.Start()
	register(r2, groups.Detail)
	release2, _ := r2.Origin.Block()
	t.Cleanup(release2)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	res, err = r2.Svc.Ensure(ctx2, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if g := res.Groups[0]; g.Status != groupsvc.Unavailable || !g.Pending || !errors.Is(g.Err, context.DeadlineExceeded) {
		t.Errorf("detail = %+v, want unavailable, pending, naming the deadline", g)
	}
}

func TestRevalidatedReachesTheObserverAndStatusesNameThemselves(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Svc.Revalidated(groups.Detail, "ignored") // no observer: nothing happens
	obs := newObserver()
	r.Svc.SetObserver(obs)
	r.Svc.Revalidated(groups.Detail, "unchanged")
	if len(obs.reval) != 1 || obs.reval[0] != [2]string{groups.Detail, "unchanged"} {
		t.Errorf("Revalidated calls = %v", obs.reval)
	}
	for s, want := range map[groupsvc.Status]string{groupsvc.Fresh: "fresh", groupsvc.Stale: "stale", groupsvc.Unavailable: "unavailable", 0: "unknown"} {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", int(s), s.String(), want)
		}
	}
}
