package groupsvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

func TestEnsureOfFreshGroupsMakesNoOriginCallAndReportsZeroStaleness(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		obs := x.observe()
		x.seedPrim(ent1, true)
		x.seedGroup(freshGroup(x.rig, ent1, groupSide, "d"))
		x.seedGroup(withStamps(freshGroup(x.rig, ent1, groupStamped, "c"), stampA1, stampA2))

		res := x.ensure(ent1, []string{x.primary(), groupSide, groupStamped}, EnsureOptions{})
		if x.prim.callCount() != 0 || x.fetchTotal() != 0 {
			t.Fatalf("primary calls %d, fetcher calls %d, want none", x.prim.callCount(), x.fetchTotal())
		}
		if len(res.Groups) != 3 {
			t.Fatalf("Groups = %+v, want one per wanted group, the primary included", res.Groups)
		}
		for _, g := range res.Groups {
			if g.Status != Fresh || g.FromOrigin || g.Pending || g.Err != nil {
				t.Errorf("%s = %+v, want fresh from the store", g.Group, g)
			}
			if got := obs.servedOf(g.Group); len(got) != 1 || got[0] != 0 {
				t.Errorf("Served(%s) = %v, want one call with staleness 0", g.Group, got)
			}
		}
		if res.Primary.Ref != ref1 || res.Primary.Stamp != stampA1 || res.Primary.Stamp2 != stampA2 {
			t.Errorf("Primary = %+v", res.Primary)
		}
		if want := ref1 + "|" + stampA1 + "|" + stampA2; string(res.Content) != want {
			t.Errorf("Content = %q, want the stored primary content %q", res.Content, want)
		}
		if e := x.entity(ent1); e.LastAccess.IsZero() {
			t.Error("the access was not recorded")
		}
	})
}

func TestEnsureFetchesOnlyTheStaleGroupAtInteractivePriority(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		x.seedGroup(withStamps(freshGroup(x.rig, ent1, groupStamped, "c"), stampA1, stampA2))

		res := x.ensure(ent1, []string{groupSide, groupStamped}, EnsureOptions{})
		side, stamped := x.fetch[groupSide], x.fetch[groupStamped]
		if side.callCount() != 1 || stamped.callCount() != 0 || x.prim.callCount() != 0 {
			t.Fatalf("side %d, stamped %d, primary %d calls; want 1, 0, 0", side.callCount(), stamped.callCount(), x.prim.callCount())
		}
		c := side.callList()[0].Call
		if c.Class != governor.ClassInteractive || c.Purpose != governor.PurposeInteractive || c.Group != groupSide || c.BatchSize != 1 {
			t.Errorf("call = %+v, want an interactive side call of one target", c)
		}
		d := groupResult(t, res, groupSide)
		if d.Status != Fresh || !d.FromOrigin || string(d.Row.Content) == "old" {
			t.Errorf("side = %+v, want fresh from origin", d)
		}
		if s := groupResult(t, res, groupStamped); s.Status != Fresh || s.FromOrigin {
			t.Errorf("stamped = %+v, want fresh from the store", s)
		}
		stored, _ := x.group(ent1, groupSide)
		if string(stored.Content) != string(d.Row.Content) || stored.Generation != 2 {
			t.Errorf("stored side = %+v", stored)
		}
	})
}

func TestTwoConcurrentEnsuresOfOneStaleGroupShareOneFetch(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		f := x.fetch[groupSide]
		release := f.block()
		t.Cleanup(release)

		done := make(chan EnsureResult, 2)
		run := func() {
			res, err := x.svc.Ensure(context.Background(), ent1, []string{groupSide}, EnsureOptions{})
			if err != nil {
				t.Error(err)
			}
			done <- res
		}
		go run()
		if !f.waitStarted(1, 10*time.Second) {
			t.Fatal("the fetch never started")
		}
		go run()
		// The second caller joins the running flight (or finds the row fresh after it).
		time.Sleep(50 * time.Millisecond)
		release()
		for i := 0; i < 2; i++ {
			if res := <-done; groupResult(t, res, groupSide).Status != Fresh {
				t.Errorf("result %d = %+v", i, res)
			}
		}
		if n := f.callCount(); n != 1 {
			t.Fatalf("fetcher called %d times, want 1", n)
		}
	})
}

