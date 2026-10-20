package focus

import (
	"strings"
	"testing"
	"time"
)

// Coverage reads only the persisted listing status, the active count and due
// backlog and the watch sets; it never calls a tracker.

func setListing(t *testing.T, f *fixture, typ, query, status, reason string) {
	t.Helper()
	v := `{"status":"` + status + `","reason":"` + reason + `","at":"` + fixtureAt + `"}`
	if err := f.st.SetMeta("change_flow.listing."+typ+"."+query, v); err != nil {
		t.Fatal(err)
	}
}

func TestComputeCoverage(t *testing.T) {
	f := newFixture(t)
	f.pr("1", prSpec{ownership: "mine"})
	f.cfg.Watch.PR.Queries = []string{"mine"}
	in := f.inputs()

	t.Run("complete", func(t *testing.T) {
		setListing(t, f, "pr", "mine", "ok", "")
		rep, err := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		if err != nil {
			t.Fatal(err)
		}
		if rep.State.Incomplete || rep.AllFailed || len(rep.State.Reasons) != 0 {
			t.Errorf("report = %+v, want complete", rep)
		}
		if got := rep.State.PerType["pr"]; got.Active != 1 || got.DueBacklog != 0 || got.OldestHydratedAt == "" {
			t.Errorf("pr coverage = %+v", got)
		}
	})
	t.Run("degraded and failed are incomplete", func(t *testing.T) {
		for _, status := range []string{"degraded", "failed"} {
			setListing(t, f, "pr", "mine", status, "slow")
			rep, _ := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
			if !rep.State.Incomplete || len(rep.State.Reasons) != 1 || !strings.Contains(rep.State.Reasons[0], "slow") {
				t.Errorf("%s report = %+v", status, rep)
			}
			if rep.AllFailed != (status == "failed") {
				t.Errorf("%s AllFailed = %v", status, rep.AllFailed)
			}
		}
	})
	t.Run("a query with no persisted status is unknown and incomplete once a flow is wired", func(t *testing.T) {
		f.cfg.Watch.PR.Queries = []string{"mine", "other"}
		if err := f.st.RegisterConsumer("pg-router", "pr", fixtureNow); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.st.ForgetConsumer("pg-router", "pr") })
		rep, _ := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		if !rep.State.Incomplete {
			t.Fatalf("report = %+v", rep)
		}
		var found bool
		for _, l := range rep.State.PerType["pr"].Listings {
			if l.Query == "other" && l.Status == StatusUnknown {
				found = true
			}
		}
		if !found {
			t.Errorf("listings = %+v, want other unknown", rep.State.PerType["pr"].Listings)
		}
		f.cfg.Watch.PR.Queries = []string{"mine"}
	})
	t.Run("a small backlog is a notice, a large one is incomplete", func(t *testing.T) {
		setListing(t, f, "pr", "mine", "ok", "")
		late := fixtureNow.Add(30 * 24 * time.Hour) // every entity is past the sweep max age
		rep, _ := ComputeCoverage(f.st, f.cfg, late, in.Entities)
		// 1 due of 1 active: the default bound is ceil(10%) = 1, so within it.
		if rep.State.Incomplete || len(rep.Notices) != 1 {
			t.Errorf("within the bound: %+v", rep)
		}
		n := 1
		f.cfg.Focus.CoverageBacklogMax = &n
		f.pr("2", prSpec{ownership: "mine"})
		in2 := f.inputs()
		rep, _ = ComputeCoverage(f.st, f.cfg, late, in2.Entities)
		if !rep.State.Incomplete || !strings.Contains(strings.Join(rep.State.Reasons, ";"), "backlog") {
			t.Errorf("above the bound: %+v", rep)
		}
	})
}

// A pr watch query with no recorded listing status and no pr change flow
// wired (no pr consumer in the store) is explicitly not watched yet: a
// listing line, never UNKNOWN, and coverage stays complete. The same absence
// for the issue type, whose flow is wired, remains unknown and incomplete.
// Runs against a real schema-2 store.
func TestComputeCoverageNotWatchedWhileNoFlowIsWired(t *testing.T) {
	f := newFixture(t)
	f.pr("1", prSpec{ownership: "mine"})
	f.cfg.Watch.PR.Queries = []string{"mine", "team"}
	in := f.inputs()

	t.Run("no pr consumer: not watched, not incomplete", func(t *testing.T) {
		rep, err := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		if err != nil {
			t.Fatal(err)
		}
		if rep.State.Incomplete || len(rep.State.Reasons) != 0 || rep.AllFailed {
			t.Fatalf("report = %+v, want complete", rep)
		}
		got := rep.State.PerType["pr"].Listings
		want := []Listing{
			{Query: "mine", Status: StatusNotWatched, Reason: notWatchedReason},
			{Query: "team", Status: StatusNotWatched, Reason: notWatchedReason},
		}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("listings = %+v, want %+v", got, want)
		}
	})
	t.Run("a recorded status still wins while no consumer is registered", func(t *testing.T) {
		setListing(t, f, "pr", "mine", "failed", "boom")
		t.Cleanup(func() { _ = f.st.DeleteMeta("change_flow.listing.pr.mine") })
		rep, _ := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		if !rep.State.Incomplete || !strings.Contains(strings.Join(rep.State.Reasons, ";"), "pr mine listing failed (boom)") {
			t.Errorf("report = %+v, want the failed listing incomplete", rep)
		}
		if got := rep.State.PerType["pr"].Listings[1].Status; got != StatusNotWatched {
			t.Errorf("team = %q, want %q", got, StatusNotWatched)
		}
	})
	t.Run("a registered pr consumer makes a missing status unknown and incomplete", func(t *testing.T) {
		if err := f.st.RegisterConsumer("pg-router", "pr", fixtureNow); err != nil {
			t.Fatal(err)
		}
		rep, _ := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		if !rep.State.Incomplete || len(rep.State.Reasons) != 2 {
			t.Fatalf("report = %+v, want both queries unknown and incomplete", rep)
		}
		for _, l := range rep.State.PerType["pr"].Listings {
			if l.Status != StatusUnknown || l.Reason != unknownReason {
				t.Errorf("listing = %+v, want unknown", l)
			}
		}
	})
	t.Run("the issue type has no not-watched grace", func(t *testing.T) {
		f.cfg.Watch.Issue.Queries = []string{"open"}
		rep, _ := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		l := rep.State.PerType["issue"].Listings
		if len(l) != 1 || l[0].Status != StatusUnknown {
			t.Errorf("issue listings = %+v, want unknown", l)
		}
	})
}

func TestWatchedUnwatched(t *testing.T) {
	w := Watched{
		"pr":    {IDs: map[string]bool{"a": true}, Known: true},
		"issue": {IDs: map[string]bool{}, Known: false},
	}
	if w.Unwatched(Key{"pr", "a"}) {
		t.Errorf("a watched id read unwatched")
	}
	if !w.Unwatched(Key{"pr", "b"}) {
		t.Errorf("an id outside a known watch set must read unwatched")
	}
	if w.Unwatched(Key{"issue", "x"}) {
		t.Errorf("a type whose watch set was never recorded must report nothing unwatched")
	}
	if w.Unwatched(Key{"thread", "x"}) {
		t.Errorf("an unlisted type is not unwatched")
	}
}
