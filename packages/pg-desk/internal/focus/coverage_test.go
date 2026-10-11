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
	t.Run("a query with no persisted status is unknown and incomplete", func(t *testing.T) {
		f.cfg.Watch.PR.Queries = []string{"mine", "other"}
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
