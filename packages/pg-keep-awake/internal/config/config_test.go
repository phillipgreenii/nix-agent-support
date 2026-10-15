package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const fullFile = `
heartbeat_interval = "5s"
watchdog_timeout = "2m"
linger = "45s"

[battery]
hold_pct = 40
release_pct = 25
hysteresis_pct = 7

[otel]
endpoint = "http://collector.example:4317"
`

func TestDefaultsValues(t *testing.T) {
	d := Defaults()
	if d.HeartbeatInterval != 10*time.Second || d.WatchdogTimeout != 10*time.Minute || d.Linger != 30*time.Second {
		t.Fatalf("durations = %v %v %v", d.HeartbeatInterval, d.WatchdogTimeout, d.Linger)
	}
	if d.Battery != (BatteryConfig{HoldPct: 30, ReleasePct: 20, HysteresisPct: 5}) {
		t.Fatalf("battery = %+v", d.Battery)
	}
	if d.OTel.Endpoint != "" {
		t.Fatalf("default otel endpoint = %q, want none", d.OTel.Endpoint)
	}
}

func TestLoadAbsentFileGivesDefaultsAndNoError(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "config.toml"), filepath.Join(dir, "config.last-good.toml"))
	if err != nil {
		t.Fatalf("absent file reported error: %v", err)
	}
	if cfg != Defaults() {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
	if cfg.OTel.Endpoint != "" {
		t.Fatalf("otel endpoint = %q, want none", cfg.OTel.Endpoint)
	}
}

func TestLoadAbsentFileIgnoresLastGood(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "config.last-good.toml", fullFile)
	cfg, err := Load(filepath.Join(dir, "config.toml"), good)
	if err != nil || cfg != Defaults() {
		t.Fatalf("cfg=%+v err=%v; an absent file means defaults, not last-good", cfg, err)
	}
}

func TestLoadFullFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "config.toml", fullFile)
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		HeartbeatInterval: 5 * time.Second,
		WatchdogTimeout:   2 * time.Minute,
		Linger:            45 * time.Second,
		Battery:           BatteryConfig{HoldPct: 40, ReleasePct: 25, HysteresisPct: 7},
		OTel:              OTelConfig{Endpoint: "http://collector.example:4317"},
	}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestLoadPartialFileKeepsOtherDefaults(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "config.toml", "linger = \"1m\"\n[battery]\nhold_pct = 50\n")
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.Linger = time.Minute
	want.Battery.HoldPct = 50
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestOTelAbsentMeansNoEndpoint(t *testing.T) {
	for name, body := range map[string]string{
		"no table":       "linger = \"1s\"\n",
		"empty table":    "[otel]\n",
		"other key only": "[battery]\nhold_pct = 10\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OTel.Endpoint != "" {
				t.Fatalf("endpoint = %q, want none", cfg.OTel.Endpoint)
			}
		})
	}
}

func TestOTelEndpointReadWhenPresent(t *testing.T) {
	cfg, err := Parse([]byte("[otel]\nendpoint = \"http://x:4317\"\n"))
	if err != nil || cfg.OTel.Endpoint != "http://x:4317" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadFallbackChain(t *testing.T) {
	dir := t.TempDir()
	bad := write(t, dir, "config.toml", "heartbeat_interval = [oops\n")
	goodBody := "linger = \"90s\"\n[otel]\nendpoint = \"http://last-good:4317\"\n"
	good := write(t, dir, "config.last-good.toml", goodBody)

	t.Run("last-good used when file is invalid", func(t *testing.T) {
		cfg, err := Load(bad, good)
		if err == nil {
			t.Fatal("parse error was not reported")
		}
		if cfg.Linger != 90*time.Second || cfg.OTel.Endpoint != "http://last-good:4317" {
			t.Fatalf("cfg = %+v, want the last-good values", cfg)
		}
	})

	t.Run("defaults when last-good is absent", func(t *testing.T) {
		cfg, err := Load(bad, filepath.Join(dir, "missing.toml"))
		if err == nil {
			t.Fatal("parse error was not reported")
		}
		if cfg != Defaults() {
			t.Fatalf("cfg = %+v, want defaults", cfg)
		}
	})

	t.Run("defaults when last-good is also invalid", func(t *testing.T) {
		alsoBad := write(t, dir, "bad2.toml", "linger = 5\n") // int, not a string
		cfg, err := Load(bad, alsoBad)
		if err == nil {
			t.Fatal("parse error was not reported")
		}
		if cfg != Defaults() {
			t.Fatalf("cfg = %+v, want defaults", cfg)
		}
	})

	t.Run("defaults when no last-good path is given", func(t *testing.T) {
		cfg, err := Load(bad, "")
		if err == nil || cfg != Defaults() {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})

	t.Run("reported error is the primary file's, not last-good's", func(t *testing.T) {
		_, err := Load(bad, good)
		if err == nil || !strings.Contains(err.Error(), "parse config") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLoadInvalidValuesFallBack(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"bad duration":      "heartbeat_interval = \"soon\"\n",
		"zero heartbeat":    "heartbeat_interval = \"0s\"\n",
		"negative linger":   "linger = \"-1s\"\n",
		"pct above 100":     "[battery]\nhold_pct = 101\n",
		"negative pct":      "[battery]\nrelease_pct = -1\n",
		"wrong type":        "[battery]\nhold_pct = \"thirty\"\n",
		"unknown key":       "heartbeat_intervall = \"10s\"\n",
		"unknown table key": "[otel]\nendpont = \"x\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := write(t, dir, "c.toml", body)
			cfg, err := Load(p, "")
			if err == nil {
				t.Fatal("invalid config reported no error")
			}
			if cfg != Defaults() {
				t.Fatalf("cfg = %+v, want defaults", cfg)
			}
		})
	}
}

func TestLoadCapsReleaseAtHold(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "c.toml", "[battery]\nhold_pct = 15\nrelease_pct = 40\n")
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("an inverted release is capped at runtime, not an error: %v", err)
	}
	if cfg.Battery.ReleasePct != 15 {
		t.Fatalf("release = %d, want capped at hold 15", cfg.Battery.ReleasePct)
	}
}

func TestLoadHostileInputDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "good.toml", "linger = \"2s\"\n")

	huge := strings.Repeat("# padding padding padding padding\n", MaxFileSize/10)
	hostile := map[string][]byte{
		"oversized":        []byte(huge + "linger = \"1s\"\n"),
		"nul bytes":        []byte("linger = \"1s\"\x00\x00\x00"),
		"binary":           {0xff, 0xfe, 0x00, 0x80, 0x81},
		"deep nesting":     []byte("a = " + strings.Repeat("[", 50000) + strings.Repeat("]", 50000)),
		"unterminated":     []byte("[otel\nendpoint = \"x"),
		"huge string":      []byte("[otel]\nendpoint = \"" + strings.Repeat("a", MaxFileSize/2) + "\"\n"),
		"duplicate keys":   []byte("linger = \"1s\"\nlinger = \"2s\"\n"),
		"many tables":      []byte(strings.Repeat("[t]\n", 1000)),
		"empty":            {},
		"only whitespace":  []byte("\n\n   \n"),
		"invalid utf8 key": []byte("\xc3\x28 = 1\n"),
	}
	for name, body := range hostile {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, "hostile.toml")
			if err := os.WriteFile(p, body, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, _ := Load(p, good) // must not panic; the result must be usable
			if cfg.HeartbeatInterval <= 0 || cfg.WatchdogTimeout <= 0 {
				t.Fatalf("unusable config: %+v", cfg)
			}
		})
	}

	t.Run("oversized file is rejected, last-good used", func(t *testing.T) {
		p := filepath.Join(dir, "big.toml")
		if err := os.WriteFile(p, hostile["oversized"], 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(p, good)
		if err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("err = %v, want the size refusal", err)
		}
		if cfg.Linger != 2*time.Second {
			t.Fatalf("cfg = %+v, want last-good", cfg)
		}
	})

	t.Run("file at the limit is accepted", func(t *testing.T) {
		line := "linger = \"3s\"\n"
		body := line + strings.Repeat("#", MaxFileSize-len(line))
		p := write(t, dir, "limit.toml", body)
		cfg, err := Load(p, "")
		if err != nil || cfg.Linger != 3*time.Second {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})
}

func TestLoadNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir, "") // a directory in place of the file
	if err == nil {
		t.Fatal("a directory was accepted as a config file")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a directory is not 'absent': %v", err)
	}
	if cfg != Defaults() {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.toml")
	cases := []struct {
		name      string
		body      string
		wantError bool
	}{
		{"full valid", fullFile, false},
		{"empty valid", "", false},
		{"malformed", "heartbeat_interval = \n", true},
		{"unknown key", "typo = 1\n", true},
		{"watchdog below 6 heartbeats", "heartbeat_interval = \"10s\"\nwatchdog_timeout = \"59s\"\n", true},
		{"watchdog exactly 6 heartbeats", "heartbeat_interval = \"10s\"\nwatchdog_timeout = \"60s\"\n", false},
		{"release above hold", "[battery]\nhold_pct = 20\nrelease_pct = 30\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := write(t, dir, "check.toml", tc.body)
			err := Check(p, true)
			if (err != nil) != tc.wantError {
				t.Fatalf("err = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
	t.Run("absent file valid by default", func(t *testing.T) {
		if err := Check(missing, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("absent file invalid when required", func(t *testing.T) {
		if err := Check(missing, true); err == nil {
			t.Fatal("missing explicit file accepted")
		}
	})
}

func TestResolveFrom(t *testing.T) {
	cases := []struct {
		name, xdg, home, want string
		wantErr               bool
	}{
		{"xdg absolute", "/x/cfg", "/h", "/x/cfg/pg-keep-awake", false},
		{"xdg relative ignored", "rel", "/h", "/h/.config/pg-keep-awake", false},
		{"xdg unset", "", "/h", "/h/.config/pg-keep-awake", false},
		{"nothing", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveFrom(tc.xdg, tc.home)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestDefaultPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/test")
	got, err := DefaultPath()
	if err != nil || got != "/xdg/test/pg-keep-awake/config.toml" {
		t.Fatalf("got %q, %v", got, err)
	}
}
