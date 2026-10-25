package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	jquery "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	jqlMine = "assignee = me"
	jqlTeam = "project = TEAM"

	instance  = "main-instance"
	retention = 24 * time.Hour
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

var quotedKey = regexp.MustCompile(`"([^"]+)"`)

// fakePjira answers the searches of the real origin runner: a `key in (...)`
// search answers the named issues, any other search the keys its JQL is mapped
// to.
type fakePjira struct {
	mu      sync.Mutex
	issues  map[string]string
	queries map[string][]string
	calls   int
	err     error
}

func newFakePjira() *fakePjira {
	return &fakePjira{issues: map[string]string{}, queries: map[string][]string{}}
}

func (f *fakePjira) Binary() string { return "/abs/pjira" }

func (f *fakePjira) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if len(args) < 3 || args[0] != "search" {
		return "", fmt.Errorf("fakePjira: unexpected args %v", args)
	}
	jql := args[2]
	var keys []string
	if strings.HasPrefix(jql, "key in (") {
		for _, m := range quotedKey.FindAllStringSubmatch(jql, -1) {
			keys = append(keys, m[1])
		}
	} else {
		keys = f.queries[jql]
	}
	var items []string
	for _, k := range keys {
		if js, ok := f.issues[k]; ok {
			items = append(items, js)
		}
	}
	return fmt.Sprintf(`{"items":[%s],"truncated":false}`, strings.Join(items, ",")), nil
}

// setIssue stores or replaces an issue; category is the status category.
func (f *fakePjira) setIssue(key, category string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[key] = fmt.Sprintf(`{"key":"%s","summary":"title of %s","status":"Some Status","status_category":"%s","issuetype":"Task","project":"%s","updated":"2026-10-05T10:11:12.000+0000","comments":[]}`,
		key, key, category, strings.SplitN(key, "-", 2)[0])
}

func (f *fakePjira) list(jql string, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries[jql] = keys
}

func (f *fakePjira) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakePjira) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var settingsRules = groups.Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: time.Hour, TerminalTTL: 24 * time.Hour}

// rig is a real store, scheduler, governor, origin runner, change engine, query
// runner and feed around a fake pjira, with a manual clock.
type rig struct {
	t        *testing.T
	st       *store.Store
	clk      *governor.ManualClock
	fake     *fakePjira
	eng      *changes.Engine
	pers     *summary.Persister
	runner   *lq.Runner
	feed     *libfeed.Feed
	queries  libfeed.Queries
	handlers map[string]scriptout.OpHandler
	obs      *observer
}

type observer struct {
	mu   sync.Mutex
	rows int
}

