package ciops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const rerun4 = "repos/acme-org/widget/actions/runs/4/rerun-failed-jobs"

// rerunnableRuns is the PR's runs as GitHub lists them (newest first): a passing
// run, then two that can be rerun. Once a rerun was accepted (a POST was seen)
// run 4 is reported in progress, as GitHub does.
func rerunnableRuns(g *fakeGH) ghAnswer {
	if len(g.posts) > 0 {
		return ok(runsBody(
			runJSON(5, "lint", head1, "completed", "success", 1),
			runJSON(4, "ci", head1, "in_progress", "", 2),
			runJSON(3, "ci", head1, "completed", "failure", 1),
		))
	}
	return ok(runsBody(
		runJSON(5, "lint", head1, "completed", "success", 1),
		runJSON(4, "ci", head1, "completed", "timed_out", 1),
		runJSON(3, "ci", head1, "completed", "failure", 1),
	))
}

func newRerunEnv(t *testing.T) *env {
	t.Helper()
	gh := &fakeGH{}
	gh.runs = func(string, int) ghAnswer { return rerunnableRuns(gh) }
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	return e
}

func rerun(e *env, pr string) error {
	e.t.Helper()
	got, err := e.call(OpRerunFailed, map[string]any{"pr_id": pr})
	if err == nil && got != nil {
		e.t.Errorf("rerun_failed result = %#v, want nil as the old op", got)
	}
	return err
}

func TestRerunFailedRerunsTheNewestRerunnableRunByRepositoryExplicitREST(t *testing.T) {
	e := newRerunEnv(t)
	if err := rerun(e, ref1); err != nil {
		t.Fatal(err)
	}
	// Run 5 passed; run 4 (timed_out) is the newest that can be rerun; run 3 is older.
	if got := e.gh.postList(); !reflect.DeepEqual(got, []string{rerun4}) {
		t.Fatalf("posts = %v, want exactly %q", got, rerun4)
	}
	var argv []string
	for i := range e.gh.argv {
		if a := e.gh.argvOf(i); a[len(a)-1] == "POST" {
			argv = a
		}
	}
	if want := []string{"api", "--include", rerun4, "--method", "POST"}; !reflect.DeepEqual(argv, want) {
		t.Errorf("gh argv = %v, want %v (no `gh run rerun`, the repository in the path)", argv, want)
	}
}

func TestRerunFailedIsANoOpSuccessWhenARunOfTheCurrentHeadIsActive(t *testing.T) {
	for _, status := range []string{"queued", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			gh := &fakeGH{runs: func(string, int) ghAnswer {
				return ok(runsBody(
					runJSON(9, "ci", head1, status, "", 1),
					runJSON(4, "ci", head1, "completed", "failure", 1),
				))
			}}
			e := newEnv(t, envOptions{gh: gh})
			e.seedPR(node1)
			e.seedRuns(node1, false, ciRun("4", "ci", head1, "completed", "failure"))
			before, _ := e.rig.Group(node1, groups.Runs)

			if err := rerun(e, ref1); err != nil {
				t.Fatal(err)
			}
			if gh.postCount() != 0 {
				t.Errorf("%d rerun requests sent, want none", gh.postCount())
			}
			after, _ := e.rig.Group(node1, groups.Runs)
			if after.Generation != before.Generation {
				t.Error("the no-op invalidated the runs group")
			}
		})
	}
}

func TestRerunFailedAnActiveRunOfAnOlderHeadDoesNotBlockTheRerun(t *testing.T) {
	gh := &fakeGH{runs: func(string, int) ghAnswer {
		return ok(runsBody(
			runJSON(9, "preview", "head-old", "in_progress", "", 1),
			runJSON(4, "ci", head1, "completed", "failure", 1),
		))
	}}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	if err := rerun(e, ref1); err != nil {
		t.Fatal(err)
	}
	if got := gh.postList(); len(got) != 1 {
		t.Errorf("posts = %v, want one rerun", got)
	}
}

