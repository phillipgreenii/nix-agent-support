package snapshot

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// fixedNow is a stable instant used across the retention tests so the
// boundary math is exact and not wall-clock-dependent (pg2-ew4kf explicitly
// requires an INJECTED clock here — a real-time test would be flaky by
// construction).
var fixedNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func mergedAtAgo(d time.Duration) string {
	return fixedNow.Add(-d).Format(time.RFC3339)
}

// TestWithinMergedRetention_Boundary is the retention-window predicate's own
// boundary test: merged 23h ago is still within the 24h window; merged 25h
// ago is not. An empty or unparsable mergedAt fails safe (treated as
// expired), and exactly-24h-ago is already outside (the window is a strict
// "less than", not "less than or equal").
func TestWithinMergedRetention_Boundary(t *testing.T) {
	cases := []struct {
		name     string
		mergedAt string
		want     bool
	}{
		{"23h ago: within window", mergedAtAgo(23 * time.Hour), true},
		{"25h ago: outside window", mergedAtAgo(25 * time.Hour), false},
		{"exactly 24h ago: outside window (strict less-than)", mergedAtAgo(24 * time.Hour), false},
		{"just under 24h ago: within window", mergedAtAgo(24*time.Hour - time.Second), true},
		{"empty mergedAt: fails safe to expired", "", false},
		{"unparsable mergedAt: fails safe to expired", "not-a-timestamp", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithinMergedRetention(tc.mergedAt, fixedNow); got != tc.want {
				t.Errorf("WithinMergedRetention(%q, fixedNow) = %v, want %v", tc.mergedAt, got, tc.want)
			}
		})
	}
}

// mineMergedInput builds a PRInput for a merged PR of mine, merged `ago`
// before fixedNow.
func mineMergedInput(number int, ago time.Duration) PRInput {
	return PRInput{
		PR: api.PR{
			Repo: "o/r", Number: number, Author: "me", State: "merged",
			Merged: true, MergedAt: mergedAtAgo(ago), Title: "merged pr", URL: "u",
		},
		Ownership: ownership.Mine,
	}
}

// TestBuild_MergedMinePR_RetentionBoundary is the Build-level acceptance-
// criteria test: a PR of mine merged within the last 24h (measured from
// mergedAt, via an INJECTED GeneratedAt — never wall-clock) is retained in
// the snapshot; merged more than 24h ago it is absent — dropped
// automatically, with no persisted "seen" state (recomputed fresh from
// mergedAt vs GeneratedAt on every Build call). Mirrors the bead's exact
// probe points: 23h ago present, 25h ago absent.
func TestBuild_MergedMinePR_RetentionBoundary(t *testing.T) {
	t.Run("merged 23h ago: present", func(t *testing.T) {
		snap := Build(BuilderInput{
			GeneratedAt: fixedNow,
			Self:        "me",
			PRs:         []PRInput{mineMergedInput(1, 23*time.Hour)},
		})
		if len(snap.Mine) != 1 || snap.Mine[0].Number != 1 {
			t.Fatalf("expected merged-23h-ago PR retained in Mine, got %+v", snap.Mine)
		}
		if !snap.Mine[0].Merged {
			t.Errorf("retained merged row must carry the Merged marker for de-emphasis, got %+v", snap.Mine[0])
		}
	})

	t.Run("merged 25h ago: absent", func(t *testing.T) {
		snap := Build(BuilderInput{
			GeneratedAt: fixedNow,
			Self:        "me",
			PRs:         []PRInput{mineMergedInput(2, 25*time.Hour)},
		})
		if len(snap.Mine) != 0 {
			t.Fatalf("expected merged-25h-ago PR dropped from Mine, got %+v", snap.Mine)
		}
	})
}

// TestBuild_MergedMinePR_NoPersistedSeenState proves the retention verdict is
// recomputed fresh every Build call from mergedAt vs the CURRENT GeneratedAt
// — not cached/latched from a prior call. The exact same PRInput (same
// mergedAt) is built twice with different GeneratedAt values and must flip
// from present to absent as the injected clock advances past the window.
func TestBuild_MergedMinePR_NoPersistedSeenState(t *testing.T) {
	in := mineMergedInput(3, 23*time.Hour) // merged at fixedNow-23h

	before := Build(BuilderInput{GeneratedAt: fixedNow, Self: "me", PRs: []PRInput{in}})
	if len(before.Mine) != 1 {
		t.Fatalf("expected present just before the window closes, got %+v", before.Mine)
	}

	// Advance the injected clock by 2h: the same PR is now merged 25h ago.
	after := Build(BuilderInput{GeneratedAt: fixedNow.Add(2 * time.Hour), Self: "me", PRs: []PRInput{in}})
	if len(after.Mine) != 0 {
		t.Fatalf("expected the SAME input to drop out once the clock advances past the window, got %+v", after.Mine)
	}
}

