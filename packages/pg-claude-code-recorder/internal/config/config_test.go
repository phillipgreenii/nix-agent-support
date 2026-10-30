package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaultsFollowEnvironment(t *testing.T) {
	c := Defaults(env(map[string]string{"HOME": "/home-x", "CLAUDE_CONFIG_DIR": "/c", "XDG_STATE_HOME": "/s"}))
	if c.ClaudeDir != "/c" || c.StateDir != "/s/pg-claude-code-recorder" || c.Socket != "/s/pg-claude-code-recorder/recorder.sock" {
		t.Fatalf("%+v", c)
	}
	if got := DefaultPath(env(map[string]string{"HOME": "/h"})); got != "/h/.config/pg-claude-code-recorder/config.toml" {
		t.Fatal(got)
	}
}

func TestLoadReadsKnownKeysAndToleratesLaterPhaseKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	body := `
claudeDir = "/example/claude"
stateDir = "/example/state"
controlStateDir = "/example/control"
blockList = ["*.secret"]
minFreeBytes = 1000

[sweep]
interval = "2h"
[wait]
max = "30s"
maxWaiters = 8
[feed]
retention = "7d"
[remove]
after = "1d12h"
[metrics]
addr = "127.0.0.1:9"
[parser]
someFutureKey = 3
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, env(map[string]string{"HOME": "/h"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.SweepInterval != 2*time.Hour || c.WaitMax != 30*time.Second || c.WaitMaxWaiter != 8 ||
		c.FeedRetention != 7*24*time.Hour || c.RemoveAfter != 36*time.Hour || c.MinFreeBytes != 1000 ||
		c.MetricsAddr != "127.0.0.1:9" || c.Socket != "/example/state/recorder.sock" {
		t.Fatalf("%+v", c)
	}
	if got := c.Roots(); len(got) != 2 || got[1] != "/example/control/ends" {
		t.Fatalf("roots %v", got)
	}
}

func TestEnvOverridesSocketAndMissingFileRules(t *testing.T) {
	e := env(map[string]string{"HOME": "/h", EnvSocket: "/x/s.sock", EnvMetricsAddr: "127.0.0.1:1"})
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"), false, e)
	if err != nil || c.Socket != "/x/s.sock" || c.MetricsAddr != "127.0.0.1:1" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml"), true, e); err == nil {
		t.Fatal("missing explicit file accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.toml")
	_ = os.WriteFile(bad, []byte("[sweep]\ninterval = \"xyz\"\n"), 0o600)
	if _, err := Load(bad, true, e); err == nil {
		t.Fatal("bad duration accepted")
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"3d": 72 * time.Hour, "90s": 90 * time.Second, "1d1h": 25 * time.Hour} {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ParseDuration("xd"); err == nil {
		t.Error("xd accepted")
	}
}

// The grace-window fallback is off by default (pg2-8wt1uz measured the registry
// file deleted before the exit on every clean path) and stays an option.
func TestGraceWindowFallbackIsOffByDefaultAndCanBeEnabled(t *testing.T) {
	if Defaults(env(nil)).GraceEnable {
		t.Fatal("process.grace must default to off")
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[process]\ngrace = true\ngraceWindow = \"2s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, env(map[string]string{"HOME": "/h"}))
	if err != nil || !c.GraceEnable || c.GraceWindow != 2*time.Second {
		t.Fatalf("%v %+v", err, c)
	}
}
