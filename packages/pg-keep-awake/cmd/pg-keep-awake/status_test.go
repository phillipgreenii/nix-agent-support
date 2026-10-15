package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// statusFixture is a hand-written status.json. Zero values are the quiet
// defaults: keeper up, gate ok, no assertion, no unexplained ticks.
type statusFixture struct {
	heartbeatAgeS      float64 // keeper.heartbeat_age_s as of written_at
	writtenAgo         time.Duration
	heartbeatIntervalS int // 0 omits the field
	held               bool
	gate               string // default ok
	paused             bool
	lingerFor          time.Duration // linger_until = now + lingerFor when non-zero
	unexplainedTicks   int
	leases             string // raw JSON array for "leases"; default []
	schemaVersion      int    // default 1
}

func (f statusFixture) json(now time.Time) string {
	if f.gate == "" {
		f.gate = "ok"
	}
	if f.leases == "" {
		f.leases = "[]"
	}
	if f.schemaVersion == 0 {
		f.schemaVersion = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{
  "schema_version": %d,
  "written_at": %q,
  "keeper": {"pid": 321, "version": "0.1.0", "heartbeat_age_s": %v, "config_error": null},
  "assertion": {"held": %v, "flags": ["i","s"], "pid": 123},
  "paused": %s,
  "battery": {"percent": 64, "on_battery": false, "gate": %q, "read_ok": true},
  "leases": %s,
  "last_release": null,
  "external": [{"pid": 419, "process": "powerd", "type": "PreventUserIdleSystemSleep", "age_s": 14040}]`,
		f.schemaVersion, now.Add(-f.writtenAgo).Format(time.RFC3339Nano), f.heartbeatAgeS, f.held,
		map[bool]string{true: `{"schema_version":1,"until":null,"created_at":"` + now.Format(time.RFC3339) + `","by_pid":1}`, false: "null"}[f.paused],
		f.gate, f.leases)
	if f.heartbeatIntervalS > 0 {
		fmt.Fprintf(&b, `,
  "heartbeat_interval_s": %d`, f.heartbeatIntervalS)
	}
	if f.unexplainedTicks > 0 {
		fmt.Fprintf(&b, `,
  "unexplained_ticks": %d`, f.unexplainedTicks)
	}
	if f.lingerFor != 0 {
		fmt.Fprintf(&b, `,
  "linger_until": %q`, now.Add(f.lingerFor).Format(time.RFC3339Nano))
	}
	b.WriteString("\n}\n")
	return b.String()
}

// stateFixture creates the state directory and writes hand-made files into it.
type stateFixture struct {
	t   *testing.T
	dir string
}

func newStateFixture(t *testing.T) *stateFixture {
	t.Helper()
	sd := useStateDir(t)
	if _, err := lease.EnsureDir(sd); err != nil {
		t.Fatal(err)
	}
	return &stateFixture{t: t, dir: sd}
}

func (s *stateFixture) write(rel, body string) {
	s.t.Helper()
	p := filepath.Join(s.dir, rel)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stateFixture) status(f statusFixture) {
	s.t.Helper()
	s.write("status.json", f.json(time.Now().UTC()))
}

// timerLease writes a pure-timer lease that expires in d (negative: already
// expired), so it is live without any process.
func (s *stateFixture) timerLease(owner, key string, d time.Duration) {
	s.t.Helper()
	now := time.Now().UTC()
	s.write("leases/"+owner+"."+key+".json", fmt.Sprintf(`{
  "schema_version": 1, "id": %q, "owner": %q, "key": %q, "kind": "acquire",
  "client": null, "expires_at": %q, "watch": null, "flags": ["i"], "argv": [],
  "created_at": %q
}
`, owner+"."+key, owner, key, now.Add(d).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
}

func TestStatusMissingStateDirectory(t *testing.T) {
	useStateDir(t) // not created

	code, stdout, stderr := runCLI(t, "status")
	if code != 0 || strings.TrimSpace(stdout) != "keeper: never started" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, _ := runCLI(t, "status", "--check"); code != 5 {
		t.Errorf("--check exit = %d, want 5", code)
	}
	code, stdout, _ = runCLI(t, "status", "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d", code)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, stdout)
	}
	if k := obj["keeper"].(map[string]any); k["state"] != "never_started" {
		t.Errorf("keeper = %v", k)
	}
}

func TestStatusDoesNotCreateStateDirectory(t *testing.T) {
	sd := useStateDir(t)
	runCLI(t, "status")
	runCLI(t, "status", "--check")
	if fileExists(sd) {
		t.Error("status created the state directory")
	}
}

func TestStatusEmptySystem(t *testing.T) {
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 3})

	code, stdout, stderr := runCLI(t, "status")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	first := strings.SplitN(stdout, "\n", 2)[0]
	if !strings.HasPrefix(first, "no leases; assertion: none; keeper: ok (") || !strings.HasSuffix(first, "s)") {
		t.Errorf("first line = %q", first)
	}
	if !strings.Contains(stdout, "other    pid 419 powerd  PreventUserIdleSystemSleep  3h54m") {
		t.Errorf("external holder missing:\n%s", stdout)
	}
	if code, _, _ := runCLI(t, "status", "--check"); code != 0 {
		t.Errorf("--check exit = %d, want 0", code)
	}
}

func TestStatusHumanLayout(t *testing.T) {
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 4, held: true})
	s.timerLease("pa-monitor", "default", 10*time.Minute+30*time.Second)
	s.timerLease("shim", "4412-17604123456789", 4*time.Minute+30*time.Second)

	code, stdout, stderr := runCLI(t, "status")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"keeper   up  heartbeat ", "v0.1.0",
		"assert   HELD -i -s  caffeinate pid 123  battery 64% AC",
		"leases   OWNER", "KEY", "CLIENT", "EXPIRES", "STATE",
		"pa-monitor", "4412-17604123...", "in 4m", "live",
		"other    pid 419 powerd",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestStatusJSONIsOneObjectOnStdout(t *testing.T) {
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 4, held: true, gate: "no_spawn"})
	s.timerLease("pa-monitor", "default", time.Hour)

	var out, errb bytes.Buffer
	if code := run([]string{"status", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("diagnostics on stderr: %q", errb.String())
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var obj struct {
		SchemaVersion int `json:"schema_version"`
		Keeper        struct {
			State string  `json:"state"`
			Age   float64 `json:"heartbeat_age_s"`
		} `json:"keeper"`
		Assertion struct {
			Held bool `json:"held"`
		} `json:"assertion"`
		Battery struct {
			Gate string `json:"gate"`
		} `json:"battery"`
		Leases []struct {
			ID   string `json:"id"`
			Live bool   `json:"live"`
		} `json:"leases"`
		External []struct {
			PID int `json:"pid"`
		} `json:"external"`
	}
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Errorf("more than one JSON value on stdout:\n%s", out.String())
	}
	if obj.SchemaVersion != 1 || obj.Keeper.State != "up" || !obj.Assertion.Held || obj.Battery.Gate != "no_spawn" {
		t.Errorf("object = %+v", obj)
	}
	if len(obj.Leases) != 1 || obj.Leases[0].ID != "pa-monitor.default" || !obj.Leases[0].Live {
		t.Errorf("leases = %+v", obj.Leases)
	}
	if len(obj.External) != 1 || obj.External[0].PID != 419 {
		t.Errorf("external = %+v", obj.External)
	}
}

func TestStatusCheckExit5HeartbeatStale(t *testing.T) {
	cases := []struct {
		name string
		f    statusFixture
		want int
	}{
		{"fresh", statusFixture{heartbeatAgeS: 3}, 0},
		{"older than twice the default interval", statusFixture{heartbeatAgeS: 25}, 5},
		{"younger than twice the default interval", statusFixture{heartbeatAgeS: 15}, 0},
		{"status.json interval widens the limit", statusFixture{heartbeatAgeS: 25, heartbeatIntervalS: 30}, 0},
		{"status.json interval narrows the limit", statusFixture{heartbeatAgeS: 15, heartbeatIntervalS: 5}, 5},
		{"written long ago", statusFixture{heartbeatAgeS: 3, writtenAgo: 5 * time.Minute}, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStateFixture(t)
			s.status(c.f)
			if code, _, stderr := runCLI(t, "status", "--check"); code != c.want {
				t.Errorf("exit = %d, want %d (stderr %q)", code, c.want, stderr)
			}
			// Without --check the same state is a plain report.
			if code, _, _ := runCLI(t, "status"); code != 0 {
				t.Errorf("plain status exit = %d, want 0", code)
			}
		})
	}
}

func TestStatusCheckHeartbeatFileIsTheContract(t *testing.T) {
	s := newStateFixture(t)
	// status.json alone says the keeper is long gone ...
	s.status(statusFixture{heartbeatAgeS: 3, writtenAgo: 10 * time.Minute})
	if code, _, _ := runCLI(t, "status", "--check"); code != 5 {
		t.Fatalf("without a heartbeat file: exit = %d, want 5", code)
	}
	// ... but a freshly touched keeper.heartbeat says otherwise.
	s.write("keeper.heartbeat", "")
	if code, _, _ := runCLI(t, "status", "--check"); code != 0 {
		t.Errorf("with a fresh heartbeat file: exit = %d, want 0", code)
	}
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(filepath.Join(s.dir, "keeper.heartbeat"), old, old); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI(t, "status", "--check"); code != 5 {
		t.Errorf("with a stale heartbeat file: exit = %d, want 5", code)
	}
}

func TestStatusCheckExit6Degraded(t *testing.T) {
	cases := []struct {
		name string
		f    statusFixture
		want int
	}{
		{"live leases, no assertion, nothing explains it", statusFixture{heartbeatAgeS: 3, unexplainedTicks: 2}, 6},
		{"long-running degradation", statusFixture{heartbeatAgeS: 3, unexplainedTicks: 9}, 6},
		{"an assertion is held", statusFixture{heartbeatAgeS: 3, held: true, unexplainedTicks: 2}, 0},
		{"gate no_spawn explains it", statusFixture{heartbeatAgeS: 3, gate: "no_spawn", unexplainedTicks: 2}, 0},
		{"gate released explains it", statusFixture{heartbeatAgeS: 3, gate: "released", unexplainedTicks: 2}, 0},
		{"gate unknown does not explain it", statusFixture{heartbeatAgeS: 3, gate: "unknown", unexplainedTicks: 2}, 6},
		{"a pause explains it", statusFixture{heartbeatAgeS: 3, paused: true, unexplainedTicks: 2}, 0},
		{"the linger window explains it", statusFixture{heartbeatAgeS: 3, lingerFor: 20 * time.Second, unexplainedTicks: 2}, 0},
		{"a linger window that ended does not", statusFixture{heartbeatAgeS: 3, lingerFor: -time.Second, unexplainedTicks: 2}, 6},
		{"one tick is not enough", statusFixture{heartbeatAgeS: 3, unexplainedTicks: 1}, 0},
		{"no tick yet", statusFixture{heartbeatAgeS: 3}, 0},
		{"keeper down wins over degraded", statusFixture{heartbeatAgeS: 60, unexplainedTicks: 5}, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStateFixture(t)
			s.status(c.f)
			s.timerLease("pa-monitor", "default", time.Hour)
			if code, _, stderr := runCLI(t, "status", "--check"); code != c.want {
				t.Errorf("exit = %d, want %d (stderr %q)", code, c.want, stderr)
			}
		})
	}
}

func TestStatusCheckExit6NeedsALiveLease(t *testing.T) {
	t.Run("no leases", func(t *testing.T) {
		s := newStateFixture(t)
		s.status(statusFixture{heartbeatAgeS: 3, unexplainedTicks: 3})
		if code, _, _ := runCLI(t, "status", "--check"); code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})
	t.Run("only an expired lease", func(t *testing.T) {
		s := newStateFixture(t)
		s.status(statusFixture{heartbeatAgeS: 3, unexplainedTicks: 3})
		s.timerLease("pa-monitor", "default", -time.Minute)
		code, stdout, _ := runCLI(t, "status", "--check")
		if code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
		if !strings.Contains(stdout, "dead (expired)") {
			t.Errorf("expired lease not shown dead:\n%s", stdout)
		}
	})
	t.Run("a lease whose client is gone", func(t *testing.T) {
		requireProcInfo(t)
		s := newStateFixture(t)
		s.status(statusFixture{heartbeatAgeS: 3, unexplainedTicks: 3})
		now := time.Now().UTC()
		s.write("leases/run.k1.json", fmt.Sprintf(`{"schema_version":1,"id":"run.k1","owner":"run","key":"k1","kind":"run",
"client":{"pid":%d,"start_us":1700000000000000},"expires_at":null,"watch":null,"flags":["i"],"argv":[],"created_at":%q}`,
			deadPID(t), now.Format(time.RFC3339)))
		if code, stdout, _ := runCLI(t, "status", "--check"); code != 0 || !strings.Contains(stdout, "client_gone") {
			t.Errorf("exit = %d:\n%s", code, stdout)
		}
	})
	t.Run("the keeper last judged it not live", func(t *testing.T) {
		s := newStateFixture(t)
		s.status(statusFixture{
			heartbeatAgeS: 3, unexplainedTicks: 3,
			leases: `[{"id":"pa-monitor.default","owner":"pa-monitor","key":"default","kind":"acquire","expires_at":null,"live":false,"reason":"orphaned"}]`,
		})
		s.timerLease("pa-monitor", "default", time.Hour)
		if code, _, _ := runCLI(t, "status", "--check"); code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})
}

func TestStatusAssertionWithDeadCaffeinateIsNotHeld(t *testing.T) {
	requireProcInfo(t)
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 3, held: true})
	s.timerLease("pa-monitor", "default", time.Hour)
	s.write("assertion.json", fmt.Sprintf(`{"schema_version":1,
"anchor":{"pid":%d,"start_us":1700000000000000,"argv":["pg-keep-awake","anchor"]},
"caffeinate":{"pid":%d,"start_us":1700000000000000,"argv":["/usr/bin/caffeinate","-i"]},
"flags":["i"],"params":{},"started_at":"2026-10-14T20:00:00Z","keeper_version":"0.1.0"}`, deadPID(t), deadPID(t)))

	_, stdout, _ := runCLI(t, "status", "--json")
	var obj struct {
		Assertion struct {
			Held bool `json:"held"`
		} `json:"assertion"`
	}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Assertion.Held {
		t.Error("assertion reported held although its caffeinate process is gone")
	}
}

func TestStatusAssertionWithLiveCaffeinateStaysHeld(t *testing.T) {
	requireProcInfo(t)
	ref, err := lease.Ref(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 3, held: true})
	s.write("assertion.json", fmt.Sprintf(`{"schema_version":1,
"anchor":{"pid":%d,"start_us":%d,"argv":["pg-keep-awake","anchor"]},
"caffeinate":{"pid":%d,"start_us":%d,"argv":["/usr/bin/caffeinate","-i"]},
"flags":["i"],"params":{},"started_at":"2026-10-14T20:00:00Z","keeper_version":"0.1.0"}`, ref.PID, ref.StartUS, ref.PID, ref.StartUS))
	_, stdout, _ := runCLI(t, "status")
	if !strings.Contains(stdout, "HELD") {
		t.Errorf("assertion not held:\n%s", stdout)
	}
}

func TestStatusPauseFileExplainsDegradation(t *testing.T) {
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 3, unexplainedTicks: 4})
	s.timerLease("pa-monitor", "default", time.Hour)
	if code, _, _ := runCLI(t, "status", "--check"); code != 6 {
		t.Fatalf("before pause: exit = %d, want 6", code)
	}
	if code, _, _ := runCLI(t, "pause", "--for", "30m"); code != 0 {
		t.Fatal("pause failed")
	}
	code, stdout, _ := runCLI(t, "status", "--check")
	if code != 0 || !strings.Contains(stdout, "(paused)") {
		t.Errorf("after pause: exit = %d:\n%s", code, stdout)
	}
}

func TestStatusFutureSchemaLeaseIsUnreadableNotDeletedNotLive(t *testing.T) {
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 3, unexplainedTicks: 3})
	future := `{"schema_version": 99, "id": "pa-monitor.default"}`
	s.write("leases/pa-monitor.default.json", future)

	code, stdout, stderr := runCLI(t, "status", "--check")
	if code != 0 {
		t.Errorf("exit = %d, want 0 (the lease is not counted live); stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "unreadable") {
		t.Errorf("not shown as unreadable:\n%s", stdout)
	}
	got, err := os.ReadFile(filepath.Join(s.dir, "leases", "pa-monitor.default.json"))
	if err != nil || string(got) != future {
		t.Errorf("future-schema lease was changed or deleted: %q, %v", got, err)
	}
	_, jsonOut, _ := runCLI(t, "status", "--json")
	var obj struct {
		Leases []struct {
			Live       bool `json:"live"`
			Unreadable bool `json:"unreadable"`
		} `json:"leases"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &obj); err != nil || len(obj.Leases) != 1 || obj.Leases[0].Live || !obj.Leases[0].Unreadable {
		t.Errorf("json leases = %+v, %v", obj.Leases, err)
	}
}

func TestStatusFutureSchemaStatusFileIsUnreadableAndKept(t *testing.T) {
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 3, schemaVersion: 7})
	s.write("keeper.heartbeat", "")
	before, _ := os.ReadFile(filepath.Join(s.dir, "status.json"))

	code, stdout, stderr := runCLI(t, "status", "--check")
	if code != 0 {
		t.Errorf("exit = %d, want 0; stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "unreadable  status.json") {
		t.Errorf("status.json not shown as unreadable:\n%s", stdout)
	}
	after, _ := os.ReadFile(filepath.Join(s.dir, "status.json"))
	if !bytes.Equal(before, after) {
		t.Error("status.json was modified")
	}
}

