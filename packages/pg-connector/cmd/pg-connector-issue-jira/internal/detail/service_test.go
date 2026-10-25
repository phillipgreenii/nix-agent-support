package detail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var (
	t0       = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	settings = groups.Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: 30 * time.Minute, TerminalTTL: 24 * time.Hour}
)

const (
	text429   = "pjira search: status 429 Too Many Requests"
	updated1  = "2026-10-05T10:11:12.000+0000"
	updated2  = "2026-10-05T10:11:13.000+0000"
	operator  = "operator@example.com"
	bodyCap   = 64
	issueKey  = "A-1"
	otherKey  = "B-2"
	closedCat = "done"
	openCat   = "indeterminate"
)

var quotedKey = regexp.MustCompile(`"([^"]+)"`)

// fakeIssue is one canned issue of the fake pjira.
type fakeIssue struct {
	Key, Category, Updated string
	Assignee               string // email
	Comments               []fakeComment
}

type fakeComment struct{ ID, Author, Body, Created string }

func (f fakeIssue) json() string {
	m := map[string]any{
		"key": f.Key, "summary": "title of " + f.Key, "status": "Some Status",
		"status_category": f.Category, "issuetype": "Task",
		"project": strings.SplitN(f.Key, "-", 2)[0], "updated": f.Updated,
		"created": "2026-09-01T00:00:00.000+0000",
	}
	if f.Assignee != "" {
		m["assignee"] = map[string]any{"email": f.Assignee}
	}
	cs := []map[string]any{}
	for _, c := range f.Comments {
		cs = append(cs, map[string]any{"id": c.ID, "author": map[string]any{"display_name": c.Author}, "body": c.Body, "created": c.Created})
	}
	m["comments"] = cs
	m["changelog"] = []any{}
	b, _ := json.Marshal(m)
	return string(b)
}

// fakePjira is the inner internal.Runner: it answers `pjira search` for the keys
// named in the JQL from canned issues and records every call.
type fakePjira struct {
	mu     sync.Mutex
	issues map[string]fakeIssue
	calls  [][]string
	err    error
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
	var items []string
	for _, m := range quotedKey.FindAllStringSubmatch(args[2], -1) {
		if is, ok := f.issues[m[1]]; ok {
			items = append(items, is.json())
		}
	}
	return fmt.Sprintf(`{"items":[%s],"truncated":false}`, strings.Join(items, ",")), nil
}

func (f *fakePjira) set(is fakeIssue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issues == nil {
		f.issues = map[string]fakeIssue{}
	}
	f.issues[is.Key] = is
}

func (f *fakePjira) remove(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.issues, key)
}

