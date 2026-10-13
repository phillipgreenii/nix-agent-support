package status

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// ---- fakes -------------------------------------------------------------

type fakeBudgets struct{ st governor.State }

func (f fakeBudgets) State() governor.State { return f.st }

type fakeQueue struct{ st scheduler.State }

func (f fakeQueue) State() scheduler.State { return f.st }

type fakeQueries map[string]store.QueryRow

func (f fakeQueries) GetQuery(_ context.Context, key string) (store.QueryRow, bool, error) {
	r, ok := f[key]
	return r, ok, nil
}

type fakeIdentity struct{ snap identity.Snapshot }

func (f fakeIdentity) Current() identity.Snapshot { return f.snap }

func healthyBudgets() governor.State {
	return governor.State{At: t0, Buckets: []governor.BucketState{
		{
			Bucket: governor.BucketGraphQL, Cap: 4000, BackgroundCap: 2800, Reserve: 200, Window: time.Hour,
			Spent: 310, BackgroundSpent: 120, InFlight: 5, Remaining: 3600, Limit: 5000, ResetAt: t0.Add(40 * time.Minute),
		},
		{
			Bucket: governor.BucketRESTCore, Cap: 3000, BackgroundCap: 2100, Reserve: 100, Window: time.Hour,
			Spent: 40, BackgroundSpent: 10, Remaining: -1,
		},
		{
			Bucket: governor.BucketRESTSearch, Cap: 25, BackgroundCap: 15, Reserve: 5, Window: time.Minute,
			Spent: 2, BackgroundSpent: 1, Remaining: -1,
		},
	}}
}

func queueState() scheduler.State {
	return scheduler.State{At: t0, Running: 1, Classes: []scheduler.ClassState{
		{Class: governor.ClassInteractive},
		{Class: governor.ClassInvalidated},
		{Class: governor.ClassQuery, Depth: 4, OldestOverdue: 90 * time.Second},
		{Class: governor.ClassKeepAlive, Depth: 12},
		{Class: governor.ClassFill, Depth: 30},
	}}
}

func okIdentity() identity.Snapshot { return identity.Snapshot{Login: "phillipgreenii", FetchedAt: t0} }

func okQueries() fakeQueries {
	return fakeQueries{
		"mine": {Key: "mine", Configured: true, LastSuccessAt: t0.Add(-2 * time.Minute)},
		"team": {Key: "team", Configured: true, LastSuccessAt: t0.Add(-5 * time.Minute)},
	}
}

// testStatic is a Static over paths that do not exist, with the supervisor
// probe answered by the test.
func testStatic(state proc.SupervisorState, kind string) Static {
	return Static{
		Version: "26.10.12.00001+abcd1234", BuildID: "26.10.12.00001+abcd1234",
		StateDir: "/state", SocketPath: "/state/sock",
		StoreFile: "/state/" + config.StoreFileName,
		EventLog:  "/state/" + config.EventLogFileName, DaemonLog: "/state/" + config.DaemonLogFileName, ClientLog: "/state/" + config.ClientLogFileName,
		SupervisorLabel: "com.example.pg-connector-github-daemon",
		SupervisorKind:  kind,
		Probe:           func(string) proc.SupervisorState { return state },
		QueryNames:      []string{"team", "mine"},
	}
}

func testConfig(t *testing.T) (Config, *governor.ManualClock) {
	t.Helper()
	clk := governor.NewManualClock(t0)
	return Config{
		Static:    testStatic(proc.SupervisorRunning, KindLaunchd),
		Budgets:   fakeBudgets{healthyBudgets()},
		Scheduler: fakeQueue{queueState()},
		Queries:   okQueries(),
		Identity:  fakeIdentity{okIdentity()},
		Started:   t0.Add(-3*time.Hour - 25*time.Second),
		Clock:     clk,
	}, clk
}