func TestStatusIgnoresConfiguration(t *testing.T) {
	s := newStateFixture(t)
	s.status(statusFixture{heartbeatAgeS: 3})
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "pg-keep-awake"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A config that would fail `config check` (and set another heartbeat) must
	// not matter to status.
	if err := os.WriteFile(filepath.Join(cfg, "pg-keep-awake", "config.toml"), []byte("heartbeat_interval = [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, "status", "--check"); code != 0 || stderr != "" {
		t.Errorf("exit = %d, stderr %q", code, stderr)
	}
}

func TestStatusUsageErrorsExit2(t *testing.T) {
	useStateDir(t)
	for _, args := range [][]string{{"status", "--bogus"}, {"status", "extra"}} {
		if code, _, _ := runCLI(t, args...); code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
	}
}

func TestStatusUnusableStateDirectoryExits3(t *testing.T) {
	sd := useStateDir(t)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI(t, "status"); code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
}

func TestStatusGarbageStatusFileIsIgnoredWithDiagnostic(t *testing.T) {
	s := newStateFixture(t)
	s.write("status.json", "{not json")
	code, stdout, stderr := runCLI(t, "status", "--json")
	if code != 0 || !strings.Contains(stderr, "status.json") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Errorf("stdout is not one JSON object: %v", err)
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[time.Duration]string{
		0: "0s", 4 * time.Second: "4s", 59 * time.Second: "59s", time.Minute: "1m", 4*time.Minute + 30*time.Second: "4m",
		time.Hour: "1h", 3*time.Hour + 54*time.Minute: "3h54m", 52 * time.Hour: "2d4h", 48 * time.Hour: "2d", -time.Second: "0s",
	}
	for d, want := range cases {
		if got := formatAge(d); got != want {
			t.Errorf("formatAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestStatusLeasesWithoutAKeeperAreStillListed(t *testing.T) {
	sd := useStateDir(t)
	if code, _, stderr := runCLI(t, "acquire", "--owner", "pa-monitor", "--for", "5m"); code != 0 {
		t.Fatalf("acquire: %d %q", code, stderr)
	}
	code, stdout, _ := runCLI(t, "status")
	if code != 0 || !strings.Contains(stdout, "keeper   never started") || !strings.Contains(stdout, "pa-monitor") {
		t.Errorf("exit %d:\n%s", code, stdout)
	}
	if code, _, _ := runCLI(t, "status", "--check"); code != 5 {
		t.Errorf("--check exit = %d, want 5", code)
	}
	if !fileExists(filepath.Join(sd, "leases", "pa-monitor.default.json")) {
		t.Error("status removed the lease")
	}
}
