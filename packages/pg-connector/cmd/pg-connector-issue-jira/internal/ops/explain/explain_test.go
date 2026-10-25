package explain

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var (
	t0       = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	settings = groups.Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: time.Hour, TerminalTTL: 24 * time.Hour}
)

const key = "ABC-1"

// originCalls counts every call that reaches the fake origin: the origin
// runner and the scheduler's summary executor (which is what would fetch). The
// handler has no origin in its Deps, so a non-zero count means it reached one
// through the scheduler.
type originCalls struct{ n atomic.Int64 }

func (o *originCalls) Run(context.Context, ...string) (string, error) {
	o.n.Add(1)
	return "", errors.New("the origin must not be called")
}

func (o *originCalls) Binary() string { return "/abs/pjira" }

// failingExecutor is registered for the summary and detail tasks: running one
// is an origin call.
type failingExecutor struct{ origin *originCalls }

func (failingExecutor) MaxBatch() int                              { return 1 }
func (failingExecutor) Spend(governor.Class, int) governor.Request { return governor.Request{} }
func (e failingExecutor) Execute(_ context.Context, items []scheduler.Item) []scheduler.Result {
	_, _ = e.origin.Run(context.Background())
	return make([]scheduler.Result, len(items))
}

type rig struct {
	t      *testing.T
	st     *store.Store
	sched  *scheduler.Scheduler
	clk    *governor.ManualClock
	origin *originCalls
	ops    map[string]scriptout.OpHandler
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	origin := &originCalls{}
	// The scheduler is never run: tasks stay queued, so positions are stable.
	sched := scheduler.New(scheduler.Options{Clock: clk})
	for _, g := range groups.All() {
		sched.Register(g, failingExecutor{origin})
	}
	g := &rig{t: t, st: st, sched: sched, clk: clk, origin: origin}
	g.ops = Handlers(Deps{
		Store: st, Scheduler: sched, Clock: clk,
		KeepAlive: summary.KeepAlive{Store: st, Clock: clk, TerminalTTL: settings.TerminalTTL},
		Rules:     groups.Rules(settings),
	})
	t.Cleanup(func() {
		if n := g.origin.n.Load(); n != 0 {
			t.Errorf("explain reached the origin %d times", n)
		}
	})
	return g
}

