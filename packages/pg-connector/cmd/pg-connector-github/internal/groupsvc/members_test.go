package groupsvc_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// scanCountingAliases is the real store with a count of the alias-by-reference
// reads it served: each AliasByRefFold is one scan of the alias table (the fold
// cannot use the alias_ref index) and each AliasesByRefFold is one scan per
// chunk of 500 references.
type scanCountingAliases struct {
	*store.Store
	scans atomic.Int64
}

func (c *scanCountingAliases) AliasByRefFold(ctx context.Context, ref string) (store.AliasRow, bool, error) {
	c.scans.Add(1)
	return c.Store.AliasByRefFold(ctx, ref)
}

func (c *scanCountingAliases) AliasesByRefFold(ctx context.Context, refs []string) (map[string]store.AliasRow, error) {
	c.scans.Add(int64((len(refs) + 499) / 500))
	return c.Store.AliasesByRefFold(ctx, refs)
}

// Members must not scan the alias table once per query member: with 150 members
// in two overlapping queries that was 200 scans a call (pg2-hjdas), and the
// service calls it on every group call.
func TestMembersReadsTheAliasTableOnceForAllMembers(t *testing.T) {
	ctx := context.Background()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const n = 150
	var refs []string
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("Acme-Org/Widget#%d", i+1)
		if err := st.PutAlias(ctx, store.AliasRow{NodeID: fmt.Sprintf("PR_%03d", i), Ref: ref, SeenAt: time.UnixMilli(1000)}); err != nil {
			t.Fatal(err)
		}
		// Queries spell the reference in lower case: the match is case-folded.
		refs = append(refs, strings.ToLower(ref))
	}
	// Two overlapping queries, a node id member, a reference no alias names and
	// one that is not a reference at all.
	q1 := append(append([]string{}, refs[:100]...), "PR_RAW", "acme-org/unknown#1", "not#a#ref")
	q2 := refs[50:]
	for key, ids := range map[string][]string{"a": q1, "b": q2} {
		if err := st.PutQuery(ctx, store.QueryRow{Key: key, Configured: true, IDs: ids, Complete: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutQuery(ctx, store.QueryRow{Key: "unconfigured", IDs: []string{"acme-org/widget#1", "PR_OTHER"}}); err != nil {
		t.Fatal(err)
	}

	counted := &scanCountingAliases{Store: st}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{Store: st, AliasStore: counted})
	got, err := r.Svc.Members(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != n+1 || !got["PR_RAW"] || got["PR_OTHER"] {
		t.Errorf("members = %d ids (PR_RAW %v, PR_OTHER %v), want the %d aliased PRs plus PR_RAW", len(got), got["PR_RAW"], got["PR_OTHER"], n)
	}
	for i := 0; i < n; i++ {
		if !got[fmt.Sprintf("PR_%03d", i)] {
			t.Fatalf("member PR_%03d missing", i)
		}
	}
	if scans := counted.scans.Load(); scans > 1 {
		t.Errorf("Members scanned the alias table %d times for %d members, want one scan for all of them", scans, n)
	}
}
