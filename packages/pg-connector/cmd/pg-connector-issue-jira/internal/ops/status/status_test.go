package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var t0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

const (
	secretKey   = "ZZZ-77"
	secretTitle = "Quarterly Payroll Leak Postmortem"
)

type fakeSettle struct {
	stats engine.Stats
	err   error
}

func (f *fakeSettle) Stats(context.Context) (engine.Stats, error) { return f.stats, f.err }

type fakeFeed struct {
	pos []feed.Position
	err error
}

func (f *fakeFeed) Positions(context.Context) ([]feed.Position, error) { return f.pos, f.err }

type fakeAuth struct {
	bad bool
	err error
}

func (f *fakeAuth) Unauthenticated() (bool, error) { return f.bad, f.err }

type rig struct {
	t      *testing.T
	st     *store.Store
	gov    *governor.Governor
	clk    *governor.ManualClock
	sched  *scheduler.Scheduler
	settle *fakeSettle
	feed   *fakeFeed
	auth   *fakeAuth
	dcfg   *config.Config
	cfg    Config
}

// newRig is a healthy daemon: one configured query that succeeded a minute
// ago with a baseline, two titled issues, one consumer at the head.
func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	st, _, err := store.Open(filepath.Join(dir, config.StoreFileName), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)

	dcfg := config.Defaults()
	dcfg.StateDir = dir
	dcfg.SocketPath = filepath.Join(dir, config.SocketFileName)
	dcfg.PjiraPath = "/abs/pjira"
	dcfg.Path = filepath.Join(dir, "config.json")
	dcfg.Queries = map[string]schema.QueryDef{
		"mine":  {Expr: schema.QueryExpr{"assignee = me"}},
		"other": {Expr: schema.QueryExpr{"reporter = me"}},
	}
	dcfg.QueryTTLDefault = config.Duration(10 * time.Minute)

	gov, err := governor.New(origin.GovernorConfig(dcfg), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t: t, st: st, gov: gov, clk: clk, dcfg: dcfg,
		sched:  scheduler.New(scheduler.Options{Clock: clk}),
		settle: &fakeSettle{}, feed: &fakeFeed{}, auth: &fakeAuth{},
	}
	ctx := context.Background()
	for _, id := range []string{secretKey, "ZZZ-78"} {
		if err := st.PutEntity(ctx, store.EntityRow{Kind: groups.EntityKind, ID: id, Version: 1, LastAccess: t0, KeepAlive: true}); err != nil {
			t.Fatal(err)
		}
		content, _ := json.Marshal(map[string]string{"key": id, "title": secretTitle})
		err := st.PutGroup(ctx, store.GroupRow{
			Kind: groups.EntityKind, ID: id, Group: groups.Summary, Content: content, Fingerprint: "f",
			FetchedAt: t0, FreshUntil: t0.Add(time.Minute), Generation: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	r.putQuery("mine", func(q *store.QueryRow) { q.IDs = []string{secretKey} })
	r.putQuery("other", nil)
	r.feed.pos = []feed.Position{{Consumer: "reader", View: "changes", Scope: "mine", AckedSeq: 4, Head: 4, SeenAt: t0}}

	r.cfg = Config{
		RouterInstance: "issue",
		Identity:       func() string { return "operator@example.test" },
		Daemon:         dcfg, Version: "v1", BuildID: "b1",
		Started: t0.Add(-48 * time.Hour),
		Store:   st, Governor: gov, Scheduler: r.sched, Settle: r.settle, Feed: r.feed, Auth: r.auth,
		Clock: clk,
		Probe: func(string) proc.SupervisorState { return proc.SupervisorRunning },
	}
	return r
}

func (r *rig) putQuery(name string, mut func(*store.QueryRow)) {
	r.t.Helper()
	q := store.QueryRow{
		Key: name, Configured: true, Complete: true, FetchedAt: t0.Add(-time.Minute),
		LastSuccessAt: t0.Add(-time.Minute), BaselineAt: t0.Add(-24 * time.Hour),
	}
	if mut != nil {
		mut(&q)
	}
	if err := r.st.PutQuery(context.Background(), q); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) collect() Report {
	r.t.Helper()
	rep, err := Collect(context.Background(), r.cfg)
	if err != nil {
		r.t.Fatalf("Collect: %v", err)
	}
	return rep
}

func hasReason(rep Report, parts ...string) bool {
	for _, reason := range rep.Reasons {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(reason, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestHealthyFixtureReportsExitZeroWithEveryField(t *testing.T) {
	r := newRig(t)
	err := r.st.InTx(context.Background(), func(tx *store.Tx) error {
		_, err := tx.AppendChange(context.Background(), store.ChangeRow{Kind: groups.EntityKind, ID: secretKey, Version: 1, Kinds: []string{"created"}, At: t0})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rep := r.collect()
	if rep.Exit() != 0 || rep.Health != Healthy || len(rep.Reasons) != 0 {
		t.Fatalf("health = %s exit %d reasons %v, want healthy 0", rep.Health, rep.Exit(), rep.Reasons)
	}
	if rep.Source != SourceDaemon || rep.Version != "v1" || rep.BuildID != "b1" {
		t.Errorf("source/version/build = %s %s %s", rep.Source, rep.Version, rep.BuildID)
	}
	if rep.UptimeSeconds == nil || *rep.UptimeSeconds != int64((48*time.Hour).Seconds()) || rep.StartedAt == nil {
		t.Errorf("uptime = %v started = %v", rep.UptimeSeconds, rep.StartedAt)
	}
	if !rep.Store.Present || rep.Store.Path != filepath.Join(r.dcfg.StateDir, config.StoreFileName) ||
		rep.Store.SchemaVersion != r.st.Version() || rep.Store.Identity == "" {
		t.Errorf("store = %+v", rep.Store)
	}
	if rep.Requests == nil || rep.Requests.Cap != int64(r.dcfg.RequestRatePerSecond) || rep.Requests.WindowSeconds != 1 {
		t.Errorf("requests = %+v", rep.Requests)
	}
	if rep.Pause != nil {
		t.Errorf("pause = %+v, want none", rep.Pause)
	}
	if rep.Queue == nil || len(rep.Queue.Classes) != 5 || rep.Queue.Classes[0].Name == "" || strings.ContainsAny(rep.Queue.Classes[0].Name, "0123456789") {
		t.Errorf("queue = %+v", rep.Queue)
	}
	if len(rep.Queries) != 2 || rep.Queries[0].Name != "mine" || rep.Queries[1].Name != "other" {
		t.Fatalf("queries = %+v", rep.Queries)
	}
	q := rep.Queries[0]
	if q.LastSuccessAt == nil || *q.LastSuccessAgeSeconds != 60 || q.LastError != "" || q.BaselineAt == nil || q.BaselineOpenAgeSeconds != nil {
		t.Errorf("query = %+v", q)
	}
	if rep.Auth == nil || rep.Auth.State != AuthOK || rep.Auth.Identity != "operator@example.test" {
		t.Errorf("auth = %+v", rep.Auth)
	}
	if rep.Changes == nil || rep.Changes.HeadSeq != 1 || len(rep.Changes.Consumers) != 1 || rep.Changes.Consumers[0].Consumer != "reader" {
		t.Errorf("changes = %+v", rep.Changes)
	}
	if rep.Tracked == nil || rep.Tracked.Members != 1 || rep.Tracked.OtherOpen != 1 || rep.Tracked.Watched != 0 {
		t.Errorf("tracked = %+v", rep.Tracked)
	}
	if rep.Supervisor.State != "running" || rep.Supervisor.Label != r.dcfg.SupervisorLabel {
		t.Errorf("supervisor = %+v", rep.Supervisor)
	}
}

func TestRequestCounterIsTheInjectedTotalOrTheWindowSpend(t *testing.T) {
	r := newRig(t)
	if got := r.collect().Requests.Total; got != 0 {
		t.Errorf("total = %d, want the window spend 0", got)
	}
	r.cfg.Requests = func() int64 { return 1234 }
	if got := r.collect().Requests.Total; got != 1234 {
		t.Errorf("total = %d, want 1234", got)
	}
}

func TestAPauseInForceIsDegradedWithItsReasonAndEnd(t *testing.T) {
	r := newRig(t)
	if err := r.gov.Pause(origin.Bucket, 10*time.Minute, "jira answered 429"); err != nil {
		t.Fatal(err)
	}
	rep := r.collect()
	if rep.Exit() != 2 || rep.Health != Degraded {
		t.Fatalf("health = %s exit %d, want degraded 2", rep.Health, rep.Exit())
	}
	if rep.Pause == nil || rep.Pause.Reason != "jira answered 429" || !rep.Pause.Until.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("pause = %+v", rep.Pause)
	}
	if !hasReason(rep, "paused", "jira answered 429", "2026-10-24T12:10:00Z") {
		t.Errorf("reasons = %v", rep.Reasons)
	}
}

func TestAQueryWithALastErrorIsDegradedAndNamed(t *testing.T) {
	r := newRig(t)
	r.putQuery("other", func(q *store.QueryRow) { q.LastError = "jql rejected" })
	rep := r.collect()
	if rep.Exit() != 2 {
		t.Fatalf("exit = %d, want 2", rep.Exit())
	}
	if !hasReason(rep, "query other", "jql rejected") || hasReason(rep, "query mine") {
		t.Errorf("reasons = %v", rep.Reasons)
	}
}

func TestAQueryWithNoSuccessWithinThreeTTLsIsDegraded(t *testing.T) {
	r := newRig(t)
	// ttl 10m: 31 minutes of silence is past three TTLs, 29 is not.
	r.putQuery("mine", func(q *store.QueryRow) { q.LastSuccessAt = t0.Add(-31 * time.Minute) })
	rep := r.collect()
	if rep.Exit() != 2 || !hasReason(rep, "query mine", "no whole-query success") {
		t.Fatalf("exit %d reasons %v", rep.Exit(), rep.Reasons)
	}
	r.putQuery("mine", func(q *store.QueryRow) { q.LastSuccessAt = t0.Add(-29 * time.Minute) })
	if rep := r.collect(); rep.Exit() != 0 {
		t.Fatalf("29 minutes: exit %d reasons %v", rep.Exit(), rep.Reasons)
	}
}

func TestAQueryTTLOverrideSetsTheThreshold(t *testing.T) {
	r := newRig(t)
	r.dcfg.QueryTTL = map[string]config.Duration{"mine": config.Duration(time.Minute)}
	r.putQuery("mine", func(q *store.QueryRow) { q.LastSuccessAt = t0.Add(-4 * time.Minute) })
	if rep := r.collect(); rep.Exit() != 2 || !hasReason(rep, "query mine") {
		t.Fatalf("exit %d reasons %v", rep.Exit(), rep.Reasons)
	}
}

func TestANeverSucceededQueryIsDegradedOnlyAfterThreeTTLsOfUptime(t *testing.T) {
	r := newRig(t)
	if err := r.st.DeleteQuery(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	r.cfg.Started = t0.Add(-5 * time.Minute)
	rep := r.collect()
	if rep.Exit() != 0 {
		t.Fatalf("young daemon: exit %d reasons %v", rep.Exit(), rep.Reasons)
	}
	var other Query
	for _, q := range rep.Queries {
		if q.Name == "other" {
			other = q
		}
	}
	if other.LastSuccessAt != nil || other.BaselineAt != nil || other.BaselineOpenAgeSeconds == nil || *other.BaselineOpenAgeSeconds != 300 {
		t.Errorf("never-run query = %+v, want no success, no baseline and a baseline-open age of 300", other)
	}
	r.cfg.Started = t0.Add(-31 * time.Minute)
	if rep := r.collect(); rep.Exit() != 2 || !hasReason(rep, "query other") {
		t.Fatalf("old daemon: exit %d reasons %v", rep.Exit(), rep.Reasons)
	}
}

func TestARestartedDaemonIsNotDegradedBeforeItsFirstRunCouldFinish(t *testing.T) {
	r := newRig(t)
	r.putQuery("mine", func(q *store.QueryRow) { q.LastSuccessAt = t0.Add(-5 * time.Hour) })
	r.cfg.Started = t0.Add(-time.Minute)
	if rep := r.collect(); rep.Exit() != 0 {
		t.Fatalf("exit %d reasons %v", rep.Exit(), rep.Reasons)
	}
}

func TestFailingAuthenticationIsDegraded(t *testing.T) {
	r := newRig(t)
	r.auth.bad, r.auth.err = true, errors.New("pjira: 401")
	rep := r.collect()
	if rep.Exit() != 2 || rep.Auth.State != AuthUnauthenticated || rep.Auth.Detail != "pjira: 401" || !hasReason(rep, "unauthenticated") {
		t.Fatalf("exit %d auth %+v reasons %v", rep.Exit(), rep.Auth, rep.Reasons)
	}
}

func TestASettleTimeoutOrFailureInTheLastHourIsDegraded(t *testing.T) {
	for name, stats := range map[string]engine.Stats{
		"timeout": {Timeouts: 1},
		"failure": {Failures: 2},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.settle.stats = stats
			rep := r.collect()
			if rep.Exit() != 2 || !hasReason(rep, "settle") {
				t.Fatalf("exit %d reasons %v", rep.Exit(), rep.Reasons)
			}
			if rep.Changes.SettleTimeoutsLastHour != stats.Timeouts || rep.Changes.SettleFailuresLastHour != stats.Failures {
				t.Errorf("changes = %+v", rep.Changes)
			}
		})
	}
}

func TestPendingSettleRowsAreReportedNotJudged(t *testing.T) {
	r := newRig(t)
	r.settle.stats = engine.Stats{Pending: 7}
	rep := r.collect()
	if rep.Exit() != 0 || rep.Changes.PendingSettle != 7 {
		t.Fatalf("exit %d pending %d", rep.Exit(), rep.Changes.PendingSettle)
	}
}

func TestAConsumerFarBehindTheHeadIsDegraded(t *testing.T) {
	cases := map[string]feed.Position{
		"lag":     {Consumer: "slow", View: "changes", Scope: "mine", AckedSeq: 1, Head: 5000, LagRows: ConsumerLagRows, OldestUnacked: t0.Add(-time.Minute), SeenAt: t0},
		"old row": {Consumer: "slow", View: "changes", Scope: "mine", AckedSeq: 1, Head: 5, LagRows: 2, OldestUnacked: t0.Add(-ConsumerOldestUnacked), SeenAt: t0},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.feed.pos = []feed.Position{p}
			rep := r.collect()
			if rep.Exit() != 2 || !hasReason(rep, "consumer slow") {
				t.Fatalf("exit %d reasons %v", rep.Exit(), rep.Reasons)
			}
		})
	}
	t.Run("small lag is fine", func(t *testing.T) {
		r := newRig(t)
		r.feed.pos = []feed.Position{{Consumer: "ok", View: "changes", Scope: "mine", AckedSeq: 1, Head: 5, LagRows: 4, OldestUnacked: t0.Add(-time.Hour), SeenAt: t0}}
		rep := r.collect()
		if rep.Exit() != 0 || rep.Changes.Consumers[0].OldestUnackedAgeSeconds != 3600 || rep.Changes.Consumers[0].LagRows != 4 {
			t.Fatalf("exit %d consumers %+v", rep.Exit(), rep.Changes.Consumers)
		}
	})
}

func TestACollectionSourceThatFailsFailsTheCollection(t *testing.T) {
	r := newRig(t)
	r.feed.err = errors.New("feed down")
	if _, err := Collect(context.Background(), r.cfg); err == nil || !strings.Contains(err.Error(), "feed down") {
		t.Fatalf("err = %v", err)
	}
	r = newRig(t)
	r.settle.err = errors.New("settle down")
	if _, err := Collect(context.Background(), r.cfg); err == nil {
		t.Fatal("a failing settle source must fail the collection")
	}
}

func TestCollectNeedsItsSources(t *testing.T) {
	r := newRig(t)
	cfg := r.cfg
	cfg.Feed = nil
	if _, err := Collect(context.Background(), cfg); err == nil {
		t.Error("Collect without a feed must fail")
	}
	rt := router.New(router.Options{})
	if err := Register(rt, cfg); err == nil {
		t.Error("Register without a feed must fail")
	}
	cfg = r.cfg
	cfg.RouterInstance = ""
	if err := Register(rt, cfg); err == nil {
		t.Error("Register without a router instance must fail")
	}
}

func TestRegisterServesTheStatusOpOnTheNamedInstance(t *testing.T) {
	r := newRig(t)
	rt := router.New(router.Options{})
	if err := Register(rt, r.cfg); err != nil {
		t.Fatal(err)
	}
	if ops := rt.Ops("issue"); len(ops) != 1 || ops[0] != Op || Op != "status" {
		t.Fatalf("ops = %v", ops)
	}
	resp := rt.Dispatch(context.Background(), router.Call{Instance: "issue", Request: scriptout.Request{Op: "status"}})
	if resp.Error != nil {
		t.Fatalf("error = %+v", resp.Error)
	}
	if resp.SchemaVersion != schema.IssueSchemaVersion {
		t.Errorf("schema version = %d, want %d", resp.SchemaVersion, schema.IssueSchemaVersion)
	}
	var rep Report
	if err := json.Unmarshal(resp.Result, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Source != SourceDaemon || rep.Health != Healthy {
		t.Errorf("report = %s %s", rep.Source, rep.Health)
	}
}

func TestCollectCallsNoOrigin(t *testing.T) {
	r := newRig(t)
	before := r.gov.State()
	_ = r.collect()
	after := r.gov.State()
	b, _ := before.Bucket(origin.Bucket)
	a, _ := after.Bucket(origin.Bucket)
	if a.Spent != b.Spent || a.InFlight != b.InFlight {
		t.Fatalf("collect spent from the origin bucket: before %+v after %+v", b, a)
	}
}

// TestPackageSourceUsesNoOriginRunner keeps the no-origin rule in the
// code: the package may use the origin package's bucket name and the
// authentication marker's type, but never its runner, and it never imports the
// pjira package.
func TestPackageSourceUsesNoOriginRunner(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"origin.New(", "origin.Runner", "pg-connector-issue-jira/internal\"", "/internal/serve\"", "pg-connector-github/internal"} {
			if bytes.Contains(src, []byte(bad)) {
				t.Errorf("%s contains %q", f, bad)
			}
		}
	}
}

func TestTheReportCarriesCountsOnlyNoIssueKeyOrTitle(t *testing.T) {
	r := newRig(t)
	r.settle.stats = engine.Stats{Pending: 3, Timeouts: 1}
	r.feed.pos = []feed.Position{{Consumer: "reader", View: "changes", Scope: "mine", AckedSeq: 1, Head: 9, LagRows: 8, OldestUnacked: t0.Add(-time.Hour), SeenAt: t0}}
	r.putQuery("other", func(q *store.QueryRow) { q.IDs = []string{secretKey}; q.LastError = "jql rejected" })
	if err := r.gov.Pause(origin.Bucket, time.Minute, "jira answered 429"); err != nil {
		t.Fatal(err)
	}
	rep := r.collect()
	var js, txt bytes.Buffer
	if err := RenderJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	if err := Render(&txt, rep); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{js.String(), txt.String()} {
		for _, secret := range []string{secretKey, "ZZZ-78", secretTitle, "Payroll"} {
			if strings.Contains(out, secret) {
				t.Errorf("report leaks %q:\n%s", secret, out)
			}
		}
	}
	if rep.Tracked.Members != 1 || rep.Tracked.OtherOpen != 1 || rep.Changes.PendingSettle != 3 {
		t.Errorf("the counts must still be there: tracked %+v changes %+v", rep.Tracked, rep.Changes)
	}
}

func TestRenderIsStableForEqualInputAndAgreesWithTheJSON(t *testing.T) {
	r := newRig(t)
	_ = r.gov.Pause(origin.Bucket, time.Minute, "jira answered 429")
	a, b := r.collect(), r.collect()
	var ta, tb, ja, jb bytes.Buffer
	for _, x := range []struct {
		w   *bytes.Buffer
		f   func(w *bytes.Buffer, rep Report) error
		rep Report
	}{
		{&ta, func(w *bytes.Buffer, rep Report) error { return Render(w, rep) }, a},
		{&tb, func(w *bytes.Buffer, rep Report) error { return Render(w, rep) }, b},
		{&ja, func(w *bytes.Buffer, rep Report) error { return RenderJSON(w, rep) }, a},
		{&jb, func(w *bytes.Buffer, rep Report) error { return RenderJSON(w, rep) }, b},
	} {
		if err := x.f(x.w, x.rep); err != nil {
			t.Fatal(err)
		}
	}
	if ta.String() != tb.String() || ja.String() != jb.String() {
		t.Fatal("equal reports rendered differently")
	}
	// The text of a report that crossed the wire is the text of the original.
	var back Report
	if err := json.Unmarshal(ja.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	var tc bytes.Buffer
	if err := Render(&tc, back); err != nil {
		t.Fatal(err)
	}
	if tc.String() != ta.String() {
		t.Errorf("round-tripped text differs:\n%s\nvs\n%s", tc.String(), ta.String())
	}
	if !strings.HasPrefix(ta.String(), "health: degraded\n  reason: ") {
		t.Errorf("text = %q", ta.String())
	}
}

func TestExitCodes(t *testing.T) {
	for h, want := range map[Health]int{Healthy: 0, Degraded: 2, Down: 3, "": 3, "weird": 3} {
		if got := (Report{Health: h}).Exit(); got != want {
			t.Errorf("Exit(%q) = %d, want %d", h, got, want)
		}
	}
}

func TestStaticForAStoppedDaemonIsDownWithEverythingTheClientNeeds(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.StateDir = dir
	cfg.SocketPath = filepath.Join(dir, "sock")
	cfg.PjiraPath = "/abs/pjira"
	cfg.Path = filepath.Join(dir, "config.json")
	cfg.SupervisorLabel = "test.label"

	// The store file is not a database: Static must stat it, never open it.
	storePath := filepath.Join(dir, config.StoreFileName)
	if err := os.WriteFile(storePath, bytes.Repeat([]byte("x"), 1234), 0o600); err != nil {
		t.Fatal(err)
	}
	mod := t0.Add(-90 * time.Second)
	if err := os.Chtimes(storePath, mod, mod); err != nil {
		t.Fatal(err)
	}
	var probed string
	env := Env{
		Probe: func(label string) proc.SupervisorState { probed = label; return proc.SupervisorRegistered },
		Now:   func() time.Time { return t0 },
	}
	rep := StaticIn(env, cfg, "v9")

	if rep.Exit() != 3 || rep.Health != Down || rep.Source != SourceLocal {
		t.Fatalf("health %s exit %d source %s, want down 3 local", rep.Health, rep.Exit(), rep.Source)
	}
	if probed != "test.label" || rep.Supervisor.State != "registered" || rep.Supervisor.Label != "test.label" {
		t.Errorf("probe = %q supervisor = %+v", probed, rep.Supervisor)
	}
	if rep.Socket != cfg.SocketPath || rep.StateDir != dir || rep.Version != "v9" || rep.BuildID != "v9" {
		t.Errorf("report = %+v", rep)
	}
	if !rep.Store.Present || rep.Store.SizeBytes != 1234 || rep.Store.AgeSeconds != 90 || rep.Store.Path != storePath {
		t.Errorf("store = %+v, want size 1234 and age 90", rep.Store)
	}
	wantLogs := Logs{
		Events: filepath.Join(dir, "events.jsonl"),
		Daemon: filepath.Join(dir, "daemon.log.jsonl"),
		Client: filepath.Join(dir, "client.log.jsonl"),
	}
	if rep.Logs != wantLogs {
		t.Errorf("logs = %+v, want %+v", rep.Logs, wantLogs)
	}
	if rep.Requests != nil || rep.Queue != nil || rep.Changes != nil || rep.Tracked != nil || rep.Auth != nil || len(rep.Queries) != 0 {
		t.Errorf("a static report holds no daemon state: %+v", rep)
	}
	// No store was opened: the file is untouched and no sibling appeared.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("state dir holds %d entries, want only the store file (no store may be opened)", len(entries))
	}
	if b, _ := os.ReadFile(storePath); len(b) != 1234 {
		t.Errorf("store file changed: %d bytes", len(b))
	}
}

func TestStaticWithNoStoreFileReportsItAbsent(t *testing.T) {
	cfg := config.Defaults()
	cfg.StateDir = t.TempDir()
	cfg.SocketPath = filepath.Join(cfg.StateDir, "sock")
	rep := Static(cfg, "v1")
	if rep.Store.Present || rep.Store.SizeBytes != 0 || rep.Store.ModifiedAt != nil || rep.Exit() != 3 {
		t.Errorf("report = %+v", rep)
	}
}

func TestFallbackAnswersTheStatusOpWithTheStaticReportAndTheCause(t *testing.T) {
	cfg := config.Defaults()
	cfg.StateDir = t.TempDir()
	cfg.SocketPath = filepath.Join(cfg.StateDir, "sock")
	static := StaticIn(Env{Probe: func(string) proc.SupervisorState { return proc.SupervisorNone }, Now: func() time.Time { return t0 }}, cfg, "v1")
	fb := Fallback(static)
	out, err := fb(context.Background(), scriptout.Request{Op: Op}, errors.New("connection refused"))
	if err != nil {
		t.Fatal(err)
	}
	rep, ok := out.(Report)
	if !ok || rep.Exit() != 3 || len(rep.Reasons) != 1 || !strings.Contains(rep.Reasons[0], "connection refused") {
		t.Fatalf("answer = %#v", out)
	}
	if len(static.Reasons) != 0 {
		t.Errorf("the static report must not be altered by a fallback call: %v", static.Reasons)
	}
	var a, b bytes.Buffer
	_ = Render(&a, rep)
	out2, _ := fb(context.Background(), scriptout.Request{Op: Op}, errors.New("connection refused"))
	_ = Render(&b, out2.(Report))
	if a.String() != b.String() {
		t.Error("two fallback answers for the same cause render differently")
	}
}
