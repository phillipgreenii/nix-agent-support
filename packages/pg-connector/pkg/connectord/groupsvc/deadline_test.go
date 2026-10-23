package groupsvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

func TestASlowFetchReturnsThePendingStaleRowAndIsNotCancelled(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		f := x.fetch[groupSide]
		release := f.block()
		t.Cleanup(release)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		res, err := x.svc.Ensure(ctx, ent1, []string{groupSide}, EnsureOptions{})
		if err != nil {
			t.Fatal(err)
		}
		g := groupResult(t, res, groupSide)
		if g.Status != Stale || !g.Pending || string(g.Row.Content) != "old" || g.FromOrigin {
			t.Fatalf("side = %+v, want the pending stale row", g)
		}

		release()
		eventuallyTrue(t, "the late fetch to be stored", func() bool {
			row, _ := x.group(ent1, groupSide)
			return string(row.Content) != "old"
		})
		if err := f.contextErr(); err != nil {
			t.Errorf("the fetch saw its context end (%v): the caller's deadline must not cancel it", err)
		}
		if n := f.callCount(); n != 1 {
			t.Errorf("fetcher called %d times", n)
		}
		// The next read is served fresh from the store.
		if g := groupResult(t, x.ensure(ent1, []string{groupSide}, EnsureOptions{}), groupSide); g.Status != Fresh || g.FromOrigin {
			t.Errorf("after the late store: %+v", g)
		}
	})
}

func TestASlowFetchWithNoRowInsideExpiryIsUnavailableNamingTheDeadline(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		old := staleGroup(x.rig, ent1, groupPassive, "ancient")
		old.FetchedAt = x.clk.Now().Add(-3 * x.svc.cfg.Expiry) // past the expiry
		x.seedGroup(old)                                       // no side row at all
		t.Cleanup(x.fetch[groupSide].block())
		t.Cleanup(x.fetch[groupPassive].block())

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		res, err := x.svc.Ensure(ctx, ent1, []string{groupSide, groupPassive}, EnsureOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{groupSide, groupPassive} {
			g := groupResult(t, res, name)
			if g.Status != Unavailable || !g.Pending || !errors.Is(g.Err, context.DeadlineExceeded) {
				t.Errorf("%s = %+v, want unavailable, pending, naming the deadline", name, g)
			}
		}
	})
}

func TestAGovernorRefusalYieldsStaleOrUnavailableNamingTheRefusalAndIsNotStored(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		refusal := &governor.Refusal{Reason: "test: the budget is spent"}
		x.fetch[groupSide].failWith(refusal)
		x.fetch[groupPassive].failWith(refusal)

		res := x.ensure(ent1, []string{groupSide, groupPassive}, EnsureOptions{})
		d := groupResult(t, res, groupSide)
		if d.Status != Stale || string(d.Row.Content) != "old" || !errors.Is(d.Err, governor.ErrRefused) || !strings.Contains(d.Err.Error(), "spent") {
			t.Errorf("side = %+v, want the stale row and an error naming the refusal", d)
		}
		p := groupResult(t, res, groupPassive)
		if p.Status != Unavailable || !errors.Is(p.Err, governor.ErrRefused) {
			t.Errorf("passive = %+v, want unavailable naming the refusal", p)
		}
		if row, _ := x.group(ent1, groupSide); row.LastError != "" {
			t.Errorf("a governor refusal was recorded as last_error: %q", row.LastError)
		}
	})
}

func TestAnOriginErrorStoresLastErrorKeepsTheContentAndServesItStale(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		x.fetch[groupSide].failWith(errors.New("origin: HTTP 502: bad gateway"))

		res := x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		g := groupResult(t, res, groupSide)
		if g.Status != Stale || string(g.Row.Content) != "old" || g.Err == nil || !strings.Contains(g.Err.Error(), "502") {
			t.Fatalf("side = %+v", g)
		}
		row, _ := x.group(ent1, groupSide)
		if string(row.Content) != "old" || !strings.Contains(row.LastError, "502") {
			t.Errorf("stored = %+v, want the old content and last_error", row)
		}
		if row.Generation != 1 {
			t.Errorf("generation = %d: recording an error must not look like a refresh", row.Generation)
		}

		// With no row, the same failure is unavailable naming the origin failure.
		x.fetch[groupPassive].failWith(errors.New("origin: HTTP 502: bad gateway"))
		res = x.ensure(ent1, []string{groupPassive}, EnsureOptions{})
		if g := groupResult(t, res, groupPassive); g.Status != Unavailable || g.Err == nil || !strings.Contains(g.Err.Error(), "502") {
			t.Errorf("passive = %+v", g)
		}
	})
}

func TestACleanSuccessClearsLastErrorAndANonFatalOutcomeErrorIsStoredWithTheContent(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		x := newEnsureRig(t, spec)
		x.start()
		x.seedPrim(ent1, true)
		x.seedGroup(staleGroup(x.rig, ent1, groupSide, "old"))
		f := x.fetch[groupSide]
		answer := func(lastError string) func(Target, int) Outcome {
			return func(tg Target, _ int) Outcome {
				return Outcome{Content: []byte("new-" + lastError), Stamp: tg.Stamp, Stamp2: tg.Stamp2, LastError: lastError}
			}
		}
		f.setOutcome(answer("results unavailable"))
		res := x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		row := groupResult(t, res, groupSide).Row
		if string(row.Content) == "old" || row.LastError != "results unavailable" {
			t.Fatalf("row = %+v, want new content and the non-fatal error", row)
		}
		f.setOutcome(answer(""))
		res = x.ensure(ent1, []string{groupSide}, EnsureOptions{Fresh: true})
		if row := groupResult(t, res, groupSide).Row; row.LastError != "" {
			t.Errorf("last_error = %q after a clean success", row.LastError)
		}
	})
}