func (o *observer) Redelivered(_, _ string, rows int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rows += rows
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newRig builds the rig with two configured queries: "mine" (watched) and
// "team" (not watched).
func newRig(t *testing.T) *rig {
	t.Helper()
	st := openStore(t)
	clk := governor.NewManualClock(t0)
	r := &rig{t: t, st: st, clk: clk, fake: newFakePjira(), obs: &observer{}}

	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 100
	cfg.Queries = map[string]schema.QueryDef{
		"mine": {Expr: schema.QueryExpr{jqlMine}, Watched: true},
		"team": {Expr: schema.QueryExpr{jqlTeam}},
	}
	gov, err := governor.New(origin.GovernorConfig(cfg), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	org, err := origin.New(origin.Options{
		Inner: r.fake, Governor: gov, Bucket: origin.Bucket, Concurrency: 2,
		DefaultPause: time.Minute, Clock: clk, Jitter: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.eng, err = changes.New(changes.Config{Clock: clk, BodyCap: 64})
	if err != nil {
		t.Fatal(err)
	}
	r.pers = &summary.Persister{Store: st, Rules: groups.Rules(settingsRules), Clock: clk, Hook: r.eng.DetectSummary, OnGone: r.eng.DetectGone}
	sched := scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 2, PollInterval: 5 * time.Millisecond})
	svc, err := summary.NewService(summary.ServiceConfig{
		Store: st, Persister: r.pers, Fetcher: org, Scheduler: sched,
		KeepAlive: summary.KeepAlive{Store: st, Clock: clk, TerminalTTL: settingsRules.TerminalTTL},
		Bucket:    origin.Bucket,
		Members:   func(ctx context.Context) (map[string]bool, error) { return jquery.Members(ctx, st) },
		Clock:     clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.runner, err = jquery.NewRunner(jquery.RunnerConfig{
		Cfg: cfg, Store: st, Persister: r.pers, Fetcher: org, Scheduler: sched,
		Chg: r.eng, Summary: svc, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	r.queries = NewQueries(r.runner, st, WithClock(clk))
	r.feed, err = New(Config{
		Store: st, Queries: r.queries, Clock: clk,
		Retention: retention, ConsumerStaleAfter: 7 * 24 * time.Hour, Observer: r.obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = Handlers(Deps{Feed: r.feed, Store: st, Renderer: NewRenderer(st), Instance: instance, Clock: clk})
	return r
}

// fetch runs the configured query name now (an interactive run).
func (r *rig) fetch(name string) store.QueryRow {
	r.t.Helper()
	row, err := r.runner.Fetch(context.Background(), lq.Key{Name: name})
	if err != nil {
		r.t.Fatalf("Fetch(%s): %v", name, err)
	}
	return row
}

// settle ends every pending change row now, as the settle lifecycle does at its
// deadline.
func (r *rig) settle() {
	r.t.Helper()
	l := &engine.Lifecycle{Engine: r.eng.Engine, Store: r.st, Clock: r.clk}
	if _, err := l.FlushAll(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

// logRows returns every row of the change log, oldest first.
func (r *rig) logRows() []store.ChangeRow {
	r.t.Helper()
	rows, err := r.st.ChangesAfter(context.Background(), 0, 1000)
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

// rowWords is the change log as "ID:kind,kind", oldest first.
func (r *rig) rowWords() []string {
	var out []string
	for _, row := range r.logRows() {
		out = append(out, row.ID+":"+strings.Join(row.Kinds, ","))
	}
	return out
}

func (r *rig) watched() []string {
	r.t.Helper()
	rows, err := r.st.ListWatched(context.Background(), groups.EntityKind)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range rows {
		out = append(out, e.ID)
	}
	sort.Strings(out)
	return out
}

// seed makes a baseline of "mine" with A-1 and A-2 and a baseline of "team" with
// T-1, all unchanged.
func (r *rig) seed() {
	r.t.Helper()
	r.fake.setIssue("A-1", "indeterminate")
	r.fake.setIssue("A-2", "indeterminate")
	r.fake.setIssue("T-1", "indeterminate")
	r.fake.list(jqlMine, "A-1", "A-2")
	r.fake.list(jqlTeam, "T-1")
	r.fetch("mine")
	r.fetch("team")
}

// call runs one op of the handlers and returns its answer or its error.
func (r *rig) call(op string, args map[string]any) (any, error) {
	r.t.Helper()
	h, ok := r.handlers[op]
	if !ok {
		r.t.Fatalf("no handler for %q", op)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		r.t.Fatal(err)
	}
	return h.Handle(context.Background(), raw)
}

// changesOf polls the feed (cached: the test controls the runs itself) and
// fails the test on an error.
func (r *rig) changesOf(consumer string, extra map[string]any) schema.ChangesResult {
	r.t.Helper()
	res, err := r.pollErr(consumer, extra)
	if err != nil {
		r.t.Fatalf("changes(%s): %v", consumer, err)
	}
	return res
}

func (r *rig) pollErr(consumer string, extra map[string]any) (schema.ChangesResult, error) {
	r.t.Helper()
	args := map[string]any{"consumer": consumer, "cached": true}
	for k, v := range extra {
		args[k] = v
	}
	out, err := r.call("changes", args)
	if err != nil {
		return schema.ChangesResult{}, err
	}
	res, ok := out.(schema.ChangesResult)
	if !ok {
		r.t.Fatalf("changes answered %T", out)
	}
	return res, nil
}

func (r *rig) ack(consumer string, seq int64, extra map[string]any) (schema.ChangesAckResult, error) {
	r.t.Helper()
	args := map[string]any{"consumer": consumer, "next_seq": seq}
	for k, v := range extra {
		args[k] = v
	}
	out, err := r.call("changes_ack", args)
	if err != nil {
		return schema.ChangesAckResult{}, err
	}
	res, ok := out.(schema.ChangesAckResult)
	if !ok {
		r.t.Fatalf("changes_ack answered %T", out)
	}
	return res, nil
}

// entityOf decodes the entity of one change.
func entityOf(t *testing.T, c schema.Change) schema.Issue {
	t.Helper()
	var is schema.Issue
	if err := json.Unmarshal(c.Entity, &is); err != nil {
		t.Fatalf("entity %s: %v", c.Entity, err)
	}
	return is
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
