package diag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changeview"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// withChangeReader replaces the env's explain handlers by ones that carry the
// real change reader over the rig's store, as the assembly does.
func (e *env) withChangeReader() {
	e.t.Helper()
	reader, err := changeview.NewReader(changeview.Config{Store: e.rig.Store})
	if err != nil {
		e.t.Fatal(err)
	}
	deps := Deps{
		Store: e.rig.Store, Aliases: e.rig.Aliases, Queue: e.rig.Sched, Rules: e.rig.Rules, Jobs: e.rig.Store,
		Clock: e.rig.Clock, Changes: reader,
	}
	e.rt = router.New(router.Options{Instances: []string{"pr", "ci"}})
	for instance, ops := range ExplainHandlers(deps) {
		for op, h := range ops {
			e.rt.Register(instance, op, h)
		}
	}
}

func TestExplainShowsThePendingSettleRowAndTheLastChangesAndNoOriginCall(t *testing.T) {
	e := newEnv(t)
	e.everyGroup()
	e.withChangeReader()
	ctx := context.Background()
	now := e.rig.Clock.Now()
	id := e.ref(node).String()

	awaited, _ := json.Marshal([]changes.Awaiting{{Group: "files", Since: now.Add(-20 * time.Second)}, {Group: "commits", Since: now.Add(-10 * time.Second)}})
	err := e.rig.Store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.PutPending(ctx, store.PendingChange{
			Kind: changes.LogKindPR, ID: node, Kinds: []string{"head_changed"}, Awaited: awaited,
			Origin: "refresh", DetectedAt: now.Add(-25 * time.Second), Deadline: now.Add(5 * time.Second),
		}); err != nil {
			return err
		}
		for i := 0; i < 6; i++ {
			if _, err := tx.AppendChange(ctx, store.ChangeRow{
				Kind: changes.LogKindPR, ID: id, Version: int64(i + 1), Kinds: []string{"head_changed"}, Origin: "refresh", At: now.Add(time.Duration(i-10) * time.Minute),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, resolves := e.rig.Origin.CallCount(), len(e.rig.Resolver.Calls())

	for _, inst := range []string{"pr", "ci"} {
		res, raw := e.explainResult(inst, id)
		if inst == "pr" {
			goldenOf(t, "explain_pr_changes.golden.json", raw)
		}
		ps := res.PendingSettle
		if ps == nil || len(ps.Awaited) != 2 || ps.Awaited[0].Group != "files" || ps.Awaited[1].Group != "commits" ||
			!ps.Awaited[0].Since.Equal(now.Add(-20*time.Second)) || !ps.Deadline.Equal(now.Add(5*time.Second)) || strings.Join(ps.Kinds, ",") != "head_changed" {
			t.Errorf("%s: pending_settle %+v", inst, ps)
		}
		if len(res.LastChanges) != explain.MaxLastChanges || res.LastChanges[0].Seq != 6 || res.LastChanges[4].Seq != 2 {
			t.Errorf("%s: last_changes %+v (the five newest of six)", inst, res.LastChanges)
		}
		for _, g := range res.Groups {
			if g.Group == "files" && (g.FetchedForHead == "" || g.FetchedForBase == "") {
				t.Errorf("%s: files lost the head and base it was fetched for: %+v", inst, g)
			}
		}
	}
	if e.rig.Origin.CallCount() != calls || len(e.rig.Resolver.Calls()) != resolves {
		t.Error("explain made an origin call")
	}

	// A pull request with no pending row and no change rows omits both fields.
	e2 := newEnv(t)
	e2.everyGroup()
	e2.withChangeReader()
	_, raw := e2.explainResult("pr", e2.ref(node).String())
	if strings.Contains(string(raw), "pending_settle") || strings.Contains(string(raw), "last_changes") {
		t.Errorf("both fields must be omitted:\n%s", raw)
	}
}