func build(t *testing.T, cfg Config) Report {
	t.Helper()
	b, err := NewBuilder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// golden compares r's JSON document with testdata/<name> (rewritten with
// -update). The comparison is of the decoded documents, not the bytes: the
// repo's formatter lays the file out its own way.
func golden(t *testing.T, name string, r Report) {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	var gotDoc, wantDoc any
	if err := json.Unmarshal(buf.Bytes(), &gotDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatalf("golden %s is not JSON: %v", name, err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("%s differs from the golden document.\ngot:\n%s\nwant:\n%s", name, buf.Bytes(), want)
	}
}

// ---- the daemon's report ------------------------------------------------

func TestDaemonReportCarriesEveryPhase2Field(t *testing.T) {
	cfg, _ := testConfig(t)
	r := build(t, cfg)
	golden(t, "status_daemon.golden.json", r)

	if r.Health != Healthy || r.Source != SourceDaemon || len(r.Reasons) != 0 {
		t.Errorf("health %q source %q reasons %v", r.Health, r.Source, r.Reasons)
	}
	if r.Version == "" || r.BuildID == "" {
		t.Errorf("version %q build %q", r.Version, r.BuildID)
	}
	if r.UptimeSeconds == nil || *r.UptimeSeconds != 3*3600+25 || r.StartedAt == nil {
		t.Errorf("uptime %v started %v", r.UptimeSeconds, r.StartedAt)
	}
	if r.Store.Path != "/state/store.sqlite" || r.Socket != "/state/sock" {
		t.Errorf("store %+v socket %q", r.Store, r.Socket)
	}
	if len(r.Budget) != 3 || r.Budget[0].Bucket != "graphql" || r.Budget[0].Spent != 310 || r.Budget[0].Remaining == nil || *r.Budget[0].Remaining != 3600 {
		t.Errorf("budget %+v", r.Budget)
	}
	if r.Budget[1].Remaining != nil {
		t.Errorf("no origin reading in force: remaining must be absent, got %v", *r.Budget[1].Remaining)
	}
	if r.Queue == nil || len(r.Queue.Classes) != 5 || r.Queue.Classes[2].Name != "query" || r.Queue.Classes[2].Depth != 4 || r.Queue.Classes[2].OldestOverdueSeconds != 90 {
		t.Errorf("queue %+v", r.Queue)
	}
	if len(r.Queries) != 2 || r.Queries[0].Name != "mine" || r.Queries[0].LastSuccessAt == nil || !r.Queries[0].LastSuccessAt.Equal(t0.Add(-2*time.Minute)) {
		t.Errorf("queries %+v", r.Queries)
	}
	if r.Auth == nil || r.Auth.State != "OK" || r.Auth.Viewer != "phillipgreenii" || r.Auth.Unauthenticated {
		t.Errorf("auth %+v", r.Auth)
	}
	if r.Supervisor.Kind != KindLaunchd || r.Supervisor.State != "running" || r.Supervisor.Label == "" {
		t.Errorf("supervisor %+v", r.Supervisor)
	}
	wantMap := []InstanceName{{RegistryNamePR, "pr"}, {RegistryNameCI, "ci"}}
	if fmt.Sprint(r.Instances) != fmt.Sprint(wantMap) {
		t.Errorf("instance-name mapping %v, want %v", r.Instances, wantMap)
	}
	wantLogs := Logs{Events: "/state/events.jsonl", Daemon: "/state/daemon.log.jsonl", Client: "/state/client.log.jsonl"}
	if r.Logs != wantLogs {
		t.Errorf("logs %+v", r.Logs)
	}
}

func TestDaemonReportIsServedByTheOpOnBothInstances(t *testing.T) {
	cfg, _ := testConfig(t)
	rt := router.New(router.Options{Instances: []string{"pr", "ci"}})
	if err := Register(rt, cfg); err != nil {
		t.Fatal(err)
	}
	for _, inst := range []string{"pr", "ci"} {
		resp := rt.Dispatch(context.Background(), router.Call{Instance: inst, Request: scriptout.Request{Op: OpName}})
		if resp.Error != nil {
			t.Fatalf("%s: %+v", inst, resp.Error)
		}
		var r Report
		if err := json.Unmarshal(resp.Result, &r); err != nil {
			t.Fatalf("%s: %v", inst, err)
		}
		if r.Source != SourceDaemon || r.Health != Healthy || r.Auth == nil || r.Auth.Viewer != "phillipgreenii" {
			t.Errorf("%s: %+v", inst, r)
		}
	}
	if err := Register(router.New(router.Options{}), Config{}); err == nil {
		t.Error("a handler without its sources must be refused at wiring time")
	}
}

// ---- degraded and healthy ------------------------------------------------

func TestHealthRule(t *testing.T) {
	paused := healthyBudgets()
	paused.Buckets[1].Paused = true
	paused.Buckets[1].PauseReason = "secondary rate limit"
	paused.Buckets[1].PausedUntil = t0.Add(10 * time.Minute)

	for _, tc := range []struct {
		name       string
		mutate     func(*Config)
		want       Health
		wantExit   int
		wantReason string
	}{
		{"nothing wrong", func(*Config) {}, Healthy, 0, ""},
		{"a paused governor bucket", func(c *Config) { c.Budgets = fakeBudgets{paused} }, Degraded, 2, "rest_core is paused: secondary rate limit"},
		{"unauthenticated", func(c *Config) {
			c.Identity = fakeIdentity{identity.Snapshot{Login: "phillipgreenii", Unauthenticated: true, AuthError: "bad credentials"}}
		}, Degraded, 2, "unauthenticated: unauthenticated: bad credentials"},
		{"a configured query with a last_error", func(c *Config) {
			q := okQueries()
			row := q["team"]
			row.LastError = "search failed: HTTP 502"
			q["team"] = row
			c.Queries = q
		}, Degraded, 2, "query team last_error: search failed: HTTP 502"},
		{"the viewer was never resolved and refresh fails", func(c *Config) {
			c.Identity = fakeIdentity{identity.Snapshot{LastError: "gh: not logged in"}}
		}, Degraded, 2, "viewer not resolved: gh: not logged in"},
		{"the viewer is not resolved yet (a fresh start)", func(c *Config) { c.Identity = fakeIdentity{identity.Snapshot{}} }, Healthy, 0, ""},
		{"an overdue queue and a missing store are reported, not judged", func(c *Config) {
			st := queueState()
			st.Classes[3].OldestOverdue = 5 * time.Hour
			c.Scheduler = fakeQueue{st}
		}, Healthy, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := testConfig(t)
			tc.mutate(&cfg)
			r := build(t, cfg)
			if r.Health != tc.want || r.Health.ExitCode() != tc.wantExit {
				t.Fatalf("health %q (exit %d) reasons %v, want %q (exit %d)", r.Health, r.Health.ExitCode(), r.Reasons, tc.want, tc.wantExit)
			}
			if tc.wantReason != "" && !containsReason(r.Reasons, tc.wantReason) {
				t.Errorf("reasons %v must include %q", r.Reasons, tc.wantReason)
			}
			if tc.want == Healthy && len(r.Reasons) != 0 {
				t.Errorf("a healthy report has no reasons: %v", r.Reasons)
			}
		})
	}
	if Down.ExitCode() != 3 || Degraded.ExitCode() != 2 || Healthy.ExitCode() != 0 {
		t.Error("exit codes are 0 healthy, 2 degraded, 3 down (INV-EXIT-1)")
	}
}

func containsReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if strings.Contains(r, want) {
			return true
		}
	}
	return false
}

