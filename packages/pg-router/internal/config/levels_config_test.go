package config

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/query"
)

// levelsWorkQuery is an ordinary query emitting "e"; tests whose roles bind "e"
// append it.
const levelsWorkQuery = `
[[query]]
name = "work"
type = "command"
emits = ["e"]
[query.command]
argv = ["x"]
format = "jsonl"
`

// levelsCfgBase is a generic config: a registry, a level-source query for the
// "host" sensor, and a sink role for it. Names are
// placeholders only.
const levelsCfgBase = `
[levels]
grace_factor = 3

[[resource]]
name = "disk"
scale = ["PLENTY", "OK", "TIGHT", "EXHAUSTED"]
sensor = "host"

[[resource]]
name = "git_auth_x"
scale = ["AVAILABLE", "UNAVAILABLE"]
sensor = "git"
unknown = "worst"

[[resource]]
name = "router"
scale = ["RUNNING", "PAUSED"]
ignored = "== RUNNING"
sensor = "router"

[[query]]
name = "probe"
type = "command"
emits = ["lvl"]
level_source = "host"
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "lvl-sink"
handler = "/bin/h"
binds = ["lvl"]
`

func TestConfig_levelsDefaults(t *testing.T) {
	lc := Default().Levels
	want := LevelsConfig{ObservedAtSkew: 5 * time.Minute, GraceFactor: 2, StartWait: 10 * time.Second, StartStagger: 30 * time.Second, WakeThreshold: 30 * time.Second}
	if lc != want {
		t.Fatalf("defaults = %+v, want %+v", lc, want)
	}
}

func TestConfig_levelsTableParsesEveryKey(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[levels]
observed_at_skew = "7m"
grace_factor = 4
start_wait = "11s"
start_stagger = "31s"
wake_threshold = "32s"
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := LevelsConfig{ObservedAtSkew: 7 * time.Minute, GraceFactor: 4, StartWait: 11 * time.Second, StartStagger: 31 * time.Second, WakeThreshold: 32 * time.Second}
	if c.Levels != want {
		t.Fatalf("Levels = %+v, want %+v", c.Levels, want)
	}
}

func TestConfig_levelsTableRejectsUnknownKeyAndBadValue(t *testing.T) {
	absentGlobalConfig(t)
	for body, want := range map[string]string{
		"[levels]\nstart_wiat = \"10s\"\n":                               "unknown key",
		"[levels]\nstart_wait = \"soon\"\n":                              "start_wait",
		"[levels]\nobserved_at_skew = \"0s\"\n":                          "observed_at_skew",
		"[levels]\ngrace_factor = 0\n":                                   "grace_factor",
		"[levels]\nwake_threshold = \"-1s\"\n":                           "wake_threshold",
		"[levels]\ngrace_factor = \"two\"\n":                             "grace_factor",
		"[[resource]]\nname = \"r\"\nbogus = 1\nscale = [\"A\",\"B\"]\n": "unknown key",
	} {
		writeCfg(t, body)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: Load() = %v, want error containing %q", body, err, want)
		}
	}
}

func TestRegistry_resourcesLoadAndDefaults(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, levelsCfgBase)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	disk, ok := c.Resources.Find("disk")
	if !ok || disk.Ignored != IgnoredAny || disk.Unknown != UnknownBest || disk.GraceFactor != 3 || disk.Sensor != "host" {
		t.Errorf("disk = %+v (grace must default to [levels].grace_factor)", disk)
	}
	git, _ := c.Resources.Find("git_auth_x")
	if git.UnknownLevel() != "UNAVAILABLE" {
		t.Errorf("credential unknown level = %q, want the worst", git.UnknownLevel())
	}
	reg := c.Resources.Registry()
	if reg["disk"].Unknown != "PLENTY" || reg["git_auth_x"].Unknown != "UNAVAILABLE" || len(reg["router"].Levels) != 2 {
		t.Errorf("levels.Registry = %+v", reg)
	}
}

func TestRegistry_resourceValidation(t *testing.T) {
	absentGlobalConfig(t)
	for body, want := range map[string]string{
		"[[resource]]\nscale = [\"A\",\"B\"]\n":                                                                  "name is required",
		"[[resource]]\nname = \"r\"\nscale = [\"A\"]\n":                                                          "at least two",
		"[[resource]]\nname = \"r\"\nscale = [\"A\",\"A\"]\n":                                                    "repeated",
		"[[resource]]\nname = \"r\"\nscale = [\"A\",\"B\"]\nunknown = \"mid\"\n":                                 "unknown must be",
		"[[resource]]\nname = \"r\"\nscale = [\"A\",\"B\"]\nignored = \"== Z\"\n":                                "ignored",
		"[[resource]]\nname = \"r\"\nscale = [\"A\",\"B\"]\nignored = \"> A\"\n":                                 "allows no level",
		"[[resource]]\nname = \"r\"\nscale = [\"A\",\"B\"]\ngrace_factor = 0\n":                                  "grace_factor",
		"[[resource]]\nname = \"r\"\nscale = [\"A\",\"B\"]\n[[resource]]\nname = \"r\"\nscale = [\"A\",\"B\"]\n": "duplicate",
	} {
		writeCfg(t, body)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: Load() = %v, want error containing %q", body, err, want)
		}
	}
}

func TestConfig_queryDeadline(t *testing.T) {
	absentGlobalConfig(t)
	const consumer = "\n[[role]]\nname = \"s\"\nhandler = \"/bin/h\"\nbinds = [\"e\"]\n"
	slow := func(deadline string) string {
		return "\n[[query]]\nname = \"slow\"\ntype = \"command\"\nemits = [\"e\"]\n" + deadline + "\n[query.command]\nargv = [\"x\"]\nformat = \"jsonl\"\n"
	}
	writeCfg(t, levelsCfgBase+levelsWorkQuery+slow(`deadline = "45s"`)+consumer)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if query.DefaultDeadline != 20*time.Second || c.Queries[1].Deadline != query.DefaultDeadline {
		t.Errorf("default deadline = %s (DefaultDeadline %s)", c.Queries[1].Deadline, query.DefaultDeadline)
	}
	if c.Queries[2].Deadline != 45*time.Second {
		t.Errorf("deadline = %s, want 45s", c.Queries[2].Deadline)
	}
	writeCfg(t, levelsCfgBase+slow(`deadline = "0s"`)+consumer)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("zero deadline: %v", err)
	}
}

// Ports of the gate config tests named in the disposition list: the timer
// query stays level-immune, and the retired path keys are still ignored.
func TestConfig_timerQueryTypeIsLevelImmune(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, levelsCfgBase+`
[[query]]
name = "tick"
type = "timer"
emits = ["timer.5m"]
trigger = { kind = "period", every = "5m" }

[[role]]
name = "ticker"
handler = "/bin/h"
binds = ["timer.5m"]
requires = ["disk >= PLENTY"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p := c.PollingUnion()["tick"]; !p.Polled || !p.Exempt {
		t.Errorf("the timer must never be gated by a requirement: %+v", p)
	}
}

func TestConfig_retiredKeysAreIgnoredAlongsideLevels(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[pool]
operator_paused_path = "/x/op"
`+levelsCfgBase)
	if _, err := Load(); err != nil {
		t.Fatalf("Load() = %v, want the retired keys ignored", err)
	}
}
