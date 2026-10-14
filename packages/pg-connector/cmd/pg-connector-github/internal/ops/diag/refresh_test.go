package diag

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

var sixGroups = []string{"summary", "detail", "conversation", "files", "commits", "runs"}

func TestRefreshOfAPRQueuesTheSixDefaultGroupsAtClass2AfterInvalidatingThem(t *testing.T) {
	e := newEnv(t)
	e.seed(node, "detail", "conversation", "files", "commits", "pending", "runs")
	ref := e.ref(node)
	before, _ := e.rig.Group(node, groups.Detail)

	res := e.refresh("pr", `{"id":"`+ref.String()+`"}`)
	if len(res.Queued) != 1 || res.Queued[0].ID != ref.String() || !reflect.DeepEqual(res.Queued[0].Groups, sixGroups) {
		t.Fatalf("answer = %+v, want one entry for %s naming %v", res, ref, sixGroups)
	}
	for _, g := range sixGroups {
		if c := e.queued(node, g); c != governor.ClassInvalidated {
			t.Errorf("%s: queued at class %d, want 2", g, c)
		}
	}
	if c := e.queued(node, "pending"); c != 0 {
		t.Errorf("pending is queued only when named, but a task of class %d is queued", c)
	}
	// Exactly one class 2 task per (entity, group).
	if depth := e.rig.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth; depth != len(sixGroups) {
		t.Errorf("class 2 holds %d tasks, want %d", depth, len(sixGroups))
	}
	// Every group was invalidated: its generation moved and it is due now.
	for _, g := range []string{"detail", "conversation", "files", "commits", "runs"} {
		row, _ := e.rig.Group(node, g)
		if row.Generation <= before.Generation || row.FreshUntil.After(e.rig.Clock.Now()) {
			t.Errorf("%s was not invalidated: %+v", g, row)
		}
	}
	// The untouched pending group keeps its generation.
	if row, _ := e.rig.Group(node, "pending"); row.Generation != 1 {
		t.Errorf("pending was invalidated: %+v", row)
	}
}

func TestRefreshNamingPendingQueuesIt(t *testing.T) {
	e := newEnv(t)
	e.seed(node)
	res := e.refresh("pr", `{"id":"`+e.ref(node).String()+`","groups":["pending","summary","pending"]}`)
	if want := []string{"summary", "pending"}; !reflect.DeepEqual(res.Queued[0].Groups, want) {
		t.Fatalf("groups = %v, want %v (table order, no duplicates)", res.Queued[0].Groups, want)
	}
	if e.queued(node, "pending") != governor.ClassInvalidated || e.queued(node, "detail") != 0 {
		t.Error("only the named groups are queued")
	}
}

func TestCIRefreshQueuesTheSummaryAndRunsOnly(t *testing.T) {
	e := newEnv(t)
	e.seed(node)
	res := e.refresh("ci", `{"pr_id":"`+e.ref(node).String()+`"}`)
	if want := []string{"summary", "runs"}; !reflect.DeepEqual(res.Queued[0].Groups, want) {
		t.Fatalf("groups = %v, want %v", res.Queued[0].Groups, want)
	}
	for _, g := range []string{"detail", "conversation", "files", "commits", "pending"} {
		if e.queued(node, g) != 0 {
			t.Errorf("ci refresh queued %s", g)
		}
	}
	e.wantError("ci", OpRefresh, `{}`, "invalid_argument")
	e.wantError("ci", OpRefresh, `{"pr_id":""}`, "invalid_argument")
}

func TestARepeatedRefreshMergesIntoTheQueuedTasks(t *testing.T) {
	e := newEnv(t)
	e.seed(node)
	args := `{"id":"` + e.ref(node).String() + `"}`
	for i := 0; i < 3; i++ {
		e.refresh("pr", args)
		if depth := e.rig.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth; depth != len(sixGroups) {
			t.Fatalf("after refresh %d class 2 holds %d tasks, want one per group (%d)", i+1, depth, len(sixGroups))
		}
	}
	// Nothing else was queued at class 2 for another pair, and each pair is
	// queued once.
	for _, g := range sixGroups {
		if e.queued(node, g) != governor.ClassInvalidated {
			t.Errorf("%s is not queued at class 2", g)
		}
	}
}

