package groupsvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestASlowFetchReturnsThePendingStaleRowAndIsNotCancelled(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	release := f[groups.Detail].Block()
	t.Cleanup(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, err := r.Svc.Ensure(ctx, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	g := result(t, res, groups.Detail)
	if g.Status != groupsvc.Stale || !g.Pending || string(g.Row.Content) != "old" || g.FromOrigin {
		t.Fatalf("detail = %+v, want the pending stale row", g)
	}

	release()
	eventually(t, "the late fetch to be stored", func() bool {
		row, _ := r.Group(node, groups.Detail)
		return string(row.Content) != "old"
	})
	if err := f[groups.Detail].CtxErr(); err != nil {
		t.Errorf("the fetch saw its context end (%v): the caller's deadline must not cancel it", err)
	}
	if n := f[groups.Detail].CallCount(); n != 1 {
		t.Errorf("fetcher called %d times", n)
	}
	// The next read is served fresh from the store.
	if g := result(t, ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{}), groups.Detail); g.Status != groupsvc.Fresh || g.FromOrigin {
		t.Errorf("after the late store: %+v", g)
	}
}

func TestASlowFetchWithNoRowInsideExpiryIsUnavailableNamingTheDeadline(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail, groups.Conversation)
	old := staleRow(r, node, groups.Conversation, "ancient")
	old.FetchedAt = r.Clock.Now().Add(-8 * 24 * time.Hour) // past the 7-day expiry
	seed(r, node, old)                                     // no detail row at all
	t.Cleanup(f[groups.Detail].Block())
	t.Cleanup(f[groups.Conversation].Block())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, err := r.Svc.Ensure(ctx, node, []string{groups.Detail, groups.Conversation}, groupsvc.EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{groups.Detail, groups.Conversation} {
		g := result(t, res, name)
		if g.Status != groupsvc.Unavailable || !g.Pending || !errors.Is(g.Err, context.DeadlineExceeded) {
			t.Errorf("%s = %+v, want unavailable, pending, naming the deadline", name, g)
		}
	}
}

func TestAGovernorRefusalYieldsStaleOrUnavailableNamingTheCap(t *testing.T) {
	clk := governor.NewManualClock(groupsvctest.T0)
	gov := newGov(t, clk, 1)
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{Gov: gov, Clock: clk})
	r.Start()
	f := register(r, groups.Detail, groups.Conversation)
	f[groups.Detail].Gov, f[groups.Conversation].Gov = gov, gov
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	// Spend the whole cap.
	tk, err := gov.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tk.Settle(governor.Spend{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: 1, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}

	res := ensure(t, r, node, []string{groups.Detail, groups.Conversation}, groupsvc.EnsureOptions{})
	d := result(t, res, groups.Detail)
	if d.Status != groupsvc.Stale || string(d.Row.Content) != "old" || !errors.Is(d.Err, governor.ErrRefused) || !strings.Contains(d.Err.Error(), "cap") {
		t.Errorf("detail = %+v, want the stale row and an error naming the cap", d)
	}
	c := result(t, res, groups.Conversation)
	if c.Status != groupsvc.Unavailable || !errors.Is(c.Err, governor.ErrRefused) || !strings.Contains(c.Err.Error(), "cap") {
		t.Errorf("conversation = %+v, want unavailable naming the cap", c)
	}
	if f.total() != 0 {
		t.Errorf("the fetchers were called %d times against a spent cap", f.total())
	}
	if row, _ := r.Group(node, groups.Detail); row.LastError != "" {
		t.Errorf("a governor refusal was recorded as last_error: %q", row.LastError)
	}
}

func TestAnOriginErrorStoresLastErrorKeepsTheContentAndServesItStale(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	f[groups.Detail].FailWith(errors.New("gh: HTTP 502: bad gateway"))
	seed(r, node, staleRow(r, node, groups.Detail, "old"))

	res := ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	g := result(t, res, groups.Detail)
	if g.Status != groupsvc.Stale || string(g.Row.Content) != "old" || g.Err == nil || !strings.Contains(g.Err.Error(), "502") {
		t.Fatalf("detail = %+v", g)
	}
	row, _ := r.Group(node, groups.Detail)
	if string(row.Content) != "old" || !strings.Contains(row.LastError, "502") {
		t.Errorf("stored = %+v, want the old content and last_error", row)
	}
	if row.Generation != 1 {
		t.Errorf("generation = %d: recording an error must not look like a refresh", row.Generation)
	}

	// With no row, the same failure is unavailable naming the origin failure.
	f[groups.Detail].FailWith(errors.New("gh: HTTP 502: bad gateway"))
	res = ensure(t, r, node, []string{groups.Conversation, groups.Detail}, groupsvc.EnsureOptions{})
	if g := result(t, res, groups.Conversation); g.Status != groupsvc.Unavailable || !errors.Is(g.Err, groupsvc.ErrNoFetcher) {
		t.Errorf("conversation = %+v", g)
	}
}

func TestACleanSuccessClearsLastErrorAndANonFatalOutcomeErrorIsStoredWithTheContent(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Runs)
	seed(r, node, staleRow(r, node, groups.Runs, "old"))
	f[groups.Runs].SetLastError("job results unavailable")
	res := ensure(t, r, node, []string{groups.Runs}, groupsvc.EnsureOptions{})
	row := result(t, res, groups.Runs).Row
	if string(row.Content) == "old" || row.LastError != "job results unavailable" {
		t.Fatalf("row = %+v, want new content and the non-fatal error", row)
	}
	f[groups.Runs].SetLastError("")
	res = ensure(t, r, node, []string{groups.Runs}, groupsvc.EnsureOptions{Fresh: true})
	if row := result(t, res, groups.Runs).Row; row.LastError != "" {
		t.Errorf("last_error = %q after a clean success", row.LastError)
	}
}
