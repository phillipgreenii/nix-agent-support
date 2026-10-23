package groupsvc_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The invented connectors: they share no entity kind, group, stamp or message.
var (
	first = groupsvctest.Names{
		Kind: "alpha", Primary: "core", Timed: "side", Stamped: "stamped", Passive: "passive",
		Stamp: "h1", Stamp2: "b1",
		NoSuch: errors.New("test: no such entity"), Bad: errors.New("test: malformed reference"), Unknown: errors.New("test: unknown entity"),
	}
	second = groupsvctest.Names{
		Kind: "gamma", Primary: "root", Timed: "ledger", Stamped: "snapshot", Passive: "note",
		Stamp: "s-9", Stamp2: "t-9",
		NoSuch: errors.New("test: nothing by that name"), Bad: errors.New("test: bad reference"), Unknown: errors.New("test: not known"),
	}
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// forEachConnector runs a test body against two registrations of the shared
// service that share no entity kind, group or reference: the service must behave
// the same under both (HT-37, INV-HOIST-25).
func forEachConnector(t *testing.T, body func(t *testing.T, r *groupsvctest.Rig)) {
	t.Helper()
	for _, n := range []groupsvctest.Names{first, second} {
		t.Run(n.Kind, func(t *testing.T) { body(t, groupsvctest.New(t, n, openStore(t))) })
	}
}

const ent = "ent-1"

func TestAFreshGroupIsServedFromTheStoreWithNoFetch(t *testing.T) {
	forEachConnector(t, func(t *testing.T, r *groupsvctest.Rig) {
		r.Start()
		r.SeedPrimary(ent, true)
		r.SeedGroup(ent, r.Names.Timed, "cached", true)
		res := r.Ensure(ent, []string{r.Names.Timed}, groupsvc.EnsureOptions{})
		g := groupsvctest.Result(t, res, r.Names.Timed)
		if g.Status != groupsvc.Fresh || g.FromOrigin || string(g.Row.Content) != "cached" {
			t.Fatalf("group = %+v, want the cached row served fresh", g)
		}
		if n := r.Fetch[r.Names.Timed].Calls() + r.PrimaryCalls(); n != 0 {
			t.Errorf("%d fetches for a fresh read, want none", n)
		}
		if got := r.Keep.AccessCalls(); !slices.Equal(got, []string{ent}) {
			t.Errorf("KeepAlive.Access calls = %v, want one for the read entity", got)
		}
	})
}

func TestAStaleGroupIsFetchedOnceAtInteractivePriorityWhoeverAsks(t *testing.T) {
	forEachConnector(t, func(t *testing.T, r *groupsvctest.Rig) {
		r.Start()
		r.SeedPrimary(ent, true)
		r.SeedGroup(ent, r.Names.Timed, "old", false)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res := r.Ensure(ent, []string{r.Names.Timed}, groupsvc.EnsureOptions{})
				if g := groupsvctest.Result(t, res, r.Names.Timed); g.Status != groupsvc.Fresh || string(g.Row.Content) == "old" {
					t.Errorf("group = %+v, want a freshly fetched row", g)
				}
			}()
		}
		wg.Wait()
		f := r.Fetch[r.Names.Timed]
		if f.Calls() != 1 {
			t.Fatalf("%d fetch calls for two concurrent reads, want one shared fetch", f.Calls())
		}
		if tg := f.Targets()[0][0]; tg.ID != ent || tg.Stamp != r.Names.Stamp || tg.Stamp2 != r.Names.Stamp2 || tg.Ref == "" {
			t.Errorf("target = %+v, want the entity with the stamps and reference of its primary content", tg)
		}
	})
}

func TestAStaleRowInsideExpiryIsServedWhenTheFetchFails(t *testing.T) {
	forEachConnector(t, func(t *testing.T, r *groupsvctest.Rig) {
		r.Start()
		r.SeedPrimary(ent, true)
		r.SeedGroup(ent, r.Names.Timed, "old", false)
		boom := errors.New("test: origin down")
		r.Fetch[r.Names.Timed].FailWith(boom)
		g := groupsvctest.Result(t, r.Ensure(ent, []string{r.Names.Timed}, groupsvc.EnsureOptions{}), r.Names.Timed)
		if g.Status != groupsvc.Stale || string(g.Row.Content) != "old" || !errors.Is(g.Err, boom) {
			t.Fatalf("group = %+v, want the old row served stale with the failure", g)
		}
		// A group whose content is past expiry is unavailable.
		r.Clock.Advance(groupsvctest.Expiry + time.Hour)
		r.SeedPrimary(ent, true)
		g = groupsvctest.Result(t, r.Ensure(ent, []string{r.Names.Timed}, groupsvc.EnsureOptions{}), r.Names.Timed)
		if g.Status != groupsvc.Unavailable {
			t.Errorf("status = %v, want unavailable once the stored row is past expiry", g.Status)
		}
	})
}

func TestAFetcherThatSaysTheEntityIsGoneTombstonesIt(t *testing.T) {
	forEachConnector(t, func(t *testing.T, r *groupsvctest.Rig) {
		r.Start()
		r.SeedPrimary(ent, true)
		r.Fetch[r.Names.Timed].SetGone(ent)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// The read that finds the entity gone either answers with the group
		// unavailable or already sees the tombstone, depending on the race with
		// the executor: both end in the sentinel below.
		if res, err := r.Svc.Ensure(ctx, ent, []string{r.Names.Timed}, groupsvc.EnsureOptions{}); err == nil {
			if g := groupsvctest.Result(t, res, r.Names.Timed); g.Status != groupsvc.Unavailable {
				t.Fatalf("group = %+v, want unavailable", g)
			}
		} else if !errors.Is(err, r.Names.NoSuch) {
			t.Fatalf("Ensure = %v", err)
		}
		groupsvctest.Eventually(t, "the tombstone", func() bool {
			e, ok, err := r.Store.GetEntity(context.Background(), r.Names.Kind, ent)
			return err == nil && ok && !e.TombstonedAt.IsZero()
		})
		_, err := r.Svc.Ensure(context.Background(), ent, []string{r.Names.Timed}, groupsvc.EnsureOptions{})
		if !errors.Is(err, r.Names.NoSuch) {
			t.Errorf("Ensure of a tombstoned entity = %v, want the NoSuchEntity sentinel of Config", err)
		}
	})
}