func TestSeveralStaleGroupsAreFetchedInParallel(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		groups := []string{groupSide, groupStamped, groupPassive}
		for _, g := range groups {
			x.seedGroup(staleGroup(x.rig, ent1, g, "old"))
		}
		releases := make([]func(), 0, len(groups))
		for _, g := range groups {
			r := x.fetch[g].block()
			t.Cleanup(r)
			releases = append(releases, r)
		}

		done := make(chan EnsureResult, 1)
		go func() {
			res, err := x.svc.Ensure(context.Background(), ent1, groups, EnsureOptions{})
			if err != nil {
				t.Error(err)
			}
			done <- res
		}()
		// Every fetch is blocked inside Fetch: a serial engine would start only the
		// first of them.
		for _, g := range groups {
			if !x.fetch[g].waitStarted(1, 10*time.Second) {
				t.Fatalf("the %s fetch did not start while the others were running: the engine is serial", g)
			}
		}
		for _, r := range releases {
			r()
		}
		res := <-done
		for _, g := range res.Groups {
			if g.Status != Fresh || !g.FromOrigin {
				t.Errorf("%s = %+v", g.Group, g)
			}
		}
	})
}

func TestAStampedRowFetchedForOlderStampsIsStaleInsideAnyTimeWindow(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		row := withStamps(freshGroup(x.rig, ent1, groupStamped, "c"), "h0", stampA2)
		row.FreshUntil = x.clk.Now().Add(24 * time.Hour)
		x.seedGroup(row)

		f := x.fetch[groupStamped]
		res := x.ensure(ent1, []string{groupStamped}, EnsureOptions{})
		if f.callCount() != 1 {
			t.Fatalf("fetcher called %d times, want 1 for a row of other stamps", f.callCount())
		}
		if got := f.callList()[0].Targets[0].Stamp; got != stampA1 {
			t.Errorf("target stamp = %q, want the primary content's %q", got, stampA1)
		}
		g := groupResult(t, res, groupStamped)
		if head, _ := g.Row.Stamps(); g.Status != Fresh || head != stampA1 {
			t.Errorf("stamped = %+v", g)
		}
		// A row for the current stamps is served from the store.
		x.ensure(ent1, []string{groupStamped}, EnsureOptions{})
		if f.callCount() != 1 {
			t.Error("a stamped row for the current stamps was fetched again")
		}
	})
}

func TestFreshTrueForcesEveryWantedGroupIncludingThePrimary(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(freshGroup(x.rig, ent1, groupSide, "d"))
		x.seedGroup(withStamps(freshGroup(x.rig, ent1, groupStamped, "c"), stampA1, stampA2))

		res := x.ensure(ent1, []string{x.primary(), groupSide, groupStamped}, EnsureOptions{Fresh: true})
		if x.prim.callCount() != 1 || x.fetch[groupSide].callCount() != 1 || x.fetch[groupStamped].callCount() != 1 {
			t.Fatalf("primary %d, side %d, stamped %d calls; want 1 each",
				x.prim.callCount(), x.fetch[groupSide].callCount(), x.fetch[groupStamped].callCount())
		}
		if c := x.prim.callClasses()[0]; c != governor.ClassInteractive {
			t.Errorf("primary task class = %v, want interactive", c)
		}
		for _, g := range res.Groups {
			if g.Status != Fresh || !g.FromOrigin {
				t.Errorf("%s = %+v, want fresh from origin", g.Group, g)
			}
		}
	})
}

func TestAWantOfThePrimaryAloneListsItWithItsStatusAndOrigin(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		res := x.ensure(ent1, []string{x.primary()}, EnsureOptions{})
		if len(res.Groups) != 1 || res.Groups[0].Group != x.primary() || res.Groups[0].Status != Fresh || !res.Groups[0].FromOrigin {
			t.Fatalf("first read = %+v, want the primary fresh from origin", res.Groups)
		}
		if len(res.Content) == 0 || res.Primary.Ref != ref1 {
			t.Errorf("first read answered Primary %+v, Content %q", res.Primary, res.Content)
		}
		res = x.ensure(ent1, []string{x.primary()}, EnsureOptions{})
		if g := res.Groups[0]; g.Status != Fresh || g.FromOrigin || x.prim.callCount() != 1 {
			t.Fatalf("second read = %+v after %d primary calls, want it served from the store", g, x.prim.callCount())
		}
	})
}

