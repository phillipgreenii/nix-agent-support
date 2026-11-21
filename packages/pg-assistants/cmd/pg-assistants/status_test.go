package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/supervisor"
)

// The status tests run in a testing/synctest bubble, so time.Now is the
// bubble's clock and the file's times are exact offsets from it.

// statusFixture writes a config naming a temp state dir and returns its path
// and the state dir.
func statusFixture(t *testing.T, assistants ...string) (configPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	stateDir = mkdir(t, filepath.Join(dir, "state"))
	if len(assistants) == 0 {
		assistants = []string{assistantTOML("example-a", "/example/work/a")}
	}
	return tempConfig(t, pathsTOML(stateDir, filepath.Join(dir, ".claude.json"))+strings.Join(assistants, "")), stateDir
}

func writeStatusFile(t *testing.T, stateDir string, st supervisor.Status) []byte {
	t.Helper()
	st.SchemaVersion = supervisor.SchemaVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "status.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

// healthy is a fresh status file with one running, healthy assistant.
func healthy(now time.Time) supervisor.Status {
	return supervisor.Status{
		WrittenAt: now,
		Daemon: supervisor.DaemonStatus{
			PID: testPID, Version: "dev", StartedAt: now.Add(-2 * time.Hour),
			ConfigPath: "/example/config.toml", ConfigHash: "abcdef0123456789abcdef0123456789",
		},
		Assistants: []supervisor.AssistantStatus{{
			ID: "example-a", Enabled: true, Running: true, PID: 5000, StartedAt: now.Add(-time.Hour),
			ContextTokens: 1000, ContextFraction: 0.005, ContextAt: now, LastTick: now,
		}},
	}
}

func runStatus(t *testing.T, configPath string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	tc := newTestCLI(t)
	code = tc.main(t.Context(), append([]string{"status", "--config", configPath}, args...))
	return code, tc.out.String(), tc.errb.String()
}

// row returns the table line for id.
func row(t *testing.T, out, id string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == id {
			return l
		}
	}
	t.Fatalf("no row for %s in:\n%s", id, out)
	return ""
}

func TestStatusTable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		bad := strings.Replace(assistantTOML("example-b", "/example/work/b"), `effort = "medium"`, `effort = "turbo"`, 1)
		configPath, stateDir := statusFixture(t,
			assistantTOML("example-a", "/example/work/a"), bad, assistantTOML("example-c", "/example/work/c"))
		st := healthy(now)
		st.Daemon.ConfigProblems = []string{"grace.kill: must be positive, got 0s"}
		st.Assistants = []supervisor.AssistantStatus{
			{
				ID: "example-a", Enabled: true, Running: true, PID: 5000, ContextTokens: 162000,
				ContextFraction: 0.81, ContextAt: now, Ladder: "meter", Rung: "nudge", LastTick: now,
			},
			// A config error has no loop, so no last tick: it is not stalled.
			{ID: "example-b", Enabled: true, ConfigError: "assistant[example-b].effort: must be one of low, medium, high, xhigh, max"},
			// tick 1m + grace.kill 2m + 45s = 3m45s; ten minutes is past it.
			{ID: "example-c", Enabled: true, Running: true, PID: 5001, LastTick: now.Add(-10 * time.Minute)},
		}
		st.Orphans = []string{"example-gone"}
		st.Blocks = []control.Limit{{Kind: "five_hour", Fraction: 0.96, ResetsAt: now.Add(2 * time.Hour), ObservedAt: now}}
		writeStatusFile(t, stateDir, st)

		code, stdout, stderr := runStatus(t, configPath)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing for a fresh file", stderr)
		}
		for _, want := range []string{
			"daemon pid 4242, up 2h00m, config abcdef012345\n",
			"config problem: grace.kill: must be positive, got 0s\n",
			"five_hour 96% (starts held until ",
			"ID ", "ENABLED", "RUNNING", "CONTEXT", "LADDER", "WAIT", "NOTE",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q:\n%s", want, stdout)
			}
		}
		a := row(t, stdout, "example-a")
		for _, want := range []string{"yes", "81% (162k)", "meter/nudge"} {
			if !strings.Contains(a, want) {
				t.Errorf("example-a row %q lacks %q", a, want)
			}
		}
		if b := row(t, stdout, "example-b"); !strings.Contains(b, "config error: assistant[example-b].effort") || strings.Contains(b, "stalled") {
			t.Errorf("example-b row = %q, want the config error and not stalled", b)
		}
		if c := row(t, stdout, "example-c"); !strings.Contains(c, "stalled") {
			t.Errorf("example-c row = %q, want stalled", c)
		}
		if g := row(t, stdout, "example-gone"); !strings.Contains(g, "not in config") {
			t.Errorf("orphan row = %q, want not in config", g)
		}
	})
}

