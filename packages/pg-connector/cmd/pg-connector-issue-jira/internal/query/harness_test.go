package query

import (
	"context"
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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

var quotedKey = regexp.MustCompile(`"([^"]+)"`)

// fakePjira is the inner internal.Runner of the harness. A `key in (...)` search
// answers the named issues; any other search answers the issues its JQL is
// mapped to. Every call is recorded.
type fakePjira struct {
	mu        sync.Mutex
	issues    map[string]string   // upper-case key to issue JSON
	queries   map[string][]string // JQL to the keys it lists
	truncated map[string]bool     // JQL to its truncated flag
	calls     [][]string
	err       error
}

func newFakePjira() *fakePjira {
	return &fakePjira{issues: map[string]string{}, queries: map[string][]string{}, truncated: map[string]bool{}}
}

func (f *fakePjira) Binary() string { return "/abs/pjira" }

func (f *fakePjira) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
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
	return fmt.Sprintf(`{"items":[%s],"truncated":%t}`, strings.Join(items, ","), f.truncated[jql]), nil
}

// setIssue stores or replaces an issue; category is the status category.
func (f *fakePjira) setIssue(key, category string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[key] = fmt.Sprintf(`{"key":"%s","summary":"title of %s","status":"Some Status","status_category":"%s","issuetype":"Task","project":"%s","updated":"2026-10-05T10:11:12.000+0000","comments":[]}`,
		key, key, category, strings.SplitN(key, "-", 2)[0])
}

// list maps a JQL to the keys it lists.
func (f *fakePjira) list(jql string, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries[jql] = keys
}

func (f *fakePjira) setTruncated(jql string, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.truncated[jql] = v
}

func (f *fakePjira) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakePjira) callList() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

var settingsRules = groups.Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: time.Hour, TerminalTTL: 24 * time.Hour}

// harness is a real store, scheduler, governor, origin runner, change engine and
// summary service around the Jira query runner, with a fake pjira and the manual
// clock.
type harness struct {
	t      *testing.T
	st     *store.Store
	clk    *governor.ManualClock
	gov    *governor.Governor
	fake   *fakePjira
	origin *origin.Runner
	sched  *scheduler.Scheduler
	pers   *summary.Persister
	eng    *changes.Engine
	svc    *summary.Service
	cfg    *config.Config
	runner *lq.Runner
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

// newHarness builds the harness with two configured queries: "mine" (watched)
// and "team" (not watched), each with one expression. rate is the request rate
// per second of the governor.
func newHarness(t *testing.T, rate int) *harness {
	t.Helper()
	st := openStore(t)
	clk := governor.NewManualClock(t0)
	h := &harness{t: t, st: st, clk: clk, fake: newFakePjira()}

	h.cfg = config.Defaults()
	h.cfg.RequestRatePerSecond = rate
	h.cfg.Queries = map[string]schema.QueryDef{
		"mine": {Expr: schema.QueryExpr{"assignee = me"}, Watched: true},
		"team": {Expr: schema.QueryExpr{"project = TEAM"}},
	}
	gov, err := governor.New(origin.GovernorConfig(h.cfg), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	h.gov = gov
	h.origin, err = origin.New(origin.Options{
		Inner: h.fake, Governor: gov, Bucket: origin.Bucket, Concurrency: 2,
		DefaultPause: time.Minute, Clock: clk, Jitter: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.eng, err = changes.New(changes.Config{Clock: clk, BodyCap: 64})
	if err != nil {
		t.Fatal(err)
	}
	h.pers = &summary.Persister{Store: st, Rules: groups.Rules(settingsRules), Clock: clk, Hook: h.eng.DetectSummary, OnGone: h.eng.DetectGone}
	h.sched = scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 2, PollInterval: 5 * time.Millisecond})
	h.svc, err = summary.NewService(summary.ServiceConfig{
		Store: st, Persister: h.pers, Fetcher: h.origin, Scheduler: h.sched,
		KeepAlive: summary.KeepAlive{Store: st, Clock: clk, TerminalTTL: settingsRules.TerminalTTL},
		Bucket:    origin.Bucket,
		Members:   func(ctx context.Context) (map[string]bool, error) { return Members(ctx, st) },
		Clock:     clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.runner, err = NewRunner(RunnerConfig{
		Cfg: h.cfg, Store: st, Persister: h.pers, Fetcher: h.origin, Scheduler: h.sched,
		Chg: h.eng, Summary: h.svc, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return h
}

// fetch runs the configured query name now (an interactive run).
func (h *harness) fetch(name string) store.QueryRow {
	h.t.Helper()
	row, err := h.runner.Fetch(context.Background(), lq.Key{Name: name})
	if err != nil {
		h.t.Fatalf("Fetch(%s): %v", name, err)
	}
	return row
}

// changeRows returns every row of the change log as "ID:kind,kind".
func (h *harness) changeRows() []string {
	h.t.Helper()
	rows, err := h.st.ChangesAfter(context.Background(), 0, 1000)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.ID+":"+strings.Join(r.Kinds, ","))
	}
	sort.Strings(out)
	return out
}

func (h *harness) watched() []string {
	h.t.Helper()
	rows, err := h.st.ListWatched(context.Background(), groups.EntityKind)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

// settle ends every pending change row now, as the settle lifecycle does at its
// deadline.
func (h *harness) settle() {
	h.t.Helper()
	l := &engine.Lifecycle{Engine: h.eng.Engine, Store: h.st, Clock: h.clk}
	if _, err := l.FlushAll(context.Background()); err != nil {
		h.t.Fatal(err)
	}
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

func governorConfigOf(h *harness) governor.Config { return origin.GovernorConfig(h.cfg) }

// newOriginOver is an origin runner over the harness's fake pjira and gov.
func newOriginOver(t *testing.T, h *harness, gov *governor.Governor) *origin.Runner {
	t.Helper()
	o, err := origin.New(origin.Options{
		Inner: h.fake, Governor: gov, Bucket: origin.Bucket, Concurrency: 2,
		DefaultPause: time.Minute, Clock: h.clk, Jitter: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}