func TestThePrimaryComesFirstAndLaterGroupsUseItsRefreshedStamps(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		// First sighting: no primary row at all.
		res := x.ensure(ent1, []string{groupSide, groupStamped}, EnsureOptions{})
		if x.prim.callCount() != 1 {
			t.Fatalf("primary calls = %d, want the one fetch", x.prim.callCount())
		}
		for _, g := range []string{groupSide, groupStamped} {
			c := x.fetch[g].callList()
			if len(c) != 1 || c[0].Targets[0].Stamp != stampA1 || c[0].Targets[0].Stamp2 != stampA2 || c[0].Targets[0].Ref != ref1 {
				t.Errorf("%s calls = %+v, want one built from the refreshed primary content", g, c)
			}
			if got := c[0].Targets[0].Attrs["label"]; got != "lbl-"+ref1 {
				t.Errorf("%s target attrs = %v, want the primary's opaque attributes", g, c[0].Targets[0].Attrs)
			}
			if !groupResult(t, res, g).FromOrigin {
				t.Errorf("%s not from origin", g)
			}
		}
		if res.Primary.Stamp != stampA1 {
			t.Errorf("Primary = %+v", res.Primary)
		}

		// The stamps move at the origin; Fresh makes every later group use the new ones.
		x.prim.setStamps(stampB1, stampB2)
		res = x.ensure(ent1, []string{groupSide, groupStamped}, EnsureOptions{Fresh: true})
		for _, g := range []string{groupSide, groupStamped} {
			c := x.fetch[g].callList()
			if got := c[len(c)-1].Targets[0].Stamp; got != stampB1 {
				t.Errorf("%s fetched for stamp %q, want %q", g, got, stampB1)
			}
		}
		if res.Primary.Stamp != stampB1 {
			t.Errorf("Primary = %+v", res.Primary)
		}
	})
}

// A primary row this very call fetched is current for the call, however late its
// answer arrives. The hook moves the clock past the primary TTL right after the
// row is stored, as a host too loaded to run the caller for a while would: the
// check once judged the row's time window a second time and every dependent
// group came back unavailable.
func TestAPrimaryFetchedByTheCallIsCurrentEvenIfTheClockPassesItsTTLBeforeTheAnswerIsUsed(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.prim.setAfter(func() { x.clk.Advance(x.prim.ttl + time.Second) })

		res := x.ensure(ent1, []string{x.primary(), groupStamped, groupSide}, EnsureOptions{})
		for _, g := range res.Groups {
			if g.Status != Fresh || !g.FromOrigin || g.Err != nil {
				t.Errorf("%s = %+v, want fresh from origin: the primary it was judged against was this call's own", g.Group, g)
			}
		}
		if x.fetch[groupStamped].callCount() != 1 || x.fetch[groupSide].callCount() != 1 {
			t.Errorf("stamped fetched %d times and side %d, want once each", x.fetch[groupStamped].callCount(), x.fetch[groupSide].callCount())
		}
	})
}

func TestAnUnknownReferenceIsResolvedFirstThenThePrimaryIsFetched(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		const wire, id = "Zone-A/Item-B~9", "ent-9"
		x.resolver.known["zone-a/item-b~9"] = id

		res := x.ensure("ZONE-A/ITEM-B~9", []string{x.primary(), groupSide}, EnsureOptions{})
		if got := x.resolver.callList(); len(got) != 1 || len(got[0]) != 1 {
			t.Fatalf("resolver calls = %v, want exactly one", got)
		}
		if x.prim.callCount() != 1 || x.fetch[groupSide].callCount() != 1 {
			t.Fatalf("primary %d, side %d calls", x.prim.callCount(), x.fetch[groupSide].callCount())
		}
		if got := x.fetch[groupSide].callList()[0].Targets[0].ID; got != id {
			t.Errorf("fetched for %q, want the resolved id %q", got, id)
		}
		if res.Primary.Ref != ref1 {
			t.Errorf("answered for %+v", res.Primary)
		}
		// Known now: the same reference makes no second resolver call.
		x.ensure(wire, []string{x.primary()}, EnsureOptions{})
		if len(x.resolver.callList()) != 1 {
			t.Errorf("resolver called again for a known reference")
		}
	})
}

