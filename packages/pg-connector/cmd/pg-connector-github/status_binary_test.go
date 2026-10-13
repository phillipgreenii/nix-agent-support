package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The status subcommand and the status and explain ops are tested through the
// BUILT binary against an in-process daemon: the real process shell, socket
// server, router and store, with the Phase 2 handlers registered by hand (the
// full serve is assembled later; the same check against a real serve belongs to
// that packet's tests).

type fixedBudgets struct{ st governor.State }

func (f fixedBudgets) State() governor.State { return f.st }

type fixedQueue struct{ st scheduler.State }

func (f fixedQueue) State() scheduler.State                  { return f.st }
func (fixedQueue) Position(entity, group string) (int, bool) { return 2, group == "summary" }

type fixedIdentity struct{ snap identity.Snapshot }

func (f fixedIdentity) Current() identity.Snapshot { return f.snap }

type fixedQueries map[string]store.QueryRow

func (f fixedQueries) GetQuery(_ context.Context, key string) (store.QueryRow, bool, error) {
	r, ok := f[key]
	return r, ok, nil
}

// statusDaemon is the in-process daemon with the Phase 2 handlers.
type statusDaemon struct {
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

// stop drains the daemon; it is idempotent.
func (d *statusDaemon) stop() {
	d.once.Do(func() {
		d.cancel()
		<-d.done
	})
}

type statusDaemonOpts struct {
	paused bool
}

const (
	statusNode = "PR_node_status"
	statusRef  = "example-org/example-repo#7"
)

func startStatusDaemon(t *testing.T, cfgPath string, o statusDaemonOpts) *statusDaemon {
	t.Helper()
	cfg, err := config.Load(cfgPath, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := store.Open(filepath.Join(cfg.StateDir, config.StoreFileName), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	ctx := context.Background()
	if err := st.PutAlias(ctx, store.AliasRow{NodeID: statusNode, Ref: statusRef, SeenAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.GroupRow{
		Kind: entity.KindPR, ID: statusNode, Group: "summary", Content: []byte(`{}`),
		FetchedAt: now.Add(-time.Minute), FreshUntil: now.Add(time.Minute), FetchedForHead: "head-1", FetchedForBase: "base-1",
	}); err != nil {
		t.Fatal(err)
	}

	budgets := governor.State{At: now, Buckets: []governor.BucketState{{Bucket: governor.BucketGraphQL, Cap: 100, Window: time.Hour, Remaining: -1}}}
	if o.paused {
		budgets.Buckets[0].Paused, budgets.Buckets[0].PauseReason, budgets.Buckets[0].PausedUntil = true, "secondary rate limit", now.Add(time.Minute)
	}
	queue := fixedQueue{scheduler.State{At: now, Classes: []scheduler.ClassState{{Class: governor.ClassQuery, Depth: 1}}}}
	static := status.StaticFromConfig(cfg, "test-daemon")
	static.Probe = func(string) proc.SupervisorState { return proc.SupervisorRunning }
	static.SupervisorKind = status.KindLaunchd

	r := router.New(router.Options{Instances: []string{"pr", "ci"}})
	if err := status.Register(r, status.Config{
		Static: static, Budgets: fixedBudgets{budgets}, Scheduler: queue, Queries: fixedQueries{},
		Identity: fixedIdentity{identity.Snapshot{Login: "phillipgreenii"}}, Started: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := explain.Register(r, explain.Config{Store: st, Queue: queue}); err != nil {
		t.Fatal(err)
	}

	srv, err := proc.Start(proc.Config{StateDir: cfg.StateDir, SocketPath: cfg.SocketPath, DrainTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithCancel(context.Background())
	d := &statusDaemon{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		_ = srv.Serve(dctx, socket.Handler(r, socket.ServerConfig{Build: "test-daemon"}), nil)
	}()
	t.Cleanup(d.stop)
	return d
}

// runBinary runs bin with args, an empty environment and working directory "/".
func runBinary(t *testing.T, bin string, stdin string, args ...string) clientResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := clientResult{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	default:
		t.Fatal(err)
	}
	return res
}

func decodeReport(t *testing.T, res clientResult) status.Report {
	t.Helper()
	var r status.Report
	if err := json.Unmarshal([]byte(res.stdout), &r); err != nil {
		t.Fatalf("stdout is not a status document: %v\nstdout: %s\nstderr: %s", err, res.stdout, res.stderr)
	}
	return r
}

func TestStatusSubcommandAgainstALiveAndAStoppedDaemon(t *testing.T) {
	bin := fastClient(t)
	cfg, state, sock := clientSetup(t)
	d := startStatusDaemon(t, cfg, statusDaemonOpts{})

	// Daemon up: the document parses, exit 0, every Phase 2 section present.
	res := runBinary(t, bin, "", "--config", cfg, "status", "--json")
	if res.code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	up := decodeReport(t, res)
	if up.Health != status.Healthy || up.Source != status.SourceDaemon || up.Version != "test-daemon" || up.UptimeSeconds == nil ||
		len(up.Budget) != 1 || up.Queue == nil || up.Auth == nil || up.Auth.Viewer != "phillipgreenii" || len(up.Instances) != 2 ||
		up.Supervisor.State != "running" || up.Socket != sock || !up.Store.Present || up.ClientBuildID != "dev" || !up.BuildDiffers {
		t.Errorf("%+v", up)
	}

	// The text form is the same report.
	text := runBinary(t, bin, "", "--config", cfg, "status")
	if text.code != 0 || !strings.Contains(text.stdout, "health: healthy") || !strings.Contains(text.stdout, "pg-connector-pr-github: pr") ||
		!strings.Contains(text.stdout, "viewer: phillipgreenii") || !strings.Contains(text.stdout, "uptime_seconds: ") {
		t.Errorf("exit %d\n%s", text.code, text.stdout)
	}

	// Daemon stopped: the local report and exit 3.
	d.stop()
	res = runBinary(t, bin, "", "--config", cfg, "status", "--json")
	if res.code != 3 {
		t.Fatalf("exit %d, want 3 (down)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	down := decodeReport(t, res)
	wantLogs := status.Logs{
		Events: filepath.Join(state, config.EventLogFileName), Daemon: filepath.Join(state, config.DaemonLogFileName), Client: filepath.Join(state, config.ClientLogFileName),
	}
	if down.Health != status.Down || down.Source != status.SourceLocal || down.Socket != sock || down.Logs != wantLogs ||
		down.Store.Path != filepath.Join(state, config.StoreFileName) || !down.Store.Present || down.Store.SizeBytes == 0 ||
		down.Supervisor.Kind == "" || down.Supervisor.State == "" || len(down.Instances) != 2 || down.Budget != nil {
		t.Errorf("%+v", down)
	}
	if len(down.Reasons) != 1 || !strings.Contains(down.Reasons[0], sock) {
		t.Errorf("the reason names the socket: %v", down.Reasons)
	}
	textDown := runBinary(t, bin, "", "--config", cfg, "status")
	if textDown.code != 3 || !strings.Contains(textDown.stdout, "health: down") || !strings.Contains(textDown.stdout, "socket: "+sock) {
		t.Errorf("exit %d\n%s", textDown.code, textDown.stdout)
	}
}

func TestStatusSubcommandExitsTwoWhenDegraded(t *testing.T) {
	bin := fastClient(t)
	cfg, _, _ := clientSetup(t)
	startStatusDaemon(t, cfg, statusDaemonOpts{paused: true})
	res := runBinary(t, bin, "", "--config", cfg, "status", "--json")
	if res.code != 2 {
		t.Fatalf("exit %d, want 2 (degraded)\n%s %s", res.code, res.stdout, res.stderr)
	}
	r := decodeReport(t, res)
	if r.Health != status.Degraded || len(r.Reasons) != 1 || !strings.Contains(r.Reasons[0], "paused") {
		t.Errorf("%+v", r)
	}
}

func TestStatusSubcommandRejectsAnUnknownArgument(t *testing.T) {
	bin := buildClient(t, "")
	cfg, _, _ := clientSetup(t)
	res := runBinary(t, bin, "", "--config", cfg, "status", "--yaml")
	if res.code != exitUsage || !strings.Contains(res.stderr, "--yaml") {
		t.Errorf("exit %d stderr %q", res.code, res.stderr)
	}
}

func TestStatusAndExplainOpsThroughTheClientMode(t *testing.T) {
	bin := fastClient(t)
	cfg, _, _ := clientSetup(t)
	d := startStatusDaemon(t, cfg, statusDaemonOpts{})

	for _, mode := range []string{"pr", "ci"} {
		res := runBinary(t, bin, `{"op":"status"}`, "--config", cfg, mode)
		var resp struct {
			Result status.Report `json:"result"`
		}
		if err := json.Unmarshal([]byte(res.stdout), &resp); err != nil || res.code != 0 || resp.Result.Source != status.SourceDaemon || resp.Result.Health != status.Healthy {
			t.Errorf("%s status: exit %d %s (%v)", mode, res.code, res.stdout, err)
		}
	}

	res := runBinary(t, bin, `{"op":"explain","args":{"id":"`+statusRef+`"}}`, "--config", cfg, "pr")
	var ex struct {
		Result explain.Result `json:"result"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &ex); err != nil || res.code != 0 || len(ex.Result.Groups) != 1 ||
		ex.Result.Groups[0].FetchedForHead != "head-1" || ex.Result.Groups[0].FetchedForBase != "base-1" ||
		ex.Result.Groups[0].QueuePosition == nil || *ex.Result.Groups[0].QueuePosition != 2 {
		t.Errorf("explain: exit %d %s (%v)", res.code, res.stdout, err)
	}
	res = runBinary(t, bin, `{"op":"explain","args":{"id":"example-org/example-repo#404"}}`, "--config", cfg, "pr")
	if e := res.errorBody(t); e.Code != "not_found" || res.code != scriptout.ExitCodeForCode("not_found") {
		t.Errorf("explain unknown id: %+v exit %d", e, res.code)
	}

	// With the daemon down the status op is answered locally, not unavailable.
	d.stop()
	res = runBinary(t, bin, `{"op":"status"}`, "--config", cfg, "pr")
	var local struct {
		Result status.Report `json:"result"`
		Error  *scriptout.Error
	}
	if err := json.Unmarshal([]byte(res.stdout), &local); err != nil || res.code != 0 || local.Error != nil ||
		local.Result.Health != status.Down || local.Result.Source != status.SourceLocal || local.Result.Supervisor.State == "" {
		t.Errorf("status op, daemon down: exit %d %s (%v)", res.code, res.stdout, err)
	}
	// explain has no local fallback: it answers unavailable.
	res = runBinary(t, bin, `{"op":"explain","args":{"id":"`+statusRef+`"}}`, "--config", cfg, "pr")
	if e := res.errorBody(t); e.Code != "unavailable" || res.code != exitUnavailable {
		t.Errorf("explain, daemon down: %+v exit %d", e, res.code)
	}
}

func TestStatusStoreSizeAndAgeAreFromTheFile(t *testing.T) {
	bin := fastClient(t)
	cfg, state, _ := clientSetup(t)
	p := filepath.Join(state, config.StoreFileName)
	if err := os.WriteFile(p, bytes.Repeat([]byte("s"), 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	res := runBinary(t, bin, "", "--config", cfg, "status", "--json")
	r := decodeReport(t, res)
	if res.code != 3 || r.Store.SizeBytes != 2048 || r.Store.AgeSeconds < 7200 || r.Store.AgeSeconds > 7300 {
		t.Errorf("exit %d store %+v", res.code, r.Store)
	}
}
