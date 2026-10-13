package groupsvc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestAGroupFetchThatSaysTheCallIsGoneTombstonesTheEntityAndCancelsItsQueuedTasks(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail, groups.Conversation)
	seed(r, node, freshRow(r, node, groups.Conversation, "c"))
	f[groups.Detail].SetGone(node, true)
	// A background task of another group is queued for the entity.
	if err := r.Svc.QueueGroup(ctx, node, groups.Conversation, governor.ClassKeepAlive, r.Clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	_, err := r.Svc.Ensure(ctx, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if !errors.Is(err, groupsvc.ErrNoSuchPR) {
		t.Fatalf("Ensure of a PR whose group fetch says gone = %v, want ErrNoSuchPR", err)
	}
	if e, _ := r.Entity(node); e.TombstonedAt.IsZero() || !e.TombstonedAt.Equal(r.Clock.Now()) {
		t.Errorf("entity = %+v, want TombstonedAt = now", e)
	}
	if r.Sched.Queued(node, groups.Conversation) {
		t.Error("a queued task of the tombstoned entity survived")
	}
	if row, ok := r.Group(node, groups.Conversation); !ok || string(row.Content) != "c" {
		t.Errorf("stored groups must be left for expiry: %+v %v", row, ok)
	}
	// From then on the PR does not exist: no origin call at all.
	calls, origin := f.total(), r.Origin.CallCount()
	for i := 0; i < 2; i++ {
		if _, err := r.Svc.Ensure(ctx, node, []string{groups.Summary, groups.Detail, groups.Conversation}, groupsvc.EnsureOptions{Fresh: true}); !errors.Is(err, groupsvc.ErrNoSuchPR) {
			t.Fatalf("later Ensure = %v, want ErrNoSuchPR", err)
		}
	}
	if f.total() != calls || r.Origin.CallCount() != origin {
		t.Errorf("a tombstoned PR was fetched again")
	}
	// A background refresh skips it as well.
	if err := r.Svc.InvalidateAndQueue(ctx, node, groups.Detail); err != nil {
		t.Fatal(err)
	}
	if r.Sched.Queued(node, groups.Detail) {
		t.Error("a task was queued for a tombstoned PR")
	}
}

func TestASummaryThatSaysGoneAlsoEndsInErrNoSuchPR(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	r.Origin.Set(node, groupsvctest.PR{Gone: true})
	if _, err := r.Svc.Ensure(context.Background(), node, []string{groups.Detail}, groupsvc.EnsureOptions{}); !errors.Is(err, groupsvc.ErrNoSuchPR) {
		t.Fatalf("Ensure = %v, want ErrNoSuchPR", err)
	}
	if f.total() != 0 {
		t.Error("a group was fetched for a PR the summary says is gone")
	}
}

func TestAGraphQLRateLimitAnswerPausesTheBackgroundAndIsNotAnAuthenticationFailure(t *testing.T) {
	clk := governor.NewManualClock(groupsvctest.T0)
	gov := newGov(t, clk, 1000)
	m := &marker{}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{Gov: gov, Clock: clk, Identity: m})
	r.Start()
	f := register(r, groups.Detail)
	f[groups.Detail].Gov = gov
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	// gh turns every 403 into an authentication error: the text is what tells.
	f[groups.Detail].FailWith(fmt.Errorf("gh: You have exceeded a secondary rate limit (HTTP 403): %w", vcs.ErrAuthInvalid))

	res := ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	g := result(t, res, groups.Detail)
	var rl *ratefeed.RateLimitError
	if g.Status != groupsvc.Stale || !errors.As(g.Err, &rl) || errors.Is(g.Err, vcs.ErrAuthInvalid) {
		t.Fatalf("detail = %+v, want stale with a rate-limit error that is not an auth failure", g)
	}
	if marks, _ := m.counts(); marks != 0 {
		t.Errorf("MarkUnauthenticated called %d times for a rate limit", marks)
	}
	bs, _ := gov.State().Bucket(governor.BucketGraphQL)
	if !bs.Paused {
		t.Error("the background classes were not paused")
	}
	// Background spend is refused for the pause; an interactive read still goes.
	if err := gov.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassKeepAlive, Cost: 1}); !errors.Is(err, governor.ErrRefused) {
		t.Errorf("background check = %v, want a refusal", err)
	}
	if err := gov.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: 1}); err != nil {
		t.Errorf("interactive check = %v", err)
	}
	if row, _ := r.Group(node, groups.Detail); row.LastError == "" || string(row.Content) != "old" {
		t.Errorf("row = %+v, want last_error recorded and the content kept", row)
	}
}

func TestAnInvalidCredentialMarksTheIdentityUnauthenticatedAndASuccessClearsIt(t *testing.T) {
	m := &marker{}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{Identity: m})
	r.Start()
	f := register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	f[groups.Detail].FailWith(fmt.Errorf("gh: Bad credentials (HTTP 401): %w", vcs.ErrAuthInvalid))

	res := ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if g := result(t, res, groups.Detail); g.Status != groupsvc.Stale || !errors.Is(g.Err, vcs.ErrAuthInvalid) {
		t.Fatalf("detail = %+v, want stale with the auth error", g)
	}
	if marks, clears := m.counts(); marks != 1 || clears != 0 {
		t.Fatalf("marks %d, clears %d, want 1 and 0", marks, clears)
	}
	f[groups.Detail].FailWith(nil)
	ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{Fresh: true})
	if marks, clears := m.counts(); marks != 1 || clears != 1 {
		t.Fatalf("marks %d, clears %d after a success, want 1 and 1", marks, clears)
	}
}

func TestAFetcherThatReturnsNoContentIsAFailureThatKeepsTheOldRow(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	f[groups.Detail].SetOutcome(func(groups.Target, int) groups.Outcome { return groups.Outcome{} })
	res := ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if g := result(t, res, groups.Detail); g.Status != groupsvc.Stale || string(g.Row.Content) != "old" || g.Err == nil {
		t.Errorf("detail = %+v", g)
	}
}

func TestOutcomeFreshUntilAndStampsOverrideTheRules(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Runs)
	seed(r, node)
	until := r.Clock.Now().Add(7 * time.Minute)
	f[groups.Runs].SetOutcome(func(tg groups.Target, _ int) groups.Outcome {
		return groups.Outcome{Content: []byte("runs"), FreshUntil: until, Stamps: groups.Stamps{Head: "head-x", Base: tg.Stamps.Base}}
	})
	ensure(t, r, node, []string{groups.Runs}, groupsvc.EnsureOptions{})
	row, _ := r.Group(node, groups.Runs)
	if !row.FreshUntil.Equal(until) || row.FetchedForHead != "head-x" || row.FetchedForBase != "base-1" || row.Fingerprint != groups.Fingerprint([]byte("runs")) {
		t.Errorf("row = %+v", row)
	}
}

func TestNewAndRegisterRejectWhatTheyCannotWire(t *testing.T) {
	if _, err := groupsvc.New(groupsvc.Config{}); err == nil {
		t.Error("New accepted an empty config")
	}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	for _, name := range []string{groups.Summary, "bogus"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Register accepted a fetcher for %q", name)
				}
			}()
			r.Svc.Register(groupsvctest.NewFetcher(name))
		}()
	}
}