func (g *rig) entity(id string) {
	g.t.Helper()
	if err := g.st.PutEntity(context.Background(), store.EntityRow{Kind: groups.EntityKind, ID: id, Version: 1, LastAccess: t0, KeepAlive: true}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) watch(id, via string) {
	g.t.Helper()
	err := g.st.InTx(context.Background(), func(tx *store.Tx) error {
		_, err := tx.SetWatched(context.Background(), groups.EntityKind, id, via, t0)
		return err
	})
	if err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) group(id, group string, mut func(*store.GroupRow)) {
	g.t.Helper()
	row := store.GroupRow{
		Kind: groups.EntityKind, ID: id, Group: group, Content: []byte(`{}`), Fingerprint: "f",
		FetchedAt: t0, FreshUntil: t0.Add(time.Minute), Generation: 1,
	}
	if mut != nil {
		mut(&row)
	}
	if err := g.st.PutGroup(context.Background(), row); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) query(name string, ids ...string) {
	g.t.Helper()
	if err := g.st.PutQuery(context.Background(), store.QueryRow{Key: name, Configured: true, IDs: ids, Complete: true, FetchedAt: t0}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) queue(id, group string, due time.Time) {
	g.t.Helper()
	if err := g.sched.Submit(scheduler.Task{Kind: group, Entity: id, Group: group, Class: governor.ClassKeepAlive, Due: due, Access: t0}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) explain(args string) (Result, error) {
	g.t.Helper()
	out, err := g.ops[OpName].Handle(context.Background(), json.RawMessage(args))
	if err != nil {
		return Result{}, err
	}
	res, ok := out.(Result)
	if !ok {
		g.t.Fatalf("answer is %T, not a Result", out)
	}
	return res, nil
}

func (g *rig) must(args string) Result {
	g.t.Helper()
	res, err := g.explain(args)
	if err != nil {
		g.t.Fatalf("explain %s: %v", args, err)
	}
	return res
}

func byGroup(res Result, name string) GroupExplain {
	for _, g := range res.Groups {
		if g.Group == name {
			return g
		}
	}
	panic("no group " + name)
}

func TestHandlersAreExactlyExplainAtTheIssueSchemaVersion(t *testing.T) {
	g := newRig(t)
	if len(g.ops) != 1 || !reflect.DeepEqual(Ops, []string{"explain"}) {
		t.Fatalf("handlers %v, Ops %v", g.ops, Ops)
	}
	h, ok := g.ops["explain"]
	if !ok || h.SchemaVersion != schema.IssueSchemaVersion || h.Handle == nil {
		t.Errorf("explain handler %+v, want schema version %d", h, schema.IssueSchemaVersion)
	}
}

func TestHandlersPanicWithoutAStoreOrScheduler(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a wiring mistake did not panic")
		}
	}()
	Handlers(Deps{})
}

func TestUnknownAndNonJiraIDsAreNotFoundAndABlankIDIsInvalid(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	for _, id := range []string{"ZZZ-9", "not a key", "ABC", "ABC-", "owner/repo#1"} {
		if _, err := g.explain(`{"id":"` + id + `"}`); !errors.Is(err, scriptout.ErrNotFound) {
			t.Errorf("%q: %v, want not_found", id, err)
		}
	}
	for _, args := range []string{``, `{}`, `{"id":" "}`, `[1]`} {
		if _, err := g.explain(args); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("%q: %v, want invalid_argument", args, err)
		}
	}
}

func TestExplainFoldsTheKeyAndNamesNoIssueContent(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	g.group(key, groups.Summary, func(r *store.GroupRow) { r.Content = []byte(`{"issue":{"title":"SECRET TITLE"}}`) })
	res := g.must(`{"id":"abc-1"}`)
	if res.ID != key {
		t.Errorf("ID %q, want %q", res.ID, key)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("not JSON: %s", raw)
	}
	if strings.Contains(string(raw), "SECRET TITLE") {
		t.Errorf("the answer carries issue content: %s", raw)
	}
}

func TestGroupsReportFreshnessErrorsAndQueuePosition(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	g.group(key, groups.Summary, nil)
	g.group(key, groups.Detail, func(r *store.GroupRow) {
		r.FetchedAt, r.FreshUntil, r.MaxAgeAt = t0.Add(-2*time.Hour), t0.Add(-time.Hour), t0.Add(-30*time.Minute)
		r.LastError = "pjira: status 502"
	})
	g.queue(key, groups.Summary, t0)
	// A task of another issue due earlier ranks ahead of this one.
	g.queue("ZZZ-1", groups.Summary, t0.Add(-time.Minute))

	res := g.must(`{"id":"` + key + `"}`)
	if len(res.Groups) != 2 || res.Groups[0].Group != "summary" || res.Groups[1].Group != "detail" {
		t.Fatalf("groups %+v, want summary then detail", res.Groups)
	}
	s, d := byGroup(res, "summary"), byGroup(res, "detail")
	if !s.Fresh || s.FreshReason != "" || !s.FetchedAt.Equal(t0) || !s.FreshUntil.Equal(t0.Add(time.Minute)) || !s.MaxAgeAt.IsZero() {
		t.Errorf("summary %+v", s)
	}
	if s.QueuePosition != 2 || !s.Queued || s.Running {
		t.Errorf("a queued summary task behind an earlier one has position 2: %+v", s)
	}
	if d.Fresh || d.FreshReason == "" || d.LastError != "pjira: status 502" || !d.MaxAgeAt.Equal(t0.Add(-30*time.Minute)) {
		t.Errorf("detail %+v, want not fresh with its error and max age", d)
	}
	if d.QueuePosition != -1 || d.Queued || d.Running {
		t.Errorf("a group with no task has position -1: %+v", d)
	}
}

