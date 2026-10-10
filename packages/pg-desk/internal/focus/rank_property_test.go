package focus

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

// genItems builds n items with small, colliding value domains (so every key
// is tied for many pairs and the later keys get exercised) and distinct
// candidate keys. The fields are drawn independently, even where the rank
// would derive one from another: the order must be a strict weak order over
// ANY values, not just the ones a real ranking produces.
func genItems(rng *rand.Rand, n int) []*item {
	kinds := []Kind{KindPR, KindJira, KindBead}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := make([]*item, n)
	for i := range items {
		kind := kinds[rng.Intn(len(kinds))]
		typ := entityTypeIssue
		if kind == KindPR {
			typ = entityTypePR
		}
		items[i] = &item{
			cand:     Candidate{Key: Key{typ, fmt.Sprintf("id-%03d", i)}, Kind: kind, Seed: rng.Intn(2) == 0},
			tier:     rng.Intn(3),
			started:  rng.Intn(2) == 0,
			hasDue:   rng.Intn(3) > 0,
			due:      calendarDay(rng.Intn(4)),
			dueIn:    rng.Intn(4) - 1,
			horizon:  rng.Intn(2) == 0,
			unblocks: rng.Intn(3),
			prio:     rng.Intn(4),
			age:      base.AddDate(0, 0, rng.Intn(3)),
			ageKnown: rng.Intn(4) > 0,
		}
	}
	return items
}

func TestRankIsTotalAndTransitive(t *testing.T) {
	rng := rand.New(rand.NewSource(20261010))
	for round := 0; round < 20; round++ {
		items := genItems(rng, 24)
		for _, a := range items {
			if less(a, a) {
				t.Fatalf("round %d: %v < itself", round, a.cand.Key)
			}
		}
		for _, a := range items {
			for _, b := range items {
				if a != b && less(a, b) == less(b, a) {
					t.Fatalf("round %d: %v and %v are tied or both ordered; the tiebreak must make the order total", round, a.cand.Key, b.cand.Key)
				}
				for _, c := range items {
					if less(a, b) && less(b, c) && !less(a, c) {
						t.Fatalf("round %d: %v < %v < %v but not %v < %v", round, a.cand.Key, b.cand.Key, c.cand.Key, a.cand.Key, c.cand.Key)
					}
				}
			}
		}
	}

	// A fixed-seed shuffle from a non-canonical start gives an identical order.
	items := genItems(rng, 40)
	want := append([]*item(nil), items...)
	sort.Slice(want, func(i, j int) bool { return less(want[i], want[j]) })
	for seed := int64(1); seed <= 10; seed++ {
		got := append([]*item(nil), items...)
		r := rand.New(rand.NewSource(seed))
		r.Shuffle(len(got), func(i, j int) { got[i], got[j] = got[j], got[i] })
		sort.Slice(got, func(i, j int) bool { return less(got[i], got[j]) })
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: a shuffled input sorted differently", seed)
		}
	}
}

// Two items equal on every key resolve by kind (pr, jira, bead), then by key.
func TestRankTiesResolveByKindAndKey(t *testing.T) {
	mk := func(kind Kind, typ, id string) *item {
		return &item{cand: Candidate{Key: Key{typ, id}, Kind: kind}, tier: 2, prio: 2, ageKnown: true}
	}
	bead := mk(KindBead, "issue", "bd-1")
	jira := mk(KindJira, "issue", "PROJ-9")
	prB := mk(KindPR, "pr", "o/r#2")
	prA := mk(KindPR, "pr", "o/r#10")
	items := []*item{bead, jira, prB, prA}
	sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })
	// "o/r#10" sorts before "o/r#2" as a string: the key order is the string order.
	want := []*item{prA, prB, jira, bead}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("order = %v, want pr o/r#10, pr o/r#2, jira, bead", keysOfItems(items))
	}
	if c, key := compare(prA, prB); c != -1 || key != KeyTiebreak {
		t.Errorf("compare(prA, prB) = %d %q, want -1 tiebreak", c, key)
	}
	if c, key := compare(prB, prA); c != 1 || key != KeyTiebreak {
		t.Errorf("compare(prB, prA) = %d %q, want 1 tiebreak", c, key)
	}
	if c, key := compare(prA, prA); c != 0 || key != "" {
		t.Errorf("compare(prA, prA) = %d %q, want 0 and no key", c, key)
	}
}

func keysOfItems(items []*item) []Key {
	out := make([]Key, len(items))
	for i, it := range items {
		out[i] = it.cand.Key
	}
	return out
}

// An item with no creation time and no first-seen time sorts after every
// dated one, and two undated ones tie on age.
func TestAgeUnknownSortsLast(t *testing.T) {
	mk := func(id string, known bool, day int) *item {
		return &item{
			cand: Candidate{Key: Key{"issue", id}, Kind: KindBead}, tier: 2, prio: 2,
			ageKnown: known, age: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC),
		}
	}
	a, b, c := mk("bd-a", true, 5), mk("bd-b", false, 1), mk("bd-c", true, 9)
	for _, tc := range []struct {
		x, y *item
		want int
		key  string
	}{
		{a, c, -1, KeyAge}, {c, a, 1, KeyAge}, {c, b, -1, KeyAge}, {b, c, 1, KeyAge},
	} {
		if got, key := compare(tc.x, tc.y); got != tc.want || key != tc.key {
			t.Errorf("compare(%v, %v) = %d %q, want %d %q", tc.x.cand.Key, tc.y.cand.Key, got, key, tc.want, tc.key)
		}
	}
	// Whatever stale value an undated item carries, it is not compared.
	d := mk("bd-d", false, 3)
	if got, key := compare(b, d); got != -1 || key != KeyTiebreak {
		t.Errorf("two undated items: %d %q, want the tiebreak", got, key)
	}
}
