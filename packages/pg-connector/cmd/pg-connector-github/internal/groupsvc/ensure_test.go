package groupsvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestEnsureOfFreshGroupsMakesNoOriginCallAndReportsZeroStaleness(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail, groups.Conversation)
	obs := newObserver()
	r.Svc.SetObserver(obs)
	seed(r, node, freshRow(r, node, groups.Detail, "d"), freshRow(r, node, groups.Conversation, "c"))

	res := ensure(t, r, node, []string{groups.Summary, groups.Detail, groups.Conversation}, groupsvc.EnsureOptions{})
	if r.Origin.CallCount() != 0 || f.total() != 0 {
		t.Fatalf("origin calls %d, fetcher calls %d, want none", r.Origin.CallCount(), f.total())
	}
	if len(res.Groups) != 3 {
		t.Fatalf("Groups = %+v, want one per wanted group, summary included", res.Groups)
	}
	for _, g := range res.Groups {
		if g.Status != groupsvc.Fresh || g.FromOrigin || g.Pending || g.Err != nil {
			t.Errorf("%s = %+v, want fresh from the store", g.Group, g)
		}
		if got := obs.servedOf(g.Group); len(got) != 1 || got[0] != 0 {
			t.Errorf("Served(%s) = %v, want one call with staleness 0", g.Group, got)
		}
	}
	if res.Summary.NodeID != node || res.Summary.HeadRefOid != "head-1" {
		t.Errorf("Summary = %+v", res.Summary)
	}
	if e, _ := r.Entity(node); e.LastAccess.IsZero() {
		t.Error("the access was not recorded")
	}
}

func TestEnsureFetchesOnlyTheStaleGroupAtInteractivePriority(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail, groups.Conversation)
	seed(r, node, staleRow(r, node, groups.Detail, "old"), freshRow(r, node, groups.Conversation, "c"))

	res := ensure(t, r, node, []string{groups.Detail, groups.Conversation}, groupsvc.EnsureOptions{})
	if f[groups.Detail].CallCount() != 1 || f[groups.Conversation].CallCount() != 0 || r.Origin.CallCount() != 0 {
		t.Fatalf("detail %d, conversation %d, origin %d calls; want 1, 0, 0",
			f[groups.Detail].CallCount(), f[groups.Conversation].CallCount(), r.Origin.CallCount())
	}
	c := f[groups.Detail].Calls()[0]
	if c.Class != governor.ClassInteractive || c.Purpose != governor.PurposeInteractive || c.Group != groups.Detail || c.BatchSize != 1 {
		t.Errorf("call = %+v, want an interactive detail call of one target", c)
	}
	d := result(t, res, groups.Detail)
	if d.Status != groupsvc.Fresh || !d.FromOrigin || string(d.Row.Content) == "old" {
		t.Errorf("detail = %+v, want fresh from origin", d)
	}
	if cv := result(t, res, groups.Conversation); cv.Status != groupsvc.Fresh || cv.FromOrigin {
		t.Errorf("conversation = %+v, want fresh from the store", cv)
	}
	stored, _ := r.Group(node, groups.Detail)
	if string(stored.Content) != string(d.Row.Content) || stored.Generation != 2 {
		t.Errorf("stored detail = %+v", stored)
	}
}

func TestTwoConcurrentEnsuresOfOneStaleGroupShareOneFetch(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	release := f[groups.Detail].Block()

	done := make(chan groupsvc.EnsureResult, 2)
	run := func() {
		res, err := r.Svc.Ensure(context.Background(), node, []string{groups.Detail}, groupsvc.EnsureOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}
	go run()
	if !f[groups.Detail].WaitStarted(1, 10*time.Second) {
		t.Fatal("the fetch never started")
	}
	go run()
	// The second caller joins the running flight (or finds the row fresh after it).
	time.Sleep(50 * time.Millisecond)
	release()
	for i := 0; i < 2; i++ {
		if res := <-done; result(t, res, groups.Detail).Status != groupsvc.Fresh {
			t.Errorf("result %d = %+v", i, res)
		}
	}
	if n := f[groups.Detail].CallCount(); n != 1 {
		t.Fatalf("fetcher called %d times, want 1", n)
	}
}

func TestSeveralStaleGroupsAreFetchedInParallel(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail, groups.Conversation, groups.Runs)
	seed(r, node,
		staleRow(r, node, groups.Detail, "d"), staleRow(r, node, groups.Conversation, "c"), staleRow(r, node, groups.Runs, "r"))
	// Every call waits until all three have started: a serial engine deadlocks
	// here and the deadline of ensure() fails the test.
	b := groupsvctest.NewBarrier(3)
	for _, x := range f {
		x.Join(b)
	}
	res := ensure(t, r, node, []string{groups.Detail, groups.Conversation, groups.Runs}, groupsvc.EnsureOptions{})
	for _, g := range res.Groups {
		if g.Status != groupsvc.Fresh || !g.FromOrigin {
			t.Errorf("%s = %+v", g.Group, g)
		}
	}
}

