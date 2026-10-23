package groupsvc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

func TestAGroupFetchThatSaysTheEntityIsGoneTombstonesItAndEnsureReportsItNonexistent(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		ctx := context.Background()
		x := newEnsureRig(t, spec)
		x.start()
		x.keep.keep(ent1)
		x.seedPrim(ent1, true)
		x.seedGroup(withStamps(freshGroup(x.rig, ent1, groupStamped, "c"), stampA1, stampA2))
		x.fetch[groupSide].setGone(ent1, true)
		// A background task of another group is queued for the entity.
		if err := x.svc.QueueGroup(ctx, ent1, groupStamped, governor.ClassKeepAlive, x.clk.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}

		_, err := x.svc.Ensure(ctx, ent1, []string{groupSide}, EnsureOptions{})
		if !errors.Is(err, errTestNoSuch) {
			t.Fatalf("Ensure of an entity whose group fetch says gone = %v, want the NoSuchEntity sentinel", err)
		}
		if e := x.entity(ent1); e.TombstonedAt.IsZero() || !e.TombstonedAt.Equal(x.clk.Now()) {
			t.Errorf("entity = %+v, want TombstonedAt = now", e)
		}
		if x.sched.Queued(ent1, groupStamped) {
			t.Error("a queued task of the tombstoned entity survived")
		}
		if row, ok := x.group(ent1, groupStamped); !ok || string(row.Content) != "c" {
			t.Errorf("stored groups must be left for expiry: %+v %v", row, ok)
		}
		// From then on the entity does not exist: no origin call at all.
		calls, primCalls := x.fetchTotal(), x.prim.callCount()
		for i := 0; i < 2; i++ {
			if _, err := x.svc.Ensure(ctx, ent1, []string{x.primary(), groupSide, groupStamped}, EnsureOptions{Fresh: true}); !errors.Is(err, errTestNoSuch) {
				t.Fatalf("later Ensure = %v, want the NoSuchEntity sentinel", err)
			}
		}
		if x.fetchTotal() != calls || x.prim.callCount() != primCalls {
			t.Errorf("a tombstoned entity was fetched again")
		}
		// A background refresh skips it as well.
		if err := x.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
			t.Fatal(err)
		}
		if x.sched.Queued(ent1, groupSide) {
			t.Error("a task was queued for a tombstoned entity")
		}
	})
}

func TestAPrimaryThatSaysTheEntityIsGoneAlsoEndsInNonexistent(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.prim.setGone(ent1)
		if _, err := x.svc.Ensure(context.Background(), ent1, []string{groupSide}, EnsureOptions{}); !errors.Is(err, errTestNoSuch) {
			t.Fatalf("Ensure = %v, want the NoSuchEntity sentinel", err)
		}
		if x.fetchTotal() != 0 {
			t.Error("a group was fetched for an entity the primary says is gone")
		}
	})
}

func TestARateLimitAnswerIsNotAnAuthenticationFailureAndAServedRowKeepsItsContent(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		// The connector's own policy turns an error whose text says "quota" into its
		// rate-limit error, dropping the credentials error the origin wrapped it in.
		x.fetch[groupSide].failWith(fmt.Errorf("origin: quota exceeded: %w", errTestAuth))

		res := x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		g := groupResult(t, res, groupSide)
		if g.Status != Stale || !errors.Is(g.Err, errTestRateLimited) || errors.Is(g.Err, errTestAuth) {
			t.Fatalf("side = %+v, want stale with a rate-limit error that is not an auth failure", g)
		}
		if marks, _ := x.marker.counts(); marks != 0 {
			t.Errorf("MarkUnauthenticated called %d times for a rate limit", marks)
		}
		if row, _ := x.group(ent1, groupSide); row.LastError == "" || string(row.Content) != "old" {
			t.Errorf("row = %+v, want last_error recorded and the content kept", row)
		}
	})
}

func TestARefusedCredentialMarksTheIdentityUnauthenticatedAndASuccessClearsIt(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		f := x.fetch[groupSide]
		f.failWith(fmt.Errorf("origin: bad credentials: %w", errTestAuth))

		res := x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		if g := groupResult(t, res, groupSide); g.Status != Stale || !errors.Is(g.Err, errTestAuth) {
			t.Fatalf("side = %+v, want stale with the auth error", g)
		}
		if marks, clears := x.marker.counts(); marks != 1 || clears != 0 {
			t.Fatalf("marks %d, clears %d, want 1 and 0", marks, clears)
		}
		f.failWith(nil)
		x.ensure(ent1, []string{groupSide}, EnsureOptions{Fresh: true})
		if marks, clears := x.marker.counts(); marks != 1 || clears != 1 {
			t.Fatalf("marks %d, clears %d after a success, want 1 and 1", marks, clears)
		}
	})
}

func TestAFetcherThatReturnsNoContentIsAFailureThatKeepsTheOldRow(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		x.fetch[groupSide].setOutcome(func(Target, int) Outcome { return Outcome{} })

		res := x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		if g := groupResult(t, res, groupSide); g.Status != Stale || string(g.Row.Content) != "old" || g.Err == nil {
			t.Errorf("side = %+v", g)
		}
	})
}

func TestAnOutcomeFreshUntilAndStampsOverrideTheRulesWhenReadThroughEnsure(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		until := x.clk.Now().Add(7 * time.Minute)
		x.fetch[groupPassive].setOutcome(func(tg Target, _ int) Outcome {
			return Outcome{Content: []byte("p"), FreshUntil: until, Stamp: "h-x", Stamp2: tg.Stamp2}
		})

		x.ensure(ent1, []string{groupPassive}, EnsureOptions{})
		row, _ := x.group(ent1, groupPassive)
		head, base := row.Stamps()
		if !row.FreshUntil.Equal(until) || head != "h-x" || base != stampA2 {
			t.Errorf("row = %+v, want the outcome's fresh_until and stamps", row)
		}
	})
}
