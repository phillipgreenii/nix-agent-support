package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	jira "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/notice"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "jcl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// events is the ordered record of what the fake daemon and the fake origin tool
// saw, so a test can assert the order of a write's three steps.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, s)
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

// fakeRunner is the origin tool: it records each call and answers the pjira
// shapes the backend decodes.
type fakeRunner struct {
	ev      *events
	delay   time.Duration
	failOn  string
	created string
}

func (f *fakeRunner) Binary() string { return "fake-pjira" }

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.ev.add("pjira " + args[0])
	time.Sleep(f.delay)
	if f.failOn == args[0] {
		return "", errors.New("fake-pjira: boom")
	}
	switch args[0] {
	case "create":
		return `{"key":"` + f.created + `"}`, nil
	case "issue":
		return `{"key":"` + args[len(args)-1] + `","summary":"s","status":"To Do","issuetype":"Task","labels":[],"url":"u"}`, nil
	}
	return `{}`, nil
}

// testConfig is a config over a short socket path that nothing listens on.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := shortTempDir(t)
	cfg := config.Defaults()
	cfg.StateDir = dir
	cfg.SocketPath = filepath.Join(dir, "s")
	cfg.PjiraPath = "/bin/pjira"
	cfg.Instance = "jira-test"
	cfg.SupervisorLabel = "com.example.daemon"
	cfg.Path = "/etc/example/config.json"
	cfg.WriteNoticeTimeout = config.Duration(400 * time.Millisecond)
	cfg.Queries = map[string]schema.QueryDef{
		"mine":  {Expr: schema.QueryExpr{"assignee = currentUser()"}, Watched: true},
		"other": {Expr: schema.QueryExpr{"project = X"}},
	}
	return cfg
}

type result struct {
	code int
	out  string
}

func runClient(t *testing.T, o Options, stdin string) result {
	t.Helper()
	var out bytes.Buffer
	code := Run(context.Background(), o, strings.NewReader(stdin), &out)
	return result{code, out.String()}
}

func baseOptions(cfg *config.Config, backend *jira.Backend) Options {
	o := Options{Config: cfg, Build: "build-1", Budget: 800 * time.Millisecond, Margin: 100 * time.Millisecond}
	if backend != nil {
		o.Backend = backend
	}
	return o
}

func errBody(t *testing.T, out string) *scriptout.Error {
	t.Helper()
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Error == nil {
		t.Fatalf("want an error body, got %q (%v)", out, err)
	}
	return resp.Error
}

// serveDaemon serves r on cfg's socket through the real process shell.
func serveDaemon(t *testing.T, cfg *config.Config, r *router.Router, protocol int) {
	t.Helper()
	pc := proc.Config{StateDir: cfg.StateDir, SocketPath: cfg.SocketPath, DrainTimeout: time.Second}
	srv, err := proc.Start(pc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, socket.Handler(r, socket.ServerConfig{Build: "daemon-1", Protocol: protocol}), nil)
	}()
	t.Cleanup(func() { cancel(); <-done })
}

// noticeDaemon is a daemon that answers the two write notices and records them.
func noticeDaemon(t *testing.T, cfg *config.Config, ev *events) *router.Router {
	t.Helper()
	r := router.New(router.Options{})
	for _, op := range []string{noticeBegin, noticeInvalidate} {
		r.Register(RouterInstance, op, scriptout.OpHandler{SchemaVersion: 1, Handle: func(_ context.Context, args json.RawMessage) (any, error) {
			ev.add(op + " " + compact(args))
			return nil, nil
		}})
	}
	serveDaemon(t, cfg, r, 0)
	return r
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

const commentReq = `{"op":"comment","args":{"id":"ABC-7","body":"hello"}}`

func TestCapabilitiesAnsweredWithNoDaemon(t *testing.T) {
	cfg := testConfig(t)
	start := time.Now()
	res := runClient(t, baseOptions(cfg, nil), `{"op":"capabilities"}`)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.out)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Error("capabilities waited on a daemon that is down")
	}
	var caps struct {
		ProtocolVersion int            `json:"protocolVersion"`
		SchemaVersions  map[string]int `json:"schemaVersions"`
		Ops             []string       `json:"ops"`
		Vocabulary      map[string]any `json:"vocabulary"`
		Version         string         `json:"version"`
	}
	if err := json.Unmarshal([]byte(res.out), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Version != "build-1" || caps.ProtocolVersion != scriptout.ProtocolVersion {
		t.Errorf("%+v", caps)
	}
	if caps.Vocabulary["owns_changes"] != true || caps.Vocabulary["cache_opt_out"] != true {
		t.Errorf("vocabulary %v must declare owns_changes and cache_opt_out", caps.Vocabulary)
	}
	queries, _ := caps.Vocabulary["queries"].(map[string]any)
	for name, want := range map[string]bool{"mine": true, "other": false} {
		q, _ := queries[name].(map[string]any)
		if got, ok := q["watched"].(bool); !ok || got != want {
			t.Errorf("queries.%s.watched = %v, want %v (all: %v)", name, q["watched"], want, queries)
		}
	}
	if len(queries) != 2 {
		t.Errorf("queries %v must list exactly the configured queries", queries)
	}
	for _, key := range []string{"state", "priority", "activity_kinds"} {
		if caps.Vocabulary[key] == nil {
			t.Errorf("vocabulary lacks %s", key)
		}
	}
	for _, want := range append(slices.Clone(WriteOps), "capabilities", "show", "list", "changes", "status") {
		if !slices.Contains(caps.Ops, want) {
			t.Errorf("ops %v lack %s", caps.Ops, want)
		}
	}
	if !slices.IsSorted(caps.Ops) || !slices.Equal(caps.Ops, OpNames()) {
		t.Errorf("ops %v must be the sorted union %v", caps.Ops, OpNames())
	}
	for _, name := range []string{"issue", "search", "activity"} {
		if caps.SchemaVersions[name] == 0 {
			t.Errorf("schemaVersions %v lacks %s", caps.SchemaVersions, name)
		}
	}
}

