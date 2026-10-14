package diag

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const node = "PR_node_1"

// fakeQueries is the query runner as refresh sees it: the stored rows are the
// real store's, and a queued re-run is only recorded.
type fakeQueries struct {
	st    *store.Store
	queue []queuedRun
}

type queuedRun struct {
	Key   query.Key
	Class governor.Class
}

func (q *fakeQueries) Row(ctx context.Context, key query.Key) (store.QueryRow, bool, error) {
	return q.st.GetQuery(ctx, key.String())
}

func (q *fakeQueries) QueueRun(key query.Key, class governor.Class) error {
	q.queue = append(q.queue, queuedRun{key, class})
	return nil
}

// env is the real engine over the groupsvc test rig, with a fake fetcher for
// every non-summary group, the handlers of both ops on a router.
type env struct {
	t        *testing.T
	rig      *groupsvctest.Rig
	fetchers map[string]*groupsvctest.Fetcher
	queries  *fakeQueries
	cfg      *config.Config
	rt       *router.Router
}

func newEnv(t *testing.T) *env {
	t.Helper()
	rig := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	e := &env{t: t, rig: rig, fetchers: map[string]*groupsvctest.Fetcher{}}
	for _, g := range groups.All() {
		if g != groups.Summary {
			e.fetchers[g] = rig.Register(groupsvctest.NewFetcher(g))
		}
	}
	e.queries = &fakeQueries{st: rig.Store}
	e.cfg = config.Defaults()
	e.cfg.PR.Queries = map[string]schema.QueryExpr{"mine": {"is:open author:@me"}}
	deps := Deps{
		Engine: rig.Svc, Summaries: rig.Summary, Queries: e.queries, Config: e.cfg,
		Store: rig.Store, Aliases: rig.Aliases,
		Queue: rig.Sched, Rules: rig.Rules, Jobs: rig.Store, Clock: rig.Clock,
	}
	e.rt = router.New(router.Options{Instances: []string{"pr", "ci"}})
	for _, set := range []map[string]map[string]scriptout.OpHandler{RefreshHandlers(deps), ExplainHandlers(deps)} {
		for instance, ops := range set {
			for op, h := range ops {
				e.rt.Register(instance, op, h)
			}
		}
	}
	return e
}

func (e *env) call(instance, op, args string) scriptout.Response {
	e.t.Helper()
	return e.rt.Dispatch(context.Background(), router.Call{
		Instance: instance, Request: scriptout.Request{Op: op, Args: json.RawMessage(args)},
	})
}

func (e *env) refresh(instance, args string) RefreshResult {
	e.t.Helper()
	resp := e.call(instance, OpRefresh, args)
	if resp.Error != nil {
		e.t.Fatalf("refresh %s %s answered an error: %+v", instance, args, resp.Error)
	}
	var res RefreshResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) wantError(instance, op, args, code string) {
	e.t.Helper()
	resp := e.call(instance, op, args)
	if resp.Error == nil || resp.Error.Code != code {
		e.t.Errorf("%s %s %s: got %+v, want error %s", instance, op, args, resp, code)
	}
}

// seed stores the summary of node, a fresh row of every non-summary group named
// and an entity accessed now, as a fetch at the rig's now would have left them.
func (e *env) seed(id string, names ...string) {
	e.t.Helper()
	e.rig.SeedSummary(id, e.rig.Clock.Now())
	for _, g := range names {
		e.rig.SeedGroup(e.row(id, g))
	}
}

func (e *env) row(id, group string) store.GroupRow {
	sum := e.rig.Origin.Summary(id)
	fresh, maxAge := e.rig.Rules.Schedule(group, false, e.rig.Clock.Now())
	content := `{"seeded":true}`
	if group == groups.Runs {
		content = `{"runs":[]}`
	}
	return store.GroupRow{
		ID: id, Group: group, Content: []byte(content), Fingerprint: groups.Fingerprint([]byte(content)),
		FetchedAt: e.rig.Clock.Now(), FreshUntil: fresh, MaxAgeAt: maxAge,
		FetchedForHead: sum.HeadRefOid, FetchedForBase: sum.BaseRefOid, Generation: 1,
	}
}

// queued reports the class of the queued task of (entity, group), 0 when none.
func (e *env) queued(id, group string) governor.Class {
	c, ok := e.rig.Sched.InspectQueued(id, group)
	if !ok {
		return 0
	}
	return c
}

func (e *env) ref(id string) entity.Ref { return e.rig.Origin.Ref(id) }