func TestRerunFailedWithNoRerunnableRunIsNotFound(t *testing.T) {
	gh := &fakeGH{runs: func(string, int) ghAnswer {
		return ok(runsBody(
			runJSON(7, "lint", head1, "completed", "success", 1),
			runJSON(6, "docs", head1, "completed", "skipped", 1),
			runJSON(5, "gate", head1, "completed", "action_required", 1),
		))
	}}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	err := rerun(e, ref1)
	if code(err) != "not_found" || !strings.Contains(err.Error(), "no failed runs to rerun") {
		t.Fatalf("err = %v, want not_found \"no failed runs to rerun\"", err)
	}
	if gh.postCount() != 0 {
		t.Error("a rerun was sent")
	}
	// And for a PR with no runs at all.
	empty := &fakeGH{}
	e2 := newEnv(t, envOptions{gh: empty})
	e2.seedPR(node1)
	if err := rerun(e2, ref1); code(err) != "not_found" {
		t.Errorf("no runs: err = %v", err)
	}
}

func TestRerunFailedRetriedAfterTheFirstSuccessIsTheNoOp(t *testing.T) {
	e := newRerunEnv(t)
	if err := rerun(e, ref1); err != nil {
		t.Fatal(err)
	}
	if err := rerun(e, ref1); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if e.gh.postCount() != 1 {
		t.Errorf("%d reruns sent over two calls, want 1", e.gh.postCount())
	}
}

func TestRerunFailedRetriedAfterAnUnknownOutcomeIsSafe(t *testing.T) {
	// The first request is answered with a gateway error (the daemon cannot tell
	// whether GitHub accepted it), but GitHub did accept it: the run is now active.
	gh := &fakeGH{}
	accepted := false
	gh.runs = func(string, int) ghAnswer {
		if accepted {
			return ok(runsBody(runJSON(4, "ci", head1, "in_progress", "", 2)))
		}
		return ok(runsBody(runJSON(4, "ci", head1, "completed", "failure", 1)))
	}
	gh.post = func(string, int) ghAnswer {
		accepted = true
		return ghAnswer{status: 502, body: `{"message":"Bad Gateway"}`}
	}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	if err := rerun(e, ref1); code(err) != "unavailable" {
		t.Fatalf("first call: err = %v, want unavailable", err)
	}
	if err := rerun(e, ref1); err != nil {
		t.Fatalf("the retry after the unknown outcome: %v", err)
	}
	if gh.postCount() != 1 {
		t.Errorf("%d reruns sent, want the retry to send none", gh.postCount())
	}
}

func TestRerunFailedSuccessInvalidatesExactlyTheRunsGroupAndQueuesOneClass2Refresh(t *testing.T) {
	e := newRerunEnv(t)
	detail := e.rig.Register(groupsvctest.NewFetcher(groups.Detail))
	e.rig.SeedGroup(freshGroup(e, node1, groups.Detail, "d"))
	e.seedRuns(node1, false, ciRun("4", "ci", head1, "completed", "timed_out"))
	runsBefore, _ := e.rig.Group(node1, groups.Runs)
	detailBefore, _ := e.rig.Group(node1, groups.Detail)
	sumBefore, _ := e.rig.Group(node1, groups.Summary)

	if err := rerun(e, ref1); err != nil {
		t.Fatal(err)
	}

	// Exactly one refresh of the runs group, at class 2, and it was stored.
	eventually(t, "the queued runs refresh", func() bool { return e.rec.countBy(governor.PurposeRefresh) >= 1 })
	eventually(t, "the refresh to be stored", func() bool { return len(e.refreshes()) >= 1 })
	time.Sleep(100 * time.Millisecond) // a second refresh, if one was queued, would have run by now
	var refresh []governor.Class
	for _, m := range e.rec.metas() {
		if m.Purpose == governor.PurposeRefresh {
			refresh = append(refresh, m.Class)
		}
	}
	if !reflect.DeepEqual(refresh, []governor.Class{governor.ClassInvalidated}) {
		t.Errorf("refresh classes = %v, want exactly one class 2 refresh", refresh)
	}
	runsAfter, _ := e.rig.Group(node1, groups.Runs)
	if runsAfter.Generation <= runsBefore.Generation {
		t.Errorf("runs generation %d -> %d, want it bumped", runsBefore.Generation, runsAfter.Generation)
	}
	for _, r := range e.refreshes() {
		if r.Group != groups.Runs {
			t.Errorf("a %s group was refreshed", r.Group)
		}
	}
	if got, _ := e.rig.Group(node1, groups.Detail); !reflect.DeepEqual(got, detailBefore) {
		t.Errorf("detail row changed: %+v -> %+v", detailBefore, got)
	}
	if detail.CallCount() != 0 {
		t.Errorf("the detail group was fetched %d times", detail.CallCount())
	}
	if got, _ := e.rig.Group(node1, groups.Summary); got.Generation < sumBefore.Generation {
		t.Errorf("summary row went backwards: %+v", got)
	}
}