func TestCapabilitiesAreTheSameWithADaemonUp(t *testing.T) {
	cfg := testConfig(t)
	down := runClient(t, baseOptions(cfg, nil), `{"op":"capabilities"}`)
	r := router.New(router.Options{})
	r.Register(RouterInstance, "capabilities", scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) {
		t.Error("capabilities must never reach the daemon")
		return nil, nil
	}})
	serveDaemon(t, cfg, r, 0)
	up := runClient(t, baseOptions(cfg, nil), `{"op":"capabilities"}`)
	if down != up {
		t.Errorf("down %q != up %q", down.out, up.out)
	}
}

// The daemon's registered ops, the client's listed ops and the notice ops are
// one set, and the client addresses the daemon's own router instance.
func TestOpTablesAndNamesMatchTheDaemon(t *testing.T) {
	if RouterInstance != serve.Instance {
		t.Errorf("RouterInstance %q != serve.Instance %q", RouterInstance, serve.Instance)
	}
	if noticeBegin != notice.OpWriteBegin || noticeInvalidate != notice.OpInvalidate {
		t.Errorf("notice op names %q, %q drifted from %v", noticeBegin, noticeInvalidate, notice.Ops)
	}
	daemon := serve.OpNames()
	// The daemon registers the reads it serves and the notices; the writes are
	// the client's own and capabilities is never forwarded.
	want := slices.Clone(ForwardedOps)
	want = append(want, notice.Ops...)
	slices.Sort(want)
	slices.Sort(daemon)
	if !slices.Equal(daemon, want) {
		t.Errorf("daemon ops %v != forwarded + notice ops %v", daemon, want)
	}
	for _, op := range WriteOps {
		if slices.Contains(daemon, op) {
			t.Errorf("the daemon registers the write %s; writes must stay in the client", op)
		}
	}
}

