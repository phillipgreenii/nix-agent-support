package serve

import (
	"fmt"

	jira "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/explain"
	opsfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/notice"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/read"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// opsWiring is what the ops are built from: every component startup assembled.
type opsWiring struct {
	cfg       *config.Config
	st        *store.Store
	gov       *governor.Governor
	sched     *scheduler.Scheduler
	chg       *changes.Engine
	auth      *origin.AuthState
	runner    *origin.Runner
	backend   *jira.Backend
	rules     groupkit.Table
	keepAlive summary.KeepAlive
	table     *notice.Table
	sum       *summary.Service
	det       *detail.Service
	qrun      *lq.Runner
	feed      *libfeed.Feed
	settle    *engine.Lifecycle
}

// OpNames is the union of the op names the sibling packages declare: what the
// daemon registers under Instance. The drift guard compares the registered table
// with it, so an op added to a sibling package and not registered (or the
// reverse) fails a test.
func OpNames() []string {
	var out []string
	out = append(out, read.Ops...)
	out = append(out, opsfeed.Ops...)
	out = append(out, notice.Ops...)
	out = append(out, status.Op)
	out = append(out, explain.Ops...)
	return out
}

// registerOps registers every op of the Jira backend under Instance.
func (d *daemon) registerOps(w opsWiring) error {
	handlers := []map[string]scriptout.OpHandler{
		read.Handlers(read.Deps{
			Auth: w.auth, Summary: w.sum, Detail: w.det, Runner: w.qrun,
			Settings: query.NewSettings(w.cfg), Chg: w.chg, Store: w.st, Backend: w.backend,
			Pause: w.runner, Expiry: w.cfg.Expiry.Std(),
		}),
		opsfeed.Handlers(opsfeed.Deps{
			Feed: w.feed, Store: w.st, Renderer: opsfeed.NewRenderer(w.st), Instance: w.cfg.Instance,
		}),
		notice.Handlers(notice.Deps{
			Table: w.table, Summary: w.sum, Detail: w.det, Store: w.st,
			Queries: func() []string { return d.requeueQueries(w.qrun) },
		}),
		explain.Handlers(explain.Deps{
			Store: w.st, Scheduler: w.sched, KeepAlive: w.keepAlive, Rules: w.rules,
		}),
	}
	for _, set := range handlers {
		for op, h := range set {
			d.rt.Register(Instance, op, h)
		}
	}
	return status.Register(d.rt, status.Config{
		RouterInstance: Instance, Identity: w.auth.Identity,
		Daemon: w.cfg, Version: d.o.Version, BuildID: d.o.Version, Started: d.started,
		Store: w.st, Governor: w.gov, Scheduler: w.sched, Settle: w.settle, Feed: w.feed, Auth: w.auth,
		Readings: w.runner.Readings(),
	})
}

// requeueQueries queues one re-run of every configured query (at the class of a
// write-invalidated task) and returns the names it queued.
func (d *daemon) requeueQueries(qrun *lq.Runner) []string {
	var queued []string
	for _, name := range qrun.Names() {
		if err := qrun.QueueRun(lq.Key{Name: name}, governor.ClassInvalidated); err != nil {
			d.log.Log("query_requeue_failed", "query", name, "error", fmt.Sprint(err))
			continue
		}
		queued = append(queued, name)
	}
	return queued
}
