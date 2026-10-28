package main

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The beads halves of the issue change-flow gates and the three reports of
// `pg-desk doctor`, over the gate fixture's scripted pg-connector.

// capsBeads builds a capabilities answer for the two beads instances of the
// gate fixture, each with its own watched value.
func capsBeads(watchedA, watchedB bool) string {
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	return `{"sources":[],"backends":[` +
		`{"backend":"owning-demo","vocabulary":{"owns_changes":true},"queries":{"q-mine":{"watched":true},"q-team":{"watched":true}}},` +
		`{"backend":"beads-a","vocabulary":{"owns_changes":false},"queries":{"bd-a":{"watched":` + b(watchedA) + `}}},` +
		`{"backend":"beads-b","vocabulary":{"owns_changes":false},"queries":{"bd-b":{"watched":` + b(watchedB) + `}}}]}`
}

// seedBackendEntity writes an issue entity with the backend column and the
// watched mark given (nil watched leaves the column NULL).
func (f *gateFixture) seedBackendEntity(id, backend string, active bool, watched *bool) {
	f.t.Helper()
	at := doctorNow.Format(time.RFC3339)
	e := store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: at}
	opts := store.WriteOptions{Watched: watched}
	if backend != "" {
		opts.Backend = &backend
	}
	if _, err := f.seed.WriteEntityStateWithLogOpts(e, 0, at, active, nil, []string{"added"}, "test", at, opts); err != nil {
		f.t.Fatalf("seed %s: %v", id, err)
	}
}

func boolPtr(v bool) *bool { return &v }

func (f *gateFixture) watchSet(query string, ids ...string) {
	f.t.Helper()
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = `"` + id + `"`
	}
	if err := f.seed.SetMeta("change_flow.watchset.issue."+query, "["+strings.Join(quoted, ",")+"]"); err != nil {
		f.t.Fatal(err)
	}
}

func TestDoctorBeadsDispatchCheckWatchedMismatchFails(t *testing.T) {
	f := newGateFixture(t)
	f.caps(capsBeads(false, true), 0)
	wantGateFailure(t, gateNameDispatch, "FAIL", "watched_differs", "beads-a", "bd-a")
}

func TestDoctorBeadsDispatchCheckQueryNotListedFails(t *testing.T) {
	f := newGateFixture(t)
	f.caps(`{"sources":[],"backends":[`+
		`{"backend":"owning-demo","vocabulary":{"owns_changes":true},"queries":{"q-mine":{"watched":true},"q-team":{"watched":true}}},`+
		`{"backend":"beads-a","vocabulary":{"owns_changes":false},"queries":{}},`+
		`{"backend":"beads-b","vocabulary":{"owns_changes":false},"queries":{"bd-b":{"watched":true}}}]}`, 0)
	wantGateFailure(t, gateNameDispatch, "FAIL", "query_missing", "beads-a", "bd-a")
}

func TestDoctorBeadsBackendWithoutWatchedQueryFailsAndPassesWithOne(t *testing.T) {
	f := newGateFixture(t)
	f.seedBackendEntity("pg2-1", "beads-a", true, boolPtr(true))
	f.caps(capsBeads(false, true), 0)
	wantGateFailure(t, gateNameNoWatchedQuery, "FAIL", "beads-a", "1 active issue entities", "no watched query")

	f.caps(capsBeads(true, true), 0)
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a declared watched query must pass: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, gateNameNoWatchedQuery+": ok") {
		t.Errorf("stdout lacks the ok line:\n%s", stdout)
	}
}

func TestDoctorBeadsBackendWithoutWatchedQueryIgnoresInactiveEntities(t *testing.T) {
	f := newGateFixture(t)
	f.seedBackendEntity("pg2-1", "beads-a", false, nil)
	f.caps(capsBeads(false, false), 0)
	stdout, _ := runDoctorCmd(t)
	if !strings.Contains(stdout, gateNameNoWatchedQuery+": ok") {
		t.Errorf("an inactive entity tripped the gate:\n%s", stdout)
	}
}

// The default state of a deployment that declared nothing: an active issue
// entity attributable to no backend while no non-owning backend declares a
// watched query.
func TestDoctorUnattributedEntityFailsWhileNoBeadsBackendDeclaresAWatchedQuery(t *testing.T) {
	f := newGateFixture(t)
	f.seedBackendEntity("pg2-9", "", true, nil)
	f.caps(capsBeads(false, false), 0)
	wantGateFailure(t, gateNameNoWatchedQuery, "FAIL", "1 active issue entities", "attributable to no backend")

	// One non-owning backend declaring a watched query makes the unattributed
	// entity a matter for the dispatch of the entity, not this gate.
	f.caps(capsBeads(false, true), 0)
	stdout, _ := runDoctorCmd(t)
	if !strings.Contains(stdout, gateNameNoWatchedQuery+": ok") {
		t.Errorf("gate failed though a backend declares a watched query:\n%s", stdout)
	}
}

func TestDoctorUnattributedEntityFailsWithNoBackendConfigured(t *testing.T) {
	f := newGateFixture(t)
	f.cfg.Watch.Issue.Queries = nil
	f.cfg.Watch.Issue.Backends = nil
	f.seedBackendEntity("pg2-9", "", true, nil)
	wantGateFailure(t, gateNameNoWatchedQuery, "FAIL", "attributable to no backend")
}

