package summary

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const text429 = "pjira search: status 429 Too Many Requests"

var quotedKey = regexp.MustCompile(`"([^"]+)"`)

// fakePjira is the inner internal.Runner of the harness: it answers a
// `pjira search` for the keys named in its JQL from a map of canned issue
// JSON, and records every call.
type fakePjira struct {
	mu        sync.Mutex
	issues    map[string]string // upper-case key to issue JSON
	calls     [][]string
	err       error
	truncated bool
	// beforeReturn, when set, runs after the call is recorded and before it
	// answers: the seam a test uses to interleave a write with the fetch.
	beforeReturn func()
}

func (f *fakePjira) Binary() string { return "/abs/pjira" }

func (f *fakePjira) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	before, err, truncated := f.beforeReturn, f.err, f.truncated
	f.mu.Unlock()
	if before != nil {
		before()
	}
	if err != nil {
		return "", err
	}
	if len(args) < 3 || args[0] != "search" {
		return "", fmt.Errorf("fakePjira: unexpected args %v", args)
	}
	var items []string
	f.mu.Lock()
	for _, m := range quotedKey.FindAllStringSubmatch(args[2], -1) {
		if js, ok := f.issues[m[1]]; ok {
			items = append(items, js)
		}
	}
	f.mu.Unlock()
	return fmt.Sprintf(`{"items":[%s],"truncated":%t}`, strings.Join(items, ","), truncated), nil
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

func (f *fakePjira) set(key, category string, commentCreated ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issues == nil {
		f.issues = map[string]string{}
	}
	f.issues[key] = pj(key, category, commentCreated...)
}

func (f *fakePjira) remove(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.issues, key)
}

// pj is the JSON of one pjira issue.
func pj(key, category string, commentCreated ...string) string {
	var cs []string
	for i, c := range commentCreated {
		cs = append(cs, fmt.Sprintf(`{"id":"%d","body":"b","created":"%s"}`, i+1, c))
	}
	return fmt.Sprintf(`{"key":"%s","summary":"title of %s","status":"Some Status","status_category":"%s","issuetype":"Task","project":"%s","updated":"2026-10-05T10:11:12.000+0000","comments":[%s]}`,
		key, key, category, strings.SplitN(key, "-", 2)[0], strings.Join(cs, ","))
}

// observer records the batches the executor sent.
type observer struct {
	mu   sync.Mutex
	due  [][]string
	fill [][]string
	gone []string
}

func (o *observer) BatchSent(ids []string, role scheduler.Role) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if role == scheduler.RoleDue {
		o.due = append(o.due, append([]string(nil), ids...))
	} else {
		o.fill = append(o.fill, append([]string(nil), ids...))
	}
}

func (o *observer) Gone(id string) {
	o.mu.Lock()
	o.gone = append(o.gone, id)
	o.mu.Unlock()
}

type harnessOpts struct {
	fillHorizon time.Duration
	expiry      time.Duration
	batchSize   int
}

// harness is a real store, scheduler, governor, origin runner and change
// engine around the service, with a fake pjira and the manual clock.
type harness struct {
	t      *testing.T
	st     *store.Store
	clk    *governor.ManualClock
	gov    *governor.Governor
	fake   *fakePjira
	sched  *scheduler.Scheduler
	pers   *Persister
	eng    *changes.Engine
	svc    *Service
	obs    *observer
	mu     sync.Mutex
	spends []governor.SpendEvent

	memMu   sync.Mutex
	members map[string]bool
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	st := openStore(t)
	clk := governor.NewManualClock(t0)
	h := &harness{t: t, st: st, clk: clk, fake: &fakePjira{}, obs: &observer{}, members: map[string]bool{}}

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
		DefaultPause: time.Minute, Clock: clk, Jitter: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := changes.New(changes.Config{Clock: clk, BodyCap: 64})
	if err != nil {
		t.Fatal(err)
	}
	h.eng = eng
	h.pers = &Persister{Store: st, Rules: groups.Rules(settings), Clock: clk, Hook: eng.DetectSummary, OnGone: eng.DetectGone}
	h.sched = scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 1, FillHorizon: o.fillHorizon})
	svc, err := NewService(ServiceConfig{
		Store: st, Persister: h.pers, Fetcher: runner, Scheduler: h.sched,
		KeepAlive: KeepAlive{Store: st, Clock: clk, TerminalTTL: settings.TerminalTTL},
		Bucket:    origin.Bucket, Expiry: o.expiry, BatchSize: o.batchSize,
		Members: func(context.Context) (map[string]bool, error) {
			h.memMu.Lock()
			defer h.memMu.Unlock()
			out := make(map[string]bool, len(h.members))
			for k, v := range h.members {
				out[k] = v
			}
			return out, nil
		},
		Observer: h.obs, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

// seed stores the issue as the summary of a first sighting (open or closed).
func (h *harness) seed(id, category string) Stored {
	h.t.Helper()
	return persist(h.t, h.pers, issue(id, category, "seeded"))
}

func (h *harness) setMember(id string, member bool) {
	h.memMu.Lock()
	h.members[id] = member
	h.memMu.Unlock()
}

func (h *harness) drain() { h.sched.Drain(context.Background()) }

func (h *harness) spendList() []governor.SpendEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]governor.SpendEvent(nil), h.spends...)
}

