package focus

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
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
	f.issue("K-1", issueSpec{state: "open"})
	f.cfg.Watch.Issue.Queries = []string{"mine"}
	in := f.inputs()

	t.Run("complete", func(t *testing.T) {
		setListing(t, f, "issue", "mine", "ok", "")
		rep, err := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		if err != nil {
			t.Fatal(err)
		}
		if rep.State.Incomplete || rep.AllFailed || len(rep.State.Reasons) != 0 {
			t.Errorf("report = %+v, want complete", rep)
		}
		if got := rep.State.PerType["issue"]; got.Active != 1 || got.DueBacklog != 0 || got.OldestHydratedAt == "" {
			t.Errorf("issue coverage = %+v", got)
		}
	})
	t.Run("degraded and failed are incomplete", func(t *testing.T) {
		for _, status := range []string{"degraded", "failed"} {
			setListing(t, f, "issue", "mine", status, "slow")
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
		f.cfg.Watch.Issue.Queries = []string{"mine", "other"}
		if err := f.st.RegisterConsumer("pg-router", "issue", fixtureNow); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.st.ForgetConsumer("pg-router", "issue") })
		rep, _ := ComputeCoverage(f.st, f.cfg, fixtureNow, in.Entities)
		if !rep.State.Incomplete {
			t.Fatalf("report = %+v", rep)
		}
		var found bool
		for _, l := range rep.State.PerType["issue"].Listings {
			if l.Query == "other" && l.Status == StatusUnknown {
				found = true
			}
		}
		if !found {
			t.Errorf("listings = %+v, want other unknown", rep.State.PerType["issue"].Listings)
		}
		f.cfg.Watch.Issue.Queries = []string{"mine"}
	})
	t.Run("a small backlog is a notice, a large one is incomplete", func(t *testing.T) {
		// The issue backlog is its pending hydrations: watched entities never
		// hydrated, plus the failed-ingest entries. A PR has no due backlog (below).
		g := newFixture(t)
		g.cfg.Watch.Issue.Queries = []string{"mine"}
		setListing(t, g, "issue", "mine", "ok", "")
		g.pending("K-1")
		rep, _ := ComputeCoverage(g.st, g.cfg, fixtureNow, g.inputs().Entities)
		// 1 pending of 1 active: the default bound is ceil(10%) = 1, so within it.
		if rep.State.Incomplete || len(rep.Notices) != 1 {
			t.Errorf("within the bound: %+v", rep)
		}
		n := 1
		g.cfg.Focus.CoverageBacklogMax = &n
		g.pending("K-2")
		rep, _ = ComputeCoverage(g.st, g.cfg, fixtureNow, g.inputs().Entities)
		if !rep.State.Incomplete || !strings.Contains(strings.Join(rep.State.Reasons, ";"), "backlog") {
			t.Errorf("above the bound: %+v", rep)
		}
		// Failed-ingest entries count too: one more of them keeps it above the bound.
		if got := rep.State.PerType["issue"].DueBacklog; got != 2 {
			t.Errorf("DueBacklog = %d, want 2 pending hydrations", got)
		}
	})
	t.Run("a hydrated issue set is no backlog however old", func(t *testing.T) {
		g := newFixture(t)
		g.cfg.Watch.Issue.Queries = []string{"mine"}
		setListing(t, g, "issue", "mine", "ok", "")
		for i := 0; i < 50; i++ {
			k := g.issue(fmt.Sprintf("K-%d", i), issueSpec{state: "open"})
			watched := true
			if err := g.st.SetEntityColumns(testRepo, "issue", k.ID, nil, &watched); err != nil {
				t.Fatal(err)
			}
		}
		n := 1
		g.cfg.Focus.CoverageBacklogMax = &n
		late := fixtureNow.Add(365 * 24 * time.Hour) // far past hydrated_at + any sweep age
		rep, _ := ComputeCoverage(g.st, g.cfg, late, g.inputs().Entities)
		if rep.State.Incomplete || len(rep.Notices) != 0 {
			t.Errorf("a fully hydrated set read incomplete or noisy: %+v", rep)
		}
		if got := rep.State.PerType["issue"]; got.Active != 50 || got.DueBacklog != 0 {
			t.Errorf("issue coverage = %+v, want 50 active and no due backlog", got)
		}
	})
	t.Run("failed-ingest entries are counted into the issue backlog", func(t *testing.T) {
		g := newFixture(t)
		g.cfg.Watch.Issue.Queries = []string{"mine"}
		setListing(t, g, "issue", "mine", "ok", "")
		g.pending("K-1")
		g.pending("K-2")
		g.pending("K-3")
		for i, id := range []string{"PROJ-1", "PROJ-2"} {
			if err := g.st.RecordIngestFailure(store.IngestFailure{Instance: "jira-a", EntityID: id, Seq: int64(i + 1), StoreIdentity: "s", FirstFailedAt: fixtureAt}); err != nil {
				t.Fatal(err)
			}
		}
		rep, _ := ComputeCoverage(g.st, g.cfg, fixtureNow, g.inputs().Entities)
		if got := rep.State.PerType["issue"].DueBacklog; got != 5 {
			t.Errorf("DueBacklog = %d, want 5 (3 pending hydrations and 2 failed ingests)", got)
		}
	})
}

