package groupsvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

func TestWithNoPrimaryAndAFailingOriginEveryGroupIsUnavailableNamingTheFailure(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.prim.failWith(errors.New("origin: HTTP 502: bad gateway"))

		res := x.ensure(ent1, []string{x.primary(), groupSide}, EnsureOptions{})
		if res.Primary.Ref != "" || len(res.Content) != 0 {
			t.Errorf("Primary = %+v, Content = %q, want none", res.Primary, res.Content)
		}
		for _, g := range res.Groups {
			if g.Status != Unavailable || g.Err == nil || !strings.Contains(g.Err.Error(), "502") {
				t.Errorf("%s = %+v, want unavailable naming the origin failure", g.Group, g)
			}
		}
		if x.fetchTotal() != 0 {
			t.Error("a group was fetched without primary content to build its target from")
		}
	})
}

func TestAStalePrimaryThatCannotBeRefreshedServesTheStoredRowsStale(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, false) // past its fresh_until, inside expiry
		x.seedGroup(freshGroup(x.rig, ent1, groupSide, "d"))
		x.prim.failWith(errors.New("origin: HTTP 502: bad gateway"))

		res := x.ensure(ent1, []string{x.primary(), groupSide}, EnsureOptions{})
		if res.Primary.Ref != ref1 {
			t.Errorf("Primary = %+v, want the stored one", res.Primary)
		}
		for _, g := range res.Groups {
			if g.Status != Stale || g.Err == nil || !strings.Contains(g.Err.Error(), "502") || g.FromOrigin {
				t.Errorf("%s = %+v, want stale with the origin failure", g.Group, g)
			}
		}
		if x.fetchTotal() != 0 {
			t.Error("a group was fetched against primary content that could not be refreshed")
		}
	})
}

func TestAPrimaryStillRunningAtTheDeadlineLeavesEveryGroupPending(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, false)
		x.seedGroup(freshGroup(x.rig, ent1, groupSide, "d"))
		release := x.prim.block()
		t.Cleanup(release)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		res, err := x.svc.Ensure(ctx, ent1, []string{x.primary(), groupSide}, EnsureOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !x.prim.waitStarted(1, 10*time.Second) {
			t.Fatal("the primary fetch never started")
		}
		for _, g := range res.Groups {
			if g.Status != Stale || !g.Pending {
				t.Errorf("%s = %+v, want the stale row, pending", g.Group, g)
			}
		}

		// With no stored primary content at all the deadline is what the groups name.
		x2 := newEnsureRig(t, spec)
		x2.start()
		release2 := x2.prim.block()
		t.Cleanup(release2)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel2()
		res, err = x2.svc.Ensure(ctx2, ent1, []string{groupSide}, EnsureOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if g := res.Groups[0]; g.Status != Unavailable || !g.Pending || !errors.Is(g.Err, context.DeadlineExceeded) {
			t.Errorf("side = %+v, want unavailable, pending, naming the deadline", g)
		}
	})
}

func TestAPrimaryContentTheDescriberCannotReadIsAnErrorNamingTheEntity(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedEntity(ent1, x.clk.Now())
		x.seedGroup(freshGroup(x.rig, ent1, x.primary(), "not|enough"))

		_, err := x.svc.Ensure(context.Background(), ent1, []string{groupSide}, EnsureOptions{})
		if err == nil || !strings.Contains(err.Error(), "decode primary content of "+ent1) {
			t.Fatalf("Ensure err = %v, want the decode failure wrapped with the entity id", err)
		}
		if x.fetchTotal() != 0 {
			t.Error("a group was fetched for content that does not decode")
		}
	})
}