func TestRerunFailedAFailedRequestInvalidatesNothing(t *testing.T) {
	cases := []struct {
		name string
		ans  ghAnswer
		want string
	}{
		{"server error", ghAnswer{status: 500, body: `{"message":"boom"}`}, "unavailable"},
		{"missing run, HTTP error status only", ghAnswer{status: 404, body: `{"message":"Not Found"}`}, "not_found"},
		{"missing run, gh exit status", ghAnswer{
			status: 404, body: `{"message":"Not Found"}`,
			err: errors.New("gh api: exit status 1: gh: Not Found (HTTP 404)"),
		}, "not_found"},
		{"forbidden", ghAnswer{status: 403, body: `{"message":"Resource not accessible by integration"}`}, "unauthenticated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newRerunEnv(t)
			e.gh.post = func(string, int) ghAnswer { return c.ans }
			e.seedRuns(node1, false, ciRun("4", "ci", head1, "completed", "timed_out"))
			before, _ := e.rig.Group(node1, groups.Runs)

			err := rerun(e, ref1)
			if code(err) != c.want {
				t.Fatalf("err = %v (%s), want %s", err, code(err), c.want)
			}
			if e.gh.postCount() != 1 {
				t.Fatalf("%d requests sent, want the one that failed", e.gh.postCount())
			}
			after, _ := e.rig.Group(node1, groups.Runs)
			if !reflect.DeepEqual(after, before) {
				t.Errorf("runs row changed: %+v -> %+v", before, after)
			}
			if d := e.rig.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth; d != 0 {
				t.Errorf("%d class 2 tasks queued after a failed request", d)
			}
			time.Sleep(50 * time.Millisecond)
			if n := e.rec.countBy(governor.PurposeRefresh); n != 0 {
				t.Errorf("%d refreshes ran after a failed request", n)
			}
		})
	}
}

func TestRerunFailedSpendsAsClass2PurposeWrite(t *testing.T) {
	e := newRerunEnv(t)
	if err := rerun(e, ref1); err != nil {
		t.Fatal(err)
	}
	var writes []governor.SpendEvent
	for _, ev := range e.spends(governor.BucketRESTCore) {
		if ev.Purpose == governor.PurposeWrite {
			writes = append(writes, ev)
		}
	}
	// The precondition read (runs and the jobs of the current head's failures) and
	// the rerun itself: every origin REST request the call made.
	want := e.rec.countBy(governor.PurposeWrite) + len(e.gh.jobCalls) + e.gh.postCount()
	if len(writes) != want || e.gh.postCount() != 1 {
		t.Fatalf("%d write spends for %d requests (%d runs, %d jobs, %d posts)", len(writes), want,
			e.rec.countBy(governor.PurposeWrite), len(e.gh.jobCalls), e.gh.postCount())
	}
	for _, ev := range writes {
		if ev.Class != governor.ClassInvalidated || ev.Group != groups.Runs {
			t.Errorf("spend %+v, want class 2 in the runs group", ev)
		}
	}
	// The only other origin spend of the call is the one fresh summary read.
	for _, ev := range e.spends(governor.BucketRESTCore) {
		if ev.Purpose != governor.PurposeWrite && ev.Purpose != governor.PurposeRefresh {
			t.Errorf("unexpected REST spend %+v", ev)
		}
	}
}

func TestRerunFailedReadsTheCurrentHeadFromOriginNotTheStoredSummary(t *testing.T) {
	e := newRerunEnv(t) // the stored summary says head-1
	// A push the daemon has not seen: the origin's head is now head-2.
	e.rig.Origin.Update(node1, func(p *groupsvctest.PR) { p.HeadRefOid = "head-2" })
	gh := e.gh
	gh.runs = func(string, int) ghAnswer {
		// A run of the OLD head is in progress; the failed run is on the new head.
		return ok(runsBody(
			runJSON(8, "ci", "head-2", "completed", "failure", 1),
			runJSON(7, "ci", head1, "in_progress", "", 1),
		))
	}
	before := e.rig.Origin.CallCount()
	if err := rerun(e, ref1); err != nil {
		t.Fatal(err)
	}
	if e.rig.Origin.CallCount() != before+1 {
		t.Errorf("%d summary calls, want exactly 1 fresh read", e.rig.Origin.CallCount()-before)
	}
	if got := gh.postList(); !reflect.DeepEqual(got, []string{"repos/acme-org/widget/actions/runs/8/rerun-failed-jobs"}) {
		t.Errorf("posts = %v, want the rerun of the failed run of the new head", got)
	}
}