// TestBuild_MergedMinePRsSortBelowActive verifies retained merged PRs of mine
// sort BELOW every active (open/draft) Mine row, regardless of input order —
// the merged entry is deliberately listed FIRST in in.PRs to prove the
// ordering is not merely input-order-preserving.
func TestBuild_MergedMinePRsSortBelowActive(t *testing.T) {
	active1 := PRInput{PR: api.PR{Repo: "o/r", Number: 10, Author: "me", State: "open"}, Ownership: ownership.Mine}
	active2 := PRInput{PR: api.PR{Repo: "o/r", Number: 11, Author: "me", State: "open"}, Ownership: ownership.Mine}
	merged := mineMergedInput(9, 1*time.Hour)

	snap := Build(BuilderInput{
		GeneratedAt: fixedNow,
		Self:        "me",
		PRs:         []PRInput{merged, active1, active2}, // merged listed first on purpose
	})

	if len(snap.Mine) != 3 {
		t.Fatalf("expected 3 Mine rows, got %d: %+v", len(snap.Mine), snap.Mine)
	}
	gotOrder := []int{snap.Mine[0].Number, snap.Mine[1].Number, snap.Mine[2].Number}
	wantOrder := []int{10, 11, 9} // both actives, in order, before the merged row
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Fatalf("Mine order = %v, want actives before merged: %v", gotOrder, wantOrder)
		}
	}
	if !snap.Mine[2].Merged {
		t.Errorf("the trailing row must be the Merged one, got %+v", snap.Mine[2])
	}
}

// TestBuild_MergedCoOwnedIsNotRetentionScoped proves the retention window is
// gated on Ownership==Mine specifically, not the broader ActsAsMine()
// (Mine|CoOwned): a hypothetical merged CoOwned PRInput (which refresh.go
// never actually constructs — a merged co-owned/team PR is dropped
// immediately by the caller, per the bead's "team PRs out of scope" ruling)
// is NOT subject to the 24h drop here; Build has no special-case for it and
// simply renders it as an ordinary (non-de-emphasised) Mine row.
func TestBuild_MergedCoOwnedIsNotRetentionScoped(t *testing.T) {
	in := PRInput{
		PR: api.PR{
			Repo: "o/r", Number: 20, Author: "teammate", State: "merged",
			Merged: true, MergedAt: mergedAtAgo(30 * time.Hour), // well past the window
		},
		Ownership: ownership.CoOwned,
	}
	snap := Build(BuilderInput{GeneratedAt: fixedNow, Self: "me", PRs: []PRInput{in}})
	if len(snap.Mine) != 1 || snap.Mine[0].Number != 20 {
		t.Fatalf("a merged CoOwned PR is out of this bead's retention scope and must not be dropped by it, got %+v", snap.Mine)
	}
	if snap.Mine[0].Merged {
		t.Errorf("an out-of-scope merged CoOwned row must not carry the Merged de-emphasis marker, got %+v", snap.Mine[0])
	}
}

// TestBuild_MergedMinePRRetention_WithDependencyPassWired is the pg2-4dz88.3.7
// regression case this bead's task explicitly required: the pg2-ew4kf
// "merged rows sort below every active row" ordering must survive the new
// whole-set PR-dependency pass being wired into the SAME Build call. The
// fixture gives the pass real work to do (a PR genuinely stacked on the
// retained merged PR, resolving via the merged-middle ruling) rather than an
// inert TrunkRefs-only pass, so a mistake that perturbed Mine's ordering via
// the dependency annotation would show up here.
func TestBuild_MergedMinePRRetention_WithDependencyPassWired(t *testing.T) {
	active := PRInput{
		PR:        api.PR{Repo: "o/r", Number: 30, Author: "me", Branch: "feat-active", Base: "main", State: "open"},
		Ownership: ownership.Mine,
	}
	merged := mineMergedInput(31, 1*time.Hour)
	merged.PR.Branch = "feat-merged"
	merged.PR.Base = "main"
	// Stacked on the retained-merged PR: resolves via ResolutionUnblocked
	// (merged-middle), not ResolutionUpstream — #31 has already merged.
	stacked := PRInput{
		PR:        api.PR{Repo: "o/r", Number: 32, Author: "me", Branch: "feat-stacked", Base: "feat-merged", State: "open"},
		Ownership: ownership.Mine,
	}

	// merged listed first, on purpose, mirroring
	// TestBuild_MergedMinePRsSortBelowActive's ordering-is-not-input-order proof.
	snap := Build(BuilderInput{GeneratedAt: fixedNow, Self: "me", PRs: []PRInput{merged, active, stacked}})

	if len(snap.Mine) != 3 {
		t.Fatalf("want 3 mine rows, got %+v", snap.Mine)
	}
	gotOrder := []int{snap.Mine[0].Number, snap.Mine[1].Number, snap.Mine[2].Number}
	wantOrder := []int{30, 32, 31} // both actives before the retained merged row
	for i, want := range wantOrder {
		if gotOrder[i] != want {
			t.Fatalf("Mine order = %v, want actives before merged: %v (pg2-ew4kf ordering must survive the dependency pass)",
				gotOrder, wantOrder)
		}
	}
	if !snap.Mine[2].Merged {
		t.Errorf("the trailing row must be the Merged one, got %+v", snap.Mine[2])
	}

	var stackedRow MineRow
	for _, r := range snap.Mine {
		if r.Number == 32 {
			stackedRow = r
		}
	}
	if stackedRow.DependencyUnblockedFrom != "o/r#31" {
		t.Errorf("stacked row #32 must carry DependencyUnblockedFrom=o/r#31 (merged-middle) even though #31 is a retained-merged row, got %q",
			stackedRow.DependencyUnblockedFrom)
	}
}
