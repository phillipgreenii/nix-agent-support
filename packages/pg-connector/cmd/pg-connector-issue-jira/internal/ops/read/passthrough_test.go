package read

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// activityArgs is a valid list_activity range.
const activityArgs = `{"since":"2026-10-01T00:00:00Z","before":"2026-10-05T00:00:00Z"}`

// fakePjira is the inner runner under the governed origin runner: it answers
// issue, search and auth-status from canned data and records every call.
type fakePjira struct {
	mu    sync.Mutex
	calls [][]string
	// known issue keys; any other key is "not found".
	known map[string]bool
	// authState is what pjira auth-status prints, authErr what it fails with.
	authState string
	authErr   error
}

func (f *fakePjira) Binary() string { return "/abs/pjira" }

func (f *fakePjira) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	switch args[0] {
	case "auth-status":
		if f.authErr != nil {
			return f.authState, f.authErr
		}
		return f.authState, nil
	case "issue":
		key := args[len(args)-1]
		if !f.known[key] {
			return "", fmt.Errorf("pjira: issue %s not found", key)
		}
		return fmt.Sprintf(`{"key":%q,"summary":"title of %s","status":"To Do","issuetype":"Task","project":"ABC","url":"https://example.invalid/%s"}`, key, key, key), nil
	case "search":
		jql := args[2]
		if strings.HasPrefix(jql, "parent =") {
			return `{"items":[{"key":"ABC-2","summary":"child","status":"To Do","issuetype":"Task","parent":"ABC-1"}],"truncated":false}`, nil
		}
		if strings.Contains(strings.Join(args, " "), "--expand") {
			return `{"items":[{"key":"ABC-3","summary":"mine","status":"To Do","issuetype":"Task","project":"ABC","created":"2026-10-02T00:00:00.000+0000","reporter":{"email":"operator@example.invalid"}}],"truncated":false}`, nil
		}
		return `{"items":[{"key":"ABC-3","summary":"found","status":"To Do","issuetype":"Task","url":"https://example.invalid/ABC-3"}],"truncated":false}`, nil
	}
	return "", fmt.Errorf("fakePjira: unexpected args %v", args)
}

func (f *fakePjira) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// passRig is the governed backend of the pass-through ops: a fake pjira under
// the real origin runner and governor, whose spend is recorded.
type passRig struct {
	t      *testing.T
	st     *store.Store
	fake   *fakePjira
	gov    *governor.Governor
	runner *origin.Runner
	auth   *origin.AuthState
	clk    *governor.ManualClock
	ops    map[string]scriptout.OpHandler

	mu     sync.Mutex
	spends []governor.SpendEvent
}

func newPassRig(t *testing.T) *passRig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &passRig{t: t, st: st, fake: &fakePjira{known: map[string]bool{"ABC-1": true}, authState: "OK"}, auth: origin.NewAuthState()}
	p.clk = governor.NewManualClock(t0)
	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 100
	p.gov, err = governor.New(origin.GovernorConfig(cfg), governor.Options{
		Clock: p.clk,
		OnSpend: func(e governor.SpendEvent) {
			p.mu.Lock()
			p.spends = append(p.spends, e)
			p.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.runner, err = origin.New(origin.Options{
		Inner: p.fake, Governor: p.gov, Bucket: origin.Bucket, Concurrency: 2,
		DefaultPause: time.Minute, Clock: p.clk, Jitter: func() float64 { return 0 }, Auth: p.auth,
	})
	if err != nil {
		t.Fatal(err)
	}
	be := internal.New(p.runner).WithGetenv(func(k string) string {
		if k == internal.EnvEmail {
			return "operator@example.invalid"
		}
		return ""
	})
	p.ops = Handlers(Deps{Auth: p.auth, Backend: be, Store: st, Pause: p.runner, Clock: p.clk})
	return p
}

func (p *passRig) call(op, args string) (any, error) {
	p.t.Helper()
	return p.ops[op].Handle(context.Background(), json.RawMessage(args))
}

func (p *passRig) spendList() []governor.SpendEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]governor.SpendEvent(nil), p.spends...)
}

// storedNothing fails the test when the store holds anything.
func (p *passRig) storedNothing() {
	p.t.Helper()
	ctx := context.Background()
	if head, err := p.st.ChangeHead(ctx); err != nil || head != 0 {
		p.t.Errorf("change head = %d, %v, want an empty log", head, err)
	}
	if qs, err := p.st.ListQueries(ctx, false); err != nil || len(qs) != 0 {
		p.t.Errorf("queries = %v, %v, want none", qs, err)
	}
	for _, id := range []string{"ABC-1", "ABC-2", "ABC-3"} {
		if _, ok, err := p.st.GetEntity(ctx, "issue", id); err != nil || ok {
			p.t.Errorf("entity %s stored (%v, %v)", id, ok, err)
		}
		for _, g := range []string{"summary", "detail", "children"} {
			if _, ok, err := p.st.GetGroup(ctx, "issue", id, g); err != nil || ok {
				p.t.Errorf("group %s/%s stored (%v, %v)", id, g, ok, err)
			}
		}
	}
}

