package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeConfig writes body to <tmp>/config.yaml and returns the path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// isolateEnv points every location Load and StorePath consult at fresh temp
// dirs and returns (configHome, stateHome).
func isolateEnv(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	cfgHome := filepath.Join(home, "xdg-config")
	stHome := filepath.Join(home, "xdg-state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_STATE_HOME", stHome)
	return cfgHome, stHome
}

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	isolateEnv(t)
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.Interval != "1h" || c.Schedule.Window != "48h" {
		t.Errorf("schedule = %+v, want 1h/48h", c.Schedule)
	}
	if c.Store.Path != "$XDG_STATE_HOME/work-report/store.db" {
		t.Errorf("store.path = %q", c.Store.Path)
	}
	if c.Timezone != "" {
		t.Errorf("timezone = %q, want empty (system zone)", c.Timezone)
	}
	loc, err := c.Location()
	if err != nil || loc == nil {
		t.Fatalf("Location() = %v, %v", loc, err)
	}
	if !c.SourceEnabled("anything") || c.SourceLabels("anything") != nil {
		t.Error("an absent source must be enabled with no labels")
	}
}

func TestLoadOverridingFile(t *testing.T) {
	p := writeConfig(t, `
timezone: America/New_York
sources:
  backend-a:
    enable: false
    labels: [x, y]
  backend-b:
    labels: [only-labels]
schedule:
  interval: 30m
  window: 7d
store:
  path: /var/lib/wr/store.db
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Timezone != "America/New_York" {
		t.Errorf("timezone = %q", c.Timezone)
	}
	loc, err := c.Location()
	if err != nil || loc.String() != "America/New_York" {
		t.Errorf("Location() = %v, %v", loc, err)
	}
	if c.SourceEnabled("backend-a") {
		t.Error("backend-a must be disabled")
	}
	if got := c.SourceLabels("backend-a"); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("backend-a labels = %v", got)
	}
	// A block that sets only labels stays enabled.
	if !c.SourceEnabled("backend-b") {
		t.Error("a labels-only block must stay enabled")
	}
	if got := c.SourceLabels("backend-b"); !reflect.DeepEqual(got, []string{"only-labels"}) {
		t.Errorf("backend-b labels = %v", got)
	}
	if !c.SourceEnabled("backend-c") {
		t.Error("an unlisted backend must be enabled")
	}
	if c.Schedule.Interval != "30m" || c.Schedule.Window != "7d" {
		t.Errorf("schedule = %+v", c.Schedule)
	}
	if c.Store.Path != "/var/lib/wr/store.db" {
		t.Errorf("store.path = %q", c.Store.Path)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	p := writeConfig(t, "schedule:\n  window: 24h\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.Interval != "1h" || c.Schedule.Window != "24h" {
		t.Errorf("schedule = %+v, want interval default 1h and window 24h", c.Schedule)
	}
	if c.Store.Path != DefaultStorePath {
		t.Errorf("store.path = %q, want default", c.Store.Path)
	}
}

func TestLoadEmptyFileIsDefaults(t *testing.T) {
	c, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.Window != "48h" {
		t.Errorf("window = %q", c.Schedule.Window)
	}
}

func TestLoadUnknownKeyIsError(t *testing.T) {
	for _, tc := range []struct{ name, body, key string }{
		{"top level", "bogus: 1\n", "bogus"},
		{"nested", "schedule:\n  cadence: 1h\n", "cadence"},
		{"source block", "sources:\n  b:\n    enabled: true\n", "enabled"},
		{"narrative unknown option", "kinds:\n  narrative:\n    bogus: 1\n", "bogus"},
		{"unknown kind name", "kinds:\n  other:\n    model: m\n", "other"},
		{"kinds unknown sibling", "kinds:\n  narrative:\n    model: m\n  baseline:\n    model: m\n", "baseline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("want error for unknown key")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name key %q", err, tc.key)
			}
		})
	}
}

func TestLoadNarrativeKindOptions(t *testing.T) {
	c, err := Load(writeConfig(t, "kinds:\n  narrative:\n    model: some-model\n    systemPromptFile: /nonexistent/p.md\n"))
	if err != nil {
		t.Fatalf("a narrative config must load (the prompt file is not checked): %v", err)
	}
	if got := c.Kinds.Narrative; got.Model != "some-model" || got.SystemPromptFile != "/nonexistent/p.md" {
		t.Errorf("kinds.narrative = %+v", got)
	}
}

func TestLoadNarrativeOptionsAreOptional(t *testing.T) {
	for _, body := range []string{"", "kinds:\n  narrative:\n    model: only-model\n", "kinds:\n  narrative:\n    systemPromptFile: /p.md\n"} {
		c, err := Load(writeConfig(t, body))
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if body == "" && (c.Kinds.Narrative != NarrativeCfg{}) {
			t.Errorf("absent options must be empty, got %+v", c.Kinds.Narrative)
		}
	}
}

func TestLoadMissingExplicitPathIsError(t *testing.T) {
	isolateEnv(t)
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	_, err := Load(missing)
	if err == nil {
		t.Fatal("a missing explicit --config path must be an error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the path", err)
	}
}

func TestLoadHonorsXDGConfigHome(t *testing.T) {
	cfgHome, _ := isolateEnv(t)
	dir := filepath.Join(cfgHome, "work-report")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("schedule:\n  interval: 15m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.Interval != "15m" {
		t.Errorf("interval = %q, want 15m from $XDG_CONFIG_HOME", c.Schedule.Interval)
	}
}

func TestLoadFallsBackToHomeDotConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".config", "work-report")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("schedule:\n  interval: 20m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.Interval != "20m" {
		t.Errorf("interval = %q, want 20m from ~/.config", c.Schedule.Interval)
	}
}

func TestWindow(t *testing.T) {
	for _, tc := range []struct {
		window string
		ok     bool
	}{
		{"48h", true},
		{"7d", true},
		{"1h", true},
		{"90m", false},
		{"2d12h", false},
		{"", false},
		{"h", false},
		{"0h", false},
		{"-5h", false},
		{"48", false},
		{"4.5h", false},
		{"48H", false},
		{" 48h", false},
	} {
		body := "schedule:\n  window: " + quote(tc.window) + "\n"
		_, err := Load(writeConfig(t, body))
		if tc.ok && err != nil {
			t.Errorf("window %q: unexpected error %v", tc.window, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("window %q: want error", tc.window)
			} else if !strings.Contains(err.Error(), "schedule.window") {
				t.Errorf("window %q: error %q does not name schedule.window", tc.window, err)
			}
		}
	}
}

func quote(s string) string { return `"` + s + `"` }

func TestInterval(t *testing.T) {
	for _, tc := range []struct {
		interval string
		ok       bool
	}{
		{"1h", true},
		{"90m", true},
		{"1h30m", true},
		{"", false},
		{"0s", false},
		{"-1h", false},
		{"soon", false},
		{"2d", false},
	} {
		body := "schedule:\n  interval: " + quote(tc.interval) + "\n"
		_, err := Load(writeConfig(t, body))
		if tc.ok && err != nil {
			t.Errorf("interval %q: unexpected error %v", tc.interval, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("interval %q: want error", tc.interval)
			} else if !strings.Contains(err.Error(), "schedule.interval") {
				t.Errorf("interval %q: error %q does not name schedule.interval", tc.interval, err)
			}
		}
	}
}

func TestUnknownTimezoneIsError(t *testing.T) {
	_, err := Load(writeConfig(t, "timezone: Mars/Olympus_Mons\n"))
	if err == nil || !strings.Contains(err.Error(), "Mars/Olympus_Mons") {
		t.Errorf("err = %v, want one naming the bad zone", err)
	}
}

func TestStorePathPrecedence(t *testing.T) {
	_, stHome := isolateEnv(t)
	t.Setenv("WR_TEST_DIR", "/data")

	var c Config
	c.Store.Path = "$WR_TEST_DIR/s.db"

	if got := StorePath(c, "/flag/s.db"); got != "/flag/s.db" {
		t.Errorf("flag must win: got %q", got)
	}
	if got := StorePath(c, ""); got != "/data/s.db" {
		t.Errorf("store.path with env expanded: got %q", got)
	}

	// Defaults, from a loaded all-defaults Config and from a zero Config.
	want := filepath.Join(stHome, "work-report", "store.db")
	d, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := StorePath(d, ""); got != want {
		t.Errorf("default store path = %q, want %q", got, want)
	}
	if got := StorePath(Config{}, ""); got != want {
		t.Errorf("zero Config store path = %q, want %q", got, want)
	}
}

func TestStorePathWithoutXDGStateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	want := filepath.Join(home, ".local", "state", "work-report", "store.db")

	if got := StorePath(Config{}, ""); got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	// The expansion itself falls back too.
	var c Config
	c.Store.Path = "${XDG_STATE_HOME}/custom/s.db"
	if got, w := StorePath(c, ""), filepath.Join(home, ".local", "state", "custom", "s.db"); got != w {
		t.Errorf("expanded = %q, want %q", got, w)
	}
}

func TestPGRouterQueryTextGolden(t *testing.T) {
	const defaults = `[[query]]
name = "work-report-pull"
emits = ["escalated.pg2"]
type = "command"
[query.command]
argv = ["work-report", "pull", "--range", "last-48h", "--output", "pg-router"]
format = "json"
[query.trigger]
kind = "period"
every = "1h"
`
	const custom = `[[query]]
name = "work-report-pull"
emits = ["escalated.pg2"]
type = "command"
[query.command]
argv = ["work-report", "pull", "--range", "last-7d", "--output", "pg-router"]
format = "json"
[query.trigger]
kind = "period"
every = "90m"
`
	isolateEnv(t)
	d, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.PGRouterQueryText(); got != defaults {
		t.Errorf("defaults stanza:\n%s\nwant:\n%s", got, defaults)
	}
	if got := (Config{}).PGRouterQueryText(); got != defaults {
		t.Errorf("zero Config stanza:\n%s\nwant:\n%s", got, defaults)
	}

	c, err := Load(writeConfig(t, "schedule:\n  interval: 90m\n  window: 7d\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.PGRouterQueryText(); got != custom {
		t.Errorf("custom stanza:\n%s\nwant:\n%s", got, custom)
	}
	if strings.Contains(c.PGRouterQueryText(), "work-report.degraded") {
		t.Error("the stanza must emit escalated.pg2, not work-report.degraded")
	}
}

func TestSourceAccessorsOnZeroConfig(t *testing.T) {
	var c Config
	if !c.SourceEnabled("x") {
		t.Error("zero Config: every backend is enabled")
	}
	if c.SourceLabels("x") != nil {
		t.Error("zero Config: no labels")
	}
}