func TestAGroupNeverStoredIsReportedNotFreshWithItsQueuePosition(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	g.queue(key, groups.Detail, t0)
	res := g.must(`{"id":"` + key + `"}`)
	d := byGroup(res, "detail")
	if d.Fresh || d.FreshReason != "no content stored" || !d.FetchedAt.IsZero() || d.QueuePosition != 1 || !d.Queued {
		t.Errorf("%+v", d)
	}
	if s := byGroup(res, "summary"); s.Fresh || s.QueuePosition != -1 {
		t.Errorf("%+v", s)
	}
}

func TestWatchedByAQueryReportsItsCauseAndStaysWatchedAfterLeavingEveryQuery(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	g.watch(key, registry.ViaQuery+"q")
	g.query("q", key)

	res := g.must(`{"id":"` + key + `"}`)
	if !res.Watched || res.WatchedVia != "query:q" || !res.WatchedAt.Equal(t0) {
		t.Errorf("watched %v via %q at %v, want query:q", res.Watched, res.WatchedVia, res.WatchedAt)
	}
	if !res.Tracked || res.TrackedWhy != TrackedMember {
		t.Errorf("tracked %v why %q, want member", res.Tracked, res.TrackedWhy)
	}

	// The query no longer lists the issue: the flag is sticky and so is the cause.
	g.query("q")
	res = g.must(`{"id":"` + key + `"}`)
	if !res.Watched || res.WatchedVia != "query:q" {
		t.Errorf("an issue that left every query is still watched: %+v", res)
	}
	if !res.Tracked || res.TrackedWhy != TrackedWatched || !res.RetainedByKeepAlive {
		t.Errorf("a watched, retained non-member is tracked as watched: %+v", res)
	}
}

func TestDirectlyWatchedAndReadOnlyIssues(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	g.watch(key, registry.ViaDirect)
	if res := g.must(`{"id":"` + key + `"}`); !res.Watched || res.WatchedVia != "direct" {
		t.Errorf("%+v", res)
	}

	g.entity("DEF-2") // read by a caller only
	res := g.must(`{"id":"DEF-2"}`)
	if res.Watched || res.WatchedVia != "" || !res.WatchedAt.IsZero() {
		t.Errorf("an unwatched issue reports no watch: %+v", res)
	}
	if !res.Tracked || res.TrackedWhy != TrackedRead || !res.RetainedByKeepAlive {
		t.Errorf("an open issue read by a caller is tracked as read: %+v", res)
	}
}

func TestAnIssueClosedPastTheKeepAliveCutoffIsNoLongerTracked(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	if err := g.st.SetClosedAt(context.Background(), groups.EntityKind, key, t0); err != nil {
		t.Fatal(err)
	}
	res := g.must(`{"id":"` + key + `"}`)
	if !res.Tracked || !res.RetainedByKeepAlive || !res.ClosedAt.Equal(t0) {
		t.Errorf("closed inside terminal_ttl: %+v", res)
	}
	g.clk.Advance(25 * time.Hour)
	res = g.must(`{"id":"` + key + `"}`)
	if res.Tracked || res.TrackedWhy != "" || res.RetainedByKeepAlive || !res.ClosedAt.Equal(t0) {
		t.Errorf("closed past terminal_ttl: %+v", res)
	}
}

func TestATombstonedIssueIsExplainedAndNotTracked(t *testing.T) {
	g := newRig(t)
	if err := g.st.PutEntity(context.Background(), store.EntityRow{Kind: groups.EntityKind, ID: key, Version: 1, LastAccess: t0, KeepAlive: true, TombstonedAt: t0}); err != nil {
		t.Fatal(err)
	}
	g.query("q", key) // even a listed member is not tracked once tombstoned
	res := g.must(`{"id":"` + key + `"}`)
	if !res.Tombstoned || res.Tracked || res.RetainedByKeepAlive {
		t.Errorf("%+v", res)
	}
}