func TestForwardedReadWithNoDaemonIsUnavailableNamingSocketAndHint(t *testing.T) {
	cfg := testConfig(t)
	for _, op := range []string{"show", "list", "changes", "something_new"} {
		o := baseOptions(cfg, nil)
		o.ClientLog = logs.NewClientLog(cfg.StateDir, "build-1")
		res := runClient(t, o, `{"op":"`+op+`"}`)
		e := errBody(t, res.out)
		if e.Code != "unavailable" || res.code != scriptout.ExitCodeForCode("unavailable") {
			t.Errorf("%s: %+v exit %d", op, e, res.code)
		}
		hint := proc.RestartHint(cfg.SupervisorLabel, cfg.Path, config.DaemonName)
		for _, w := range []string{cfg.SocketPath, hint, cfg.SupervisorLabel} {
			if !strings.Contains(e.Message, w) {
				t.Errorf("%s: message %q must contain %q", op, e.Message, w)
			}
		}
		if strings.Contains(res.out, "served_from") || strings.Contains(res.out, "cache") {
			t.Errorf("%s: a down daemon never answers from a cache: %s", op, res.out)
		}
	}
	raw, err := os.ReadFile(logs.ClientLogPath(cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(rows) != 4 {
		t.Fatalf("want one daemon_unreachable row per unavailable answer, got %d: %s", len(rows), raw)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(rows[0]), &row); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{"kind": "request", "outcome": "daemon_unreachable", "op": "show", "instance": "jira-test", "error_code": "unavailable"} {
		if row[k] != v {
			t.Errorf("row %s = %v, want %v", k, row[k], v)
		}
	}
}

// The op catalog says the status op reports even when the daemon is down, so
// it is the one forwarded op answered locally: the static report as a result
// (exit 0), health down, source local, naming the socket and the supervisor,
// stamped with the issue schema version, and no daemon_unreachable row.
func TestStatusOpWithNoDaemonIsTheLocalDownReportNotUnavailable(t *testing.T) {
	cfg := testConfig(t)
	o := baseOptions(cfg, nil)
	o.ClientLog = logs.NewClientLog(cfg.StateDir, "build-1")
	res := runClient(t, o, `{"op":"status"}`)
	var resp struct {
		SchemaVersion int           `json:"schemaVersion"`
		Result        status.Report `json:"result"`
		Error         *scriptout.Error
	}
	if err := json.Unmarshal([]byte(res.out), &resp); err != nil {
		t.Fatalf("%q: %v", res.out, err)
	}
	if res.code != 0 || resp.Error != nil {
		t.Fatalf("exit %d, error %+v: %s", res.code, resp.Error, res.out)
	}
	r := resp.Result
	if r.Health != status.Down || r.Source != status.SourceLocal || r.Socket != cfg.SocketPath || r.Version != "build-1" {
		t.Errorf("report = %+v, want a local down report for %s", r, cfg.SocketPath)
	}
	if r.Supervisor.Label != cfg.SupervisorLabel || r.Supervisor.RestartHint != proc.RestartHint(cfg.SupervisorLabel, cfg.Path, config.DaemonName) {
		t.Errorf("supervisor = %+v", r.Supervisor)
	}
	if len(r.Reasons) == 0 || !strings.Contains(r.Reasons[0], "the daemon is not reachable") || !strings.Contains(r.Reasons[0], cfg.SocketPath) {
		t.Errorf("reasons = %v, want the unreachable reason naming the socket", r.Reasons)
	}
	if resp.SchemaVersion != schema.IssueSchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", resp.SchemaVersion, schema.IssueSchemaVersion)
	}
	if _, err := os.Stat(logs.ClientLogPath(cfg.StateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a locally answered status op is not an outage: client log stat err = %v", err)
	}
}

func TestAMigratingDaemonAnswersUnavailableBeginningMigrating(t *testing.T) {
	cfg := testConfig(t)
	r := router.New(router.Options{})
	r.Register(RouterInstance, "show", scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) { return "ok", nil }})
	serveDaemon(t, cfg, r, 0)
	r.SetMigrating(true, "")
	res := runClient(t, baseOptions(cfg, nil), `{"op":"show","args":{"id":"ABC-1"}}`)
	if e := errBody(t, res.out); e.Code != "unavailable" || !strings.HasPrefix(e.Message, "migrating:") || res.code != scriptout.ExitCodeForCode("unavailable") {
		t.Errorf("%+v exit %d", e, res.code)
	}
}

func TestForwardRelaysTheDaemonsAnswerAndItsDeadline(t *testing.T) {
	cfg := testConfig(t)
	r := router.New(router.Options{})
	r.Register(RouterInstance, "list", scriptout.OpHandler{SchemaVersion: 7, Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
		dl, ok := ctx.Deadline()
		return map[string]any{"args": json.RawMessage(args), "has_deadline": ok, "left_ms": time.Until(dl).Milliseconds()}, nil
	}})
	serveDaemon(t, cfg, r, 0)
	res := runClient(t, baseOptions(cfg, nil), `{"op":"list","args":{"query":"mine"}}`)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.out)
	}
	var resp struct {
		SchemaVersion int `json:"schemaVersion"`
		Result        struct {
			Args        map[string]string `json:"args"`
			HasDeadline bool              `json:"has_deadline"`
			LeftMS      int64             `json:"left_ms"`
		}
	}
	if err := json.Unmarshal([]byte(res.out), &resp); err != nil {
		t.Fatal(err)
	}
	// Budget 800ms less the 100ms margin: the daemon was told about 700ms.
	if resp.SchemaVersion != 7 || resp.Result.Args["query"] != "mine" || !resp.Result.HasDeadline || resp.Result.LeftMS > 700 || resp.Result.LeftMS < 300 {
		t.Errorf("%+v", resp)
	}
	// An op nobody registered is forwarded and the daemon says unknown_op.
	res = runClient(t, baseOptions(cfg, nil), `{"op":"nonsense"}`)
	if e := errBody(t, res.out); e.Code != "unknown_op" || res.code != scriptout.ExitCodeForCode("unknown_op") {
		t.Errorf("%+v exit %d", e, res.code)
	}
}

