package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// write puts content in a temp config file and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = `{"state_dir": "/tmp/s", "pjira_path": "/opt/pjira"}`

func loadMinimal(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load(write(t, minimal), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDefaultsTable(t *testing.T) {
	cfg := loadMinimal(t)
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	min := func(n int) time.Duration { return time.Duration(n) * time.Minute }
	hr := func(n int) time.Duration { return time.Duration(n) * time.Hour }
	day := func(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

	for _, c := range []struct {
		name string
		got  Duration
		want time.Duration
	}{
		{"default_pause", cfg.DefaultPause, sec(60)},
		{"write_notice_timeout", cfg.WriteNoticeTimeout, 500 * time.Millisecond},
		{"write_lease", cfg.WriteLease, sec(30)},
		{"summary_ttl", cfg.SummaryTTL, sec(60)},
		{"detail_ttl", cfg.DetailTTL, min(10)},
		{"detail_max_age", cfg.DetailMaxAge, min(30)},
		{"query_ttl_default", cfg.QueryTTLDefault, sec(120)},
		{"terminal_ttl", cfg.TerminalTTL, hr(24)},
		{"expiry", cfg.Expiry, day(7)},
		{"settle_timeout", cfg.SettleTimeout, sec(30)},
		{"change_log_retention", cfg.ChangeLogRetention, day(14)},
		{"consumer_stale_after", cfg.ConsumerStaleAfter, day(7)},
		{"fill_horizon (one summary_ttl)", cfg.FillHorizon, sec(60)},
	} {
		if c.got.Std() != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got.Std(), c.want)
		}
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"request_rate_per_second", cfg.RequestRatePerSecond, 2},
		{"concurrency", cfg.Concurrency, 2},
		{"workers (= concurrency)", cfg.Workers, 2},
		{"batch_size", cfg.BatchSize, 74},
		{"tracked_set_alert_size", cfg.TrackedSetAlertSize, 55},
		{"change_body_cap", int(cfg.ChangeBodyCap), 4096},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if cfg.Instance != "pg-connector-issue-jira" || cfg.SupervisorLabel != "com.phillipg.pg-connector-issue-jira-daemon" {
		t.Errorf("identity defaults = %q %q", cfg.Instance, cfg.SupervisorLabel)
	}
	if cfg.SocketPath != "/tmp/s/sock" {
		t.Errorf("socket default = %q", cfg.SocketPath)
	}
	if cfg.OTelEnabled() {
		t.Errorf("otel must be off when no endpoint is configured")
	}
	if len(cfg.Queries) != 0 || len(cfg.PjiraEnv) != 0 || len(cfg.QueryTTL) != 0 {
		t.Errorf("no query, pjira_env or query_ttl entry is built in: %v %v %v", cfg.Queries, cfg.PjiraEnv, cfg.QueryTTL)
	}
	if cfg.StateDir != "/tmp/s" || cfg.PjiraPath != "/opt/pjira" || cfg.Path == "" || cfg.Hash == "" {
		t.Errorf("paths/hash: %q %q %q %q", cfg.StateDir, cfg.PjiraPath, cfg.Path, cfg.Hash)
	}
}

func TestGoldenExampleRoundTrips(t *testing.T) {
	cfg, err := Load("testdata/example.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	def, err := Load(write(t, `{"state_dir": "/tmp/pg-connector-issue-jira-example", "pjira_path": "/usr/local/bin/pjira"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Path differs; everything else, hash included, is the defaults.
	cfg.Path, def.Path = "", ""
	if !reflect.DeepEqual(cfg, def) {
		t.Fatalf("the golden example is not the defaults:\n got %+v\nwant %+v", cfg, def)
	}
}

func TestUnknownKeyRejectedByName(t *testing.T) {
	for _, body := range []string{
		`{"state_dir": "/tmp/s", "pjira_path": "/p", "bogus_key": 1}`,
		`{"state_dir": "/tmp/s", "pjira_path": "/p", "queries": {"q": {"expr": "x", "typo_key": true}}}`,
	} {
		_, err := Load(write(t, body), nil)
		if err == nil {
			t.Fatalf("expected an error for %s", body)
		}
		if !strings.Contains(err.Error(), "bogus_key") && !strings.Contains(err.Error(), "typo_key") {
			t.Errorf("error does not name the key: %v", err)
		}
	}
}

func TestTrailingDataRejected(t *testing.T) {
	if _, err := Load(write(t, minimal+` {}`), nil); err == nil || !strings.Contains(err.Error(), "after the top-level object") {
		t.Fatalf("trailing data: %v", err)
	}
}

func TestRequiredKeys(t *testing.T) {
	for body, want := range map[string]string{
		`{"pjira_path": "/p"}`:    "state_dir is required",
		`{"state_dir": "/tmp/s"}`: "pjira_path is required",
	} {
		if _, err := Load(write(t, body), nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body, err, want)
		}
	}
}

func TestEmptyPathIsAnError(t *testing.T) {
	if _, err := Load("", func(string) string { return write(t, minimal) }); err == nil {
		t.Fatal("an empty path must not fall back to the environment")
	}
}

func TestLoadReadsNoEnvironment(t *testing.T) {
	getenv := func(k string) string {
		t.Errorf("Load read the environment variable %s", k)
		return "/tmp/override"
	}
	cfg, err := Load(write(t, minimal), getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != "/tmp/s" || cfg.SocketPath != "/tmp/s/sock" {
		t.Errorf("state=%q socket=%q", cfg.StateDir, cfg.SocketPath)
	}
}

func TestRelativePathsRejectedByName(t *testing.T) {
	for key, body := range map[string]string{
		"state_dir":   `{"state_dir": "s", "pjira_path": "/p"}`,
		"socket_path": `{"state_dir": "/tmp/s", "pjira_path": "/p", "socket_path": "run/sock"}`,
		"pjira_path":  `{"state_dir": "/tmp/s", "pjira_path": "pjira"}`,
	} {
		if _, err := Load(write(t, body), nil); err == nil || !strings.Contains(err.Error(), key+" must be an absolute path") {
			t.Errorf("%s: err = %v", key, err)
		}
	}
}

func TestPjiraPathNeedNotExist(t *testing.T) {
	if _, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/nonexistent/pjira"}`), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSocketPathTooLongRejected(t *testing.T) {
	long := "/tmp/" + strings.Repeat("a", 200)
	_, err := Load(write(t, `{"state_dir": "`+long+`", "pjira_path": "/p"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "socket_path") || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("a socket path over the platform limit must be rejected: %v", err)
	}
}

func TestInvalidValuesRejected(t *testing.T) {
	for _, kv := range []string{
		`"request_rate_per_second": 0`, `"request_rate_per_second": -1`,
		`"concurrency": 0`, `"workers": 0`, `"batch_size": 0`,
		`"write_lease": "0s"`, `"write_lease": "-1s"`, `"default_pause": "0s"`,
		`"summary_ttl": "0s"`, `"detail_ttl": "-1m"`, `"detail_max_age": "0s"`,
		`"query_ttl_default": "0s"`, `"query_ttl": {"q": "0s"}`,
		`"terminal_ttl": "0s"`, `"expiry": "0s"`, `"fill_horizon": "-1s"`,
		`"change_body_cap": 0`, `"tracked_set_alert_size": 0`,
		`"summary_ttl": 60`, `"summary_ttl": "soon"`, `"instance": ""`,
	} {
		body := `{"state_dir": "/tmp/s", "pjira_path": "/p", ` + kv + `}`
		if _, err := Load(write(t, body), nil); err == nil {
			t.Errorf("%s: expected an error", kv)
		}
	}
}

func TestOverridesAndDurationSyntax(t *testing.T) {
	cfg, err := Load(write(t, `{
		"state_dir": "/tmp/s", "pjira_path": "/p", "socket_path": "/tmp/x.sock",
		"instance": "other", "otel_endpoint": "http://localhost:4318",
		"request_rate_per_second": 5, "concurrency": 3, "write_lease": "1m",
		"expiry": "1d12h", "change_body_cap": 1024, "fill_horizon": "5m",
		"query_ttl": {"mine": "30s"}, "query_ttl_default": "3m"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SocketPath != "/tmp/x.sock" || cfg.Instance != "other" || !cfg.OTelEnabled() {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.RequestRatePerSecond != 5 || cfg.Concurrency != 3 || cfg.Workers != 3 {
		t.Errorf("rate=%d concurrency=%d workers=%d (workers follows concurrency)", cfg.RequestRatePerSecond, cfg.Concurrency, cfg.Workers)
	}
	if cfg.WriteLease.Std() != time.Minute || cfg.Expiry.Std() != 36*time.Hour || cfg.ChangeBodyCap != 1024 || cfg.FillHorizon.Std() != 5*time.Minute {
		t.Errorf("durations: %v %v %v %v", cfg.WriteLease.Std(), cfg.Expiry.Std(), cfg.ChangeBodyCap, cfg.FillHorizon.Std())
	}
	if cfg.QueryTTLFor("mine").Std() != 30*time.Second || cfg.QueryTTLFor("other").Std() != 3*time.Minute {
		t.Errorf("QueryTTLFor: %v %v", cfg.QueryTTLFor("mine"), cfg.QueryTTLFor("other"))
	}
}

func TestExplicitWorkersWinsOverConcurrency(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "concurrency": 3, "workers": 7}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers != 7 {
		t.Errorf("workers = %d", cfg.Workers)
	}
}

func TestFillHorizonZeroMeansSummaryTTL(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "summary_ttl": "90s", "fill_horizon": "0s"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FillHorizon.Std() != 90*time.Second {
		t.Errorf("fill_horizon = %v", cfg.FillHorizon.Std())
	}
}

func TestNullMeansUnset(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/opt/pjira", "socket_path": null,
		"instance": null, "otel_endpoint": null, "pjira_env": null, "queries": null,
		"query_ttl": null, "concurrency": null, "workers": null, "write_lease": null,
		"change_body_cap": null, "fill_horizon": null}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	def := loadMinimal(t)
	cfg.Path, def.Path = "", ""
	if !reflect.DeepEqual(cfg, def) {
		t.Errorf("null did not leave the defaults:\n got %+v\nwant %+v", cfg, def)
	}
}

func TestQueriesDecodeThroughQueryDef(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "queries": {
		"bare": "text",
		"list": ["a", "b"],
		"watched": {"expr": "text", "watched": true},
		"plain": {"expr": ["x"]}
	}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if q := cfg.Queries["bare"]; q.Watched || len(q.Expr) != 1 || q.Expr[0] != "text" {
		t.Errorf("bare = %+v", q)
	}
	if q := cfg.Queries["list"]; q.Watched || len(q.Expr) != 2 {
		t.Errorf("list = %+v", q)
	}
	if q := cfg.Queries["watched"]; !q.Watched || len(q.Expr) != 1 || q.Expr[0] != "text" {
		t.Errorf("watched = %+v", q)
	}
	if q := cfg.Queries["plain"]; q.Watched {
		t.Errorf("an object without watched must be unwatched: %+v", q)
	}
	for _, bad := range []string{
		`{"watched": true}`, `{"expr": "x", "watched": "yes"}`, `{"expr": "x", "other": 1}`,
	} {
		body := `{"state_dir": "/tmp/s", "pjira_path": "/p", "queries": {"q": ` + bad + `}}`
		if _, err := Load(write(t, body), nil); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestPjiraEnvIsAStringMap(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "pjira_env": {"A": "1", "B": "two"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.PjiraEnv, map[string]string{"A": "1", "B": "two"}) {
		t.Errorf("pjira_env = %v", cfg.PjiraEnv)
	}
	if _, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "pjira_env": {"A": 1}}`), nil); err == nil {
		t.Error("a non-string pjira_env value must be rejected")
	}
}

func TestStampedHashIsTrustedAndConfigPathKept(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "config_hash": "abc123", "config_path": "/etc/rendered.json"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hash != "abc123" || cfg.StampedHash != "abc123" || cfg.RenderedPath != "/etc/rendered.json" {
		t.Errorf("hash=%q stamped=%q rendered=%q", cfg.Hash, cfg.StampedHash, cfg.RenderedPath)
	}
	if cfg.Path == "/etc/rendered.json" || !filepath.IsAbs(cfg.Path) {
		t.Errorf("Path is the file actually read, got %q", cfg.Path)
	}
}

func TestComputedHashIgnoresStampsAndKeyOrder(t *testing.T) {
	a, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "write_lease": "60s"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(write(t, `{"write_lease": "1m", "config_path": "/x", "pjira_path": "/p", "state_dir": "/tmp/s"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Hash) != 64 || a.Hash != b.Hash {
		t.Errorf("hashes: %q vs %q", a.Hash, b.Hash)
	}
	c, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "write_lease": "2m"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash == a.Hash {
		t.Error("different settings must hash differently")
	}
}

func TestSharedFileNames(t *testing.T) {
	for _, p := range [][2]string{
		{StoreFileName, "store.sqlite"},
		{SocketFileName, "sock"},
		{LockFileName, "daemon.lock"},
		{EventLogFileName, "events.jsonl"},
		{DaemonLogFileName, "daemon.log.jsonl"},
		{ClientLogFileName, "client.log.jsonl"},
		{DaemonName, "pg-connector-issue-jira"},
		{StateDirName, "pg-connector-issue-jira-daemon"},
		{EventServiceName, "pg-connector-issue-jira-daemon"},
		{DaemonLogServiceName, "pg-connector-issue-jira-daemon"},
		{ClientLogServiceName, "pg-connector-issue-jira-daemon-client"},
	} {
		got, want := p[0], p[1]
		if got != want {
			t.Errorf("file/name constant = %q, want %q", got, want)
		}
	}
	if StateDirName == DaemonName {
		t.Error("the daemon's state directory must differ from the old backend's")
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json"), nil); err == nil {
		t.Fatal("expected a read error")
	}
}