func TestRerunFailedRefusesWhatItCannotDo(t *testing.T) {
	e := newRerunEnv(t)
	cases := []struct {
		name string
		args any
		want string
	}{
		{"no id", map[string]any{}, "invalid_argument"},
		{"blank id", map[string]any{"pr_id": " "}, "invalid_argument"},
		{"malformed id", map[string]any{"pr_id": "acme-org/widget"}, "invalid_argument"},
		{"bad arguments", "not an object", "invalid_argument"},
		{"unknown PR", map[string]any{"pr_id": "acme-org/nowhere#9"}, "not_found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := e.call(OpRerunFailed, c.args); code(err) != c.want {
				t.Errorf("err = %v (%s), want %s", err, code(err), c.want)
			}
		})
	}
	if e.gh.postCount() != 0 {
		t.Error("a rerun was sent")
	}
}

func TestRerunFailedIsUnavailableWhenTheGovernorRefusesTheRerun(t *testing.T) {
	// The REST core cap leaves exactly the requests of the precondition read (one
	// runs list and the jobs of the two failures) and nothing for the rerun.
	cfg := governor.Config{
		GraphQL: governor.Limits{Cap: 1000}, RESTCore: governor.Limits{Cap: 3}, RESTSearch: governor.Limits{Cap: 1000},
		BackgroundShare: 1,
	}
	gh := &fakeGH{}
	gh.runs = func(string, int) ghAnswer { return rerunnableRuns(gh) }
	e := newEnv(t, envOptions{govConfig: &cfg, gh: gh})
	e.seedPR(node1)
	err := rerun(e, ref1)
	if code(err) != "unavailable" || !strings.Contains(err.Error(), "cap reached") {
		t.Fatalf("err = %v, want unavailable naming the cap", err)
	}
	if gh.postCount() != 0 {
		t.Error("the rerun was sent past the cap")
	}
}

// twoPRs adds PR_2 (its own branch and head) to the fake origin and the store.
func twoPRs(e *env) {
	e.rig.Origin.Set(node2, groupsvctest.PR{
		Repo: "acme-org/widget", Number: 2, State: "OPEN", HeadRefOid: "head-2", BaseRefOid: "base-1", HeadRefName: "feature-b",
	})
	e.seedPR(node2)
}

func TestRerunFailedSameLockSerializesWritesToOnePR(t *testing.T) {
	e := newRerunEnv(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.gh.onPost = func(string) {
		once.Do(func() { close(entered) })
		<-release
	}
	results := make(chan error, 2)
	go func() { results <- rerun(e, ref1) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first rerun never reached its request")
	}
	// The second call (the same PR, spelled by its node id) waits for the lock:
	// it makes no origin request, not even its precondition read.
	go func() { results <- rerun(e, node1) }()
	time.Sleep(150 * time.Millisecond)
	if n := e.gh.runsCount(); n != 1 {
		t.Errorf("%d runs reads while the first write holds the lock, want 1 (the first call's)", n)
	}
	select {
	case err := <-results:
		t.Fatalf("a call finished (%v) while the first write was in flight", err)
	default:
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	// The second call ran after the first and found the run active: the no-op.
	// (Precondition reads only: the refresh the first write queued is a read too.)
	if reads := e.rec.countBy(governor.PurposeWrite); e.gh.postCount() != 1 || reads != 2 {
		t.Errorf("%d reruns and %d precondition reads, want 1 and 2", e.gh.postCount(), reads)
	}
}

func TestRerunFailedWritesToDifferentPRsRunInParallel(t *testing.T) {
	gh := &fakeGH{}
	gh.runs = func(branch string, _ int) ghAnswer {
		return ok(runsBody(runJSON(100, "ci", map[string]string{"feature": "head-1", "feature-b": "head-2"}[branch], "completed", "failure", 1)))
	}
	e := newEnv(t, envOptions{gh: gh})
	e.seedPR(node1)
	twoPRs(e)
	var arrived atomic.Int32
	both := make(chan struct{})
	gh.onPost = func(string) {
		if arrived.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both: // each POST can only finish once the other one has started
		case <-time.After(10 * time.Second):
			t.Error("the other PR's rerun never started: the writes were serialized")
		}
	}
	errs := make(chan error, 2)
	go func() { errs <- rerun(e, ref1) }()
	go func() { errs <- rerun(e, "acme-org/widget#2") }()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Errorf("rerun: %v", err)
		}
	}
	if gh.postCount() != 2 {
		t.Errorf("%d reruns, want 2", gh.postCount())
	}
}

