package executor

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/gwm"
	"github.com/phillipgreenii/x/cmdrun"
)

func endHarness(t *testing.T) *gwmHarness {
	h := newGWMHarness(t, false)
	h.run.gwm = gwmState{on: true, lease: gwm.Lease{LeaseID: "l-1", Name: "worker/bd-1", Path: "/wt"}, parkTTL: 72 * time.Hour}
	return h
}

func TestSessionEndReleases(t *testing.T) {
	h := endHarness(t)
	h.gwm.On(gwmByVerb("release")).Return(cmdrun.Result{})
	h.run.gwmSessionEnd(context.Background(), h.role.CCPool, gwmSess, true)
	if got := gwmCalls(h.gwm, "release"); len(got) != 1 {
		t.Fatalf("release calls = %v, want 1", got)
	}
	if n := len(gwmCalls(h.gwm, "expire")); n != 0 {
		t.Errorf("expire calls = %d, want 0", n)
	}
}

func TestDirtyEndExpires(t *testing.T) {
	h := endHarness(t)
	h.gwm.On(gwmByVerb("release")).Return(cmdrun.Result{Code: 23, Stdout: []byte(gwmEnvelope("dirty", "x", `{}`))})
	h.gwm.On(gwmByVerb("expire")).Return(cmdrun.Result{})
	h.run.gwmSessionEnd(context.Background(), h.role.CCPool, gwmSess, true)
	if len(gwmCalls(h.gwm, "release")) != 1 || len(gwmCalls(h.gwm, "expire")) != 1 {
		t.Errorf("want release then expire, got %v", h.gwm.Calls())
	}
}

func TestParkedSessionRenewsWithParkedTTL(t *testing.T) {
	h := endHarness(t)
	h.gwm.On(gwmByVerb("renew")).Return(cmdrun.Result{})
	h.run.gwmSessionEnd(context.Background(), h.role.CCPool, gwmSess, false)
	got := gwmCalls(h.gwm, "renew")
	if len(got) != 1 || !slices2(got[0], "3d") {
		t.Errorf("renew calls = %v, want one --ttl 3d", got)
	}
	if n := len(gwmCalls(h.gwm, "release")); n != 0 {
		t.Errorf("a parked session must not be released, got %d release calls", n)
	}
}