func TestStatusShowsHoldReason(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		configPath, stateDir := statusFixture(t)
		st := healthy(now)
		st.Assistants = []supervisor.AssistantStatus{{
			ID: "example-a", Enabled: true, WaitReason: "block_hold", WaitUntil: now.Add(2 * time.Hour), LastTick: now,
		}}
		writeStatusFile(t, stateDir, st)
		_, stdout, _ := runStatus(t, configPath)
		want := "block_hold until " + clock(now.Add(2*time.Hour), now)
		if a := row(t, stdout, "example-a"); !strings.Contains(a, want) {
			t.Errorf("example-a row = %q, want WAIT %q", a, want)
		}
	})
}

func TestStatusContextAge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		configPath, stateDir := statusFixture(t)
		st := healthy(now)
		st.Assistants[0].ContextFraction, st.Assistants[0].ContextTokens = 0.5, 100000
		st.Assistants[0].ContextAt = now.Add(-2 * time.Minute) // two ticks: not old yet
		writeStatusFile(t, stateDir, st)
		if _, stdout, _ := runStatus(t, configPath); strings.Contains(row(t, stdout, "example-a"), "ago") {
			t.Errorf("a reading two ticks old shows an age: %q", row(t, stdout, "example-a"))
		}
		st.Assistants[0].ContextAt = now.Add(-10 * time.Minute)
		writeStatusFile(t, stateDir, st)
		if _, stdout, _ := runStatus(t, configPath); !strings.Contains(row(t, stdout, "example-a"), "50% (100k, 10m ago)") {
			t.Errorf("a stale reading shows no age: %q", row(t, stdout, "example-a"))
		}
	})
}

func TestStatusNotePriority(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	exit := &supervisor.LastExit{Status: 1, UptimeS: 12}
	stalledTick := now.Add(-time.Hour)
	cases := []struct {
		name string
		a    supervisor.AssistantStatus
		want string
	}{
		{"config error over stalled", supervisor.AssistantStatus{ConfigError: "x: bad", LastTick: stalledTick}, "config error: x: bad"},
		{"stalled over hint", supervisor.AssistantStatus{Hint: "h", LastTick: stalledTick}, "stalled (last tick 1h00m ago)"},
		{"stalled keeps the last error", supervisor.AssistantStatus{Hint: "h", LastError: "usage: no reply", LastTick: stalledTick}, "stalled (last tick 1h00m ago): usage: no reply"},
		// A loop that panicked on its first tick published a zero last tick.
		{"stalled before any tick", supervisor.AssistantStatus{LastError: "panic: boom (loop restarts in 4m0s)"}, "stalled (no tick yet): panic: boom (loop restarts in 4m0s)"},
		{"stalled before any tick, no error", supervisor.AssistantStatus{}, "stalled (no tick yet)"},
		{"idle over hint and last error", supervisor.AssistantStatus{IdleSince: now.Add(-12 * time.Minute), Hint: "h", LastError: "e", LastTick: now}, "IDLE 12m: h"},
		{"idle alone", supervisor.AssistantStatus{IdleSince: now.Add(-3 * time.Minute), LastTick: now}, "IDLE 3m"},
		{"stalled over idle", supervisor.AssistantStatus{IdleSince: now.Add(-3 * time.Minute), LastTick: stalledTick}, "stalled (last tick 1h00m ago)"},
		{"hint over last error", supervisor.AssistantStatus{Hint: "h", LastError: "e", LastTick: now}, "h"},
		{"last error over last exit", supervisor.AssistantStatus{LastError: "e", LastExit: exit, LastTick: now}, "e"},
		{"last exit", supervisor.AssistantStatus{LastExit: exit, LastTick: now}, "last exit: status 1 after 12s"},
		{"signal", supervisor.AssistantStatus{LastExit: &supervisor.LastExit{Signal: 15, UptimeS: 90}, LastTick: now}, "last exit: signal 15 after 1m"},
		{"nothing", supervisor.AssistantStatus{LastTick: now}, "-"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stalled := isStalled(c.a, time.Minute, 2*time.Minute, time.Time{}, now)
			if got := note(c.a, stalled, now); got != c.want {
				t.Errorf("note = %q, want %q", got, c.want)
			}
		})
	}
	// isStalled never calls a config error stalled; note also puts the config
	// error first on its own.
	if got := note(supervisor.AssistantStatus{ConfigError: "x: bad"}, true, now); got != "config error: x: bad" {
		t.Errorf("note(config error, stalled) = %q, want the config error", got)
	}
	if isStalled(supervisor.AssistantStatus{ConfigError: "x: bad"}, time.Minute, 2*time.Minute, time.Time{}, now) {
		t.Errorf("isStalled reports an assistant with a config error (no loop, no last tick) stalled")
	}
}