// PR is not a coverage type (PR lane refactor, packet P9a-3): the daemon is the
// PR feed, so a stored PR adds no per-type coverage entry, no listing and no
// backlog however old it is, and a legacy watch.pr.queries key (ignored by the
// loader) cannot make coverage incomplete.
func TestComputeCoverageHasNoPRInput(t *testing.T) {
	f := newFixture(t)
	f.pr("1", prSpec{ownership: "mine"})
	f.pr("2", prSpec{ownership: "mine"})
	f.pr("3", prSpec{ownership: "mine"})
	n := 1
	f.cfg.Focus.CoverageBacklogMax = &n
	late := fixtureNow.Add(30 * 24 * time.Hour)
	rep, err := ComputeCoverage(f.st, f.cfg, late, f.inputs().Entities)
	if err != nil {
		t.Fatal(err)
	}
	if rep.State.Incomplete || len(rep.Notices) != 0 || len(rep.State.Reasons) != 0 {
		t.Errorf("stored PRs made coverage incomplete or noisy: %+v", rep)
	}
	if _, ok := rep.State.PerType["pr"]; ok {
		t.Errorf("PerType carries a pr entry: %+v", rep.State.PerType)
	}
	for _, typ := range CoverageTypes {
		if typ == "pr" {
			t.Errorf("CoverageTypes = %v, want no pr", CoverageTypes)
		}
	}
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

func TestWatchedUnwatchedForAnOwningBackendReadsTheColumn(t *testing.T) {
	w := Watched{
		"issue": {
			IDs:   map[string]bool{"PROJ-1": true, "bd-1": true},
			Known: true,
			Owned: map[string]bool{"PROJ-1": true, "PROJ-2": true, "PROJ-3": true},
		},
	}
	if w.Unwatched(Key{"issue", "PROJ-1"}) {
		t.Errorf("an owning issue with watched set read unwatched")
	}
	for _, id := range []string{"PROJ-2", "PROJ-3"} {
		if !w.Unwatched(Key{"issue", id}) {
			t.Errorf("owning issue %s (watched 0 or NULL) must read unwatched", id)
		}
	}
	if w.Unwatched(Key{"issue", "bd-1"}) || !w.Unwatched(Key{"issue", "bd-2"}) {
		t.Errorf("a beads issue must follow the persisted set exactly as before")
	}
	// An owning entity is judged by its column even when no persisted set
	// of the type was ever recorded.
	w["issue"] = WatchSet{IDs: map[string]bool{}, Known: false, Owned: map[string]bool{"PROJ-2": true}}
	if !w.Unwatched(Key{"issue", "PROJ-2"}) {
		t.Errorf("an owning issue with watched NULL must read unwatched even with no persisted set")
	}
	if w.Unwatched(Key{"issue", "bd-2"}) {
		t.Errorf("a beads issue with no persisted set stays not-unwatched")
	}
}

func TestLoadWatchedReadsTheWatchedColumnForAnOwningBackend(t *testing.T) {
	f := newFixture(t)
	f.cfg.Watch.Issue.Queries = []string{"open", "tracked"}
	f.cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"open":    {Instance: "beads-a"},
		"tracked": {Instance: "jira-a", OwnsChanges: true},
	}
	for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3", "bd-1"} {
		f.writeEntity(Key{"issue", id}, `{}`, true)
	}
	jira, beads, yes, no := "jira-a", "beads-a", true, false
	set := func(id string, backend *string, watched *bool) {
		if err := f.st.SetEntityColumns(testRepo, "issue", id, backend, watched); err != nil {
			t.Fatal(err)
		}
	}
	set("PROJ-1", &jira, &yes)
	set("PROJ-2", &jira, &no)
	set("PROJ-3", &jira, nil)
	set("bd-1", &beads, nil)
	if err := f.st.SetMeta("change_flow.watchset.issue.open", `["bd-1"]`); err != nil {
		t.Fatal(err)
	}

	w := LoadWatched(f.st, f.cfg)
	if w.Unwatched(Key{"issue", "PROJ-1"}) {
		t.Error("PROJ-1 (watched 1) read unwatched")
	}
	if !w.Unwatched(Key{"issue", "PROJ-2"}) || !w.Unwatched(Key{"issue", "PROJ-3"}) {
		t.Error("PROJ-2 (watched 0) and PROJ-3 (watched NULL) must read unwatched")
	}
	if w.Unwatched(Key{"issue", "bd-1"}) {
		t.Error("bd-1 is in the persisted beads set and must read watched")
	}
	if _, found, _ := f.st.GetMeta("change_flow.watchset.issue.tracked"); found {
		t.Error("a watchset key exists for the owning query")
	}
}