func TestRefreshValidatesBeforeQueuingAnything(t *testing.T) {
	e := newEnv(t)
	e.seed(node)
	id := e.ref(node).String()
	for _, tc := range []struct{ args, code string }{
		{`{"id":"` + id + `","groups":["summary","nonsense"]}`, "invalid_argument"},
		{`{"query":"mine","groups":["nonsense"]}`, "invalid_argument"},
		{`{"query":"nope"}`, "query_not_recognized"},
		{`{}`, "invalid_argument"},
		{`{"id":"` + id + `","query":"mine"}`, "invalid_argument"},
		{`{"id":"not an id"}`, "invalid_argument"},
		{`[1]`, "invalid_argument"},
	} {
		e.wantError("pr", OpRefresh, tc.args, tc.code)
	}
	resp := e.call("pr", OpRefresh, `{"id":"`+id+`","groups":["nonsense"]}`)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "summary, detail, conversation, files, commits, pending, runs") {
		t.Errorf("an unknown group names the valid set: %+v", resp.Error)
	}
	if depth := e.rig.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth; depth != 0 {
		t.Errorf("%d class 2 tasks queued by refusals", depth)
	}
	if len(e.queries.queue) != 0 {
		t.Errorf("a query re-run was queued by a refusal: %+v", e.queries.queue)
	}
	if row, _ := e.rig.Group(node, "summary"); row.Generation != 1 {
		t.Errorf("a refusal invalidated the summary: %+v", row)
	}
}

func TestRefreshWithAQueryQueuesItsRerunAndTheGroupsOfEveryStoredMember(t *testing.T) {
	e := newEnv(t)
	other := "PR_node_2"
	e.seed(node, "detail")
	e.seed(other, "detail")
	e.rig.PutQuery("mine", e.ref(node).String(), e.ref(other).String(), "acme-org/widget#999", other)
	e.rig.PutQuery("mine", e.ref(node).String(), e.ref(other).String(), "acme-org/widget#999")

	res := e.refresh("pr", `{"query":"mine","groups":["summary","detail"]}`)
	if len(e.queries.queue) != 1 || e.queries.queue[0].Key.String() != "mine" || e.queries.queue[0].Class != governor.ClassInvalidated {
		t.Fatalf("query re-runs queued: %+v, want one for mine at class 2", e.queries.queue)
	}
	if len(res.Queued) != 2 {
		t.Fatalf("answer = %+v, want the two members the store holds (the unseen one has no entity)", res)
	}
	for _, id := range []string{node, other} {
		for _, g := range []string{"summary", "detail"} {
			if e.queued(id, g) != governor.ClassInvalidated {
				t.Errorf("%s %s is not queued at class 2", id, g)
			}
		}
		if e.queued(id, "files") != 0 {
			t.Errorf("%s: a group that was not named is queued", id)
		}
	}
}

func TestRefreshOfAnUnseenPRResolvesItOnceRecordsAnAccessAndQueuesOnlyTheSummary(t *testing.T) {
	e := newEnv(t)
	ref, err := entity.NewRef("acme-org/widget", 7)
	if err != nil {
		t.Fatal(err)
	}
	e.rig.Resolver.Know(ref, "PR_unseen")

	for _, inst := range []struct{ name, args string }{
		{"pr", `{"id":"` + ref.String() + `"}`},
		{"ci", `{"pr_id":"` + ref.String() + `"}`},
	} {
		res := e.refresh(inst.name, inst.args)
		if len(res.Queued) != 1 || res.Queued[0].ID != ref.String() || !reflect.DeepEqual(res.Queued[0].Groups, []string{"summary"}) {
			t.Fatalf("%s: answer = %+v, want the summary only", inst.name, res)
		}
	}
	if calls := e.rig.Resolver.Calls(); len(calls) != 1 {
		t.Errorf("the reference was resolved %d times, want once (the second call finds the alias)", len(calls))
	}
	ent, ok := e.rig.Entity("PR_unseen")
	if !ok || !ent.KeepAlive || ent.LastAccess.IsZero() {
		t.Errorf("entity = %+v %v, want a row in the keep-alive set with an access", ent, ok)
	}
	if e.queued("PR_unseen", "summary") != governor.ClassInvalidated {
		t.Errorf("summary queued at class %d, want 2", e.queued("PR_unseen", "summary"))
	}
	if e.queued("PR_unseen", "detail") != 0 || e.queued("PR_unseen", "runs") != 0 {
		t.Error("the other groups would be skipped by the executor until a summary exists, so they are not queued")
	}
	if e.rig.Origin.CallCount() != 0 {
		t.Error("the handler made a summary fetch")
	}
}

