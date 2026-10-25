package workitem

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/pg-decider/internal/view"
)

// Property tests of the dedup key and of adoption by title shape (docket
// pg2-6mo45.3, packet P4c-2). They use rapid, the property-test library the
// repository's pg-task-focus module also uses, with a FIXED seed: a run is the
// same run on every machine, and a failure reproduces by running the test again.
// A seed given on the command line (-rapid.seed) or by RAPID_SEED still wins.
func init() {
	pinRapid("RAPID_SEED", "rapid.seed", "1")
	pinRapid("RAPID_NOFAILFILE", "rapid.nofailfile", "true")
}

// pinRapid sets a rapid flag's default before the test binary parses its
// command line, unless the environment variable names one: an explicit flag
// or variable overrides it.
func pinRapid(env, name, value string) {
	if os.Getenv(env) != "" {
		return
	}
	if f := flag.Lookup(name); f != nil {
		_ = flag.Set(name, value)
	}
}

// The numbers that make one entity's id a prefix of another's: #1 inside #10
// inside #100.
var prefixNumbers = []int{1, 10, 100}

func numberGen() *rapid.Generator[int] {
	return rapid.OneOf(rapid.SampledFrom(prefixNumbers), rapid.IntRange(1, 2000))
}

func repoGen() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{"acme/api", "acme/web", "other/api", "a/b"})
}

func entityGen() *rapid.Generator[EntityRef] {
	return rapid.Custom(func(t *rapid.T) EntityRef {
		e := EntityRef{
			Type: rapid.SampledFrom([]string{"pr", "issue"}).Draw(t, "type"),
			ID:   fmt.Sprintf("%s#%d", repoGen().Draw(t, "repo"), numberGen().Draw(t, "number")),
		}
		if rapid.Bool().Draw(t, "has node id") {
			e.NodeID = rapid.StringMatching(`PR_[A-Za-z0-9]{6,12}`).Draw(t, "node id")
		}
		return e
	})
}

var allKinds = []Kind{KindAnchor, KindProcessFeedback, KindReviewPR, KindFixCI, KindResolveConflict, KindFocusItem}

func contextGen() *rapid.Generator[Context] {
	hex := func(label string) *rapid.Generator[string] { return rapid.StringMatching(`[0-9a-f]{7,40}`) }
	return rapid.Custom(func(t *rapid.T) Context {
		c := Context{
			HeadSHA: hex("head").Draw(t, "head"),
			// A git ref name never holds a colon, so none is drawn; it may hold a slash.
			Branch:  rapid.StringMatching(`[a-z][a-z0-9/_-]{0,12}`).Draw(t, "branch"),
			Base:    rapid.SampledFrom([]string{"main", "release/1.0", "develop"}).Draw(t, "base"),
			BaseSHA: hex("base").Draw(t, "base sha"),
			Digest:  hex("digest").Draw(t, "digest"),
		}
		if rapid.Bool().Draw(t, "has nonce") {
			c.Nonce = rapid.StringMatching(`r[0-9]{1,3}`).Draw(t, "nonce")
		}
		return c
	})
}

func TestPropertyDedupKeyRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := entityGen().Draw(t, "entity")
		k := rapid.SampledFrom(allKinds).Draw(t, "kind")
		c := contextGen().Draw(t, "context")

		keys := []struct {
			form, key, ident string
		}{{"id", DedupKey(e, k, c), e.ID}}
		if nk, ok := DedupKeyNodeID(e, k, c); ok {
			keys = append(keys, struct{ form, key, ident string }{"node_id", nk, e.NodeID})
		} else if e.NodeID != "" {
			t.Fatalf("an entity with node id %q has no node_id key", e.NodeID)
		}
		for _, kk := range keys {
			typ, ident, gotKind, suffix, ok := ParseKey(kk.key)
			if !ok {
				t.Fatalf("%s form: the key %q does not parse", kk.form, kk.key)
			}
			if typ != e.Type || ident != kk.ident || gotKind != k || suffix != ContextSuffix(k, c) {
				t.Fatalf("%s form: %q parsed to (%q, %q, %q, %q), want (%q, %q, %q, %q)",
					kk.form, kk.key, typ, ident, gotKind, suffix, e.Type, kk.ident, k, ContextSuffix(k, c))
			}
			// Parsing is the inverse of building: the parts put back give the key.
			if back := typ + ":" + ident + ":" + string(gotKind) + suffix; back != kk.key {
				t.Fatalf("%s form: the parts of %q put back give %q", kk.form, kk.key, back)
			}
			if !e.Owns(kk.key) {
				t.Fatalf("%s form: the entity does not own its own key %q", kk.form, kk.key)
			}
		}
	})
}