// ---- the local report ----------------------------------------------------

func TestLocalReportWithTheDaemonDown(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, config.StoreFileName)
	if err := os.WriteFile(storePath, bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	modified := t0.Add(-90 * time.Minute)
	if err := os.Chtimes(storePath, modified, modified); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		StateDir: dir, SocketPath: filepath.Join(dir, "sock"), SupervisorLabel: "com.example.pg-connector-github-daemon", Path: "/etc/pcg.json",
	}
	cfg.PR.Queries = nil

	for _, tc := range []struct {
		name      string
		kind      string
		probe     proc.SupervisorState
		wantState string
	}{
		{"launchd, job registered but not running", KindLaunchd, proc.SupervisorRegistered, "registered"},
		{"launchd, job not registered", KindLaunchd, proc.SupervisorNotRegistered, "not_registered"},
		{"launchd, the probe itself failed", KindLaunchd, proc.SupervisorUnknown, "unknown"},
		{"no supervisor", KindNone, proc.SupervisorNone, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := StaticFromConfig(cfg, "v1")
			s.SupervisorKind = tc.kind
			s.Probe = func(label string) proc.SupervisorState {
				if label != cfg.SupervisorLabel {
					t.Errorf("probed %q, want the configured label", label)
				}
				return tc.probe
			}
			r := Local(s, t0, "the daemon is not reachable")

			if r.Health != Down || r.Health.ExitCode() != 3 || r.Source != SourceLocal {
				t.Errorf("health %q source %q", r.Health, r.Source)
			}
			if r.Supervisor != (Supervisor{Kind: tc.kind, Label: cfg.SupervisorLabel, State: tc.wantState}) {
				t.Errorf("supervisor %+v", r.Supervisor)
			}
			if r.Socket != cfg.SocketPath {
				t.Errorf("socket %q", r.Socket)
			}
			if !r.Store.Present || r.Store.SizeBytes != 4096 || r.Store.AgeSeconds != 90*60 || r.Store.Path != storePath {
				t.Errorf("store %+v", r.Store)
			}
			wantLogs := Logs{
				Events: filepath.Join(dir, config.EventLogFileName), Daemon: filepath.Join(dir, config.DaemonLogFileName), Client: filepath.Join(dir, config.ClientLogFileName),
			}
			if r.Logs != wantLogs {
				t.Errorf("logs %+v, want %+v", r.Logs, wantLogs)
			}
			if len(r.Instances) != 2 || r.Budget != nil || r.Queue != nil || r.Auth != nil || r.Queries != nil {
				t.Errorf("a local report has no daemon-only sections: %+v", r)
			}
			if len(r.Reasons) != 1 || !strings.Contains(r.Reasons[0], "not reachable") {
				t.Errorf("reasons %v", r.Reasons)
			}
		})
	}

	// A missing store file is reported as such, not as an error.
	missing := StaticFromConfig(&config.Config{StateDir: t.TempDir(), SocketPath: "/s"}, "v1")
	missing.Probe = func(string) proc.SupervisorState { return proc.SupervisorNone }
	if r := Local(missing, t0, ""); r.Store.Present || r.Store.SizeBytes != 0 || r.Store.ModifiedAt != nil {
		t.Errorf("store %+v", r.Store)
	}
}

