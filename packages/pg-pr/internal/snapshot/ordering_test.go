package snapshot

import (
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// teamRowWithReasons builds a minimal TeamRow identified by number, carrying
// exactly the given MatchReason set -- the shape reviewerRoleTier reads.
func teamRowWithReasons(number int, reasons ...string) TeamRow {
	return TeamRow{Repo: "o/r", Number: number, MatchReason: reasons}
}

// ----------------------------------------------------------------------
// Key 1: reviewer-role tier -- all 10 pairwise combinations of the 5 rungs,
// plus one case each for keys 2-4.
// ----------------------------------------------------------------------

// TestCompareTeamRows_TieBreakLadder covers, table-driven, every rung/key this
// bead's amended sort sequence defines. Key 1's five rungs are compared
// pairwise -- C(5,2) = 10 combinations, not the parent design field's stale
// six (that literal example predates the 2026-08-24 already-engaged rung; see
// this bead's own corrected description) -- and keys 2-4 each get one case,
// holding every HIGHER key equal.
func TestCompareTeamRows_TieBreakLadder(t *testing.T) {
	t.Run("key1: reviewer-role tier, all 10 pairwise rung combinations", func(t *testing.T) {
		rungs := []struct {
			name    string
			reasons []string
		}{
			{"already-engaged", []string{MatchReasonReviewedByMe}},
			{"requested-reviewer", []string{MatchReasonReviewRequested}},
			{"codeowners-required", []string{MatchReasonCodeownersRequired}},
			{"watch-label-only", []string{MatchReasonLabelPrefix + "watch-a"}},
			{"rest", []string{MatchReasonTeamAuthored}},
		}
		for hi := 0; hi < len(rungs); hi++ {
			for lo := hi + 1; lo < len(rungs); lo++ {
				name := rungs[hi].name + " before " + rungs[lo].name
				t.Run(name, func(t *testing.T) {
					better := teamRowWithReasons(1, rungs[hi].reasons...)
					worse := teamRowWithReasons(2, rungs[lo].reasons...)
					if got := CompareTeamRows(better, worse); got >= 0 {
						t.Errorf("CompareTeamRows(%s, %s) = %d, want < 0", rungs[hi].name, rungs[lo].name, got)
					}
					if got := CompareTeamRows(worse, better); got <= 0 {
						t.Errorf("CompareTeamRows(%s, %s) = %d, want > 0", rungs[lo].name, rungs[hi].name, got)
					}
				})
			}
		}
	})

	// already-engaged fires on EITHER reviewed-by-me OR assigned-to-me,
	// regardless of owner/labels riding alongside -- a row carrying both a
	// lower-rung label reason AND an already-engaged reason must still rank
	// as already-engaged.
	t.Run("key1: already-engaged wins even alongside a lower-rung reason", func(t *testing.T) {
		engaged := teamRowWithReasons(1, MatchReasonAssignedToMe, MatchReasonLabelPrefix+"watch-a")
		labelOnly := teamRowWithReasons(2, MatchReasonLabelPrefix+"watch-a")
		if got := CompareTeamRows(engaged, labelOnly); got >= 0 {
			t.Errorf("CompareTeamRows(engaged+label, label-only) = %d, want < 0", got)
		}
	})

	t.Run("key2: stale-review-of-mine before never-reviewed, key1 held equal", func(t *testing.T) {
		stale := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, AttentionReason: AttentionReasonReReview}
		never := TeamRow{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonTeamAuthored}, AttentionReason: AttentionReasonUnreviewed}
		if got := CompareTeamRows(stale, never); got >= 0 {
			t.Errorf("CompareTeamRows(stale-review, never-reviewed) = %d, want < 0", got)
		}
	})

	t.Run("key3: upstream-of-another-PR (or standalone) before waiting-on-another-PR, keys 1-2 held equal", func(t *testing.T) {
		upstreamOrStandalone := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, DependencyOrderingKey: 0}
		waiting := TeamRow{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonTeamAuthored}, DependencyOrderingKey: -1}
		if got := CompareTeamRows(upstreamOrStandalone, waiting); got >= 0 {
			t.Errorf("CompareTeamRows(not-blocked, blocked) = %d, want < 0", got)
		}
	})

	t.Run("key4: smaller first, keys 1-3 held equal", func(t *testing.T) {
		small := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 10}
		big := TeamRow{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 1000}
		if got := CompareTeamRows(small, big); got >= 0 {
			t.Errorf("CompareTeamRows(small, big) = %d, want < 0", got)
		}
	})
}

