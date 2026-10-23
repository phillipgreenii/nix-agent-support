package groupsvc

import (
	"fmt"
	"strings"
	"testing"
)

// Members must not resolve the alias table once per query member: with 150
// members in two overlapping queries a call per member was 200 lookups a call, and
// the service reads the set on every group call. The reference lookup is ONE
// batched Resolve for all of them.
func TestMembersResolvesAllReferencesInOneBatch(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := t.Context()

	const n = 150
	var refs []string
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("Zone-A/Item-B~%d", i+1)
		if err := r.aliases.Put(ctx, fmt.Sprintf("ent-%03d", i), ref); err != nil {
			t.Fatal(err)
		}
		// Queries spell the reference in lower case: the match is case-folded.
		refs = append(refs, strings.ToLower(ref))
	}
	// Two overlapping queries, a store id member, a reference no alias names and
	// one that is not a reference at all.
	q1 := append(append([]string{}, refs[:100]...), "ent-raw", "zone-a/unknown~1", "bad~x~y")
	q2 := refs[50:]
	r.putQuery("a", q1...)
	r.putQuery("b", q2...)
	if err := r.st.PutQuery(ctx, queryRow("unconfigured", "zone-a/item-b~1", "ent-other")); err != nil {
		t.Fatal(err)
	}

	got, err := r.svc.members(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n+1 || !got["ent-raw"] || got["ent-other"] {
		t.Errorf("members = %d ids (ent-raw %v, ent-other %v), want the %d aliased entities plus ent-raw", len(got), got["ent-raw"], got["ent-other"], n)
	}
	for i := 0; i < n; i++ {
		if !got[fmt.Sprintf("ent-%03d", i)] {
			t.Fatalf("member ent-%03d missing", i)
		}
	}
	if calls := r.aliases.resolveCalls(); calls != 1 {
		t.Errorf("members made %d alias lookups for %d members, want one batch for all of them", calls, n)
	}
}

func TestMembersWithNoReferenceMakesNoAliasLookup(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	r.putQuery("a", "ent-1", "ent-2")
	got, err := r.svc.members(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["ent-1"] || !got["ent-2"] {
		t.Errorf("members = %v", got)
	}
	if calls := r.aliases.resolveCalls(); calls != 0 {
		t.Errorf("%d alias lookups for a set of plain ids, want none", calls)
	}
}