func TestPendingSettleRowListsTheAwaitedGroupsAndDeadline(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	if res := g.must(`{"id":"` + key + `"}`); res.Pending != nil {
		t.Errorf("no pending row: %+v", res.Pending)
	}
	aw, err := json.Marshal([]engine.Awaiting{{Group: groups.Detail, Since: t0}})
	if err != nil {
		t.Fatal(err)
	}
	put := func(id string, aw json.RawMessage) {
		err := g.st.InTx(context.Background(), func(tx *store.Tx) error {
			return tx.PutPending(context.Background(), store.PendingChange{
				Kind: changes.LogKindIssue, ID: id, Kinds: []string{"status_changed"}, Awaited: aw,
				DetectedAt: t0, Deadline: t0.Add(30 * time.Second),
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("DEF-2", json.RawMessage(`[{"group":"summary","since":"2026-10-05T12:00:00Z"}]`)) // another issue's
	put(key, aw)
	res := g.must(`{"id":"` + key + `"}`)
	if res.Pending == nil || !reflect.DeepEqual(res.Pending.Awaited, []string{"detail"}) || !res.Pending.Deadline.Equal(t0.Add(30*time.Second)) {
		t.Errorf("pending %+v, want awaiting detail by t0+30s", res.Pending)
	}
}

func TestLastChangesAreTheFiveNewestOfThisIssueNewestFirst(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	if res := g.must(`{"id":"` + key + `"}`); res.LastChanges != nil {
		t.Errorf("no change rows: %+v", res.LastChanges)
	}
	err := g.st.InTx(context.Background(), func(tx *store.Tx) error {
		for i := 1; i <= 7; i++ {
			c := store.ChangeRow{Kind: changes.LogKindIssue, ID: key, Version: int64(i), Kinds: []string{"status_changed"}, Origin: "refresh", At: t0.Add(time.Duration(i) * time.Minute)}
			if _, err := tx.AppendChange(context.Background(), c); err != nil {
				return err
			}
			other := c
			other.ID = "DEF-2"
			if _, err := tx.AppendChange(context.Background(), other); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res := g.must(`{"id":"` + key + `"}`)
	if len(res.LastChanges) != MaxLastChanges {
		t.Fatalf("%d rows, want %d", len(res.LastChanges), MaxLastChanges)
	}
	for i := 1; i < len(res.LastChanges); i++ {
		if res.LastChanges[i].Seq >= res.LastChanges[i-1].Seq {
			t.Errorf("not newest first: %+v", res.LastChanges)
		}
	}
	c := res.LastChanges[0]
	if !c.At.Equal(t0.Add(7*time.Minute)) || c.Origin != "refresh" || !reflect.DeepEqual(c.Kinds, []string{"status_changed"}) {
		t.Errorf("newest row %+v", c)
	}
}

func TestExplainNeverRunsATask(t *testing.T) {
	g := newRig(t)
	g.entity(key)
	g.group(key, groups.Summary, nil)
	g.queue(key, groups.Summary, t0)
	g.queue(key, groups.Detail, t0)
	_ = g.must(`{"id":"` + key + `"}`)
	if n := g.origin.n.Load(); n != 0 {
		t.Errorf("origin calls %d", n)
	}
	if !g.sched.Queued(key, groups.Summary) || !g.sched.Queued(key, groups.Detail) {
		t.Error("explain disturbed the queue")
	}
}

func TestResultJSONNames(t *testing.T) {
	raw, err := json.Marshal(Result{
		Groups:      []GroupExplain{{Group: "summary"}},
		Pending:     &PendingExplain{Awaited: []string{"detail"}},
		LastChanges: []ChangeExplain{{Seq: 1, Kinds: []string{"k"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id", "tracked", "retained_by_keep_alive", "watched", "groups", "pending", "last_changes"} {
		if _, ok := doc[name]; !ok {
			t.Errorf("%q missing from %s", name, raw)
		}
	}
}