func TestTheSentinelsOfConfigComeBackFromResolve(t *testing.T) {
	forEachConnector(t, func(t *testing.T, r *groupsvctest.Rig) {
		ctx := context.Background()
		if _, _, err := r.Svc.Resolve(ctx, "Zone-A/Item-B~x"); !errors.Is(err, r.Names.Bad) {
			t.Errorf("malformed reference: %v, want BadRef", err)
		}
		if _, _, err := r.Svc.Resolve(ctx, "bad id"); !errors.Is(err, r.Names.Bad) {
			t.Errorf("malformed store id: %v, want BadRef", err)
		}
		if _, _, err := r.Svc.Resolve(ctx, "Zone-A/Item-B~7"); !errors.Is(err, r.Names.NoSuch) {
			t.Errorf("unknown reference: %v, want NoSuchEntity", err)
		}
		r.Resolver.Know("Zone-A/Item-B~7", "ent-7")
		id, ref, err := r.Svc.Resolve(ctx, "zone-a/item-b~7")
		if err != nil || id != "ent-7" || ref != "zone-a/item-b~7" {
			t.Errorf("first sighting = (%q, %q, %v), want the resolver's answer", id, ref, err)
		}
		before := r.Resolver.Calls()
		if id, _, err := r.Svc.Resolve(ctx, "Zone-A/Item-B~7"); err != nil || id != "ent-7" || r.Resolver.Calls() != before {
			t.Errorf("second sighting = (%q, %v) with %d resolver calls, want the alias table's answer", id, err, r.Resolver.Calls()-before)
		}
		if err := r.Svc.QueueGroup(ctx, "Zone-A/Nobody~3", r.Names.Timed, governor.ClassQuery, time.Time{}); !errors.Is(err, r.Names.Unknown) {
			t.Errorf("queueing an unknown reference: %v, want the UnknownEntity sentinel of Config", err)
		}
	})
}

func TestRegisterTakesTheNonPrimaryGroupsOnly(t *testing.T) {
	forEachConnector(t, func(t *testing.T, r *groupsvctest.Rig) {
		for _, name := range []string{r.Names.Primary, "bogus", first.Timed + "x"} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("Register accepted a fetcher for %q", name)
					}
				}()
				r.Svc.Register(groupsvctest.NewFetcher(name))
			}()
		}
		// A group registered again replaces its fetcher.
		r.Start()
		r.SeedPrimary(ent, true)
		r.SeedGroup(ent, r.Names.Passive, "old", false)
		replacement := groupsvctest.NewFetcher(r.Names.Passive)
		r.Svc.Register(replacement)
		r.Ensure(ent, []string{r.Names.Passive}, groupsvc.EnsureOptions{})
		if replacement.Calls() != 1 || r.Fetch[r.Names.Passive].Calls() != 0 {
			t.Errorf("replacement fetched %d times and the first %d times, want 1 and 0", replacement.Calls(), r.Fetch[r.Names.Passive].Calls())
		}
	})
}

// TestRetentionIsTwoFactsPlusTheKeepAliveStub is HT-38's shared half (INV-HOIST-26):
// the shared service itself decides a tombstoned entity is out and a member of a
// configured query is in, and asks KeepAlive.Retain about every other entity,
// booking exactly as the stub answers; it knows no window or closed state.
func TestRetentionIsTwoFactsPlusTheKeepAliveStub(t *testing.T) {
	forEachConnector(t, func(t *testing.T, r *groupsvctest.Rig) {
		const (
			tombstoned = "ent-tomb"
			member     = "ent-member"
			kept       = "ent-kept"
			dropped    = "ent-dropped"
		)
		for _, id := range []string{tombstoned, member, kept, dropped} {
			r.SeedPrimary(id, true)
			r.SeedGroup(id, r.Names.Timed, "old", false)
		}
		r.Tombstone(tombstoned)
		r.PutQuery("mine", member)
		// The stub would retain the tombstoned entity and the member too: the
		// shared facts MUST win without asking.
		r.Keep.Keep(tombstoned, member, kept)

		n, err := r.Svc.Rebuild(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("Rebuild queued %d tasks, want 2 (the member and the retained entity)", n)
		}
		calls := r.Keep.RetainCalls()
		slices.Sort(calls)
		calls = slices.Compact(calls)
		if want := []string{dropped, kept}; !slices.Equal(calls, want) {
			t.Errorf("Retain was asked about %v, want %v (never a tombstoned entity or a member)", calls, want)
		}
		for id, want := range map[string]governor.Class{member: governor.ClassQuery, kept: governor.ClassKeepAlive} {
			if c, queued, _, _ := r.Svc.Inspect(context.Background(), id, r.Names.Timed); !queued || c != want {
				t.Errorf("%s: class %v queued %v, want a class %v task", id, c, queued, want)
			}
		}
		for _, id := range []string{tombstoned, dropped} {
			if _, queued, _, _ := r.Svc.Inspect(context.Background(), id, r.Names.Timed); queued {
				t.Errorf("%s was booked", id)
			}
		}
	})
}
