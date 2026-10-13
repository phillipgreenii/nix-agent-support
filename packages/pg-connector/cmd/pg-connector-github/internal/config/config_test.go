package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func noEnv(string) string { return "" }

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

const minimal = `{"state_dir": "/tmp/s"}`

func TestDefaultsTable(t *testing.T) {
	cfg, err := Load(write(t, minimal), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	min := func(n int) time.Duration { return time.Duration(n) * time.Minute }
	hr := func(n int) time.Duration { return time.Duration(n) * time.Hour }
	day := func(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

	durations := []struct {
		name string
		got  Duration
		want time.Duration
	}{
		{"query_ttl mine", cfg.QueryTTL["mine"], sec(60)},
		{"query_ttl team", cfg.QueryTTL["team"], sec(120)},
		{"adhoc_query_ttl", cfg.AdhocQueryTTL, sec(120)},
		{"summary_ttl", cfg.SummaryTTL, sec(60)},
		{"detail_ttl", cfg.DetailTTL, min(10)},
		{"detail_max_age", cfg.DetailMaxAge, min(30)},
		{"conversation_ttl", cfg.ConversationTTL, min(10)},
		{"conversation_max_age", cfg.ConversationMaxAge, min(30)},
		{"pending_ttl", cfg.PendingTTL, min(2)},
		{"ci_active_ttl", cfg.CIActiveTTL, sec(60)},
		{"ci_max_age", cfg.CIMaxAge, min(30)},
		{"ci_failed_ttl", cfg.CIFailedTTL, min(5)},
		{"terminal_ttl", cfg.TerminalTTL, hr(24)},
		{"settle_timeout", cfg.SettleTimeout, sec(30)},
		{"keepalive_window", cfg.KeepaliveWindow, hr(24)},
		{"expiry", cfg.Expiry, day(7)},
		{"fill_horizon (one summary_ttl)", cfg.FillHorizon, sec(60)},
		{"change_log_retention", cfg.ChangeLogRetention, day(14)},
		{"consumer_stale_after", cfg.ConsumerStaleAfter, day(7)},
	}
	for _, c := range durations {
		if c.got.Std() != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got.Std(), c.want)
		}
	}
	if cfg.ChangeBodyCap != 4*1024 {
		t.Errorf("change_body_cap = %d bytes, want 4096 (4 KiB)", cfg.ChangeBodyCap)
	}
	ints := []struct {
		name      string
		got, want int
	}{
		{"batch_size", cfg.BatchSize, 74},
		{"workers", cfg.Workers, 4},
		{"rate_reserve_points", cfg.RateReservePoints, 1000},
		{"graphql_points_per_hour", cfg.GraphQLPointsPerHour, 1500},
		{"rest_requests_per_hour", cfg.RESTRequestsPerHour, 3000},
		{"rest_search_per_minute", cfg.RESTSearchPerMinute, 20},
	}
	for _, c := range ints {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if cfg.BackgroundShare != 0.9 {
		t.Errorf("background_share = %v, want 0.9", cfg.BackgroundShare)
	}
	if cfg.Host != "github.com" || cfg.GH != "gh" || cfg.SupervisorLabel != "com.phillipg.pg-connector-github-daemon" {
		t.Errorf("identity defaults = %q %q %q", cfg.Host, cfg.GH, cfg.SupervisorLabel)
	}
	if cfg.OTelEnabled() {
		t.Errorf("otel must be off when no endpoint is configured")
	}
	if len(cfg.PR.Queries) != 0 || cfg.HasQuery("mine") || cfg.HasQuery("team") {
		t.Errorf("no query is hardcoded, got pr=%v", cfg.PR.Queries)
	}
	if cfg.SocketPath != filepath.Join("/tmp/s", SocketFileName) {
		t.Errorf("socket default = %q", cfg.SocketPath)
	}
}

func TestCutoverCapIsOneConstant(t *testing.T) {
	if CutoverGraphQLPointsPerHour != 4000 || DefaultGraphQLPointsPerHour != 1500 {
		t.Fatalf("cap constants = shadow %d cutover %d", DefaultGraphQLPointsPerHour, CutoverGraphQLPointsPerHour)
	}
}

func TestGoldenExampleLoadsAtDefaults(t *testing.T) {
	cfg, err := Load("testdata/example.json", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	def, err := Load(write(t, `{"state_dir": "/tmp/pg-connector-github-example"}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	// The golden example writes every setting at its default, so the settings
	// block must equal the defaults-only load.
	a, _ := json.Marshal(cfg.Settings)
	b, _ := json.Marshal(def.Settings)
	if string(a) != string(b) {
		t.Fatalf("golden settings differ from defaults:\n%s\n%s", a, b)
	}
	if len(cfg.PR.Queries["team"]) != 2 || cfg.PR.Queries["mine"][0] != "is:pr is:open author:@me" {
		t.Errorf("queries = %#v", cfg.PR.Queries)
	}
	if cfg.HashStamped() || cfg.Hash == "" {
		t.Errorf("golden carries no stamp: stamped=%v hash=%q", cfg.HashStamped(), cfg.Hash)
	}
	if cfg.Path != "testdata/example.json" {
		t.Errorf("Path = %q", cfg.Path)
	}
	// The example documents the optional keys at their defaults, so it names
	// pr.posted_state_dir, whose default ("" is unset) the sidecar resolver
	// replaces with the daemon's environment.
	raw, err := os.ReadFile("testdata/example.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"posted_state_dir": ""`) || cfg.PR.PostedStateDir != "" {
		t.Errorf("golden must write pr.posted_state_dir at its default (unset): value %q", cfg.PR.PostedStateDir)
	}
}

func TestFileOverrides(t *testing.T) {
	cfg, err := Load(write(t, `{
		"state_dir": "/tmp/s", "host": "ghe.example.test", "gh_path": "/opt/gh",
		"otel_endpoint": "http://127.0.0.1:4318",
		"summary_ttl": "90s", "expiry": "1d12h", "workers": 8, "background_share": 0.5,
		"change_body_cap": 1024, "query_ttl": {"team": "3m", "other": "5m"},
		"graphql_points_per_hour": 4000
	}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "ghe.example.test" || cfg.GH != "/opt/gh" || !cfg.OTelEnabled() {
		t.Errorf("identity = %q %q %q", cfg.Host, cfg.GH, cfg.OTelEndpoint)
	}
	if cfg.SummaryTTL.Std() != 90*time.Second || cfg.Expiry.Std() != 36*time.Hour {
		t.Errorf("summary=%v expiry=%v", cfg.SummaryTTL.Std(), cfg.Expiry.Std())
	}
	if cfg.FillHorizon != cfg.SummaryTTL {
		t.Errorf("fill_horizon must follow summary_ttl, got %v", cfg.FillHorizon.Std())
	}
	if cfg.Workers != 8 || cfg.BackgroundShare != 0.5 || cfg.ChangeBodyCap != 1024 || cfg.GraphQLPointsPerHour != 4000 {
		t.Errorf("workers=%d share=%v cap=%d gql=%d", cfg.Workers, cfg.BackgroundShare, cfg.ChangeBodyCap, cfg.GraphQLPointsPerHour)
	}
	// query_ttl merges over the defaults and QueryTTLFor falls back to adhoc.
	if cfg.QueryTTLFor("mine").Std() != time.Minute || cfg.QueryTTLFor("team").Std() != 3*time.Minute ||
		cfg.QueryTTLFor("other").Std() != 5*time.Minute || cfg.QueryTTLFor("unlisted").Std() != 2*time.Minute {
		t.Errorf("query ttls = %v", cfg.QueryTTL)
	}
}

func TestEnvOverridesWinOverFile(t *testing.T) {
	path := write(t, `{"state_dir": "/tmp/file-state", "socket_path": "/tmp/file-state/file.sock", "gh_path": "file-gh", "graphql_points_per_hour": 2000}`)
	cfg, err := Load(path, envOf(map[string]string{
		EnvSocket: "/tmp/env.sock", EnvStateDir: "/tmp/env-state", EnvGH: "env-gh",
		EnvGraphQLPointsPerHour: "4000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SocketPath != "/tmp/env.sock" || cfg.StateDir != "/tmp/env-state" || cfg.GH != "env-gh" || cfg.GraphQLPointsPerHour != 4000 {
		t.Errorf("socket=%q state=%q gh=%q gql=%d", cfg.SocketPath, cfg.StateDir, cfg.GH, cfg.GraphQLPointsPerHour)
	}
}

func TestEnvStateDirMovesDefaultSocketOnly(t *testing.T) {
	cfg, err := Load(write(t, minimal), envOf(map[string]string{EnvStateDir: "/tmp/env-state"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SocketPath != "/tmp/env-state/sock" {
		t.Errorf("socket = %q", cfg.SocketPath)
	}
}

func TestConfigEnvUsedOnlyWithoutFlag(t *testing.T) {
	fromEnv := write(t, `{"state_dir": "/tmp/from-env"}`)
	fromFlag := write(t, `{"state_dir": "/tmp/from-flag"}`)
	env := envOf(map[string]string{EnvConfig: fromEnv})

	cfg, err := Load(fromFlag, env)
	if err != nil || cfg.StateDir != "/tmp/from-flag" || cfg.Path != fromFlag {
		t.Fatalf("flag must win: %v %+v", err, cfg)
	}
	cfg, err = Load("", env)
	if err != nil || cfg.StateDir != "/tmp/from-env" || cfg.Path != fromEnv {
		t.Fatalf("env used without flag: %v %+v", err, cfg)
	}
	if _, err = Load("", noEnv); err == nil || !strings.Contains(err.Error(), EnvConfig) {
		t.Fatalf("no flag and no env must name %s, got %v", EnvConfig, err)
	}
}

func TestEmptyEnvironmentLoads(t *testing.T) {
	// env -i: no getenv at all, and a nil getenv.
	for name, getenv := range map[string]func(string) string{"empty": noEnv, "nil": nil} {
		if _, err := Load("testdata/example.json", getenv); err != nil {
			t.Errorf("%s getenv: %v", name, err)
		}
	}
}

func TestUnknownKeyRejectedByName(t *testing.T) {
	for name, body := range map[string]string{
		"top level": `{"state_dir": "/tmp/s", "summry_ttl": "60s"}`,
		"nested":    `{"state_dir": "/tmp/s", "pr": {"quries": {}}}`,
	} {
		_, err := Load(write(t, body), noEnv)
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		key := "summry_ttl"
		if name == "nested" {
			key = "quries"
		}
		if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("%s: error %q must name unknown key %q", name, err, key)
		}
	}
}

func TestInvalidValuesRejected(t *testing.T) {
	cases := map[string]string{
		"missing state_dir":      `{}`,
		"numeric duration":       `{"state_dir":"/s","summary_ttl": 60}`,
		"bad duration":           `{"state_dir":"/s","summary_ttl": "soon"}`,
		"non-positive duration":  `{"state_dir":"/s","summary_ttl": "0s", "pending_ttl": "-1s"}`,
		"zero batch":             `{"state_dir":"/s","batch_size": 0}`,
		"share above one":        `{"state_dir":"/s","background_share": 1.5}`,
		"negative reserve":       `{"state_dir":"/s","rate_reserve_points": -1}`,
		"trailing data":          `{"state_dir":"/s"} {}`,
		"not json":               `state_dir: /s`,
		"empty host":             `{"state_dir":"/s","host": ""}`,
		"bad size":               `{"state_dir":"/s","change_body_cap": "lots"}`,
		"non-positive query ttl": `{"state_dir":"/s","query_ttl": {"mine": "0s"}}`,
		"relative state_dir":     `{"state_dir":"state"}`,
		"relative socket_path":   `{"state_dir":"/s","socket_path":"run/sock"}`,
		"null state_dir":         `{"state_dir":null}`,
	}
	for name, body := range cases {
		if _, err := Load(write(t, body), noEnv); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := Load(write(t, minimal), envOf(map[string]string{EnvGraphQLPointsPerHour: "many"})); err == nil ||
		!strings.Contains(err.Error(), EnvGraphQLPointsPerHour) {
		t.Errorf("bad cap env must name the variable, got %v", err)
	}
	if _, err := Load("testdata/nope.json", noEnv); err == nil {
		t.Error("missing file must fail")
	}
}

func TestDurationAndSizeSyntax(t *testing.T) {
	for in, want := range map[string]time.Duration{
		`"60s"`: time.Minute, `"1m"`: time.Minute, `"7d"`: 7 * 24 * time.Hour,
		`"1d12h"`: 36 * time.Hour, `"24h"`: 24 * time.Hour, `"1500ms"`: 1500 * time.Millisecond,
	} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err != nil || d.Std() != want {
			t.Errorf("%s = %v, %v; want %v", in, d.Std(), err, want)
		}
	}
	for in, want := range map[string]ByteSize{`4096`: 4096, `"4KiB"`: 4096, `"1MiB"`: 1 << 20, `"512"`: 512, `"2B"`: 2} {
		var b ByteSize
		if err := json.Unmarshal([]byte(in), &b); err != nil || b != want {
			t.Errorf("%s = %d, %v; want %d", in, b, err, want)
		}
	}
}

func TestSharedFileNames(t *testing.T) {
	want := map[string]string{
		StoreFileName: "store.sqlite", SocketFileName: "sock", LockFileName: "daemon.lock",
		EventLogFileName: "events.jsonl", DaemonLogFileName: "daemon.log.jsonl", ClientLogFileName: "client.log.jsonl",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("file name constant = %q, want %q", got, w)
		}
	}
}

func TestBlockHelpers(t *testing.T) {
	cfg, err := Load(write(t, minimal), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	// A block carrying neither field, a null block, an empty one and a non-object
	// one are all accepted.
	for _, block := range []string{``, `null`, `{}`, `{"base_url":"x"}`, `"text"`, `[1]`} {
		if err := CheckBlock(cfg, json.RawMessage(block)); err != nil {
			t.Errorf("block %q must be accepted, got %v", block, err)
		}
	}
	matching := `{"config_hash":"` + cfg.Hash + `","config_path":"/etc/other.json"}`
	if err := CheckBlock(cfg, json.RawMessage(matching)); err != nil {
		t.Errorf("matching hash must be accepted: %v", err)
	}
	err = CheckBlock(cfg, json.RawMessage(`{"config_hash":"deadbeef","config_path":"/etc/stale.json"}`))
	if !errors.Is(err, ErrConfigHashMismatch) {
		t.Fatalf("want ErrConfigHashMismatch, got %v", err)
	}
	for _, s := range []string{"/etc/stale.json", cfg.Path, "deadbeef", cfg.Hash} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("mismatch error %q must name %q", err, s)
		}
	}
	h, p, err := BlockStamp(json.RawMessage(matching))
	if err != nil || h != cfg.Hash || p != "/etc/other.json" {
		t.Errorf("BlockStamp = %q %q %v", h, p, err)
	}
	// FieldConfigHash / FieldConfigPath are the documented names.
	if FieldConfigHash != "config_hash" || FieldConfigPath != "config_path" {
		t.Errorf("stamp field names = %q %q", FieldConfigHash, FieldConfigPath)
	}
}

func TestRelativeEnvPathsRejected(t *testing.T) {
	for _, env := range []map[string]string{{EnvStateDir: "state"}, {EnvSocket: "run/sock"}} {
		if _, err := Load(write(t, minimal), envOf(env)); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Errorf("env %v: want an absolute-path error, got %v", env, err)
		}
	}
}

func TestNullMeansUnset(t *testing.T) {
	// A renderer MAY emit null for an unset nix option; the default applies.
	cfg, err := Load(write(t, `{"state_dir":"/tmp/s","host":null,"gh_path":null,"summary_ttl":null,
		"expiry":null,"change_body_cap":null,"workers":null,"background_share":null,
		"query_ttl":null,"pr":null,"ci":null,"otel_endpoint":null,"socket_path":null}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	def, err := Load(write(t, minimal), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(cfg)
	b, _ := json.Marshal(def)
	if string(a) != string(b) {
		t.Fatalf("null must equal unset:\n%s\n%s", a, b)
	}
	if cfg.QueryTTLFor("mine").Std() != time.Minute || cfg.QueryTTLFor("team").Std() != 2*time.Minute {
		t.Errorf("query_ttl null must keep the mine/team defaults, got %v", cfg.QueryTTL)
	}
	if cfg.Hash != def.Hash {
		t.Errorf("null and unset must hash alike: %q vs %q", cfg.Hash, def.Hash)
	}
}

func TestCISectionTakesNoKeys(t *testing.T) {
	if _, err := Load(write(t, `{"state_dir":"/tmp/s","ci":{}}`), noEnv); err != nil {
		t.Errorf("empty ci section must load: %v", err)
	}
	_, err := Load(write(t, `{"state_dir":"/tmp/s","ci":{"queries":{"mine":"q"}}}`), noEnv)
	if err == nil || !strings.Contains(err.Error(), "queries") || !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("ci.queries must be rejected naming the key, got %v", err)
	}
	// The ci instance resolves a query name against the pr queries.
	cfg, err := Load(write(t, `{"state_dir":"/tmp/s","pr":{"queries":{"mine":"q"}}}`), noEnv)
	if err != nil || !cfg.HasQuery("mine") || cfg.HasQuery("nope") {
		t.Errorf("HasQuery: %v %v", err, cfg)
	}
}

func TestPostedStateDirSetting(t *testing.T) {
	const dir = "/var/example/pg-posted"
	cfg, err := Load(write(t, minimal), noEnv)
	if err != nil || cfg.PR.PostedStateDir != "" {
		t.Fatalf("unset must stay empty: %v %q", err, cfg.PR.PostedStateDir)
	}
	cfg, err = Load(write(t, `{"state_dir":"/tmp/s","pr":{"posted_state_dir":"`+dir+`"}}`), noEnv)
	if err != nil || cfg.PR.PostedStateDir != dir {
		t.Fatalf("an absolute value must load: %v %q", err, cfg.PR.PostedStateDir)
	}
	// It sits beside the queries and leaves them alone.
	cfg, err = Load(write(t, `{"state_dir":"/tmp/s","pr":{"queries":{"mine":"q"},"posted_state_dir":"`+dir+`"}}`), noEnv)
	if err != nil || !cfg.HasQuery("mine") || cfg.PR.PostedStateDir != dir {
		t.Fatalf("queries and posted_state_dir together: %v %+v", err, cfg.PR)
	}
	// The loader renders the file only: the daemon's environment is the
	// sidecar resolver's business, never a value this field takes.
	env := envOf(map[string]string{"PG_CONNECTOR_PR_GITHUB_STATE_DIR": "/e/state", "XDG_STATE_HOME": "/e/xdg", "HOME": "/e/home"})
	cfg, err = Load(write(t, minimal), env)
	if err != nil || cfg.PR.PostedStateDir != "" {
		t.Fatalf("the environment must not fill the setting: %v %q", err, cfg.PR.PostedStateDir)
	}
}

func TestRelativePostedStateDirRejectedByName(t *testing.T) {
	for _, dir := range []string{"state/posted", "./posted", "../posted", "~/posted", "posted"} {
		_, err := Load(write(t, `{"state_dir":"/tmp/s","pr":{"posted_state_dir":"`+dir+`"}}`), noEnv)
		if err == nil {
			t.Errorf("%q: a relative path must be rejected", dir)
			continue
		}
		for _, want := range []string{SettingPostedStateDir, "absolute", dir} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error %q must contain %q", dir, err, want)
			}
		}
	}
	if SettingPostedStateDir != "pr.posted_state_dir" {
		t.Errorf("setting name = %q", SettingPostedStateDir)
	}
}

func TestPostedStateDirLivesOnlyUnderPR(t *testing.T) {
	// The strict decoder accepts the key under pr and nowhere else, and a
	// misspelling is named, not ignored.
	for name, tc := range map[string]struct{ body, key string }{
		"top level":  {`{"state_dir":"/tmp/s","posted_state_dir":"/x"}`, "posted_state_dir"},
		"ci section": {`{"state_dir":"/tmp/s","ci":{"posted_state_dir":"/x"}}`, "posted_state_dir"},
		"misspelled": {`{"state_dir":"/tmp/s","pr":{"posted_state_dirr":"/x"}}`, "posted_state_dirr"},
	} {
		_, err := Load(write(t, tc.body), noEnv)
		if err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), `"`+tc.key+`"`) {
			t.Errorf("%s: want an unknown-key error naming %q, got %v", name, tc.key, err)
		}
	}
}

func TestConfigPathStampIsInformational(t *testing.T) {
	plain, err := Load(write(t, minimal), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	withPath, err := Load(write(t, `{"state_dir":"/tmp/s","config_path":"/etc/rendered/config.json"}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if withPath.StampedPath != "/etc/rendered/config.json" {
		t.Errorf("StampedPath = %q", withPath.StampedPath)
	}
	if withPath.Path == withPath.StampedPath {
		t.Errorf("Path is the file actually read, not the stamp: %q", withPath.Path)
	}
	if withPath.Hash != plain.Hash || withPath.HashStamped() {
		t.Errorf("the path stamp must not enter the hash: %q vs %q (stamped=%v)", withPath.Hash, plain.Hash, withPath.HashStamped())
	}
}

func TestRenderedGolden(t *testing.T) {
	// testdata/rendered.json is what the nix module renders after cutover: only
	// the keys it sets, a stamped hash, and a null for an unset option.
	cfg, err := Load("testdata/rendered.json", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HashStamped() || cfg.Hash != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Errorf("stamped hash must be returned as stamped: %q (stamped=%v)", cfg.Hash, cfg.HashStamped())
	}
	if cfg.GraphQLPointsPerHour != CutoverGraphQLPointsPerHour || !cfg.OTelEnabled() || cfg.GH != "/opt/example/bin/gh" {
		t.Errorf("gql=%d otel=%q gh=%q", cfg.GraphQLPointsPerHour, cfg.OTelEndpoint, cfg.GH)
	}
	if cfg.SummaryTTL.Std() != DefaultSummaryTTL || cfg.FillHorizon != cfg.SummaryTTL {
		t.Errorf("a null summary_ttl keeps the default: summary=%v fill=%v", cfg.SummaryTTL.Std(), cfg.FillHorizon.Std())
	}
	if !cfg.HasQuery("mine") || !cfg.HasQuery("team") {
		t.Errorf("queries = %#v", cfg.PR.Queries)
	}
}