// ----------------------------------------------------------------------
// TestCompareTeamRows_HigherKeyWins: for each ADJACENT key pair, the lower
// key would invert the order on its own, but the higher key must override.
// ----------------------------------------------------------------------

func TestCompareTeamRows_HigherKeyWins(t *testing.T) {
	t.Run("key1 overrides key2", func(t *testing.T) {
		// betterTier has the worse (higher-numbered) staleness rank; worseTier
		// has the better staleness rank. On key2 alone, worseTier would sort
		// first -- key1 must override that.
		betterTier := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonReviewedByMe}, AttentionReason: ""}
		worseTier := TeamRow{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonTeamAuthored}, AttentionReason: AttentionReasonReReview}
		if got := CompareTeamRows(betterTier, worseTier); got >= 0 {
			t.Fatalf("CompareTeamRows(better-tier-worse-staleness, worse-tier-better-staleness) = %d, want < 0 (key1 must win)", got)
		}
	})

	t.Run("key2 overrides key3", func(t *testing.T) {
		// Both rows share key1. betterStale has the worse dependency key;
		// worseStale has the better one. On key3 alone, worseStale sorts
		// first -- key2 must override that.
		betterStale := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, AttentionReason: AttentionReasonReReview, DependencyOrderingKey: -1}
		worseStale := TeamRow{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonTeamAuthored}, AttentionReason: AttentionReasonUnreviewed, DependencyOrderingKey: 0}
		if got := CompareTeamRows(betterStale, worseStale); got >= 0 {
			t.Fatalf("CompareTeamRows(better-stale-worse-dep, worse-stale-better-dep) = %d, want < 0 (key2 must win)", got)
		}
	})

	t.Run("key3 overrides key4", func(t *testing.T) {
		// Both rows share key1 and key2. betterDep has the worse (larger)
		// size; worseDep has the smaller size. On key4 alone, worseDep sorts
		// first -- key3 must override that.
		betterDep := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, DependencyOrderingKey: 0, LinesChanged: 1000}
		worseDep := TeamRow{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonTeamAuthored}, DependencyOrderingKey: -1, LinesChanged: 1}
		if got := CompareTeamRows(betterDep, worseDep); got >= 0 {
			t.Fatalf("CompareTeamRows(better-dep-bigger, worse-dep-smaller) = %d, want < 0 (key3 must win)", got)
		}
	})
}

// ----------------------------------------------------------------------
// TestCompareTeamRows_TotalOrder: antisymmetry, transitivity, determinism.
// ----------------------------------------------------------------------

