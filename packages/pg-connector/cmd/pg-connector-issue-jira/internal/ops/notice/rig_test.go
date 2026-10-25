package notice

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var (
	t0       = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	settings = groups.Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: 30 * time.Minute, TerminalTTL: 24 * time.Hour}
)

const (
	keyA    = "A-1"
	keyNew  = "N-9"
	lease   = 30 * time.Second
	oldText = "old title"
	newText = "new title"
)

var quotedKey = regexp.MustCompile(`"([^"]+)"`)

// fakeOrigin is the origin both services search through: it answers a search
// for the keys named in the JQL from its canned titles and counts every call.
type fakeOrigin struct {
	mu     sync.Mutex
	titles map[string]string
	calls  int
}

func (f *fakeOrigin) set(key, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.titles == nil {
		f.titles = map[string]string{}
	}
	f.titles[key] = title
}

func (f *fakeOrigin) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeOrigin) Search(_ context.Context, jql string, _ []string) (*internal.PjiraSearchResult, error) {
	f.mu.Lock()
	f.calls++
	var items []string
	for _, m := range quotedKey.FindAllStringSubmatch(jql, -1) {
		if title, ok := f.titles[m[1]]; ok {
			b, _ := json.Marshal(map[string]any{
				"key": m[1], "summary": title, "status": "Some Status",
				"status_category": "indeterminate", "issuetype": "Task",
				"project": strings.SplitN(m[1], "-", 2)[0], "updated": "2026-10-05T10:11:12.000+0000",
				"created": "2026-09-01T00:00:00.000+0000", "comments": []any{}, "changelog": []any{},
			})
			items = append(items, string(b))
		}
	}
	f.mu.Unlock()
	return internal.DecodeSearchResult(fmt.Sprintf(`{"items":[%s],"truncated":false}`, strings.Join(items, ",")))
}

// fakeTimers is the table's timer seam: a timer fires only when the test says.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (ft *fakeTimers) afterFunc(d time.Duration, f func()) func() bool {
	t := &fakeTimer{d: d, f: f}
	ft.mu.Lock()
	ft.timers = append(ft.timers, t)
	ft.mu.Unlock()
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

func (ft *fakeTimers) last() *fakeTimer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.timers[len(ft.timers)-1]
}

// fire runs the timer's function unless it was stopped, as the clock would.
func (ft *fakeTimers) fire(t *fakeTimer) {
	ft.mu.Lock()
	stopped := t.stopped
	ft.mu.Unlock()
	if !stopped {
		t.f()
	}
}

// fireStale runs the timer's function even though it was stopped: a timer that
// had already started when it was stopped.
func (ft *fakeTimers) fireStale(t *fakeTimer) { t.f() }