func (f *fakePjira) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakePjira) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakePjira) call(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

// harness is a real store, scheduler, governor, origin runner, change engine and
// summary service around the detail service, with a fake pjira and the manual
// clock.
type harness struct {
	t      *testing.T
	st     *store.Store
	clk    *governor.ManualClock
	gov    *governor.Governor
	fake   *fakePjira
	sched  *scheduler.Scheduler
	pers   *summary.Persister
	eng    *changes.Engine
	svc    *Service
	auth   *origin.AuthState
	runner *origin.Runner

	mu     sync.Mutex
	spends []governor.SpendEvent
	errs   []error
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

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := openStore(t)
	clk := governor.NewManualClock(t0)
	h := &harness{t: t, st: st, clk: clk, fake: &fakePjira{}, auth: origin.NewAuthState()}

	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 100
	gov, err := governor.New(origin.GovernorConfig(cfg), governor.Options{
		Clock: clk,
		OnSpend: func(e governor.SpendEvent) {
			h.mu.Lock()
			h.spends = append(h.spends, e)
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.gov = gov
	runner, err := origin.New(origin.Options{
		Inner: h.fake, Governor: gov, Bucket: origin.Bucket, Concurrency: 2,
		DefaultPause: time.Minute, Clock: clk, Jitter: func() float64 { return 0 }, Auth: h.auth,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.runner = runner
	eng, err := changes.New(changes.Config{Clock: clk, BodyCap: bodyCap})
	if err != nil {
		t.Fatal(err)
	}
	h.eng = eng
	rules := groups.Rules(settings)
	h.pers = &summary.Persister{Store: st, Rules: rules, Clock: clk, Hook: eng.DetectSummary, OnGone: eng.DetectGone}
	h.sched = scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 1})
	keep := summary.KeepAlive{Store: st, Clock: clk, TerminalTTL: settings.TerminalTTL}
	if _, err := summary.NewService(summary.ServiceConfig{
		Store: st, Persister: h.pers, Fetcher: runner, Scheduler: h.sched, KeepAlive: keep,
		Bucket: origin.Bucket, Clock: clk,
	}); err != nil {
		t.Fatal(err)
	}
	// The facts come from a real Backend whose runner is never called: the
	// issue as fetched carries everything.
	be := internal.New(neverRun{}).WithGetenv(func(k string) string {
		if k == internal.EnvEmail {
			return operator
		}
		return ""
	})
	svc, err := NewService(Config{
		Store: st, Scheduler: h.sched, Rules: rules, Aliases: summary.NewAliases(st),
		KeepAlive: keep, Governor: gov, Auth: h.auth, Origin: runner, Chg: eng,
		Facts: be.OperatorFacts, Clock: clk, BodyCap: bodyCap,
		OnError: func(err error) {
			h.mu.Lock()
			h.errs = append(h.errs, err)
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	h.pers.AfterCommit = svc.Revalidate
	return h
}

// neverRun is an internal.Runner that fails the test of any caller by erroring.
type neverRun struct{}

func (neverRun) Binary() string { return "/abs/pjira" }
func (neverRun) Run(context.Context, ...string) (string, error) {
	return "", errors.New("the operator facts made a pjira call")
}

func (h *harness) drain() { h.sched.Drain(context.Background()) }

func (h *harness) spendList() []governor.SpendEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]governor.SpendEvent(nil), h.spends...)
}

func (h *harness) errList() []error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]error(nil), h.errs...)
}

// summaryOf is the stored summary of an issue.
func summaryOf(key, category, title, updated string) groups.SummaryContent {
	return groups.SummaryContent{
		Issue:   schema.Issue{ID: key, Title: title, StatusCategory: category},
		Updated: updated,
	}
}

// persist stores a summary through the persister (so Revalidate runs after the
// commit) and returns what it stored.
func (h *harness) persist(c groups.SummaryContent) summary.Stored {
	h.t.Helper()
	ctx := context.Background()
	tok, err := h.pers.Begin(ctx, c.Issue.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	st, err := h.pers.Persist(ctx, tok, c, engine.RefreshInfo{Started: h.clk.Now(), Class: governor.ClassQuery, Purpose: governor.PurposeRefresh})
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) watch(key string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.InTx(ctx, func(tx *store.Tx) error {
		_, err := tx.SetWatched(ctx, groups.EntityKind, key, "read", h.clk.Now())
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) detailRow(key string) (store.GroupRow, bool) {
	h.t.Helper()
	g, ok, err := h.st.GetGroup(context.Background(), groups.EntityKind, key, groups.Detail)
	if err != nil {
		h.t.Fatal(err)
	}
	return g, ok
}

func (h *harness) detailContent(key string) groups.DetailContent {
	h.t.Helper()
	g, ok := h.detailRow(key)
	if !ok {
		h.t.Fatalf("no detail row for %s", key)
	}
	var d groups.DetailContent
	if err := json.Unmarshal(g.Content, &d); err != nil {
		h.t.Fatal(err)
	}
	return d
}

func (h *harness) changeRows() []store.ChangeRow {
	h.t.Helper()
	rows, err := h.st.ChangesAfter(context.Background(), 0, 1000)
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

func (h *harness) pending() int {
	h.t.Helper()
	ps, err := h.st.ListPending(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return len(ps)
}

func (h *harness) queuedDetail(key string) (governor.Class, bool) {
	return h.sched.InspectQueued(key, groups.Detail)
}

func (h *harness) entity(key string) store.EntityRow {
	h.t.Helper()
	e, ok, err := h.st.GetEntity(context.Background(), groups.EntityKind, key)
	if err != nil || !ok {
		h.t.Fatalf("entity %s: ok=%v err=%v", key, ok, err)
	}
	return e
}

func TestNewServiceNeedsItsParts(t *testing.T) {
	h := newHarness(t)
	full := Config{
		Store: h.st, Scheduler: h.sched, Rules: groups.Rules(settings), Aliases: summary.NewAliases(h.st),
		KeepAlive: summary.KeepAlive{Store: h.st}, Origin: &fakeOrigin{}, Chg: h.eng,
	}
	// The scheduler already holds the harness's detail executor: a second
	// service over it is a replacement, not an error.
	if _, err := NewService(full); err != nil {
		t.Fatalf("complete config: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"store":     func(c *Config) { c.Store = nil },
		"scheduler": func(c *Config) { c.Scheduler = nil },
		"aliases":   func(c *Config) { c.Aliases = nil },
		"keepalive": func(c *Config) { c.KeepAlive = nil },
		"origin":    func(c *Config) { c.Origin = nil },
		"changes":   func(c *Config) { c.Chg = nil },
		"rules":     func(c *Config) { c.Rules.Groups = nil },
	} {
		c := full
		mutate(&c)
		if _, err := NewService(c); err == nil {
			t.Errorf("a config without %s was accepted", name)
		}
	}
}

func TestServiceRefusesToRegisterThePrimaryGroup(t *testing.T) {
	h := newHarness(t)
	defer func() {
		if recover() == nil {
			t.Error("registering a fetcher for the summary group did not panic")
		}
	}()
	h.svc.Group().Register(&summaryFetcher{})
}

// summaryFetcher claims the primary group, which only the summary service owns.
type summaryFetcher struct{ Fetcher }

func (summaryFetcher) Group() string { return groups.Summary }

func TestEnsureMapsTheEntitySentinels(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{"", "pg2-abc", "ABC", `A-1") OR ("B-2`, "123-4"} {
		_, err := h.svc.Ensure(context.Background(), bad, []string{groups.Detail}, groupsvc.EnsureOptions{})
		if !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("Ensure(%q) = %v, want invalid_argument", bad, err)
		}
	}
	if h.fake.count() != 0 {
		t.Errorf("a malformed key reached the origin: %d calls", h.fake.count())
	}
}

func TestParseIDReturnsTheFoldedKey(t *testing.T) {
	wire, isRef, err := parseID("a-12")
	if err != nil || !isRef || wire != "A-12" {
		t.Errorf("parseID(a-12) = %q, %v, %v, want A-12, true, nil", wire, isRef, err)
	}
	if _, isRef, err := parseID("nonsense"); err == nil || isRef {
		t.Errorf("parseID(nonsense) = isRef %v, err %v, want a malformed id", isRef, err)
	}
}

func TestDescriberMarksATerminalIssueFromTheEntityRow(t *testing.T) {
	h := newHarness(t)
	h.persist(summaryOf(issueKey, closedCat, "closed", updated1))
	content, _ := json.Marshal(groups.Stored(summaryOf(issueKey, closedCat, "closed", updated1)))
	d := describer{store: h.st, rules: groups.Rules(settings), clock: h.clk}
	p, err := d.Describe(content)
	if err != nil || p.Terminal || p.Ref != issueKey {
		t.Fatalf("closed just now: %+v, %v, want Ref %s and not terminal", p, err, issueKey)
	}
	h.clk.Advance(25 * time.Hour)
	if p, _ = d.Describe(content); !p.Terminal {
		t.Error("closed for 25 hours is not terminal")
	}
	if _, err := d.Describe([]byte("{")); err == nil {
		t.Error("an undecodable summary was described")
	}
}

func TestEnsureFetchesTheDetailOfAKnownIssueThroughTheSharedService(t *testing.T) {
	h := newHarness(t)
	h.fake.set(fakeIssue{Key: issueKey, Category: openCat, Updated: updated1, Comments: []fakeComment{{"1", "Pat", "hello", "2026-10-04T10:00:00.000+0000"}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	res, err := h.svc.Ensure(ctx, "a-1", []string{groups.Summary, groups.Detail}, groupsvc.EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 2 {
		t.Fatalf("groups = %+v", res.Groups)
	}
	for _, g := range res.Groups {
		if g.Status != groupsvc.Fresh || g.Err != nil {
			t.Errorf("group %s = %+v, want fresh", g.Group, g)
		}
	}
	if d := h.detailContent(issueKey); len(d.Comments) != 1 || d.Comments[0].Body != "hello" {
		t.Errorf("stored detail = %+v", d)
	}
}

// fakeOrigin is a detail.Origin with a canned answer.
type fakeOrigin struct {
	mu    sync.Mutex
	res   *internal.PjiraSearchResult
	err   error
	jqls  []string
	exps  [][]string
	calls int
}

func (f *fakeOrigin) Search(_ context.Context, jql string, expand []string) (*internal.PjiraSearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.jqls = append(f.jqls, jql)
	f.exps = append(f.exps, expand)
	if f.err != nil {
		return nil, f.err
	}
	if f.res == nil {
		return &internal.PjiraSearchResult{}, nil
	}
	return f.res, nil
}
