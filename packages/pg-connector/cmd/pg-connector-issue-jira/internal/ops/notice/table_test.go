package notice

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestTableHoldAndRelease(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if r.table.Held(keyA) {
		t.Fatal("held before Begin")
	}
	r.table.Begin(ctx, keyA, 0)
	if !r.table.Held(keyA) || !r.table.Held("a-1") {
		t.Fatal("not held after Begin (the id folds like the daemon's ids)")
	}
	if r.table.Held("A-2") {
		t.Fatal("a hold held another issue")
	}
	if d := r.timers.last().d; d != lease {
		t.Fatalf("zero lease timer = %v, want the configured %v", d, lease)
	}
	r.table.Release(ctx, keyA)
	if r.table.Held(keyA) {
		t.Fatal("held after Release")
	}
	if !r.timers.last().stopped {
		t.Fatal("Release left the timer running")
	}
	r.table.Release(ctx, keyA) // not held: a no-op
	r.table.Release(ctx, "Z-9")
}

func TestTableNilIsNeverHeld(t *testing.T) {
	var tb *Table
	if tb.Held(keyA) {
		t.Fatal("a nil table holds")
	}
}

func TestNewTableDefaultsTheLease(t *testing.T) {
	if got := NewTable(TableConfig{}).Lease(); got != config.DefaultWriteLease {
		t.Fatalf("default lease = %v, want %v", got, config.DefaultWriteLease)
	}
	if got := NewTable(TableConfig{Lease: time.Second}).Lease(); got != time.Second {
		t.Fatalf("lease = %v, want 1s", got)
	}
}

func TestTableBeginOnAHeldIdExtendsTheLease(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	ctx := context.Background()
	r.table.Begin(ctx, keyA, time.Second)
	first := r.timers.last()
	r.table.Begin(ctx, keyA, 2*time.Second)
	second := r.timers.last()
	if first == second || !first.stopped || second.stopped || second.d != 2*time.Second {
		t.Fatalf("extension: first stopped=%v second stopped=%v d=%v", first.stopped, second.stopped, second.d)
	}
	// A timer that had already started when it was replaced does nothing.
	g := r.generation(keyA, groups.Summary)
	r.timers.fireStale(first)
	if !r.table.Held(keyA) || r.generation(keyA, groups.Summary) != g {
		t.Fatal("a replaced timer released or bumped the extended hold")
	}
	if _, ok := r.queued(keyA, groups.Summary); ok {
		t.Fatal("a replaced timer queued a re-pull")
	}
	r.timers.fire(second)
	if r.table.Held(keyA) {
		t.Fatal("the extended hold outlived its new lease")
	}
}

func TestTableTimerAfterReleaseDoesNothing(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	r.table.Begin(context.Background(), keyA, 0)
	tm := r.timers.last()
	r.table.Release(context.Background(), keyA)
	g := r.generation(keyA, groups.Summary)
	r.timers.fireStale(tm)
	if r.generation(keyA, groups.Summary) != g {
		t.Fatal("a released hold's timer bumped a generation")
	}
	if _, ok := r.queued(keyA, groups.Summary); ok {
		t.Fatal("a released hold's timer queued a re-pull")
	}
}

func TestTableExpiryBumpsReleasesAndQueuesOneRePull(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	sum1, det1 := r.generation(keyA, groups.Summary), r.generation(keyA, groups.Detail)

	r.clk.Advance(lease)
	r.timers.fire(r.timers.last())

	if r.table.Held(keyA) {
		t.Fatal("the hold outlived the lease")
	}
	if g := r.generation(keyA, groups.Summary); g != sum1+1 {
		t.Errorf("summary generation = %d, want %d", g, sum1+1)
	}
	if g := r.generation(keyA, groups.Detail); g != det1+1 {
		t.Errorf("detail generation = %d, want %d", g, det1+1)
	}
	for _, g := range []string{groups.Summary, groups.Detail} {
		if c, ok := r.queued(keyA, g); !ok || c != governor.ClassInvalidated {
			t.Errorf("%s re-pull queued=%v class=%v", g, ok, c)
		}
	}
	if len(r.tableErrs()) != 0 {
		t.Errorf("expiry errors: %v", r.tableErrs())
	}
	if r.origin.count() != 0 {
		t.Error("the expiry called the origin")
	}
}

func TestTableExpiryBeforeBindReleasesAndBumpsButQueuesNothing(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	unbound := NewTable(TableConfig{Store: r.st, Clock: r.clk, Lease: lease, AfterFunc: r.timers.afterFunc})
	unbound.Begin(context.Background(), keyA, 0)
	g := r.generation(keyA, groups.Summary)
	r.timers.fire(r.timers.last())
	if unbound.Held(keyA) {
		t.Fatal("still held")
	}
	if r.generation(keyA, groups.Summary) != g+1 {
		t.Fatal("the expiry did not bump")
	}
	if _, ok := r.queued(keyA, groups.Summary); ok {
		t.Fatal("an unbound table queued a re-pull")
	}
}

func TestTableExpiryWithoutAStoreOnlyReleases(t *testing.T) {
	r := newRig(t)
	tb := NewTable(TableConfig{Lease: lease, AfterFunc: r.timers.afterFunc})
	tb.Begin(context.Background(), keyA, 0)
	r.timers.fire(r.timers.last())
	if tb.Held(keyA) {
		t.Fatal("still held")
	}
}

func TestTableExpiryOfAnUnknownIssueQueuesTheTrackingFetchOnly(t *testing.T) {
	r := newRig(t)
	r.table.Begin(context.Background(), keyNew, 0)
	r.timers.fire(r.timers.last())
	if c, ok := r.queued(keyNew, groups.Summary); !ok || c != governor.ClassInvalidated {
		t.Fatalf("summary fetch queued=%v class=%v", ok, c)
	}
	if _, ok := r.queued(keyNew, groups.Detail); ok {
		t.Fatal("an unknown issue got a detail re-pull")
	}
}
