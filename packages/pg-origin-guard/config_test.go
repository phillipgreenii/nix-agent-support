package originguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsWithoutFile(t *testing.T) {
	env := testEnv(t)
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"), env)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(env.Home, ".local/state/host-activity/userpresence.json")
	if cfg.Presence.File != want {
		t.Errorf("presence file = %q, want %q", cfg.Presence.File, want)
	}
	if cfg.Presence.MaxAge != time.Minute || cfg.Breaker.FailureThreshold != 2 ||
		cfg.Breaker.OpenDuration != time.Minute || cfg.Breaker.StartupWindow != 10*time.Second ||
		cfg.Breaker.EstablishedAfter != time.Second || cfg.Events.RetentionDays != 14 ||
		cfg.Events.HeartbeatInterval != 5*time.Minute {
		t.Errorf("defaults wrong: %+v", cfg)
	}
	if len(cfg.Realms) != 0 {
		t.Errorf("realms = %v, want none", cfg.Realms)
	}
}

func TestXDGPaths(t *testing.T) {
	env := Env{Home: "/h", Getenv: func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": "/c", "XDG_STATE_HOME": "/s"}[k]
	}}
	if got := env.ConfigPath(); got != "/c/pg-origin-guard/config.toml" {
		t.Errorf("ConfigPath = %q", got)
	}
	if got := env.StateDir(); got != "/s/pg-origin-guard" {
		t.Errorf("StateDir = %q", got)
	}
	rel := Env{Home: "/h", Getenv: func(string) string { return "relative" }}
	if got := rel.StateDir(); got != "/h/.local/state/pg-origin-guard" {
		t.Errorf("relative XDG_STATE_HOME must be ignored, got %q", got)
	}
}

const fullConfig = `
[presence]
file = "~/p.json"
max_age = "90s"
refresh_seconds = 60

[breaker]
failure_threshold = 3
open_duration = "2m"
startup_window = "20s"
established_after = "2s"

[events]
retention_days = 7
heartbeat_interval = "1m"

[realm.zr]
match = { host = "github.com", user = "org-*" }
agent_socket = "/tmp/agent.sock"
[realm.zr.login]
attempt_timeout = "4m"
backoff = ["1m", "2m"]

[realm.plain]
`

func TestParseFull(t *testing.T) {
	env := testEnv(t)
	cfg := mustParse(t, fullConfig, env)
	if cfg.Presence.File != filepath.Join(env.Home, "p.json") || cfg.Presence.MaxAge != 90*time.Second ||
		cfg.Presence.RefreshSeconds != 60 {
		t.Errorf("presence: %+v", cfg.Presence)
	}
	if cfg.Breaker.FailureThreshold != 3 || cfg.Breaker.OpenDuration != 2*time.Minute {
		t.Errorf("breaker: %+v", cfg.Breaker)
	}
	zr := cfg.Realms["zr"]
	if zr.MatchHost != "github.com" || zr.MatchUser != "org-*" || zr.AgentSocket != "/tmp/agent.sock" ||
		zr.Login.AttemptTimeout != 4*time.Minute || len(zr.Login.Backoff) != 2 || zr.Login.MaxBackoff() != 2*time.Minute {
		t.Errorf("zr: %+v", zr)
	}
	plain := cfg.Realms["plain"]
	if plain.Login.AttemptTimeout != 3*time.Minute || len(plain.Login.Backoff) != 3 {
		t.Errorf("realm login defaults: %+v", plain.Login)
	}
	if cfg.MaxBackoff() != 30*time.Minute {
		t.Errorf("MaxBackoff = %v", cfg.MaxBackoff())
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":          "[breaker]\nfailure_treshold = 1\n",
		"bad duration":         "[breaker]\nopen_duration = \"soon\"\n",
		"zero threshold":       "[breaker]\nfailure_threshold = 0\n",
		"zero retention":       "[events]\nretention_days = 0\n",
		"established > window": "[breaker]\nstartup_window = \"1s\"\nestablished_after = \"2s\"\n",
		"relative presence":    "[presence]\nfile = \"rel.json\"\n",
		"bad realm name":       "[realm.\"Bad/Name\"]\n",
		"relative socket":      "[realm.zr]\nagent_socket = \"agent.sock\"\n",
		"empty backoff":        "[realm.zr.login]\nbackoff = []\n",
		"negative backoff":     "[realm.zr.login]\nbackoff = [\"-1m\"]\n",
		"bad glob":             "[realm.zr]\nmatch = { host = \"[\" }\n",
		"not toml":             "this is = = not toml",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(text, testEnv(t))
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *ConfigError", err)
			}
		})
	}
}

func TestLoadReadsFile(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[events]\nretention_days = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, env)
	if err != nil || cfg.Events.RetentionDays != 3 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	if err := os.WriteFile(p, []byte("x = 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, env); err == nil || !strings.HasPrefix(err.Error(), "config: ") {
		t.Errorf("err = %v", err)
	}
}

func TestExitCodesAreDistinct(t *testing.T) {
	seen := map[int]bool{}
	for _, c := range []int{ExitOK, ExitUnavailable, ExitUsage, ExitConfig, ExitInternal} {
		if seen[c] || c == 1 {
			t.Errorf("exit code %d duplicated or reserved", c)
		}
		seen[c] = true
	}
	if ExitUnavailable != 75 || ExitInternal != 70 {
		t.Error("75 (refused) and 70 (fault) must stay separate")
	}
}