func (h *harness) rebuild() int {
	h.t.Helper()
	n, err := h.svc.Rebuild(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) queuedClass(id string) (governor.Class, bool) {
	return h.sched.InspectQueued(id, groups.Summary)
}

func (h *harness) entityRow(id string) (store.EntityRow, bool) {
	h.t.Helper()
	e, ok, err := h.st.GetEntity(context.Background(), groups.EntityKind, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return e, ok
}

func (h *harness) groupRow(id string) store.GroupRow {
	h.t.Helper()
	return group(h.t, h.st, id)
}

func TestNewServiceNeedsItsParts(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	full := ServiceConfig{
		Store: h.st, Persister: h.pers, Fetcher: &fakeSearch{}, Scheduler: h.sched,
		KeepAlive: KeepAlive{Store: h.st},
	}
	if _, err := NewService(full); err != nil {
		t.Fatalf("complete config: %v", err)
	}
	for name, mutate := range map[string]func(*ServiceConfig){
		"store":     func(c *ServiceConfig) { c.Store = nil },
		"persister": func(c *ServiceConfig) { c.Persister = nil },
		"fetcher":   func(c *ServiceConfig) { c.Fetcher = nil },
		"scheduler": func(c *ServiceConfig) { c.Scheduler = nil },
		"keepalive": func(c *ServiceConfig) { c.KeepAlive = nil },
	} {
		c := full
		mutate(&c)
		if _, err := NewService(c); err == nil {
			t.Errorf("a config without %s was accepted", name)
		}
	}
}

func TestExecutorSpendsOneRequestOfTheDeclaredBucketAndBatchesToBatchSize(t *testing.T) {
	h := newHarness(t, harnessOpts{batchSize: 7})
	ex := executor{h.svc}
	if got := ex.MaxBatch(); got != 7 {
		t.Errorf("MaxBatch = %d, want the configured 7", got)
	}
	for _, n := range []int{1, 7} {
		req := ex.Spend(governor.ClassKeepAlive, n)
		if req.Bucket != origin.Bucket || req.Cost != 1 || req.Class != governor.ClassKeepAlive {
			t.Errorf("Spend(4, %d) = %+v, want one request of %s", n, req, origin.Bucket)
		}
	}
	d := newHarness(t, harnessOpts{})
	if got := (executor{d.svc}).MaxBatch(); got != config.DefaultBatchSize {
		t.Errorf("default MaxBatch = %d, want %d", got, config.DefaultBatchSize)
	}
}

func TestQueueSubmitsOneTaskAtTheGivenClassAndRefusesANonKey(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	h.seed("A-1", "indeterminate")
	h.fake.set("A-1", "indeterminate")
	if err := h.svc.Queue(ctx, "nonsense", governor.ClassInvalidated, time.Time{}); err == nil {
		t.Error("a non-Jira id was queued")
	}
	if err := h.svc.Queue(ctx, "a-1", governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if c, ok := h.queuedClass("A-1"); !ok || c != governor.ClassInvalidated {
		t.Fatalf("queued = %v class %v, want class 2", ok, c)
	}
	// The group is still fresh, but a class 2 re-pull is for a write: it fetches.
	h.drain()
	if h.fake.count() != 1 {
		t.Fatalf("calls = %d, want 1", h.fake.count())
	}
	sp := h.spendList()
	if len(sp) != 1 || sp[0].Class != governor.ClassInvalidated || sp[0].Purpose != governor.PurposeWrite || sp[0].Group != groups.Summary {
		t.Errorf("spend = %+v, want class 2, purpose write, group summary", sp)
	}
}

func TestBookAfterDepartureBooksAClass4TaskAtTheStoredFreshUntil(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	st := h.seed("A-1", "indeterminate") // fresh until t0+1m
	h.fake.set("A-1", "indeterminate")

	if err := h.svc.BookAfterDeparture(ctx, "a-1"); err != nil {
		t.Fatal(err)
	}
	if c, ok := h.queuedClass("A-1"); !ok || c != governor.ClassKeepAlive {
		t.Fatalf("queued = %v class %v, want class 4", ok, c)
	}
	h.clk.Set(st.FreshUntil.Add(-time.Second))
	h.drain()
	if h.fake.count() != 0 {
		t.Fatalf("refreshed %d times before fresh_until", h.fake.count())
	}
	h.clk.Set(st.FreshUntil)
	h.drain()
	if h.fake.count() != 1 {
		t.Fatalf("calls at fresh_until = %d, want 1", h.fake.count())
	}
}

func TestBookAfterDepartureBooksNothingForAMemberAClosedOrUnknownIssue(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	h.seed("M-1", "indeterminate")
	h.setMember("M-1", true) // still listed by another query
	h.seed("C-1", "done")
	h.clk.Advance(25 * time.Hour) // closed 25 hours ago
	for _, id := range []string{"M-1", "C-1", "U-1"} {
		if err := h.svc.BookAfterDeparture(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, ok := h.queuedClass(id); ok {
			t.Errorf("a task was booked for %s", id)
		}
	}
}

func TestServiceSweepEvictsAnIssueClosedForTheExpiry(t *testing.T) {
	h := newHarness(t, harnessOpts{expiry: 48 * time.Hour})
	h.seed("C-1", "done")
	h.seed("O-1", "indeterminate")
	h.clk.Advance(47 * time.Hour)
	if n, err := h.svc.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("Sweep at 47h = %d, %v, want 0", n, err)
	}
	h.clk.Advance(2 * time.Hour)
	n, err := h.svc.Sweep(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Sweep at 49h = %d, %v, want 1", n, err)
	}
	if _, ok := h.entityRow("C-1"); ok {
		t.Error("the closed issue survived its expiry")
	}
	if _, ok := h.entityRow("O-1"); !ok {
		t.Error("an open issue was evicted")
	}
}

// fakeSearch is a Fetcher that is never called.
type fakeSearch struct{}

func (*fakeSearch) Search(context.Context, string, []string) (*internal.PjiraSearchResult, error) {
	return nil, errors.New("not called")
}