func TestLocalReportGolden(t *testing.T) {
	golden(t, "status_local.golden.json", Local(testStatic(proc.SupervisorRegistered, KindLaunchd), t0, "the daemon is not reachable on socket /state/sock"))
}

// ---- the fallback and the collector --------------------------------------

func TestFallbackAnswersTheLocalReport(t *testing.T) {
	fb := Fallback(testStatic(proc.SupervisorNone, KindNone), func() time.Time { return t0 })
	got, err := fb(context.Background(), scriptout.Request{Op: OpName}, fmt.Errorf("connection refused"))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := got.(Report)
	if !ok || r.Health != Down || r.Source != SourceLocal || r.Supervisor.Kind != KindNone || !containsReason(r.Reasons, "connection refused") {
		t.Errorf("%#v", got)
	}
}

// ---- text and JSON carry the same values -----------------------------------

func TestTextCarriesEveryValueOfTheJSONDocument(t *testing.T) {
	cfg, _ := testConfig(t)
	paused := healthyBudgets()
	paused.Buckets[0].Paused = true
	paused.Buckets[0].PauseReason = "secondary rate limit"
	paused.Buckets[0].PausedUntil = t0.Add(7 * time.Minute)
	cfg.Budgets = fakeBudgets{paused}
	q := okQueries()
	row := q["mine"]
	row.LastError = "HTTP 502"
	q["mine"] = row
	cfg.Queries = q
	daemon := build(t, cfg)
	daemon.ClientBuildID, daemon.BuildDiffers = "other-build", true

	local := Local(testStatic(proc.SupervisorRegistered, KindLaunchd), t0, "the daemon is not reachable")

	for name, r := range map[string]Report{"daemon": daemon, "local": local} {
		var js, text bytes.Buffer
		if err := WriteJSON(&js, r); err != nil {
			t.Fatal(err)
		}
		if err := WriteText(&text, r); err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(js.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		var leaves []string
		collectLeaves(doc, &leaves)
		if len(leaves) < 20 {
			t.Fatalf("%s: only %d leaves found", name, len(leaves))
		}
		for _, leaf := range leaves {
			if !strings.Contains(text.String(), leaf) {
				t.Errorf("%s: the text form lacks the JSON value %q\n%s", name, leaf, text.String())
			}
		}
	}
}

// collectLeaves appends every string, number and boolean of a decoded JSON
// document, in the form the text writes it.
func collectLeaves(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			collectLeaves(e, out)
		}
	case []any:
		for _, e := range x {
			collectLeaves(e, out)
		}
	case string:
		*out = append(*out, x)
	case float64:
		*out = append(*out, strconv.FormatFloat(x, 'f', -1, 64))
	case bool:
		*out = append(*out, strconv.FormatBool(x))
	}
}