func TestPassthroughOpsGoThroughTheGovernorAsClass1PassthroughSpendAndStoreNothing(t *testing.T) {
	p := newPassRig(t)
	for _, c := range []struct{ op, args string }{
		{OpChildren, `{"id":"ABC-1"}`},
		{OpSearch, `{"query":"project = ABC"}`},
		{OpListActivity, activityArgs},
		{OpDeps, `{"id":"ABC-1"}`},
		{OpAuthStatus, `{}`},
	} {
		before := len(p.spendList())
		if _, err := p.call(c.op, c.args); err != nil {
			t.Fatalf("%s: %v", c.op, err)
		}
		spends := p.spendList()[before:]
		if len(spends) == 0 {
			t.Errorf("%s made no governed call", c.op)
		}
		for _, s := range spends {
			if s.Purpose != governor.PurposePassthrough || s.Class != governor.ClassInteractive || s.Bucket != origin.Bucket {
				t.Errorf("%s spend = %+v, want class 1 passthrough on the origin bucket", c.op, s)
			}
		}
	}
	p.storedNothing()
}

func TestChildrenAnswersTheNonClosedChildrenLiveAndNeverCachesThem(t *testing.T) {
	p := newPassRig(t)
	for i := 0; i < 2; i++ {
		out, err := p.call(OpChildren, `{"id":"ABC-1"}`)
		if err != nil {
			t.Fatal(err)
		}
		res, ok := out.(*schema.IssueChildrenResult)
		if !ok || len(res.Children) != 1 || res.Children[0].ID != "ABC-2" || res.Children[0].Parent != "ABC-1" {
			t.Fatalf("children = %#v", out)
		}
	}
	// Two answers, two live resolutions and two searches: nothing was cached.
	if n := p.fake.count(); n != 4 {
		t.Errorf("pjira calls = %d, want 4 (issue and search, twice)", n)
	}
	if _, err := p.call(OpChildren, `{"id":"owner/repo#1"}`); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("children of a non-Jira id = %v, want not_found", err)
	}
}

func TestSearchAnswersLiveAndStoresNothing(t *testing.T) {
	p := newPassRig(t)
	out, err := p.call(OpSearch, `{"query":"project = ABC"}`)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.([]schema.SearchResult)
	if !ok || len(res) != 1 || res[0].ID != "ABC-3" || res[0].Type != "issue" {
		t.Fatalf("search = %#v", out)
	}
	if _, err := p.call(OpSearch, `{"query":" "}`); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Errorf("search of a blank query = %v, want invalid_argument", err)
	}
	p.storedNothing()
}

func TestListActivityAnswersLiveAndStoresNothing(t *testing.T) {
	p := newPassRig(t)
	out, err := p.call(OpListActivity, activityArgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.(*schema.ActivityListResult); !ok {
		t.Fatalf("list_activity = %#v", out)
	}
	p.storedNothing()
}

func TestDepsIsTheExistenceCheckThenAnEmptyList(t *testing.T) {
	p := newPassRig(t)
	out, err := p.call(OpDeps, `{"id":"ABC-1","full":true}`)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(*schema.IssueDepsResult)
	if !ok || res.IDs == nil || len(res.IDs) != 0 || len(res.Entities) != 0 {
		t.Fatalf("deps = %#v, want an empty, non-nil id list", out)
	}
	body, _ := json.Marshal(res)
	if !strings.Contains(string(body), `"ids":[]`) {
		t.Errorf("wire = %s, want ids as an empty array", body)
	}
	if n := p.fake.count(); n == 0 {
		t.Error("deps made no existence check")
	}
	if _, err := p.call(OpDeps, `{"id":"ABC-404"}`); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("deps of an unknown issue = %v, want not_found", err)
	}
	p.storedNothing()
}