func TestMalformedStdinAnswersAnErrorWithItsExitCode(t *testing.T) {
	cfg := testConfig(t)
	for name, in := range map[string]string{"empty": "", "not json": "{nope", "no op": `{"args":{}}`} {
		res := runClient(t, baseOptions(cfg, nil), in)
		e := errBody(t, res.out)
		if e.Code != "unavailable" || res.code != scriptout.ExitCodeForCode(e.Code) || res.code == 0 || res.code == 1 {
			t.Errorf("%s: %+v exit %d", name, e, res.code)
		}
	}
}

// A daemon outside the client's protocol window is version_mismatch; one
// version behind (N-1) is served.
func TestVersionMismatchOnlyOutsideTheSupportedRange(t *testing.T) {
	cfg := testConfig(t)
	r := router.New(router.Options{})
	r.Register(RouterInstance, "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) { return "ok", nil }})
	serveDaemon(t, cfg, r, 4)

	o := baseOptions(cfg, nil)
	o.Protocol = 2 // the daemon at 4 is two ahead: outside the range
	res := runClient(t, o, `{"op":"list"}`)
	if e := errBody(t, res.out); e.Code != "version_mismatch" || res.code != scriptout.ExitCodeForCode("version_mismatch") {
		t.Errorf("%+v exit %d", e, res.code)
	}
	o.Protocol = 3 // one behind (N-1): still accepted
	if res := runClient(t, o, `{"op":"list"}`); res.code != 0 {
		t.Errorf("N-1 is accepted: %s", res.out)
	}
	o.Protocol = 4
	if res := runClient(t, o, `{"op":"list"}`); res.code != 0 {
		t.Errorf("N is accepted: %s", res.out)
	}
}

func TestStatusCollect(t *testing.T) {
	cfg := testConfig(t)
	// No daemon: the static report, down, exit 3.
	o := baseOptions(cfg, nil)
	r := CollectStatus(context.Background(), o)
	if r.Health != status.Down || r.Exit() != 3 || r.Source != status.SourceLocal || r.Socket != cfg.SocketPath {
		t.Errorf("down report %+v", r)
	}
	if len(r.Reasons) == 0 || !strings.Contains(r.Reasons[0], "not reachable") {
		t.Errorf("reasons %v", r.Reasons)
	}

	// A daemon that answers: its report, exit by its health.
	rt := router.New(router.Options{})
	health := status.Healthy
	rt.Register(RouterInstance, status.Op, scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) {
		return status.Report{Health: health, Source: status.SourceDaemon, Version: "daemon-1"}, nil
	}})
	serveDaemon(t, cfg, rt, 0)
	if r := CollectStatus(context.Background(), o); r.Source != status.SourceDaemon || r.Exit() != 0 || r.Version != "daemon-1" {
		t.Errorf("healthy report %+v", r)
	}
	health = status.Degraded
	if r := CollectStatus(context.Background(), o); r.Exit() != 2 {
		t.Errorf("degraded report %+v", r)
	}

	// A migrating daemon answers an error: the static report, degraded.
	rt.SetMigrating(true, "")
	r = CollectStatus(context.Background(), o)
	if r.Health != status.Degraded || r.Exit() != 2 || !strings.Contains(r.Reasons[0], "migrating") {
		t.Errorf("migrating report %+v", r)
	}
}

// ---- writes ----

func TestACommentWithNoDaemonSucceedsAndIsNotDelayedBeyondTheNoticeTimeout(t *testing.T) {
	cfg := testConfig(t)
	ev := &events{}
	const writeTime = 100 * time.Millisecond
	backend := jira.New(&fakeRunner{ev: ev, delay: writeTime})
	start := time.Now()
	res := runClient(t, baseOptions(cfg, backend), commentReq)
	elapsed := time.Since(start)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.out)
	}
	if got := ev.all(); !slices.Equal(got, []string{"pjira comment"}) {
		t.Errorf("events %v", got)
	}
	if limit := writeTime + cfg.WriteNoticeTimeout.Std() + 150*time.Millisecond; elapsed > limit {
		t.Errorf("a write with no daemon took %s, want at most the write plus the notice timeout (%s)", elapsed, limit)
	}
	if strings.Contains(res.out, "unavailable") {
		t.Errorf("a write is never answered unavailable for want of the daemon: %s", res.out)
	}
}

