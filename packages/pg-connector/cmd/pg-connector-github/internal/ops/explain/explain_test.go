package explain

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

const (
	nodeID = "PR_node_1"
	ref    = "example-org/example-repo#42"
)

type fakeQueue map[[2]string]int

func (q fakeQueue) Position(entity, group string) (int, bool) {
	p, ok := q[[2]string{entity, group}]
	return p, ok
}

type rig struct {
	st    *store.Store
	queue fakeQueue
	r     *router.Router
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g := &rig{st: st, queue: fakeQueue{}, r: router.New(router.Options{Instances: []string{"pr", "ci"}})}
	if err := Register(g.r, Config{Store: st, Queue: g.queue, Clock: governor.NewManualClock(t0)}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.PutAlias(ctx, store.AliasRow{NodeID: nodeID, Ref: ref, SeenAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutEntity(ctx, store.EntityRow{Kind: entity.KindPR, ID: nodeID, Version: 1, LastAccess: t0.Add(-time.Minute), KeepAlive: true}); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *rig) putGroup(t *testing.T, row store.GroupRow) {
	t.Helper()
	row.Kind, row.ID = entity.KindPR, nodeID
	if err := g.st.PutGroup(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func (g *rig) explain(instance, args string) scriptout.Response {
	return g.r.Dispatch(context.Background(), router.Call{Instance: instance, Request: scriptout.Request{Op: OpName, Args: json.RawMessage(args)}})
}

func decode(t *testing.T, resp scriptout.Response) Result {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("explain answered an error: %+v", resp.Error)
	}
	var res Result
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestExplainShowsTheSummaryGroupOfAStoredEntity(t *testing.T) {
	g := newRig(t)
	g.putGroup(t, store.GroupRow{
		Group: "summary", Content: []byte(`{}`),
		FetchedAt: t0.Add(-10 * time.Minute), FreshUntil: t0.Add(5 * time.Minute), MaxAgeAt: t0.Add(24 * time.Hour),
		FetchedForHead: "head-sha-1", FetchedForBase: "base-sha-1",
	})
	g.queue[[2]string{nodeID, "summary"}] = 3

	for _, id := range []string{ref, strings.ToUpper(ref), nodeID} {
		res := decode(t, g.explain("pr", `{"id":"`+id+`"}`))
		if res.Instance != "pr" || res.Ref != ref || res.NodeID != nodeID || !res.KeepAlive || res.LastAccess == nil {
			t.Errorf("%s: %+v", id, res)
		}
		if len(res.Groups) != 1 {
			t.Fatalf("%s: groups %+v", id, res.Groups)
		}
		gr := res.Groups[0]
		if gr.Group != "summary" ||
			gr.FetchedAt == nil || !gr.FetchedAt.Equal(t0.Add(-10*time.Minute)) ||
			gr.FreshUntil == nil || !gr.FreshUntil.Equal(t0.Add(5*time.Minute)) ||
			gr.MaxAgeAt == nil || !gr.MaxAgeAt.Equal(t0.Add(24*time.Hour)) ||
			gr.FetchedForHead != "head-sha-1" || gr.FetchedForBase != "base-sha-1" ||
			gr.QueuePosition == nil || *gr.QueuePosition != 3 ||
			!gr.Fresh || gr.ExpiredByMaxAge || gr.LastError != "" {
			t.Errorf("%s: group %+v", id, gr)
		}
	}
}

func TestExplainSaysWhyAGroupIsStale(t *testing.T) {
	g := newRig(t)
	g.putGroup(t, store.GroupRow{
		Group:     "summary",
		FetchedAt: t0.Add(-2 * time.Hour), FreshUntil: t0.Add(-time.Hour), MaxAgeAt: t0.Add(-30 * time.Minute),
		LastError: "HTTP 502", FetchedForHead: "h", FetchedForBase: "b",
	})
	res := decode(t, g.explain("pr", `{"id":"`+ref+`"}`))
	gr := res.Groups[0]
	if gr.Fresh || !gr.ExpiredByMaxAge || gr.LastError != "HTTP 502" {
		t.Errorf("%+v", gr)
	}
	if gr.QueuePosition != nil {
		t.Errorf("a group with no queued task has no queue position: %d", *gr.QueuePosition)
	}

	// A group with no hard age is stale past fresh_until alone, and says no
	// max_age_at.
	g.putGroup(t, store.GroupRow{Group: "summary", FetchedAt: t0.Add(-2 * time.Hour), FreshUntil: t0.Add(-time.Hour)})
	gr = decode(t, g.explain("pr", `{"id":"`+ref+`"}`)).Groups[0]
	if gr.Fresh || gr.ExpiredByMaxAge || gr.MaxAgeAt != nil {
		t.Errorf("%+v", gr)
	}
}

func TestExplainUnknownIDIsNotFound(t *testing.T) {
	g := newRig(t)
	for _, inst := range []string{"pr", "ci"} {
		for _, id := range []string{"example-org/example-repo#999", "PR_unknown", "not an id"} {
			resp := g.explain(inst, `{"id":"`+id+`"}`)
			if resp.Error == nil || resp.Error.Code != "not_found" {
				t.Errorf("%s %q: %+v", inst, id, resp)
			}
			if resp.SchemaVersion == 0 {
				t.Errorf("%s %q: the error is stamped with the instance's schema version", inst, id)
			}
		}
	}
}

func TestExplainNeedsAnID(t *testing.T) {
	g := newRig(t)
	for _, args := range []string{``, `{}`, `{"id":""}`, `{"id":"  "}`} {
		if resp := g.explain("pr", args); resp.Error == nil || resp.Error.Code != "invalid_argument" {
			t.Errorf("%q: %+v", args, resp)
		}
	}
	if resp := g.explain("pr", `[1]`); resp.Error == nil || resp.Error.Code != "invalid_argument" {
		t.Errorf("malformed args: %+v", resp)
	}
}

func TestExplainOnTheCIInstanceHasNoCIGroupYet(t *testing.T) {
	g := newRig(t)
	g.putGroup(t, store.GroupRow{Group: "summary", FetchedAt: t0, FreshUntil: t0.Add(time.Minute)})
	res := decode(t, g.explain("ci", `{"id":"`+ref+`"}`))
	if res.Instance != "ci" || res.Ref != ref || len(res.Groups) != 0 || res.Note == "" {
		t.Errorf("%+v", res)
	}

	// The pr instance does not answer a CI group, and the ci instance does.
	g.putGroup(t, store.GroupRow{Group: "runs", FetchedAt: t0, FreshUntil: t0.Add(time.Minute), FetchedForHead: "h"})
	if res := decode(t, g.explain("ci", `{"id":"`+ref+`"}`)); len(res.Groups) != 1 || res.Groups[0].Group != "runs" || res.Note != "" {
		t.Errorf("ci: %+v", res)
	}
	if res := decode(t, g.explain("pr", `{"id":"`+ref+`"}`)); len(res.Groups) != 1 || res.Groups[0].Group != "summary" {
		t.Errorf("pr: %+v", res)
	}
}

func TestExplainOfAKnownEntityWithNoStoredGroupHasAnEmptyList(t *testing.T) {
	g := newRig(t)
	res := decode(t, g.explain("pr", `{"id":"`+ref+`"}`))
	if res.Groups == nil || len(res.Groups) != 0 {
		t.Errorf("groups must be an empty list, not null: %s", mustJSON(t, res))
	}
	if !strings.Contains(mustJSON(t, res), `"groups":[]`) {
		t.Errorf("%s", mustJSON(t, res))
	}
}

func TestExplainIsServedFromTheStoreAndMakesNoOriginCall(t *testing.T) {
	// The handler's only collaborators are the store and the scheduler's
	// position accessor: nothing in Config can reach the origin.
	if _, err := Handler(Config{}, "pr", 1); err == nil {
		t.Error("a handler needs its store and queue")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