// A key names its entity by the whole identity segment, so one entity's key is
// never another's, whichever of the two forms either is in; and the comparison
// is symmetric.
func TestPropertyKeyComparisonIsSymmetricAndNeverMatchesAcrossEntities(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := entityGen().Draw(t, "entity a")
		b := entityGen().Draw(t, "entity b")
		ka := rapid.SampledFrom(allKinds).Draw(t, "kind a")
		kb := rapid.SampledFrom(allKinds).Draw(t, "kind b")
		ca := contextGen().Draw(t, "context a")
		cb := contextGen().Draw(t, "context b")

		forms := func(e EntityRef, k Kind, c Context) []string {
			out := []string{DedupKey(e, k, c)}
			if nk, ok := DedupKeyNodeID(e, k, c); ok {
				out = append(out, nk)
			}
			return out
		}
		// The identities an entity answers to, in either form.
		answersTo := func(e EntityRef) map[string]bool {
			m := map[string]bool{e.ID: true}
			if e.NodeID != "" {
				m[e.NodeID] = true
			}
			return m
		}
		// The key's own identity segment is the id or the node_id of the entity it
		// was built from; an entity owns a key when that segment is one of its own.
		for _, formB := range []struct{ ident, key string }{{b.ID, DedupKey(b, kb, cb)}} {
			want := a.Type == b.Type && answersTo(a)[formB.ident]
			if got := a.Owns(formB.key); got != want {
				t.Fatalf("%+v owns %q = %v, want %v", a, formB.key, got, want)
			}
		}
		if nk, ok := DedupKeyNodeID(b, kb, cb); ok {
			want := a.Type == b.Type && answersTo(a)[b.NodeID]
			if got := a.Owns(nk); got != want {
				t.Fatalf("%+v owns %q = %v, want %v", a, nk, got, want)
			}
		}
		// The comparison of two KEYS is symmetric: the entity a key names (type and
		// identity segment, nothing else) owns the other key exactly when the
		// entity the other key names owns it, and that is exactly when the two
		// identities are the same.
		for _, keyA := range forms(a, ka, ca) {
			for _, keyB := range forms(b, kb, cb) {
				ta, ia, kka, sa, _ := ParseKey(keyA)
				tb, ib, kkb, sb, _ := ParseKey(keyB)
				named := func(typ, ident string) EntityRef { return EntityRef{Type: typ, ID: ident} }
				ab, ba := named(ta, ia).Owns(keyB), named(tb, ib).Owns(keyA)
				if ab != ba {
					t.Fatalf("the comparison is not symmetric: the entity of %q owns %q = %v, the entity of %q owns %q = %v", keyA, keyB, ab, keyB, keyA, ba)
				}
				if sameIdentity := ta == tb && ia == ib; ab != sameIdentity {
					t.Fatalf("%q and %q: owned = %v but the identities are equal = %v", keyA, keyB, ab, sameIdentity)
				}
				// Two keys are equal exactly when every part is.
				same := ta == tb && ia == ib && kka == kkb && sa == sb
				if (keyA == keyB) != same {
					t.Fatalf("key equality (%v) and part equality (%v) disagree for %q and %q", keyA == keyB, same, keyA, keyB)
				}
			}
		}
	})
}

// The explicit case the numbers exist for: the keys of #1, #10 and #100 of one
// repository, for every kind, never name each other.
func TestPropertyPrefixNumberedEntitiesNeverMatch(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		repo := repoGen().Draw(t, "repo")
		k := rapid.SampledFrom(allKinds).Draw(t, "kind")
		c := contextGen().Draw(t, "context")
		i := rapid.IntRange(0, len(prefixNumbers)-1).Draw(t, "i")
		j := rapid.IntRange(0, len(prefixNumbers)-1).Draw(t, "j")
		ei := EntityRef{Type: "pr", ID: repo + "#" + strconv.Itoa(prefixNumbers[i])}
		ej := EntityRef{Type: "pr", ID: repo + "#" + strconv.Itoa(prefixNumbers[j])}
		key := DedupKey(ej, k, c)
		if got := ei.Owns(key); got != (i == j) {
			t.Fatalf("%s owns the key %q of %s: %v", ei.ID, key, ej.ID, got)
		}
		if (DedupKey(ei, k, c) == key) != (i == j) {
			t.Fatalf("the keys of %s and %s are equal: %v", ei.ID, ej.ID, i != j)
		}
	})
}

// ---- adoption by title shape --------------------------------------------------

// adoptionTitle is a generated keyless item title and what adoption MUST make of
// it for the viewed PR.
type adoptionTitle struct {
	title string
	// kind is the kind the title names for the viewed PR ("" when none).
	kind Kind
}