// An owning-backend issue is counted like any other: with the age sweeps gone
// it is pending until it is hydrated, and Active includes it.
func TestComputeCoverageCountsOwningBackendEntitiesLikeAnyOther(t *testing.T) {
	f := newFixture(t)
	f.cfg.Watch.Issue.Queries = []string{"tracked"}
	f.cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"tracked": {Instance: "jira-a", OwnsChanges: true}}
	f.pending("PROJ-1")
	jira := "jira-a"
	if err := f.st.SetEntityColumns(testRepo, "issue", "PROJ-1", &jira, nil); err != nil {
		t.Fatal(err)
	}
	// Far past any age: only the missing hydration makes it pending.
	rep, err := ComputeCoverage(f.st, f.cfg, fixtureNow.Add(1000*time.Hour), f.inputs().Entities)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.State.PerType["issue"]; got.Active != 1 || got.DueBacklog != 1 {
		t.Errorf("issue coverage = %+v, want Active 1 and DueBacklog 1 for a watched, never-hydrated owning-backend entity", got)
	}
}

// pending stores an active issue entity that is watched and never hydrated.
func (f *fixture) pending(id string) {
	f.t.Helper()
	e := store.Entity{Repo: testRepo, EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: fixtureAt, ContentHash: "h" + id}
	if _, err := f.st.WriteEntityStateWithLog(e, 0, "", true, []string{"created"}, "sync", fixtureAt); err != nil {
		f.t.Fatalf("write pending issue %s: %v", id, err)
	}
	watched := true
	if err := f.st.SetEntityColumns(testRepo, "issue", id, nil, &watched); err != nil {
		f.t.Fatal(err)
	}
}
