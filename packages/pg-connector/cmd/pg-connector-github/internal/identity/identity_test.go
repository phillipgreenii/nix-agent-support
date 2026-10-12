package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// fakeClock moves only when advanced; After channels fire when the clock
// passes their deadline.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: t0} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := &waiter{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.waiters = append(c.waiters, w)
	return w.ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var keep []*waiter
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		keep = append(keep, w)
	}
	c.waiters = keep
}

// waiting reports how many After channels are pending.
func (c *fakeClock) waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// blockUntilWaiting spins until n timers are pending (the loop has gone to
// sleep), so Advance cannot race ahead of it.
func (c *fakeClock) blockUntilWaiting(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.waiting() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d pending timers (have %d)", n, c.waiting())
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeGH is a Runner that answers the viewer query from a script.
type fakeGH struct {
	mu    sync.Mutex
	login string // current answer
	err   error  // when set, every call fails with it
	calls [][]string
}

func (f *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.err != nil {
		return nil, f.err
	}
	return viewerBody(f.login), nil
}

func (f *fakeGH) set(login string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.login, f.err = login, err
}

func (f *fakeGH) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func viewerBody(login string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"viewer":{"login":%q},"rateLimit":{"cost":1,"remaining":4990,"resetAt":"2026-10-12T10:00:00Z"}}}`, login))
}

// logRecorder collects structured-log records.
type logRecorder struct {
	mu   sync.Mutex
	recs []rec
}

type rec struct {
	change string
	kv     map[string]string
}

func (l *logRecorder) Log(change string, kv ...any) {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = fmt.Sprint(kv[i+1])
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, rec{change, m})
}

func (l *logRecorder) named(change string) []rec {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []rec
	for _, r := range l.recs {
		if r.change == change {
			out = append(out, r)
		}
	}
	return out
}

func (l *logRecorder) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, r := range l.recs {
		fmt.Fprintf(&b, "%s %v\n", r.change, r.kv)
	}
	return b.String()
}

var _ connectord.Logger = (*logRecorder)(nil)

func newTracker(gh Runner, clk Clock, log connectord.Logger) *Tracker {
	return New(Options{Host: "github.example.test", GH: gh, Clock: clk, Log: log})
}

func TestViewerResolvedAtStartAndEveryTenMinutes(t *testing.T) {
	clk := newFakeClock()
	gh := &fakeGH{login: "octo-one"}
	tr := newTracker(gh, clk, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	clk.blockUntilWaiting(t, 1)
	if gh.count() != 1 {
		t.Fatalf("viewer calls after start = %d, want 1", gh.count())
	}
	if got := tr.Current(); got.Login != "octo-one" || !got.FetchedAt.Equal(t0) {
		t.Fatalf("after start: %+v, want octo-one fetched at %s", got, t0)
	}

	clk.Advance(9*time.Minute + 59*time.Second)
	time.Sleep(20 * time.Millisecond) // a wrong early refresh would show up here
	if gh.count() != 1 {
		t.Fatalf("viewer calls before 10 minutes = %d, want 1", gh.count())
	}

	clk.Advance(time.Second)
	clk.blockUntilWaiting(t, 1)
	if gh.count() != 2 {
		t.Fatalf("viewer calls after 10 minutes = %d, want 2", gh.count())
	}
	if got := tr.Current(); !got.FetchedAt.Equal(t0.Add(10 * time.Minute)) {
		t.Errorf("fetched_at after the second resolution = %s", got.FetchedAt)
	}
}

func TestFailedRefreshIsRetriedSoonerThanTheInterval(t *testing.T) {
	clk := newFakeClock()
	gh := &fakeGH{err: errors.New("network unreachable")}
	log := &logRecorder{}
	tr := newTracker(gh, clk, log)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	clk.blockUntilWaiting(t, 1)
	if got := tr.Current(); got.Login != "" || !strings.Contains(got.LastError, "network unreachable") {
		t.Fatalf("after a failed start: %+v", got)
	}
	if len(log.named("viewer_refresh_failed")) != 1 {
		t.Errorf("want one viewer_refresh_failed record, got %v", log.text())
	}

	gh.set("octo-one", nil)
	clk.Advance(DefaultRetryInterval)
	clk.blockUntilWaiting(t, 1)
	if got := tr.Current(); got.Login != "octo-one" || got.LastError != "" {
		t.Fatalf("after the retry: %+v", got)
	}
	if gh.count() != 2 {
		t.Errorf("calls = %d, want 2", gh.count())
	}
}

func TestViewerChangeRunsEveryFlushOnceAndLogsOneRecord(t *testing.T) {
	gh := &fakeGH{login: "octo-one"}
	log := &logRecorder{}
	tr := newTracker(gh, newFakeClock(), log)

	var order []string
	tr.RegisterFlush("queries", func(context.Context) error { order = append(order, "queries"); return nil })
	tr.RegisterFlush("pending", func(context.Context) error { order = append(order, "pending"); return nil })

	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(order) != 0 || len(log.named("viewer_changed")) != 0 {
		t.Fatalf("the first resolution is not a change: flushes %v, log %s", order, log.text())
	}

	// Unchanged: nothing flushes.
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(order) != 0 {
		t.Fatalf("an unchanged viewer flushed %v", order)
	}

	gh.set("octo-two", nil)
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "queries,pending" {
		t.Fatalf("flushes after the change = %q, want queries,pending", got)
	}
	changed := log.named("viewer_changed")
	if len(changed) != 1 || changed[0].kv["old_login"] != "octo-one" || changed[0].kv["new_login"] != "octo-two" {
		t.Fatalf("viewer_changed records = %+v", changed)
	}

	// The same new viewer again: still nothing more.
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || len(log.named("viewer_changed")) != 1 {
		t.Fatalf("a repeat of the new viewer flushed again: %v, log %s", order, log.text())
	}
}

func TestAFailedFlushStaysPendingAndSucceededOnesDoNotRerun(t *testing.T) {
	gh := &fakeGH{login: "octo-one"}
	log := &logRecorder{}
	tr := newTracker(gh, newFakeClock(), log)

	runs := map[string]int{}
	failFirst := true
	tr.RegisterFlush("good", func(context.Context) error { runs["good"]++; return nil })
	tr.RegisterFlush("flaky", func(context.Context) error {
		runs["flaky"]++
		if failFirst {
			failFirst = false
			return errors.New("disk busy")
		}
		return nil
	})
	ctx := context.Background()
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	gh.set("octo-two", nil)
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if runs["good"] != 1 || runs["flaky"] != 1 {
		t.Fatalf("after the change: %v", runs)
	}
	if got := log.named("identity_flush_failed"); len(got) != 1 || got[0].kv["flush"] != "flaky" {
		t.Fatalf("flush failure records = %+v", got)
	}

	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if runs["good"] != 1 || runs["flaky"] != 2 {
		t.Fatalf("after the next refresh: %v (the failed flush retries, the good one does not)", runs)
	}
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if runs["flaky"] != 2 {
		t.Fatalf("a flush that succeeded ran again: %v", runs)
	}
	if len(log.named("viewer_changed")) != 1 {
		t.Errorf("the change is logged once, got %s", log.text())
	}
}

func TestFailedRefreshKeepsThePreviousIdentity(t *testing.T) {
	gh := &fakeGH{login: "octo-one"}
	tr := newTracker(gh, newFakeClock(), nil)
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	gh.set("", errors.New("timeout"))
	if err := tr.Refresh(context.Background()); err == nil {
		t.Fatal("want the refresh error")
	}
	got := tr.Current()
	if got.Login != "octo-one" || !strings.Contains(got.LastError, "timeout") || got.Unauthenticated {
		t.Fatalf("after a transient failure: %+v", got)
	}
}

func TestTokenRejectionMarksUnauthenticatedAndASuccessClearsIt(t *testing.T) {
	gh := &fakeGH{login: "octo-one"}
	log := &logRecorder{}
	tr := newTracker(gh, newFakeClock(), log)
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	gh.set("", fmt.Errorf("gh api: Bad credentials (HTTP 401): %w", vcs.ErrAuthInvalid))
	if err := tr.Refresh(context.Background()); err == nil {
		t.Fatal("want the auth error")
	}
	if bad, err := tr.Unauthenticated(); !bad || !errors.Is(err, vcs.ErrAuthInvalid) {
		t.Fatalf("Unauthenticated() = %v, %v", bad, err)
	}
	if tr.Current().Login != "octo-one" {
		t.Error("the cached identity is dropped on an auth failure; it should stay")
	}

	gh.set("octo-one", nil)
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bad, _ := tr.Unauthenticated(); bad {
		t.Fatal("a successful call did not clear the state")
	}
	if got := log.named("auth_state_changed"); len(got) != 2 || got[0].kv["state"] != "unauthenticated" || got[1].kv["state"] != "authenticated" {
		t.Fatalf("auth_state_changed records = %+v (one per transition)", got)
	}
}

func TestObserveAndTheSetters(t *testing.T) {
	tr := newTracker(&fakeGH{}, newFakeClock(), nil)

	if tr.Observe(errors.New("connection reset")) {
		t.Error("a non-auth error is not an auth failure")
	}
	if bad, _ := tr.Unauthenticated(); bad {
		t.Error("a non-auth error marked the state")
	}
	if !tr.Observe(fmt.Errorf("gh: %w", vcs.ErrAuthInvalid)) {
		t.Error("an auth-invalid error should be reported as one")
	}
	if bad, _ := tr.Unauthenticated(); !bad {
		t.Fatal("Observe did not mark")
	}
	// An unrelated error leaves the mark alone; only a success clears it.
	tr.Observe(errors.New("connection reset"))
	if bad, _ := tr.Unauthenticated(); !bad {
		t.Fatal("an unrelated error cleared the mark")
	}
	tr.Observe(nil)
	if bad, _ := tr.Unauthenticated(); bad {
		t.Fatal("Observe(nil) did not clear")
	}

	tr.MarkUnauthenticated(nil)
	if bad, err := tr.Unauthenticated(); !bad || !errors.Is(err, vcs.ErrAuthInvalid) {
		t.Fatalf("MarkUnauthenticated(nil) = %v, %v", bad, err)
	}
	tr.Clear()
	if bad, _ := tr.Unauthenticated(); bad {
		t.Fatal("Clear did not clear")
	}
}

// A rejected token still leaves the stored results readable with their age:
// the state gates fetches (the readers' business), never the store.
func TestCachedReadStillServedWithItsAgeWhileUnauthenticated(t *testing.T) {
	st, _, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite3"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.PutQuery(ctx, store.QueryRow{Key: "team", Configured: true, IDs: []string{"n1"}, Complete: true, FetchedAt: t0}); err != nil {
		t.Fatal(err)
	}

	clk := newFakeClock()
	tr := newTracker(&fakeGH{}, clk, nil)
	tr.MarkUnauthenticated(vcs.ErrAuthInvalid)
	clk.Advance(7 * time.Minute)

	row, ok, err := st.GetQuery(ctx, "team")
	if err != nil || !ok {
		t.Fatalf("GetQuery = %v, %v, %v", row, ok, err)
	}
	if age := clk.Now().Sub(row.FetchedAt); age != 7*time.Minute || len(row.IDs) != 1 {
		t.Errorf("served row %+v with age %s, want the stored ids and 7m", row, age)
	}
	if bad, _ := tr.Unauthenticated(); !bad {
		t.Error("the cached read cleared the state")
	}
}

func TestViewerCallIsMeteredAsOneInteractiveGraphQLSpend(t *testing.T) {
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	gh := &fakeGH{login: "octo-one"}
	tr := New(Options{GH: gh, Clock: newFakeClock(), Governor: gov, Feed: ratefeed.New(gov, gh, clk)})
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	bs, ok := gov.State().Bucket(governor.BucketGraphQL)
	if !ok || bs.Spent != 1 || bs.SpentByPurpose[governor.PurposeRefresh] != 1 || bs.BackgroundSpent != 0 {
		t.Fatalf("graphql bucket after the viewer call = %+v, want 1 foreground point spent", bs)
	}
}

func TestARefusedAdmissionIsAnErrorNotAnAuthFailure(t *testing.T) {
	clk := governor.NewManualClock(t0)
	cfg := ratefeed.GovernorConfig(config.Defaults().Settings)
	gov, err := governor.New(cfg, governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	// Exhaust the hourly cap so even an interactive spend is refused.
	tk, err := gov.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: cfg.GraphQL.Cap})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := tk.Settle(governor.Spend{Cost: cfg.GraphQL.Cap, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	gh := &fakeGH{login: "octo-one"}
	tr := New(Options{GH: gh, Clock: newFakeClock(), Governor: gov, Feed: ratefeed.New(gov, gh, clk)})
	err = tr.Refresh(context.Background())
	if !errors.Is(err, governor.ErrRefused) {
		t.Fatalf("Refresh = %v, want a refusal", err)
	}
	if gh.count() != 0 {
		t.Error("gh ran despite the refusal")
	}
	if bad, _ := tr.Unauthenticated(); bad {
		t.Error("a budget refusal marked the daemon unauthenticated")
	}
}

// stubGH writes an executable gh that records the environment it was given and
// answers the viewer query.
func stubGH(t *testing.T, login string) (bin, envFile string) {
	t.Helper()
	dir := t.TempDir()
	envFile = filepath.Join(dir, "env.log")
	bin = filepath.Join(dir, "gh")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'GH_HOST=%%s\\n' \"$GH_HOST\" >> %q\nprintf '%%s\\n' %q\n", envFile,
		fmt.Sprintf(`{"data":{"viewer":{"login":%q}}}`, login))
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, envFile
}

func TestEveryGHCallCarriesTheConfiguredHost(t *testing.T) {
	bin, envFile := stubGH(t, "octo-one")
	t.Setenv("GH_TOKEN", "fixture-token-do-not-store")
	t.Setenv("GH_HOST", "ambient.example.test") // an inherited host must not win

	const host = "github.example.test"
	tr := New(Options{Host: host, GH: NewGH(host, bin), Clock: newFakeClock()})
	for i := 0; i < 2; i++ {
		if err := tr.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 2 {
		t.Fatalf("recorded %d gh calls, want 2: %q", len(lines), raw)
	}
	for _, l := range lines {
		if l != "GH_HOST="+host {
			t.Errorf("a gh call carried %q, want GH_HOST=%s", l, host)
		}
	}
	if tr.Host() != host {
		t.Errorf("Host() = %q", tr.Host())
	}
}

// The fixture token reaches the gh child's environment (that is how gh is
// authenticated) and must be found nowhere the daemon writes: not the store
// file, not the log, not the answer to auth_status.
func TestFixtureTokenNeverReachesTheStoreOrTheLog(t *testing.T) {
	const token = "fixture-token-do-not-store"
	bin, _ := stubGH(t, "octo-one")
	t.Setenv("GH_TOKEN", token)

	dir := t.TempDir()
	path := filepath.Join(dir, "state.sqlite3")
	st, _, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	log := &logRecorder{}
	tr := New(Options{Host: "github.example.test", GH: NewGH("github.example.test", bin), Clock: newFakeClock(), Log: log})
	tr.RegisterFlush("queries", FlushMeQueries(st, nil))

	ctx := context.Background()
	if err := tr.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.PutQuery(ctx, store.QueryRow{Key: "mine author:@me", Configured: true, IDs: []string{"n1"}, FetchedAt: t0}); err != nil {
		t.Fatal(err)
	}
	tr.MarkUnauthenticated(errors.New("gh: Bad credentials (HTTP 401)"))
	tr.Clear()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries { // the database and any -wal / -shm sidecar
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), token) {
			t.Errorf("the token is in store file %s", e.Name())
		}
	}
	if strings.Contains(log.text(), token) {
		t.Errorf("the token is in the log:\n%s", log.text())
	}
	if s := fmt.Sprintf("%+v", tr.Current()); strings.Contains(s, token) {
		t.Errorf("the token is in the cached identity: %s", s)
	}
}