// A loop that panics on its first tick publishes its status with a zero last
// tick and the panic in last_error. Once its restart backoff outlasts tick +
// grace.kill + 45s it is stalled, and the panic MUST still show in its note.
func TestStatusPanickedFirstTickShowsTheError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		configPath, stateDir := statusFixture(t,
			assistantTOML("example-a", "/example/work/a"), assistantTOML("example-c", "/example/work/c"))
		st := healthy(now)
		st.Daemon.StartedAt = now.Add(-10 * time.Minute)
		st.Assistants = []supervisor.AssistantStatus{
			{ID: "example-a", Enabled: true, LastError: "panic: boom (loop restarts in 4m0s)"},
			{ID: "example-c", Enabled: true, Running: true, PID: 5001, LastError: "usage: no reply", LastTick: now.Add(-10 * time.Minute)},
		}
		writeStatusFile(t, stateDir, st)

		code, stdout, stderr := runStatus(t, configPath)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
		}
		if a, want := row(t, stdout, "example-a"), "stalled (no tick yet): panic: boom (loop restarts in 4m0s)"; !strings.HasSuffix(a, want) {
			t.Errorf("example-a row = %q, want NOTE %q", a, want)
		}
		if c, want := row(t, stdout, "example-c"), "stalled (last tick 10m ago): usage: no reply"; !strings.HasSuffix(c, want) {
			t.Errorf("example-c row = %q, want NOTE %q", c, want)
		}
		if strings.Contains(stdout, "106751d") {
			t.Errorf("a zero last tick rendered as a saturated age:\n%s", stdout)
		}

		code, stdout, _ = runStatus(t, configPath, "--check")
		if want := "degraded: example-a stalled; example-c stalled\n"; code != 5 || stdout != want {
			t.Errorf("status --check = %d, %q; want 5, %q", code, stdout, want)
		}
	})
}

func TestStatusStaleWarnsAndExitsZero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		configPath, stateDir := statusFixture(t)
		writeStatusFile(t, stateDir, healthy(now.Add(-2*time.Minute)))
		code, stdout, stderr := runStatus(t, configPath)
		if code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if want := "daemon pid 4242, up 2h00m when last written, status file 2m old, config abcdef012345\n"; !strings.Contains(stdout, want) {
			t.Errorf("a stale file's header lacks %q:\n%s", want, stdout)
		}
		assertNamesLaunchd(t, stderr)
		if !strings.Contains(stderr, "2m old") {
			t.Errorf("stderr = %q, want the file's age", stderr)
		}
	})
}

func TestStatusMissingNamesLaunchdLog(t *testing.T) {
	configPath, stateDir := statusFixture(t)
	code, stdout, stderr := runStatus(t, configPath)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	assertNamesLaunchd(t, stderr)
	if !strings.Contains(stderr, filepath.Join(stateDir, "status.json")) {
		t.Errorf("stderr = %q, want the missing file's path", stderr)
	}
}

func assertNamesLaunchd(t *testing.T, stderr string) {
	t.Helper()
	for _, want := range []string{
		"launchctl print gui/",
		"/com.phillipg.pg-assistants-daemon",
		filepath.Join("pg-assistants-launchd", "launchd-stderr.log"),
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to name %q", stderr, want)
		}
	}
}

