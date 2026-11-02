//go:build equivalence

package apply

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/view"
)

// The opt-in equivalence test of the per-PR lookup (epic plan, bead
// pg2-ch5dm7.12): for every PR of a mixed corpus, the view the rules decide
// from after reading the per-PR answer equals the one after reading the whole
// listing. The per-PR double answers the union of the three expressions of the
// production query (repo+pr_number metadata, focus source id, title contains
// "<repo>#<n>"), the way the tracker does. Run it with
// `go test -tags equivalence ./internal/apply`; it is not in the unit gate.

type corpusBead struct{ id, title, state, md string }

func corpus() []corpusBead {
	var c []corpusBead
	for n := 1000; n < 1006; n++ {
		repo := "acme/api"
		ref := fmt.Sprintf("%s#%d", repo, n)
		key := "pr:" + ref
		c = append(
			c,
			corpusBead{fmt.Sprintf("a%d", n), ref + ": anchor", "open", fmt.Sprintf(`"dedup_key":"%s:anchor","repo":"%s","pr_number":"%d"`, key, repo, n)},
			corpusBead{fmt.Sprintf("f%d", n), "fix-ci: " + ref + " @ abc1234", "closed", fmt.Sprintf(`"dedup_key":"%s:fix-ci:abc1234","repo":"%s","pr_number":%d`, key, repo, n)},
			corpusBead{fmt.Sprintf("l%d", n), ref + ": legacy keyless", "closed", ""},
			corpusBead{fmt.Sprintf("s%d", n), "focus " + ref, "open", fmt.Sprintf(`"dedup_key":"focus:%s:focus-item","source_type":"pr","source_id":"%s"`, ref, ref)},
		)
	}
	c = append(c, corpusBead{"x1", "unrelated bug", "open", `"repo":"acme/api","pr_number":"9999"`})
	return c
}

func perPRAnswer(all []corpusBead, repo string, n int) []corpusBead {
	ref := fmt.Sprintf("%s#%d", repo, n)
	var out []corpusBead
	for _, b := range all {
		e1 := strings.Contains(b.md, fmt.Sprintf(`"repo":"%s"`, repo)) &&
			(strings.Contains(b.md, fmt.Sprintf(`"pr_number":"%d"`, n)) || strings.Contains(b.md, fmt.Sprintf(`"pr_number":%d`, n)))
		e2 := strings.Contains(b.md, `"source_id":"`+ref+`"`)
		e3 := strings.Contains(b.title, ref)
		if e1 || e2 || e3 {
			out = append(out, b)
		}
	}
	return out
}

func entities(bs []corpusBead) string {
	var parts []string
	for _, b := range bs {
		parts = append(parts, ent(b.id, b.title, b.state, b.md))
	}
	return strings.Join(parts, ",")
}

func TestPerPRLookupEqualsTheFilteredFullListingForEveryPR(t *testing.T) {
	all := corpus()
	for n := 1000; n < 1006; n++ {
		v := &view.View{Type: "pr", ID: fmt.Sprintf("acme/api#%d", n), Snapshot: view.PRSnapshot{Repo: "acme/api", Number: n}}
		full := newDouble(t, respRule{Match: "issue list", Stdout: listing(entities(all), "")})
		fl, err := ReadLookup(context.Background(), testEnv(full, lookupCfg()), v)
		if err != nil {
			t.Fatal(err)
		}
		per := newDouble(t, respRule{Match: "issue list", Stdout: listing(entities(perPRAnswer(all, "acme/api", n)), "")})
		pl, err := ReadLookup(context.Background(), testEnv(per, perPRCfg()), v)
		if err != nil {
			t.Fatalf("PR %d: %v", n, err)
		}
		if got, want := pl.Augment(v), fl.Augment(v); !reflect.DeepEqual(got, want) {
			t.Errorf("PR %d: per-PR view differs from the full-listing view:\n got %+v\nwant %+v", n, got.Links, want.Links)
		}
	}
}