func TestAWriteSendsWriteBeginThenInvalidateAroundTheOriginCall(t *testing.T) {
	cfg := testConfig(t)
	ev := &events{}
	noticeDaemon(t, cfg, ev)
	backend := jira.New(&fakeRunner{ev: ev})
	for _, tc := range []struct {
		req, op string
	}{
		{commentReq, "comment"},
		{`{"op":"transition","args":{"id":"ABC-7","target_state":"Done"}}`, "transition"},
		{`{"op":"update","args":{"id":"ABC-7","fields":{"title":"t"}}}`, "update"},
		{`{"op":"close","args":{"id":"ABC-7","reason":"done"}}`, "close"},
	} {
		ev.list = nil
		res := runClient(t, baseOptions(cfg, backend), tc.req)
		if res.code != 0 && tc.op != "update" {
			t.Fatalf("%s: exit %d: %s", tc.op, res.code, res.out)
		}
		got := ev.all()
		if len(got) < 3 || got[0] != `write_begin {"id":"ABC-7"}` || got[len(got)-1] != `invalidate {"id":"ABC-7"}` || !strings.HasPrefix(got[1], "pjira ") {
			t.Errorf("%s: events %v, want write_begin, the origin call(s), then invalidate", tc.op, got)
		}
	}
}

func TestAWriteWithADaemonThatNeverAnswersWriteBeginStillHappensAndSkipsInvalidate(t *testing.T) {
	cfg := testConfig(t)
	ev := &events{}
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	var conns sync.WaitGroup
	hold := make(chan struct{})
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			conns.Add(1)
			go func() {
				defer conns.Done()
				defer func() { _ = c.Close() }()
				<-hold // reads nothing, answers nothing
			}()
		}
	}()
	t.Cleanup(func() { close(hold); _ = ln.Close(); conns.Wait() })

	const writeTime = 100 * time.Millisecond
	backend := jira.New(&fakeRunner{ev: ev, delay: writeTime})
	start := time.Now()
	res := runClient(t, baseOptions(cfg, backend), commentReq)
	elapsed := time.Since(start)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.out)
	}
	if got := ev.all(); !slices.Equal(got, []string{"pjira comment"}) {
		t.Errorf("the write must still happen: %v", got)
	}
	if n := len(accepted); n != 1 {
		t.Errorf("the daemon saw %d connections, want exactly 1 (write_begin only; invalidate is skipped)", n)
	}
	if limit := writeTime + cfg.WriteNoticeTimeout.Std() + 150*time.Millisecond; elapsed > limit {
		t.Errorf("took %s, want at most the write plus the notice timeout (%s)", elapsed, limit)
	}
}

func TestACreateHoldsTheInstanceThenInvalidatesTheNewKey(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv(jira.EnvProject, "ABC")
	ev := &events{}
	noticeDaemon(t, cfg, ev)
	backend := jira.New(&fakeRunner{ev: ev, created: "ABC-42"})
	res := runClient(t, baseOptions(cfg, backend), `{"op":"create","args":{"title":"t","issue_type":"Task"}}`)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.out)
	}
	want := []string{`write_begin {"instance":"jira-test"}`, "pjira create", "pjira issue", `invalidate {"id":"ABC-42"}`}
	if got := ev.all(); !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
}

func TestAFailedCreateReleasesTheInstanceHold(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv(jira.EnvProject, "ABC")
	ev := &events{}
	noticeDaemon(t, cfg, ev)
	backend := jira.New(&fakeRunner{ev: ev, failOn: "create"})
	res := runClient(t, baseOptions(cfg, backend), `{"op":"create","args":{"title":"t","issue_type":"Task"}}`)
	if res.code == 0 {
		t.Fatalf("a failed create must exit non-zero: %s", res.out)
	}
	want := []string{`write_begin {"instance":"jira-test"}`, "pjira create", `invalidate {"instance":"jira-test"}`}
	if got := ev.all(); !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
}

func TestAWriteWithoutAnIdSendsNoNotice(t *testing.T) {
	cfg := testConfig(t)
	ev := &events{}
	noticeDaemon(t, cfg, ev)
	res := runClient(t, baseOptions(cfg, jira.New(&fakeRunner{ev: ev})), `{"op":"comment","args":{"body":"x"}}`)
	if res.code == 0 {
		t.Fatalf("a comment without an id must fail: %s", res.out)
	}
	if got := ev.all(); len(got) != 0 {
		t.Errorf("events %v: a malformed write tells the daemon nothing", got)
	}
}