func TestRefreshOfAPRGitHubDoesNotKnowIsNotFoundAndResolverFailureIsUnavailable(t *testing.T) {
	e := newEnv(t)
	e.wantError("pr", OpRefresh, `{"id":"acme-org/widget#404"}`, "not_found")
	e.rig.Resolver.FailWith(context.DeadlineExceeded)
	e.wantError("pr", OpRefresh, `{"id":"acme-org/widget#405"}`, "unavailable")
	if depth := e.rig.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth; depth != 0 {
		t.Errorf("%d class 2 tasks queued by failures", depth)
	}
}

func TestRefreshReturnsBeforeAnyFetchCompletes(t *testing.T) {
	e := newEnv(t)
	e.seed(node, "detail", "conversation", "files", "commits", "pending", "runs")
	// Cleanups run last in first out: start the scheduler first so that the
	// fetches are released before its shutdown waits for them.
	e.rig.Start()
	release, entered := e.rig.Origin.Block()
	t.Cleanup(release)
	for _, f := range e.fetchers {
		t.Cleanup(f.Block())
	}

	done := make(chan RefreshResult, 1)
	go func() { done <- e.refresh("pr", `{"id":"`+e.ref(node).String()+`"}`) }()
	var res RefreshResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("refresh waited for a fetch")
	}
	if len(res.Queued) != 1 {
		t.Fatalf("answer = %+v", res)
	}
	// The fetches are really in flight, blocked, when the answer is out.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the summary fetch never started")
	}
	if !e.fetchers["detail"].WaitStarted(1, 10*time.Second) {
		t.Fatal("the detail fetch never started")
	}
}

func TestAfterARefreshALaterReadReturnsTheNewHeadOnceTheQueuedFetchCompletes(t *testing.T) {
	e := newEnv(t)
	e.rig.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ref := e.ref(node)

	first, err := e.rig.Svc.Ensure(ctx, node, []string{groups.Summary}, groupsvc.EnsureOptions{})
	if err != nil || first.Summary.HeadRefOid != "head-1" {
		t.Fatalf("first read = %+v, %v", first.Summary, err)
	}
	// The fresh summary is served from the store: a second read makes no call.
	calls := e.rig.Origin.CallCount()
	if _, err := e.rig.Svc.Ensure(ctx, node, []string{groups.Summary}, groupsvc.EnsureOptions{}); err != nil || e.rig.Origin.CallCount() != calls {
		t.Fatalf("a fresh summary was refetched (%d calls, was %d): %v", e.rig.Origin.CallCount(), calls, err)
	}

	// A caller pushes out of band, then refreshes.
	e.rig.Origin.Update(node, func(p *groupsvctest.PR) { p.HeadRefOid = "head-2" })
	e.refresh("pr", `{"id":"`+ref.String()+`","groups":["summary"]}`)
	row, _ := e.rig.Group(node, groups.Summary)
	if row.FreshUntil.After(e.rig.Clock.Now()) {
		t.Fatalf("the refresh left the summary fresh: %+v", row)
	}

	// The read waits on the queued fetch's flight (single-flight), not a sleep.
	second, err := e.rig.Svc.Ensure(ctx, node, []string{groups.Summary}, groupsvc.EnsureOptions{})
	if err != nil || second.Summary.HeadRefOid != "head-2" {
		t.Fatalf("read after refresh = head %q, %v; want head-2", second.Summary.HeadRefOid, err)
	}
	if e.rig.Origin.CallCount() > calls+1 {
		t.Errorf("%d origin calls after the refresh, want one fetch shared by the queued task and the read", e.rig.Origin.CallCount()-calls)
	}
}

func TestRefreshOpsAreStampedWithTheirInstancesSchemaVersion(t *testing.T) {
	e := newEnv(t)
	for _, inst := range []string{"pr", "ci"} {
		resp := e.call(inst, OpRefresh, `{}`)
		if resp.Error == nil || resp.SchemaVersion == 0 {
			t.Errorf("%s: %+v", inst, resp)
		}
	}
}
