package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
)

// The two `run issue` hydration cases that are not served by an owning
// backend (no owning backend configured; a backend that keeps no store) share
// one entity write. These tests pin what that write does to the entity row,
// the derived links and the interpretation, case by case, so the move of
// both cases from the retired plain write to RunEntityChange (SnapshotOnly)
// is a list of named differences, each decided in the plan.

const hydrateIssueID = "PROJ-41"

type hydrationCase struct {
	name  string
	setup func(f *backendFixture)
}

var hydrationCases = []hydrationCase{
	{"no owning backend", func(f *backendFixture) { f.cfg.Watch.Issue.Backends = nil }},
	{"non-owning backend", func(f *backendFixture) {
		// A held row is pinned to the non-owning instance; an absent one
		// resolves to it through the probe.
		if _, found, _ := f.seed.GetEntity("o/r", "issue", hydrateIssueID); found {
			f.setBackend(hydrateIssueID, "beads-a")
		}
	}},
}

func issueBody(extra string) string {
	return fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":4,"result":{"id":%q,"title":"t","owner":"me","assignee":"x","issue_type":"task","state":"open","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"%s}}`, hydrateIssueID, extra)
}

// hydrateRaw runs hydrateJiraIssue and returns its error.
func hydrateRaw(t *testing.T, f *backendFixture, change gather.ChangeKind) error {
	t.Helper()
	st, err := deskStoreOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	installIssueGate(st, f.cfg)
	return hydrateJiraIssue(context.Background(), pipeline.New(f.cfg, st), f.cfg, st, hydrateIssueID, change)
}

func TestHydrateJiraIssueUnownedFirstWriteBookkeeping(t *testing.T) {
	for _, tc := range hydrationCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t) // no row yet
			f.write("show-"+hydrateIssueID+".json", issueBody(""))
			tc.setup(f)
			if err := hydrateRaw(t, f, gather.ChangeAdded); err != nil {
				t.Fatal(err)
			}
			e := f.entity("issue", hydrateIssueID)
			if e.Facts == "" || e.Facts == "{}" {
				t.Fatalf("facts not written: %q", e.Facts)
			}
			if e.Version != firstWriteVersion || (e.HydratedAt != "") != firstWriteHydrated {
				t.Errorf("version=%d hydrated_at=%q, want version %d hydrated=%v", e.Version, e.HydratedAt, firstWriteVersion, firstWriteHydrated)
			}
			if e.Inactive {
				t.Error("a new issue is inactive")
			}
			if h := f.history(hydrateIssueID); len(h) != 0 {
				t.Errorf("history = %+v, want no change record from a snapshot hydration", h)
			}
			if _, found, err := f.seed.GetInterpretation("o/r", "issue", hydrateIssueID); err != nil || !found {
				t.Errorf("interpretation found=%v err=%v", found, err)
			}
		})
	}
}

func TestHydrateJiraIssueUnownedKeepsADeactivatedIssueInactive(t *testing.T) {
	for _, tc := range hydrationCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t, hydrateIssueID)
			f.write("show-"+hydrateIssueID+".json", issueBody(""))
			tc.setup(f)
			if _, err := f.seed.MarkEntityInactive("o/r", "issue", hydrateIssueID, "k"); err != nil {
				t.Fatal(err)
			}
			if err := hydrateRaw(t, f, gather.ChangeChanged); err != nil {
				t.Fatal(err)
			}
			if e := f.entity("issue", hydrateIssueID); !e.Inactive {
				t.Error("a Jira-keyed run issue event reactivated a deactivated issue")
			}
		})
	}
}

func TestHydrateJiraIssueUnownedNotFoundRemovedLeavesTheHeldSnapshot(t *testing.T) {
	for _, tc := range hydrationCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t, hydrateIssueID)
			// No show file: the connector answers not_found.
			tc.setup(f)
			before := f.entity("issue", hydrateIssueID)
			if err := hydrateRaw(t, f, gather.ChangeRemoved); err != nil {
				t.Fatal(err)
			}
			after := f.entity("issue", hydrateIssueID)
			if after.Facts != before.Facts {
				t.Errorf("facts changed from %q to %q on a removal", before.Facts, after.Facts)
			}
		})
	}
}

func TestHydrateJiraIssueUnownedNotFoundWithoutRemovalIsAnError(t *testing.T) {
	for _, tc := range hydrationCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t, hydrateIssueID)
			tc.setup(f)
			if err := hydrateRaw(t, f, gather.ChangeChanged); err == nil {
				t.Fatal("not_found for a changed event must be an error")
			}
		})
	}
}

func TestHydrateJiraIssueUnownedDegradedRead(t *testing.T) {
	for _, tc := range hydrationCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t, hydrateIssueID)
			f.write("show-"+hydrateIssueID+".json", issueBody(`,"stale":true`))
			tc.setup(f)
			before := f.entity("issue", hydrateIssueID)
			err := hydrateRaw(t, f, gather.ChangeChanged)
			after := f.entity("issue", hydrateIssueID)
			_, interpFound, _ := f.seed.GetInterpretation("o/r", "issue", hydrateIssueID)
			if degradedWritesNothing {
				if err == nil {
					t.Fatal("a degraded read must not report success")
				}
				if after.Facts != before.Facts || interpFound {
					t.Errorf("a degraded read wrote: facts %q, interpretation %v", after.Facts, interpFound)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if after.Facts == before.Facts || !interpFound {
				t.Errorf("the plain write stores a degraded read: facts %q, interpretation %v", after.Facts, interpFound)
			}
		})
	}
}

func TestHydrateJiraIssueUnownedWatchedStatement(t *testing.T) {
	for _, tc := range hydrationCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t, hydrateIssueID)
			f.write("show-"+hydrateIssueID+".json", issueBody(`,"entity_meta":{"watched":true}`))
			tc.setup(f)
			if err := hydrateRaw(t, f, gather.ChangeChanged); err != nil {
				t.Fatal(err)
			}
			e := f.entity("issue", hydrateIssueID)
			if got := e.Watched != nil && *e.Watched; got != hydrationStatesWatched {
				t.Errorf("watched = %v, want %v", got, hydrationStatesWatched)
			}
		})
	}
}

func TestHydrateJiraIssueUnownedDerivedLinksAndBackendColumn(t *testing.T) {
	f := newBackendFixture(t, hydrateIssueID)
	f.write("show-"+hydrateIssueID+".json", issueBody(`,"parent":"PROJ-1"`))
	hydrationCases[1].setup(f)
	if err := hydrateRaw(t, f, gather.ChangeChanged); err != nil {
		t.Fatal(err)
	}
	if e := f.entity("issue", hydrateIssueID); e.Backend != "beads-a" {
		t.Errorf("backend = %q, want beads-a", e.Backend)
	}
	links, err := f.seed.ListXrefLinksFrom("o/r", "issue", hydrateIssueID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ToType != "issue" || links[0].ToID != "PROJ-1" || links[0].Relation != "parent" {
		t.Errorf("derived links = %+v, want the one parent link", links)
	}
}

// Decided differences from the retired plain write, which the move to
// RunEntityChange (SnapshotOnly) makes on purpose:
//   - the first write is an observation: version 1 with hydrated_at, so the
//     next hydration classifies a change instead of a first observation;
//   - a degraded read writes nothing and is an error to retry;
//   - an entity_meta.watched statement is stored.
const (
	firstWriteVersion      int64 = 1
	firstWriteHydrated           = true
	degradedWritesNothing        = true
	hydrationStatesWatched       = true
)