func TestAFilesRowFetchedForAnOlderHeadIsStaleInsideAnyTimeWindow(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Files)
	row := freshRow(r, node, groups.Files, "files")
	row.FreshUntil = r.Clock.Now().Add(24 * time.Hour)
	row.FetchedForHead = "head-0"
	seed(r, node, row)

	res := ensure(t, r, node, []string{groups.Files}, groupsvc.EnsureOptions{})
	if f[groups.Files].CallCount() != 1 {
		t.Fatalf("fetcher called %d times, want 1 for a row of another head", f[groups.Files].CallCount())
	}
	if got := f[groups.Files].Calls()[0].Targets[0].Stamps.Head; got != "head-1" {
		t.Errorf("target head = %q, want the current summary's head-1", got)
	}
	if g := result(t, res, groups.Files); g.Status != groupsvc.Fresh || g.Row.FetchedForHead != "head-1" {
		t.Errorf("files = %+v", g)
	}
	// A row for the current head is served from the store.
	ensure(t, r, node, []string{groups.Files}, groupsvc.EnsureOptions{})
	if f[groups.Files].CallCount() != 1 {
		t.Errorf("a stamped row for the current head was fetched again")
	}
}

func TestFreshTrueForcesEveryWantedGroupIncludingTheSummary(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail, groups.Conversation)
	seed(r, node, freshRow(r, node, groups.Detail, "d"), freshRow(r, node, groups.Conversation, "c"))

	res := ensure(t, r, node, []string{groups.Summary, groups.Detail, groups.Conversation}, groupsvc.EnsureOptions{Fresh: true})
	if r.Origin.CallCount() != 1 || f[groups.Detail].CallCount() != 1 || f[groups.Conversation].CallCount() != 1 {
		t.Fatalf("origin %d, detail %d, conversation %d calls; want 1 each",
			r.Origin.CallCount(), f[groups.Detail].CallCount(), f[groups.Conversation].CallCount())
	}
	if c := r.Origin.Calls()[0]; c.Class != governor.ClassInteractive {
		t.Errorf("summary call class = %v", c.Class)
	}
	for _, g := range res.Groups {
		if g.Status != groupsvc.Fresh || !g.FromOrigin {
			t.Errorf("%s = %+v, want fresh from origin", g.Group, g)
		}
	}
}

func TestAWantOfSummaryAloneListsItWithItsStatusAndOrigin(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	res := ensure(t, r, node, []string{groups.Summary}, groupsvc.EnsureOptions{})
	if len(res.Groups) != 1 || res.Groups[0].Group != groups.Summary || res.Groups[0].Status != groupsvc.Fresh || !res.Groups[0].FromOrigin {
		t.Fatalf("first read = %+v, want summary fresh from origin", res.Groups)
	}
	res = ensure(t, r, node, []string{groups.Summary}, groupsvc.EnsureOptions{})
	if g := res.Groups[0]; g.Status != groupsvc.Fresh || g.FromOrigin || r.Origin.CallCount() != 1 {
		t.Fatalf("second read = %+v after %d origin calls, want it served from the store", g, r.Origin.CallCount())
	}
}

func TestTheSummaryComesFirstAndLaterGroupsUseItsRefreshedHead(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail, groups.Runs)
	// First sighting: no summary row at all.
	res := ensure(t, r, node, []string{groups.Detail, groups.Runs}, groupsvc.EnsureOptions{})
	if r.Origin.CallCount() != 1 {
		t.Fatalf("origin calls = %d, want the summary fetch", r.Origin.CallCount())
	}
	for _, g := range []string{groups.Detail, groups.Runs} {
		c := f[g].Calls()
		if len(c) != 1 || c[0].Targets[0].Stamps.Head != "head-1" || c[0].Targets[0].HeadRefName != "feature" {
			t.Errorf("%s calls = %+v, want one with the refreshed summary's head and branch", g, c)
		}
		if !result(t, res, g).FromOrigin {
			t.Errorf("%s not from origin", g)
		}
	}
	if res.Summary.HeadRefOid != "head-1" {
		t.Errorf("Summary = %+v", res.Summary)
	}

	// The head moves at the origin; Fresh makes every later group use the new one.
	r.Origin.Update(node, func(p *groupsvctest.PR) { p.HeadRefOid = "head-2" })
	res = ensure(t, r, node, []string{groups.Detail, groups.Runs}, groupsvc.EnsureOptions{Fresh: true})
	for _, g := range []string{groups.Detail, groups.Runs} {
		c := f[g].Calls()
		if got := c[len(c)-1].Targets[0].Stamps.Head; got != "head-2" {
			t.Errorf("%s fetched for head %q, want head-2", g, got)
		}
	}
	if res.Summary.HeadRefOid != "head-2" {
		t.Errorf("Summary = %+v", res.Summary)
	}
}