func TestRerunFailedLockWaitEndsAtTheCallersDeadline(t *testing.T) {
	e := newRerunEnv(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.gh.onPost = func(string) {
		once.Do(func() { close(entered) })
		<-release
	}
	first := make(chan error, 1)
	go func() { first <- rerun(e, ref1) }()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.callCtx(ctx, OpRerunFailed, map[string]any{"pr_id": ref1})
	if code(err) != "unavailable" || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("err = %v, want unavailable naming the deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the wait took %v, want it to end at the deadline", d)
	}
	if e.gh.runsCount() != 1 || e.gh.postCount() != 1 {
		t.Errorf("the waiter made requests: %d runs reads, %d posts", e.gh.runsCount(), e.gh.postCount())
	}
	close(release)
	if err := <-first; err != nil {
		t.Errorf("first call: %v", err)
	}
}

// freshGroup is a fresh stored row of a group with the given content, for the
// head and base of the origin's summary of the node id.
func freshGroup(e *env, node, group, content string) (row store.GroupRow) {
	sum := e.rig.Origin.Summary(node)
	fresh, maxAge := e.rig.Rules.Schedule(group, false, e.clk.Now())
	return store.GroupRow{
		ID: node, Group: group, Content: []byte(content), Fingerprint: groups.Fingerprint([]byte(content)),
		FetchedAt: e.clk.Now(), FreshUntil: fresh, MaxAgeAt: maxAge,
		FetchedForHead: sum.HeadRefOid, FetchedForBase: sum.BaseRefOid, Generation: 1,
	}
}

func TestRerunFailedARunsRefreshBegunBeforeTheWriteNeitherOverwritesNorReportsAChange(t *testing.T) {
	// The engine's runs fetcher is a fake that blocks inside Fetch; the write's own
	// precondition read goes through the real runs fetcher over the canned gh.
	engine := groupsvctest.NewFetcher(groups.Runs)
	engine.SetOutcome(func(t groups.Target, n int) groups.Outcome {
		mark := "after"
		if n == 1 {
			mark = "superseded"
		}
		return groups.Outcome{Content: []byte(`{"runs":[],"mark":"` + mark + `"}`), Stamps: t.Stamps, Cost: 1}
	})
	gh := &fakeGH{}
	gh.runs = func(string, int) ghAnswer { return rerunnableRuns(gh) }
	e := newEnv(t, envOptions{gh: gh, engineRuns: engine})
	e.seedPR(node1)

	release := engine.Block()
	defer release()
	// The refresh that started BEFORE the write.
	if err := e.rig.Svc.QueueGroup(context.Background(), node1, groups.Runs, governor.ClassKeepAlive, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if !engine.WaitStarted(1, 10*time.Second) {
		t.Fatal("the refresh never started")
	}

	if err := rerun(e, ref1); err != nil {
		t.Fatal(err)
	}
	if gh.postCount() != 1 {
		t.Fatalf("%d reruns, want 1", gh.postCount())
	}
	// The write is done; now the old refresh finishes, after it.
	release()
	eventually(t, "the refresh queued by the write to be stored", func() bool {
		row, ok := e.rig.Group(node1, groups.Runs)
		return ok && strings.Contains(string(row.Content), `"after"`)
	})
	if n := engine.CallCount(); n != 2 {
		t.Errorf("%d engine fetches, want the superseded one and the one the write queued", n)
	}
	row, _ := e.rig.Group(node1, groups.Runs)
	if strings.Contains(string(row.Content), "superseded") {
		t.Errorf("the refresh begun before the write overwrote its invalidation: %s", row.Content)
	}
	for _, r := range e.refreshes() {
		if strings.Contains(string(r.Content), "superseded") {
			t.Errorf("a change was reported from the superseded refresh: %+v", r)
		}
	}
	if n := len(e.refreshes()); n != 1 {
		t.Errorf("%d refreshes reported, want only the one queued by the write", n)
	}
}