func TestStatusCheckExitCodes(t *testing.T) {
	type edit func(st *supervisor.Status, now time.Time)
	cases := []struct {
		name   string
		write  bool
		raw    string // written instead of a status when set
		edit   edit
		want   int
		prefix string
	}{
		{"missing", false, "", nil, 3, "down: "},
		{"unparseable", true, "{not json", nil, 3, "down: "},
		{"stale", true, "", func(st *supervisor.Status, now time.Time) { st.WrittenAt = now.Add(-61 * time.Second) }, 3, "down: "},
		{"healthy", true, "", nil, 0, "ok: "},
		{"block hold only", true, "", func(st *supervisor.Status, now time.Time) {
			st.Assistants[0].Running, st.Assistants[0].WaitReason, st.Assistants[0].WaitUntil = false, "block_hold", now.Add(time.Hour)
		}, 0, "ok: "},
		{"config error", true, "", func(st *supervisor.Status, _ time.Time) {
			st.Assistants = append(st.Assistants, supervisor.AssistantStatus{ID: "example-b", ConfigError: "x: bad"})
		}, 5, "degraded: "},
		{"config problem", true, "", func(st *supervisor.Status, _ time.Time) {
			st.Daemon.ConfigProblems = []string{"grace.kill: bad"}
		}, 5, "degraded: "},
		{"hint", true, "", func(st *supervisor.Status, _ time.Time) { st.Assistants[0].Hint = "did not exit after the nudge" }, 5, "degraded: "},
		{"stalled", true, "", func(st *supervisor.Status, now time.Time) { st.Assistants[0].LastTick = now.Add(-4 * time.Minute) }, 5, "degraded: "},
		{"backoff", true, "", func(st *supervisor.Status, now time.Time) {
			st.Assistants[0].Running, st.Assistants[0].WaitReason, st.Assistants[0].WaitUntil = false, "backoff", now.Add(time.Minute)
		}, 5, "degraded: "},
		{"orphan", true, "", func(st *supervisor.Status, _ time.Time) { st.Orphans = []string{"example-gone"} }, 5, "degraded: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				configPath, stateDir := statusFixture(t)
				switch {
				case c.raw != "":
					if err := os.WriteFile(filepath.Join(stateDir, "status.json"), []byte(c.raw), 0o600); err != nil {
						t.Fatal(err)
					}
				case c.write:
					st := healthy(now)
					if c.edit != nil {
						c.edit(&st, now)
					}
					writeStatusFile(t, stateDir, st)
				}
				code, stdout, stderr := runStatus(t, configPath, "--check")
				if code != c.want {
					t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", code, c.want, stdout, stderr)
				}
				if strings.Count(stdout, "\n") != 1 || !strings.HasPrefix(stdout, c.prefix) {
					t.Errorf("stdout = %q, want one line starting %q", stdout, c.prefix)
				}
				if c.want == 3 && !strings.Contains(stdout, "launchctl print gui/") {
					t.Errorf("stdout = %q, want the launchctl command", stdout)
				}
			})
		})
	}
}

func TestStatusJSONPrintsTheFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		configPath, stateDir := statusFixture(t)
		data := writeStatusFile(t, stateDir, healthy(time.Now()))
		code, stdout, _ := runStatus(t, configPath, "--json")
		if code != 0 || stdout != string(data) {
			t.Errorf("status --json = %d,\n%s\nwant 0 and the file:\n%s", code, stdout, data)
		}
	})
}

func TestStatusDefaultConfigPath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	stateDir := filepath.Join(t.TempDir(), "state-from-default")
	mkdir(t, filepath.Join(xdg, "pg-assistants"))
	body := pathsTOML(stateDir, filepath.Join(xdg, ".claude.json")) + assistantTOML("example-a", "/example/work/a")
	if err := os.WriteFile(filepath.Join(xdg, "pg-assistants", "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI(t, "status", "--check")
	if code != 3 || !strings.Contains(stdout, stateDir) {
		t.Errorf("status --check = %d, %q; want 3 naming %s from the default config", code, stdout, stateDir)
	}
}

func TestStatusUsageAndConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.toml")
	if code, _, stderr := runCLI(t, "status", "--config", missing); code != 7 || stderr == "" {
		t.Errorf("status with a missing config = %d, %q; want 7 and a message", code, stderr)
	}
	if code, _, _ := runCLI(t, "status", "extra"); code != 2 {
		t.Errorf("status extra = %d, want 2", code)
	}
	if code, stdout, _ := runCLI(t, "status", "--help"); code != 0 || !strings.Contains(stdout, "--check") {
		t.Errorf("status --help = %d, %q; want 0 and the help", code, stdout)
	}
}

// An assistant with a config error shows as running when its session runs on
// as an orphan, and an orphan with a row of its own is not listed twice.
func TestStatusOrphanRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		configPath, stateDir := statusFixture(t)
		st := healthy(now)
		st.Assistants = []supervisor.AssistantStatus{
			{ID: "example-b", Enabled: true, ConfigError: "assistant[example-b].effort: bad"},
		}
		st.Orphans = []string{"example-b", "example-gone", "example-gone"}
		writeStatusFile(t, stateDir, st)
		code, stdout, stderr := runStatus(t, configPath)
		if code != 0 {
			t.Fatalf("exit = %d (stderr %q)", code, stderr)
		}
		if b := strings.Fields(row(t, stdout, "example-b")); len(b) < 3 || b[2] != "yes" {
			t.Errorf("config-error assistant with an orphan session: row %q, want RUNNING yes", row(t, stdout, "example-b"))
		}
		if n := strings.Count(stdout, "example-b"); n != 2 { // the row, and the config error text
			t.Errorf("example-b appears %d times, want the row and its config error only:\n%s", n, stdout)
		}
		if n := strings.Count(stdout, "example-gone\t") + strings.Count(stdout, "example-gone "); n != 1 {
			t.Errorf("orphan example-gone listed %d times, want once:\n%s", n, stdout)
		}
	})
}