func TestANonexistentEntityAndAMalformedIDHaveTheirSentinels(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		ctx := context.Background()
		primary := []string{x.primary()}
		if _, err := x.svc.Ensure(ctx, "Zone-A/Item-B~404", primary, EnsureOptions{}); !errors.Is(err, errTestNoSuch) {
			t.Errorf("nonexistent entity: err = %v, want the NoSuchEntity sentinel", err)
		}
		if x.prim.callCount() != 0 {
			t.Errorf("an entity that does not exist reached the primary fetch")
		}
		for _, id := range []string{"", "Zone-A/Item-B~x", "not an id", "Zone-A/Item-B"} {
			if _, err := x.svc.Ensure(ctx, id, primary, EnsureOptions{}); !errors.Is(err, errTestBadRef) {
				t.Errorf("Ensure(%q) err = %v, want the BadRef sentinel", id, err)
			}
		}
		if _, _, err := x.svc.Resolve(ctx, "Zone-A/Item-B~404"); !errors.Is(err, errTestNoSuch) {
			t.Errorf("Resolve: err = %v", err)
		}
		if n, ref, err := x.svc.Resolve(ctx, "ent-5"); err != nil || n != "ent-5" || ref != "" {
			t.Errorf("Resolve(store id) = %q %q %v", n, ref, err)
		}
		if err := x.aliases.Put(ctx, "ent-6", "Zone-A/Item-B~6"); err != nil {
			t.Fatal(err)
		}
		if n, ref, err := x.svc.Resolve(ctx, "ent-6"); err != nil || n != "ent-6" || ref != "Zone-A/Item-B~6" {
			t.Errorf("Resolve(known store id) = %q %q %v", n, ref, err)
		}
		if n, ref, err := x.svc.Resolve(ctx, "zone-a/item-b~6"); err != nil || n != "ent-6" || ref != "Zone-A/Item-B~6" {
			t.Errorf("Resolve(known reference) = %q %q %v, want the alias table's form", n, ref, err)
		}
		_, err := x.svc.Ensure(ctx, ent1, []string{"bogus"}, EnsureOptions{})
		if !errors.Is(err, ErrUnknownGroup) || err.Error() != `groupsvc: unknown group "bogus"` {
			t.Errorf("an unknown group name: err = %v", err)
		}
		// No resolver configured: a reference the alias table does not know is an error.
		x.svc.cfg.Resolver = nil
		_, _, err = x.svc.Resolve(ctx, "Zone-A/Item-B~77")
		if err == nil || err.Error() != "groupsvc: Zone-A/Item-B~77 is not known and no resolver is configured" {
			t.Errorf("Resolve without a resolver: err = %v", err)
		}
	})
}

func TestAGroupWithoutAFetcherIsServedStaleOrUnavailableNamingIt(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		x.dropFetcher(groupSide)
		x.dropFetcher(groupPassive)

		res := x.ensure(ent1, []string{groupSide, groupPassive}, EnsureOptions{})
		if g := groupResult(t, res, groupSide); g.Status != Stale || !errors.Is(g.Err, ErrNoFetcher) {
			t.Errorf("side = %+v", g)
		}
		if g := groupResult(t, res, groupPassive); g.Status != Unavailable || !errors.Is(g.Err, ErrNoFetcher) {
			t.Errorf("passive = %+v", g)
		}
	})
}

func TestServedReportsHowFarPastFreshUntilAStaleRowIs(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.fetch[groupSide].failWith(errors.New("origin is down"))
		obs := x.observe()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old")) // fresh_until one minute ago

		res := x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		if g := groupResult(t, res, groupSide); g.Status != Stale {
			t.Fatalf("side = %+v", g)
		}
		if got := obs.servedOf(groupSide); len(got) != 1 || got[0] != time.Minute {
			t.Errorf("Served = %v, want one call with a minute of staleness", got)
		}
	})
}

func TestTheStatusesNameThemselves(t *testing.T) {
	for s, want := range map[Status]string{Fresh: "fresh", Stale: "stale", Unavailable: "unavailable", 0: "unknown"} {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", int(s), s.String(), want)
		}
	}
}