// An entity without a backend column is attributed through the persisted watch
// sets, exactly as the dispatch of an id is.
func TestDoctorBeadsEntityIsAttributedThroughItsWatchSet(t *testing.T) {
	f := newGateFixture(t)
	f.seedBackendEntity("pg2-1", "", true, nil)
	f.watchSet("bd-a", "pg2-1")
	f.caps(capsBeads(false, true), 0)
	wantGateFailure(t, gateNameNoWatchedQuery, "FAIL", "beads-a", "1 active issue entities")
}

func TestDoctorBeadsGatesUnavailableCapabilitiesAreUnreachableNotAPass(t *testing.T) {
	f := newGateFixture(t)
	f.cfg.Watch.Issue.Queries = []string{"bd-a"}
	f.cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"bd-a": {Instance: "beads-a"}}
	f.seedBackendEntity("pg2-1", "beads-a", true, boolPtr(true))
	f.caps(gateUnavailable, 1)
	stdout, err := runDoctorCmd(t)
	if err == nil {
		t.Fatalf("doctor passed with the daemon unavailable:\n%s", stdout)
	}
	for _, gate := range []string{gateNameDispatch, gateNameNoWatchedQuery} {
		if !strings.Contains(stdout, gate+": UNREACHABLE (") || !strings.Contains(stdout, "beads-a") {
			t.Errorf("stdout lacks an UNREACHABLE %q naming beads-a:\n%s", gate, stdout)
		}
		if !strings.Contains(err.Error(), gate) {
			t.Errorf("failure list %q lacks %q", err, gate)
		}
	}
}

func TestDoctorBeadsBaselineOpenMoreThanADayFailsNamingQueryAndMembers(t *testing.T) {
	f := newGateFixture(t)
	f.seedBackendEntity("pg2-1", "beads-a", true, boolPtr(true))
	f.seedBackendEntity("pg2-2", "beads-a", true, nil)     // not watched
	f.seedBackendEntity("pg2-3", "beads-a", false, nil)    // inactive
	f.watchSet("bd-a", "pg2-1", "pg2-2", "pg2-3", "pg2-4") // pg2-4 absent
	if err := f.seed.SetMeta(changes.MetaBaselineOpenPrefix+"bd-a", doctorNow.Add(-48*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	wantGateFailure(t, gateNameBaselineOpen, "FAIL", "beads-a", "bd-a", "pg2-2 (not watched)", "pg2-3 (inactive)", "pg2-4 (absent)")

	// With the marker present the baseline is finished and the gate passes.
	if err := f.seed.SetMeta(changes.MetaBaselinePrefix+"bd-a", doctorNow.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a finished baseline must pass: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, gateNameBaselineOpen+": ok") {
		t.Errorf("stdout lacks the ok line:\n%s", stdout)
	}
}

func TestDoctorBeadsBaselineOpenUnderADayOrNeverStartedPasses(t *testing.T) {
	f := newGateFixture(t)
	if err := f.seed.SetMeta(changes.MetaBaselineOpenPrefix+"bd-a", doctorNow.Add(-23*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a baseline open for 23h (and bd-b never started) must pass: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, gateNameBaselineOpen+": ok") {
		t.Errorf("stdout lacks the ok line:\n%s", stdout)
	}
}

// Three watched and two unwatched active entities across two watch sets: the
// reports count them and never add to the failure list.
func TestDoctorIssueReportsCountWatchedMembersAndEntitiesNothingWatches(t *testing.T) {
	f := newGateFixture(t)
	f.seedBackendEntity("pg2-1", "beads-a", true, boolPtr(true))
	f.seedBackendEntity("pg2-2", "beads-a", true, boolPtr(true))
	f.seedBackendEntity("pg2-3", "beads-b", true, boolPtr(true))
	f.seedBackendEntity("pg2-4", "beads-a", true, nil)  // held by a watch set, not watched
	f.seedBackendEntity("pg2-5", "beads-b", true, nil)  // nothing watches it
	f.seedBackendEntity("pg2-6", "beads-b", false, nil) // inactive: never counted
	f.watchSet("bd-a", "pg2-1", "pg2-2", "pg2-4")
	f.watchSet("bd-b", "pg2-3")

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("the reports must never fail doctor: %v\n%s", err, stdout)
	}
	for _, want := range []string{
		"issue change-flow reports:",
		"beads-a: 2",
		"beads-b: 1",
		"bd-a: 3",
		"bd-b: 1",
		"active issue entities nothing watches: 1 (an entity nothing watches)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "FAIL") || strings.Contains(stdout, "UNREACHABLE") {
		t.Errorf("a gate failed in a clean configuration:\n%s", stdout)
	}
}

// A report is never added to the failure list: with two failing gates the list
// is exactly their names (beads-b reports its query unwatched, so the dispatch
// check fails too).
func TestDoctorIssueReportsNeverJoinTheFailureList(t *testing.T) {
	f := newGateFixture(t)
	f.seedBackendEntity("pg2-5", "beads-b", true, nil)
	f.caps(capsBeads(true, false), 0)
	stdout, err := runDoctorCmd(t)
	if err == nil {
		t.Fatalf("expected the no-watched-query gate to fail:\n%s", stdout)
	}
	if got := err.Error(); !strings.Contains(got, "2 check(s) failed: ["+gateNameDispatch+" "+gateNameNoWatchedQuery+"]") {
		t.Errorf("failure list = %q, want only the two gates", got)
	}
	if !strings.Contains(stdout, "active issue entities nothing watches: 1") {
		t.Errorf("stdout lacks the report:\n%s", stdout)
	}
}