func TestAnUnknownReferenceIsResolvedFirstThenTheSummaryIsFetched(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	ref := entity.Ref{Owner: "acme-org", Repo: "widget", Number: 9}
	r.Origin.Set("PR_9", groupsvctest.PR{Repo: "acme-org/widget", Number: 9, State: "OPEN", HeadRefOid: "head-1", BaseRefOid: "base-1", HeadRefName: "feature"})
	r.Resolver.Know(ref, "PR_9")

	res := ensure(t, r, "ACME-org/Widget#9", []string{groups.Summary, groups.Detail}, groupsvc.EnsureOptions{})
	if got := r.Resolver.Calls(); len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("resolver calls = %v, want exactly one", got)
	}
	if r.Origin.CallCount() != 1 || f[groups.Detail].CallCount() != 1 {
		t.Fatalf("origin %d, detail %d calls", r.Origin.CallCount(), f[groups.Detail].CallCount())
	}
	if res.Summary.NodeID != "PR_9" || f[groups.Detail].Calls()[0].Targets[0].NodeID != "PR_9" {
		t.Errorf("answered for %+v", res.Summary)
	}
	// Known now: the same reference makes no second resolver call.
	ensure(t, r, "acme-org/widget#9", []string{groups.Summary}, groupsvc.EnsureOptions{})
	if len(r.Resolver.Calls()) != 1 {
		t.Errorf("resolver called again for a known reference")
	}
}

func TestANonexistentPRAndAMalformedIDHaveTheirSentinels(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	register(r, groups.Detail)
	ctx := context.Background()
	if _, err := r.Svc.Ensure(ctx, "acme-org/widget#404", []string{groups.Summary}, groupsvc.EnsureOptions{}); !errors.Is(err, groupsvc.ErrNoSuchPR) {
		t.Errorf("nonexistent PR: err = %v, want ErrNoSuchPR", err)
	}
	if r.Origin.CallCount() != 0 {
		t.Errorf("a PR that does not exist reached the summary fetch")
	}
	for _, id := range []string{"", "acme-org/widget#0", "not a reference#x", "acme-org/widget", "PR 1"} {
		if _, err := r.Svc.Ensure(ctx, id, []string{groups.Summary}, groupsvc.EnsureOptions{}); !errors.Is(err, groupsvc.ErrBadRef) {
			t.Errorf("Ensure(%q) err = %v, want ErrBadRef", id, err)
		}
	}
	if _, _, err := r.Svc.Resolve(ctx, "acme-org/widget#404"); !errors.Is(err, groupsvc.ErrNoSuchPR) {
		t.Errorf("Resolve: err = %v", err)
	}
	if n, ref, err := r.Svc.Resolve(ctx, "PR_5"); err != nil || n != "PR_5" || !ref.IsZero() {
		t.Errorf("Resolve(node id) = %q %v %v", n, ref, err)
	}
	if _, err := r.Svc.Ensure(ctx, node, []string{"bogus"}, groupsvc.EnsureOptions{}); err == nil {
		t.Error("an unknown group name was accepted")
	}
}

func TestAGroupWithoutAFetcherIsServedStaleOrUnavailableNamingIt(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	res := ensure(t, r, node, []string{groups.Detail, groups.Commits}, groupsvc.EnsureOptions{})
	if g := result(t, res, groups.Detail); g.Status != groupsvc.Stale || !errors.Is(g.Err, groupsvc.ErrNoFetcher) {
		t.Errorf("detail = %+v", g)
	}
	if g := result(t, res, groups.Commits); g.Status != groupsvc.Unavailable || !errors.Is(g.Err, groupsvc.ErrNoFetcher) {
		t.Errorf("commits = %+v", g)
	}
}

func TestServedReportsHowFarPastFreshUntilAStaleRowIs(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	f[groups.Detail].FailWith(errors.New("origin is down"))
	obs := newObserver()
	r.Svc.SetObserver(obs)
	seed(r, node, staleRow(r, node, groups.Detail, "old")) // fresh_until one minute ago
	res := ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if g := result(t, res, groups.Detail); g.Status != groupsvc.Stale {
		t.Fatalf("detail = %+v", g)
	}
	if got := obs.servedOf(groups.Detail); len(got) != 1 || got[0] != time.Minute {
		t.Errorf("Served = %v, want one call with a minute of staleness", got)
	}
}