// titleGen draws a keyless item title for some PR of some repository and says
// what the viewed PR (repo, number) should do with it, BY CONSTRUCTION: the
// expectation is read off the generated parts, never computed by the code under
// test.
func titleGen(repo string, number int) *rapid.Generator[adoptionTitle] {
	return rapid.Custom(func(t *rapid.T) adoptionTitle {
		r := rapid.SampledFrom([]string{repo, repo, "other/api", "acme/api"}).Draw(t, "title repo")
		// The numbers around the viewed one: itself, the prefix numbers, ten times
		// and a tenth of it, and the next one.
		n := rapid.SampledFrom([]int{number, number, 1, 10, 100, number * 10, number/10 + 1, number + 1}).Draw(t, "title number")
		mine := r == repo && n == number
		ref := r + "#" + strconv.Itoa(n)
		kindPrefix := rapid.SampledFrom([]Kind{KindProcessFeedback, KindReviewPR, KindFixCI, KindResolveConflict, KindAnchor}).Draw(t, "kind")
		switch kindPrefix {
		case KindAnchor:
			// "<repo>#<n>: <title>"
			return adoptionTitle{title: ref + ": " + rapid.StringMatching(`[A-Za-z ]{0,16}`).Draw(t, "anchor text"), kind: pick(mine, KindAnchor)}
		default:
			base := string(kindPrefix) + ": " + ref
			switch rapid.IntRange(0, 2).Draw(t, "shape") {
			case 0:
				return adoptionTitle{title: base, kind: pick(mine, kindPrefix)}
			case 1: // the head suffix
				return adoptionTitle{title: base + " @ " + rapid.StringMatching(`[0-9a-f]{7}`).Draw(t, "short head"), kind: pick(mine, kindPrefix)}
			default: // anything else after the reference names no kind of the PR
				return adoptionTitle{title: base + rapid.SampledFrom([]string{"-0", " extra", "x"}).Draw(t, "garbage")}
			}
		}
	})
}

func pick(mine bool, k Kind) Kind {
	if mine {
		return k
	}
	return ""
}

func TestPropertyAdoptionByTitleShapeNeverAdoptsIntoAnotherEntity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		repo := repoGen().Draw(t, "view repo")
		number := numberGen().Draw(t, "view number")
		titles := rapid.SliceOfN(titleGen(repo, number), 1, 8).Draw(t, "titles")

		v := &view.View{Type: "pr", ID: fmt.Sprintf("%s#%d", repo, number)}
		v.Snapshot.Repo, v.Snapshot.Number = repo, number
		want := map[string]Kind{}
		for i, tt := range titles {
			id := "wb-" + strconv.Itoa(i)
			v.Links = append(v.Links, view.Link{Type: "issue", ID: id, Title: tt.title, State: "open"})
			if tt.kind != "" {
				want[id] = tt.kind
			}
		}
		got := map[string]Kind{}
		for _, ad := range BuildIndex(v).Adoptable() {
			got[ad.Item.ID] = ad.Kind
		}
		if !sameKinds(got, want) {
			t.Fatalf("view %s: adopted %v, want %v\ntitles: %s", v.ID, describe(got), describe(want), describeTitles(titles))
		}
	})
}

// A keyless item that names another node id is never adopted, whatever its
// title says.
func TestPropertyAnotherNodesItemIsNeverAdopted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		repo := repoGen().Draw(t, "view repo")
		number := numberGen().Draw(t, "view number")
		titles := rapid.SliceOfN(titleGen(repo, number), 1, 6).Draw(t, "titles")
		v := &view.View{Type: "pr", ID: fmt.Sprintf("%s#%d", repo, number)}
		v.Snapshot.Repo, v.Snapshot.Number, v.Snapshot.NodeID = repo, number, "PR_mine"
		for i, tt := range titles {
			v.Links = append(v.Links, view.Link{
				Type: "issue", ID: "wb-" + strconv.Itoa(i), Title: tt.title, State: "open",
				Metadata: map[string]string{"node_id": "PR_other"},
			})
		}
		if got := BuildIndex(v).Adoptable(); len(got) != 0 {
			t.Fatalf("adopted %d items that carry another PR's node id: %+v", len(got), got)
		}
	})
}

func sameKinds(a, b map[string]Kind) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func describe(m map[string]Kind) string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+string(v))
	}
	sort.Strings(out)
	return "[" + strings.Join(out, " ") + "]"
}

func describeTitles(ts []adoptionTitle) string {
	var out []string
	for i, tt := range ts {
		out = append(out, fmt.Sprintf("wb-%d %q (want kind %q)", i, tt.title, tt.kind))
	}
	return strings.Join(out, "; ")
}

// A plain negative case: the comparison the properties above guard against, a
// prefix match on the text of the key, does match across entities (#1 inside
// #10), so the explicit #1/#10/#100 property fails for it and passes for Owns.
func TestPropertyNegativeAPrefixMatchWouldCrossEntities(t *testing.T) {
	short := EntityRef{Type: "pr", ID: "acme/api#1"}
	long := EntityRef{Type: "pr", ID: "acme/api#10"}
	key := DedupKey(long, KindAnchor, Context{})
	if !strings.HasPrefix(key, short.Type+":"+short.ID) {
		t.Fatalf("setup: %q does not start with %q, so the negative case proves nothing", key, short.Type+":"+short.ID)
	}
	if short.Owns(key) {
		t.Errorf("%s owns the key %q of %s", short.ID, key, long.ID)
	}
}