func TestAuthStatusFollowsTheAnswerOnTheTracker(t *testing.T) {
	p := newPassRig(t)
	st := func() scriptout.AuthStatus {
		t.Helper()
		out, err := p.call(OpAuthStatus, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		s, ok := out.(scriptout.AuthStatus)
		if !ok {
			t.Fatalf("auth_status = %#v", out)
		}
		return s
	}

	if s := st(); s.State != scriptout.AuthOK {
		t.Fatalf("auth_status = %+v, want OK", s)
	}
	if bad, _ := p.auth.Unauthenticated(); bad {
		t.Error("an OK answer left the daemon unauthenticated")
	}

	p.fake.mu.Lock()
	p.fake.authState, p.fake.authErr = "MISSING", errors.New("exit status 1")
	p.fake.mu.Unlock()
	s := st()
	if s.State == scriptout.AuthOK {
		t.Fatalf("auth_status = %+v, want a failed state", s)
	}
	if bad, cause := p.auth.Unauthenticated(); !bad || cause == nil {
		t.Errorf("a failed answer left the daemon authenticated (%v, %v)", bad, cause)
	}

	// An OK answer clears it again through the runner's own bookkeeping.
	p.fake.mu.Lock()
	p.fake.authState, p.fake.authErr = "OK", nil
	p.fake.mu.Unlock()
	if s := st(); s.State != scriptout.AuthOK {
		t.Fatalf("auth_status = %+v, want OK", s)
	}
	if bad, _ := p.auth.Unauthenticated(); bad {
		t.Error("an OK answer after a failure left the daemon unauthenticated")
	}
}

func TestAuthStatusRecordsTheIdentityAnAnswerReports(t *testing.T) {
	auth := origin.NewAuthState()
	h := &handlers{deps: Deps{Auth: auth}}
	report := func(s scriptout.AuthStatus) scriptout.OpHandler {
		return h.watchAuth(scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) { return s, nil }})
	}
	if _, err := report(scriptout.AuthStatus{State: scriptout.AuthOK}).Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if auth.Identity() != "" {
		t.Errorf("identity = %q, want none: the answer reported none", auth.Identity())
	}
	if _, err := report(scriptout.AuthStatus{State: scriptout.AuthOK, Detail: "someone@example.invalid"}).Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if auth.Identity() != "someone@example.invalid" {
		t.Errorf("identity = %q", auth.Identity())
	}
	if _, err := report(scriptout.AuthStatus{State: scriptout.AuthOK}).Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if auth.Identity() != "someone@example.invalid" {
		t.Errorf("identity = %q, want an answer that reports none to leave it alone", auth.Identity())
	}
	if _, err := report(scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: "MISSING"}).Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if bad, _ := auth.Unauthenticated(); !bad {
		t.Error("a failed answer did not mark the daemon unauthenticated")
	}
}

func TestAPausedOriginIsUnavailableForTheLiveOpsAndIsNotAnAuthenticationFailure(t *testing.T) {
	p := newPassRig(t)
	until := t0.Add(time.Hour)
	if err := p.gov.Pause(origin.Bucket, time.Hour, "rate limited"); err != nil {
		t.Fatal(err)
	}
	before := p.fake.count()
	for _, c := range []struct{ op, args string }{
		{OpSearch, `{"query":"project = ABC"}`},
		{OpChildren, `{"id":"ABC-1"}`},
		{OpDeps, `{"id":"ABC-1"}`},
	} {
		_, err := p.call(c.op, c.args)
		if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "paused until "+until.Format(time.RFC3339)) {
			t.Errorf("%s during a pause = %v, want unavailable naming the pause", c.op, err)
		}
	}
	out, err := p.call(OpAuthStatus, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if s := out.(scriptout.AuthStatus); s.State == scriptout.AuthOK || !strings.Contains(s.Detail, "paused") {
		t.Errorf("auth_status during a pause = %+v", s)
	}
	if bad, _ := p.auth.Unauthenticated(); bad {
		t.Error("a pause was recorded as an authentication failure")
	}
	if p.fake.count() != before {
		t.Errorf("pjira was called during the pause")
	}
}

func TestWithoutABackendThePassthroughOpsAreUnavailableAtTheirVersions(t *testing.T) {
	ops := Handlers(Deps{})
	for op, v := range map[string]int{
		OpChildren: schema.IssueSchemaVersion, OpDeps: schema.IssueSchemaVersion, OpAuthStatus: schema.IssueSchemaVersion,
		OpSearch: schema.SearchSchemaVersion, OpListActivity: schema.ActivitySchemaVersion,
	} {
		h, ok := ops[op]
		if !ok || h.SchemaVersion != v {
			t.Errorf("%s: %+v (present %v), want version %d", op, h, ok, v)
			continue
		}
		if _, err := h.Handle(context.Background(), nil); !errors.Is(err, scriptout.ErrUnavailable) {
			t.Errorf("%s with no backend = %v, want unavailable", op, err)
		}
	}
	if len(ops) != len(Ops) {
		t.Errorf("handlers = %d, want %d", len(ops), len(Ops))
	}
}
