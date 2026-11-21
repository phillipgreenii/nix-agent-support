package store_test

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The two repositories of a multiple-repository configuration. The second is
// a fixture string only; nothing here reaches GitHub.
const (
	isoRepoA = "ZR-Private/ziprecruiter"
	isoRepoB = "phillipgziprecruiter/pending-review-scratch"
)

// TestRepoIsolation proves the store keys entity, interpretation, xref and
// history rows by repository: the same entity id (here the same bare number
// "7") in two repositories keeps two independent rows.
func TestRepoIsolation(t *testing.T) {
	ctx := context.Background()
	s := store.OpenNewSchemaForTest(t)

	const id = "7"
	for _, tc := range []struct {
		repo, facts string
	}{{isoRepoA, "facts-a"}, {isoRepoB, "facts-b"}} {
		e := store.Entity{Repo: tc.repo, EntityType: "pr", EntityID: id, Facts: tc.facts}
		if _, err := s.WriteEntityStateWithLog(ctx, e, 0, "2026-11-01T00:00:00Z", true, []string{"created"}, "sync", "t1"); err != nil {
			t.Fatalf("write %s: %v", tc.repo, err)
		}
		if err := s.UpsertInterpretation(ctx, store.Interpretation{
			Repo: tc.repo, EntityType: "pr", EntityID: id, Ownership: "mine", Enrichment: "{}",
			Urgency: "{}", Category: tc.repo, Dispositions: "{}", Approvals: "{}", GateState: "g",
			MatchReasons: "[]", Panel: "p", AsOf: "2026-11-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("interpretation %s: %v", tc.repo, err)
		}
		if err := s.UpsertXref(ctx, store.Xref{
			Repo: tc.repo, FromType: "pr", FromID: id, ToType: "issue", ToID: "K-" + tc.repo,
			Evidence: "title", FirstSeen: "2026-11-01T00:00:00Z", LastConfirmed: "2026-11-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("xref %s: %v", tc.repo, err)
		}
	}

	for _, tc := range []struct{ repo, facts string }{{isoRepoA, "facts-a"}, {isoRepoB, "facts-b"}} {
		got, found, err := s.GetEntity(ctx, tc.repo, "pr", id)
		if err != nil || !found || got.Facts != tc.facts || got.Repo != tc.repo {
			t.Fatalf("GetEntity(%s) = (%+v, %v, %v), want facts %q", tc.repo, got, found, err, tc.facts)
		}
		in, found, err := s.GetInterpretation(ctx, tc.repo, "pr", id)
		if err != nil || !found || in.Category != tc.repo {
			t.Fatalf("GetInterpretation(%s) = (%+v, %v, %v)", tc.repo, in, found, err)
		}
		xs, err := s.ListXrefsByFrom(ctx, tc.repo, "pr", id, "issue")
		if err != nil || len(xs) != 1 || xs[0].ToID != "K-"+tc.repo {
			t.Fatalf("ListXrefsByFrom(%s) = (%+v, %v)", tc.repo, xs, err)
		}
		h, err := s.ListEntityHistory(ctx, tc.repo, "pr", id, 0)
		if err != nil || len(h) != 1 {
			t.Fatalf("ListEntityHistory(%s) = (%+v, %v), want one record", tc.repo, h, err)
		}
	}

	// A write to repository B does not move repository A.
	eb := store.Entity{Repo: isoRepoB, EntityType: "pr", EntityID: id, Facts: "facts-b2"}
	if _, err := s.WriteEntityStateWithLog(ctx, eb, 1, "2026-11-02T00:00:00Z", false, []string{"updated"}, "sync", "t2"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	a, _, _ := s.GetEntity(ctx, isoRepoA, "pr", id)
	if a.Facts != "facts-a" || a.Version != 1 {
		t.Fatalf("repo A changed by a repo B write: %+v", a)
	}
	ha, _ := s.ListEntityHistory(ctx, isoRepoA, "pr", id, 0)
	hb, _ := s.ListEntityHistory(ctx, isoRepoB, "pr", id, 0)
	if len(ha) != 1 || len(hb) != 2 {
		t.Fatalf("history A=%d B=%d, want 1 and 2", len(ha), len(hb))
	}

	// An id stored only for A is invisible to B.
	if _, found, _ := s.GetEntity(ctx, isoRepoB, "pr", "8"); found {
		t.Fatal("an absent entity must not be found")
	}
}