// orderingCorpus is the SHARED fixture for the total-order property test and
// (via builder_test.go's TestBuild_TeamRowsAreComparatorSorted) the
// Build-is-sorted acceptance test, so the two cannot drift apart (parent
// design section 6). At least 12 rows, spanning tier x stale x dependency x
// size combinations, plus deliberate duplicates and the zero-value row.
func orderingCorpus() []TeamRow {
	return []TeamRow{
		{}, // zero value: must not panic, must sort somewhere definite
		{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonReviewedByMe}, AttentionReason: AttentionReasonReReview, DependencyOrderingKey: 0, LinesChanged: 5},
		{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonAssignedToMe}, AttentionReason: AttentionReasonUnreviewed, DependencyOrderingKey: -1, LinesChanged: 500},
		{Repo: "o/r", Number: 3, MatchReason: []string{MatchReasonReviewRequested}, DependencyOrderingKey: 0, LinesChanged: 10},
		{Repo: "o/r", Number: 4, MatchReason: []string{MatchReasonReviewRequested}, AttentionReason: AttentionReasonReReview, DependencyOrderingKey: -2, LinesChanged: 3},
		{Repo: "o/r", Number: 5, MatchReason: []string{MatchReasonCodeownersRequired}, LinesChanged: 20},
		{Repo: "o/r", Number: 6, MatchReason: []string{MatchReasonLabelPrefix + "watch-a"}, DependencyOrderingKey: -1, LinesChanged: 15},
		{Repo: "o/r", Number: 7, MatchReason: []string{MatchReasonLabelPrefix + "watch-b"}, LinesChanged: 15}, // duplicate tier+size of #6, distinct identity
		{Repo: "o/r", Number: 8, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 1000},
		{Repo: "o/r", Number: 9, MatchReason: []string{MatchReasonTeamAuthored}, AttentionReason: AttentionReasonUnreviewed, LinesChanged: 1},
		{Repo: "o/r2", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 1000}, // duplicate of #8 on every key but repo
		{Repo: "o/r", Number: 10, LinesChanged: 0}, // rest tier, empty MatchReason
		{Repo: "o/r", Number: 11, MatchReason: []string{MatchReasonReviewedByMe, MatchReasonReviewRequested}, DependencyOrderingKey: 3, LinesChanged: 50},
		{Repo: "o/r", Number: 12, MatchReason: []string{MatchReasonCodeownersRequired}, AttentionReason: AttentionReasonReReview, DependencyOrderingKey: -5, LinesChanged: 999},
	}
}

// sign returns -1/0/1 for negative/zero/positive n.
func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

func TestCompareTeamRows_TotalOrder(t *testing.T) {
	corpus := orderingCorpus()
	if len(corpus) < 12 {
		t.Fatalf("fixture too small to prove a total order: got %d rows, want >= 12", len(corpus))
	}

	// Antisymmetry: sign(C(a,b)) == -sign(C(b,a)) for every pair.
	for i := range corpus {
		for j := range corpus {
			got := sign(CompareTeamRows(corpus[i], corpus[j]))
			want := -sign(CompareTeamRows(corpus[j], corpus[i]))
			if got != want {
				t.Fatalf("antisymmetry violated for pair (%d,%d): sign(C(a,b))=%d, -sign(C(b,a))=%d", i, j, got, want)
			}
		}
	}

	// Transitivity: C(i,j) <= 0 && C(j,k) <= 0 implies C(i,k) <= 0.
	for i := range corpus {
		for j := range corpus {
			if CompareTeamRows(corpus[i], corpus[j]) > 0 {
				continue
			}
			for k := range corpus {
				if CompareTeamRows(corpus[j], corpus[k]) > 0 {
					continue
				}
				if CompareTeamRows(corpus[i], corpus[k]) > 0 {
					t.Fatalf("transitivity violated for triple (%d,%d,%d): C(i,j)<=0, C(j,k)<=0, but C(i,k)>0", i, j, k)
				}
			}
		}
	}

	// Determinism: sorting the corpus and its REVERSE yields the identical
	// sequence -- no tie is silently resolved by input order.
	forward := slices.Clone(corpus)
	slices.SortStableFunc(forward, CompareTeamRows)

	reversed := slices.Clone(corpus)
	slices.Reverse(reversed)
	slices.SortStableFunc(reversed, CompareTeamRows)
	if !reflect.DeepEqual(forward, reversed) {
		t.Fatalf("sorting the corpus and its reverse gave different sequences:\n forward %+v\n reversed %+v", forward, reversed)
	}

	// Determinism: sorting repeated fixed-seed shuffles of the corpus always
	// yields the same sequence as the forward sort above.
	rnd := rand.New(rand.NewSource(1))
	for attempt := 0; attempt < 5; attempt++ {
		shuffled := slices.Clone(corpus)
		rnd.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		slices.SortStableFunc(shuffled, CompareTeamRows)
		if !reflect.DeepEqual(forward, shuffled) {
			t.Fatalf("attempt %d: a shuffled sort differs from the forward sort:\n forward  %+v\n shuffled %+v", attempt, forward, shuffled)
		}
	}

	// The zero-value row must not have panicked getting here, and must sort
	// somewhere definite (found exactly once).
	zeroCount := 0
	for _, r := range forward {
		if reflect.DeepEqual(r, TeamRow{}) {
			zeroCount++
		}
	}
	if zeroCount != 1 {
		t.Fatalf("expected the zero-value row to appear exactly once in the sorted output, found %d", zeroCount)
	}
}