type rig struct {
	t      *testing.T
	st     *store.Store
	clk    *governor.ManualClock
	origin *fakeOrigin
	timers *fakeTimers
	table  *Table
	guard  *GuardStore
	pers   *summary.Persister
	sched  *scheduler.Scheduler
	sum    *summary.Service
	det    *detail.Service
	eng    *changes.Engine

	mu       sync.Mutex
	queries  int
	tableErr []error
	deps     Deps
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{t: t, st: st, clk: governor.NewManualClock(t0), origin: &fakeOrigin{}, timers: &fakeTimers{}}
	r.table = NewTable(TableConfig{
		Store: st, Clock: r.clk, Lease: lease, AfterFunc: r.timers.afterFunc,
		OnError: func(err error) {
			r.mu.Lock()
			r.tableErr = append(r.tableErr, err)
			r.mu.Unlock()
		},
	})
	r.guard = NewGuardStore(st, r.table)
	eng, err := changes.New(changes.Config{Clock: r.clk, BodyCap: 64})
	if err != nil {
		t.Fatal(err)
	}
	r.eng = eng
	rules := groups.Rules(settings)
	r.pers = &summary.Persister{Store: r.guard, Rules: rules, Clock: r.clk, Hook: eng.DetectSummary, OnGone: eng.DetectGone}
	r.sched = scheduler.New(scheduler.Options{Clock: r.clk, Workers: 1})
	keep := summary.KeepAlive{Store: st, Clock: r.clk, TerminalTTL: settings.TerminalTTL}
	r.sum, err = summary.NewService(summary.ServiceConfig{
		Store: st, Persister: r.pers, Fetcher: r.origin, Scheduler: r.sched, KeepAlive: keep, Clock: r.clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.det, err = detail.NewService(detail.Config{
		Store: r.guard, Scheduler: r.sched, Rules: rules, Aliases: summary.NewAliases(st),
		KeepAlive: keep, Origin: r.origin, Chg: eng, Clock: r.clk, BodyCap: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.table.Bind(r.sum, r.det)
	r.deps = Deps{
		Table: r.table, Summary: r.sum, Detail: r.det, Store: st, Clock: r.clk,
		Queries: func() []string {
			r.mu.Lock()
			r.queries++
			r.mu.Unlock()
			return []string{"q"}
		},
	}
	return r
}

func (r *rig) queryRuns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queries
}

func (r *rig) tableErrs() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.tableErr...)
}

// call runs one op handler.
func (r *rig) call(op string, args string) (any, error) {
	r.t.Helper()
	h, ok := Handlers(r.deps)[op]
	if !ok {
		r.t.Fatalf("no handler for %s", op)
	}
	return h.Handle(context.Background(), json.RawMessage(args))
}

func (r *rig) mustCall(op, args string) {
	r.t.Helper()
	res, err := r.call(op, args)
	if err != nil || res != nil {
		r.t.Fatalf("%s %s = %v, %v; want a null result", op, args, res, err)
	}
}

func summaryOf(key, title string) groups.SummaryContent {
	return groups.SummaryContent{
		Issue:   schema.Issue{ID: key, Title: title, StatusCategory: "indeterminate"},
		Updated: "2026-10-05T10:11:12.000+0000",
	}
}

// persist runs one whole refresh: token, fetch (here, the given content), commit.
func (r *rig) persist(c groups.SummaryContent) summary.Stored {
	r.t.Helper()
	tok := r.begin(c.Issue.ID)
	return r.commit(tok, c)
}

func (r *rig) begin(id string) groupkit.Token {
	r.t.Helper()
	tok, err := r.pers.Begin(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return tok
}

func (r *rig) commit(tok groupkit.Token, c groups.SummaryContent) summary.Stored {
	r.t.Helper()
	st, err := r.pers.Persist(context.Background(), tok, c, engine.RefreshInfo{Started: r.clk.Now(), Class: governor.ClassQuery, Purpose: governor.PurposeRefresh})
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

func (r *rig) watch(key string) {
	r.t.Helper()
	ctx := context.Background()
	if err := r.st.InTx(ctx, func(tx *store.Tx) error {
		_, err := tx.SetWatched(ctx, groups.EntityKind, key, "read", r.clk.Now())
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
}

// seed marks key watched (change rows exist only for a watched issue), stores its
// first summary and returns the change head after it.
func (r *rig) seed(key, title string) int64 {
	r.t.Helper()
	r.watch(key)
	if st := r.persist(summaryOf(key, title)); st.FreshUntil.IsZero() {
		r.t.Fatal("the seed summary was not stored")
	}
	return r.changeHead()
}

func (r *rig) changeHead() int64 {
	r.t.Helper()
	h, err := r.st.ChangeHead(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

func (r *rig) group(key, name string) (store.GroupRow, bool) {
	r.t.Helper()
	g, ok, err := r.st.GetGroup(context.Background(), groups.EntityKind, key, name)
	if err != nil {
		r.t.Fatal(err)
	}
	return g, ok
}

// storedTitle is the title in the stored summary of key.
func (r *rig) storedTitle(key string) string {
	r.t.Helper()
	g, ok := r.group(key, groups.Summary)
	if !ok {
		r.t.Fatalf("no summary row for %s", key)
	}
	var c groups.SummaryContent
	if err := json.Unmarshal(g.Content, &c); err != nil {
		r.t.Fatal(err)
	}
	return c.Issue.Title
}

func (r *rig) generation(key, name string) int64 {
	r.t.Helper()
	g, ok := r.group(key, name)
	if !ok {
		return 0
	}
	return g.Generation
}

// queued is the class of the queued task of (key, group); ok is false when none.
func (r *rig) queued(key, group string) (governor.Class, bool) {
	return r.sched.InspectQueued(key, group)
}