// TestCompareTeamRows_FinalKeyIsTotal proves the final key (repo+number) is
// injective: two rows equal on every product key (tier, staleness, dependency
// key, size) but differing only by identity must still receive a definite,
// nonzero comparison -- no two distinct rows may tie on every key.
func TestCompareTeamRows_FinalKeyIsTotal(t *testing.T) {
	a := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 10}
	b := TeamRow{Repo: "o/r", Number: 2, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 10}
	if got := CompareTeamRows(a, b); got == 0 {
		t.Fatalf("CompareTeamRows(a, b) = 0 for two distinct rows equal on every product key; the final key must break the tie")
	}
	if got := CompareTeamRows(a, b); got >= 0 {
		t.Errorf("CompareTeamRows(a, b) = %d, want < 0 (Number 1 before Number 2 in the same repo)", got)
	}

	// Same product keys, different repo -- the final key must still decide.
	c := TeamRow{Repo: "o/r", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 10}
	d := TeamRow{Repo: "z/z", Number: 1, MatchReason: []string{MatchReasonTeamAuthored}, LinesChanged: 10}
	if got := CompareTeamRows(c, d); got == 0 {
		t.Fatalf("CompareTeamRows(c, d) = 0 for two distinct rows in different repos equal on every other key")
	}
}

// ----------------------------------------------------------------------
// TestMineOrderIsRetentionPartitionOnly
// ----------------------------------------------------------------------

// TestMineOrderIsRetentionPartitionOnly asserts explicitly that Mine's ONLY
// ordering rule is the pg2-ew4kf retention partition (every active row, in
// the order Build received them, then every retained-merged row) -- no
// CompareMineRows exists in this package, and none is needed: no ruling
// requires Mine ordering beyond the retention partition (this bead's own
// description). Feeding active Mine PRs in an order a CompareTeamRows-shaped
// comparator would visibly reorder (biggest first) and asserting Build
// preserves that exact input order proves there is no second, finer-grained
// sort quietly applied to Mine -- an absent decision must not be
// indistinguishable from a forgotten one.
func TestMineOrderIsRetentionPartitionOnly(t *testing.T) {
	big := PRInput{PR: api.PR{Repo: "o/r", Number: 1, Author: "alice", Additions: 900, Deletions: 100}, Ownership: ownership.Mine}
	small := PRInput{PR: api.PR{Repo: "o/r", Number: 2, Author: "alice", Additions: 1, Deletions: 1}, Ownership: ownership.Mine}
	mid := PRInput{PR: api.PR{Repo: "o/r", Number: 3, Author: "alice", Additions: 50, Deletions: 0}, Ownership: ownership.Mine}
	merged := mineMergedInput(4, 1*time.Hour)

	// Deliberately fed big -> small -> mid -> merged: if Mine were sorted by
	// size (as CompareTeamRows would for a Team row), the actives would come
	// back small, mid, big -- not the input order.
	snap := Build(BuilderInput{GeneratedAt: fixedNow, Self: "alice", PRs: []PRInput{big, small, mid, merged}})

	if len(snap.Mine) != 4 {
		t.Fatalf("want 4 Mine rows, got %+v", snap.Mine)
	}
	got := []int{snap.Mine[0].Number, snap.Mine[1].Number, snap.Mine[2].Number, snap.Mine[3].Number}
	want := []int{1, 2, 3, 4} // input order preserved among actives; merged row last
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Mine order = %v, want input order preserved with the merged row last (retention-partition-only): %v", got, want)
		}
	}
	if !snap.Mine[3].Merged {
		t.Fatalf("expected the trailing row to be the retained-merged one, got %+v", snap.Mine[3])
	}
}
