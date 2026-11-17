package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/supervisor"
	"github.com/phillipgreenii/x/cmdrun"
)

// The daemon tests run the whole daemon subcommand in a testing/synctest
// bubble: real files in a temp state dir, tmux and pg-claude-control answered
// by the cmdruntest.Fake (fakeTmux), the shutdown signal replaced by the test's
// context and the watchdog's hard exit recorded.

// daemonFixture is a temp state dir, an existing cwd for example-a, a config
// and a test CLI answering the tools from srv.
type daemonFixture struct {
	*testCLI
	dir, stateDir, cwdA, claudeJSON, configPath string
	srv                                         *fakeTmux
}

// newDaemonFixture writes a config with example-a (whose cwd exists) plus
// extra, which is prepended to the file (global keys) when it does not start
// with "\n[[assistant]]", appended otherwise.
func newDaemonFixture(t *testing.T, srv *fakeTmux, global string, more ...string) *daemonFixture {
	t.Helper()
	dir := t.TempDir()
	f := &daemonFixture{
		testCLI:    newTestCLI(t),
		dir:        dir,
		stateDir:   filepath.Join(dir, "state", "pg-assistants"),
		cwdA:       mkdir(t, filepath.Join(dir, "work", "a")),
		claudeJSON: filepath.Join(dir, ".claude.json"),
		srv:        srv,
	}
	body := global + pathsTOML(f.stateDir, f.claudeJSON) + assistantTOML("example-a", f.cwdA) + strings.Join(more, "")
	f.configPath = tempConfig(t, body)
	answerTools(f.testCLI, srv)
	return f
}

// start runs the daemon in its own goroutine; the result arrives on the channel.
func (f *daemonFixture) start(ctx context.Context, args ...string) <-chan int {
	done := make(chan int, 1)
	go func() { done <- f.main(ctx, append([]string{"daemon", "--config", f.configPath}, args...)) }()
	return done
}

func (f *daemonFixture) path(name string) string { return filepath.Join(f.stateDir, name) }

func (f *daemonFixture) status(t *testing.T) supervisor.Status {
	t.Helper()
	st, err := supervisor.ReadStatus(f.path("status.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func assistantStatus(t *testing.T, st supervisor.Status, id string) supervisor.AssistantStatus {
	t.Helper()
	for _, a := range st.Assistants {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no status for %s in %+v", id, st.Assistants)
	return supervisor.AssistantStatus{}
}

// runBriefly starts the daemon, lets every loop settle, then cancels and
// waits for the exit code.
func (f *daemonFixture) runBriefly(t *testing.T, args ...string) int {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := f.start(ctx, args...)
	synctest.Wait()
	cancel()
	return <-done
}

// blockUsage makes every usage call block until its context is done, then
// wait after, and return the context's error.
func blockUsage(tc *testCLI, after time.Duration, sawCancel *atomic.Bool) {
	tc.hook = func(ctx context.Context, c cmdrun.Call) (cmdrun.Result, bool) {
		if c.Name != controlBin || c.Args[0] != "usage" {
			return cmdrun.Result{}, false
		}
		<-ctx.Done()
		if sawCancel != nil && errors.Is(ctx.Err(), context.Canceled) {
			sawCancel.Store(true)
		}
		time.Sleep(after)
		return cmdrun.Result{Code: -1, CtxErr: ctx.Err()}, true
	}
}

func TestDaemonRejectsUnparseableConfig(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cases := map[string]string{
		"unparseable":   "tick = [oops\n" + pathsTOML(stateDir, "/example/home/.claude.json"),
		"relative path": strings.Replace(pathsTOML(stateDir, "/example/home/.claude.json"), tmuxBin, "tmux", 1),
		"no paths":      assistantTOML("example-a", "/example/work/a"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := runCLI(t, "daemon", "--config", tempConfig(t, body))
			if code != 7 {
				t.Fatalf("exit = %d, want 7 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, "pg-assistants daemon: ") {
				t.Errorf("stderr = %q, want the load error", stderr)
			}
		})
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused config created the state dir (%v)", err)
	}
	missing := filepath.Join(t.TempDir(), "absent.toml")
	if code, _, _ := runCLI(t, "daemon", "--config", missing); code != 7 {
		t.Errorf("a missing config file: exit = %d, want 7", code)
	}
}

func TestDaemonUsage(t *testing.T) {
	valid := tempConfig(t, validConfig)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no --config", []string{"daemon"}, 2},
		{"zero deadline", []string{"daemon", "--config", valid, "--shutdown-deadline", "0s"}, 2},
		{"stray argument", []string{"daemon", "--config", valid, "extra"}, 2},
		{"help", []string{"daemon", "--help"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, stdout, stderr := runCLI(t, c.args...); code != c.want {
				t.Errorf("exit = %d, want %d (stdout %q, stderr %q)", code, c.want, stdout, stderr)
			}
		})
	}
}

func TestDaemonCreatesPrivateStateDir(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDaemonFixture(t, newFakeTmux(), "")
		if code := f.runBriefly(t); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		fi, err := os.Stat(f.stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o700 {
			t.Errorf("state dir mode = %o, want 700", perm)
		}
		conf, err := os.ReadFile(f.path("tmux.conf"))
		if err != nil || string(conf) != supervisor.TmuxConfLine+"\n" {
			t.Errorf("tmux.conf = %q, %v; want %q", conf, err, supervisor.TmuxConfLine+"\n")
		}
		if _, err := os.Stat(f.path("events.jsonl")); err != nil {
			t.Errorf("no events log: %v", err)
		}
	})
}

func TestDaemonRunsWithOneBadAssistant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := newFakeTmux()
		bad := strings.Replace(assistantTOML("example-b", "/example/work/b"), `effort = "medium"`, `effort = "turbo"`, 1)
		f := newDaemonFixture(t, srv, "", bad)
		ctx, cancel := context.WithCancel(t.Context())
		done := f.start(ctx)
		synctest.Wait()

		starts := tmuxCalls(f.testCLI, "new-session")
		if len(starts) != 1 {
			t.Fatalf("%d new-session calls, want 1: %v", len(starts), starts)
		}
		s := starts[0]
		wantPrefix := []string{"-L", "pg-assistants", "-f", f.path("tmux.conf"), "new-session", "-d", "-s", "example-a", "-c", f.cwdA}
		if !slices.Equal(s.Args[:len(wantPrefix)], wantPrefix) {
			t.Errorf("new-session args start %q, want %q", s.Args[:len(wantPrefix)], wantPrefix)
		}
		if !slices.Contains(s.Args, "PG_ASSISTANTS_STATE_DIR="+f.stateDir) ||
			!slices.Contains(s.Args, "00000000-0000-4000-8000-000000000001") {
			t.Errorf("new-session args %q lack the state dir or the session id from NewID", s.Args)
		}
		// keyed by the resolved directory (a temporary directory may sit under a symlink)
		trusted, err := newTrustFile(f.claudeJSON).IsTrusted(config.ResolvePath(f.cwdA))
		if err != nil || !trusted {
			t.Errorf("example-a's cwd is not trusted in paths.claude_json: %v, %v", trusted, err)
		}
		cancel()
		if code := <-done; code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		st := f.status(t)
		if a := assistantStatus(t, st, "example-a"); !a.Running || a.SessionID != "00000000-0000-4000-8000-000000000001" {
			t.Errorf("example-a status = %+v, want running with the new session id", a)
		}
		if b := assistantStatus(t, st, "example-b"); !strings.Contains(b.ConfigError, "assistant[example-b].effort") {
			t.Errorf("example-b config_error = %q, want its effort problem", b.ConfigError)
		}
	})
}

func TestGlobalConfigProblemLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDaemonFixture(t, newFakeTmux(), "[grace]\nkill = \"0s\"\n")
		if code := f.runBriefly(t); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		recs := records(t, f.path("events.jsonl"))
		if _, ok := findEvent(recs, "config_problem", "field", "grace.kill"); !ok {
			t.Errorf("no config_problem event for grace.kill in %v", eventNames(recs))
		}
		probs := f.status(t).Daemon.ConfigProblems
		if len(probs) != 1 || !strings.HasPrefix(probs[0], "grace.kill: ") {
			t.Errorf("daemon config_problems = %q, want the grace.kill problem", probs)
		}
	})
}

func TestDaemonStatusNamesItsEnvironment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDaemonFixture(t, newFakeTmux(), "")
		f.vars["PG_CLAUDE_CONTROL_STATE_DIR"] = "/example/state/pg-claude-control"
		f.vars["CLAUDE_CONFIG_DIR"] = "/example/home/.claude-example"
		started := time.Now()
		if code := f.runBriefly(t); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		data, err := os.ReadFile(f.configPath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		want := supervisor.DaemonStatus{
			PID: testPID, Version: Version, StartedAt: started, ConfigPath: f.configPath,
			ConfigHash: hex.EncodeToString(sum[:]), ControlStateDir: "/example/state/pg-claude-control",
			ClaudeConfigDir: "/example/home/.claude-example",
		}
		got := f.status(t).Daemon
		if !got.StartedAt.Equal(want.StartedAt) {
			t.Errorf("started_at = %v, want %v", got.StartedAt, want.StartedAt)
		}
		got.StartedAt = want.StartedAt
		if !slices.Equal(got.ConfigProblems, want.ConfigProblems) || got.PID != want.PID || got.Version != want.Version ||
			got.ConfigPath != want.ConfigPath || got.ConfigHash != want.ConfigHash ||
			got.ControlStateDir != want.ControlStateDir || got.ClaudeConfigDir != want.ClaudeConfigDir {
			t.Errorf("daemon status = %+v, want %+v", got, want)
		}
		recs := records(t, f.path("events.jsonl"))
		if _, ok := findEvent(recs, "daemon_start",
			"pg_claude_control_state_dir", "/example/state/pg-claude-control",
			"claude_config_dir", "/example/home/.claude-example",
			"config_hash", want.ConfigHash, "pid", "4242"); !ok {
			t.Errorf("no daemon_start event naming the environment in %v", recs)
		}
	})
}

func TestDaemonSeedsBlocksFromStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		f := newDaemonFixture(t, newFakeTmux(), "")
		mkdir(t, f.stateDir)
		reset := now.Add(2 * time.Hour)
		prev := supervisor.Status{
			SchemaVersion: supervisor.SchemaVersion, WrittenAt: now.Add(-time.Minute),
			Blocks: []control.Limit{{Kind: control.KindFiveHour, Fraction: 0.97, ResetsAt: reset, ObservedAt: now.Add(-time.Minute)}},
		}
		data, err := json.Marshal(prev)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path("status.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if code := f.runBriefly(t); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		if n := len(tmuxCalls(f.testCLI, "new-session")); n != 0 {
			t.Errorf("%d new-session calls while the seeded block holds starts, want 0", n)
		}
		st := f.status(t)
		if a := assistantStatus(t, st, "example-a"); a.WaitReason != "block_hold" || !a.WaitUntil.Equal(reset) {
			t.Errorf("example-a wait = %q until %v, want block_hold until %v", a.WaitReason, a.WaitUntil, reset)
		}
		if len(st.Blocks) != 1 || st.Blocks[0].Kind != control.KindFiveHour {
			t.Errorf("blocks = %+v, want the seeded five_hour reading", st.Blocks)
		}
	})
}

// Only an assistant loop's tick publishes the block readings, so with no
// valid assistant the seeded hold reaches the new status file only because
// the daemon puts the seeded readings on the board itself; otherwise the next
// restart would lose the hold.
func TestDaemonKeepsSeededBlocksWithNoValidAssistant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		f := newDaemonFixture(t, newFakeTmux(), "")
		bad := strings.Replace(assistantTOML("example-a", f.cwdA), `effort = "medium"`, `effort = "turbo"`, 1)
		f.configPath = tempConfig(t, pathsTOML(f.stateDir, f.claudeJSON)+bad)
		mkdir(t, f.stateDir)
		reset := now.Add(2 * time.Hour)
		seeded := control.Limit{Kind: control.KindFiveHour, Fraction: 0.97, ResetsAt: reset, ObservedAt: now.Add(-time.Minute)}
		data, err := json.Marshal(supervisor.Status{
			SchemaVersion: supervisor.SchemaVersion, WrittenAt: now.Add(-time.Minute), Blocks: []control.Limit{seeded},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path("status.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if code := f.runBriefly(t); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		st := f.status(t)
		if !st.WrittenAt.After(now.Add(-time.Minute)) {
			t.Fatalf("status.json was not rewritten (written_at %v)", st.WrittenAt)
		}
		if len(st.Blocks) != 1 || st.Blocks[0].Kind != control.KindFiveHour || st.Blocks[0].Fraction != 0.97 ||
			!st.Blocks[0].ResetsAt.Equal(reset) {
			t.Errorf("blocks = %+v, want the seeded five_hour reading kept", st.Blocks)
		}
		if a := assistantStatus(t, st, "example-a"); a.ConfigError == "" {
			t.Errorf("example-a = %+v, want its config error", a)
		}
	})
}

func TestDaemonIgnoresUnreadablePreviousStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDaemonFixture(t, newFakeTmux(), "")
		mkdir(t, f.stateDir)
		if err := os.WriteFile(f.path("status.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := f.runBriefly(t); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		if n := len(tmuxCalls(f.testCLI, "new-session")); n != 1 {
			t.Errorf("%d new-session calls, want 1: an unreadable status seeds nothing", n)
		}
	})
}

func TestDaemonStopsOnContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDaemonFixture(t, newFakeTmux("example-a"), "") // adopted at the first tick
		var sawCancel atomic.Bool
		blockUsage(f.testCLI, 0, &sawCancel)
		ctx, cancel := context.WithCancel(t.Context())
		done := f.start(ctx)
		synctest.Wait() // example-a's loop is in its usage call
		began := time.Now()
		cancel()
		if code := <-done; code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		if took := time.Since(began); took >= 8*time.Second {
			t.Errorf("shutdown took %v, want under the 8s deadline", took)
		}
		if !sawCancel.Load() {
			t.Errorf("the in-flight usage call never saw its context cancelled")
		}
		if n := len(tmuxCalls(f.testCLI, "kill-session")); n != 0 {
			t.Errorf("%d kill-session calls on shutdown, want 0: sessions keep running", n)
		}
		if exits := f.hardExits(); len(exits) != 0 {
			t.Errorf("hard exits %v, want none", exits)
		}
		recs := records(t, f.path("events.jsonl"))
		for _, want := range [][]string{
			{"shutdown_signal"},
			{"shutdown_complete"},
			{"loop_stopped", "loop", "assistant:example-a"},
			{"shutdown_phase", "phase", "stop_loops"},
		} {
			if _, ok := findEvent(recs, want[0], want[1:]...); !ok {
				t.Errorf("no %v event in %v", want, eventNames(recs))
			}
		}
	})
}

// The heartbeat returns with the context, so a loop's last tick, which ends
// after it, reaches the file only through the daemon's final flush.
func TestDaemonFlushesStatusAfterLoopsReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDaemonFixture(t, newFakeTmux("example-a"), "")
		blockUsage(f.testCLI, time.Second, nil) // the cancelled call takes a second to end
		ctx, cancel := context.WithCancel(t.Context())
		done := f.start(ctx)
		synctest.Wait()
		if _, err := os.Stat(f.path("status.json")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("status.json before the first tick ended: %v", err)
		}
		cancel()
		if code := <-done; code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, f.errb.String())
		}
		a := assistantStatus(t, f.status(t), "example-a")
		if !a.Running || a.LastTick.IsZero() || a.PID != 5000 {
			t.Errorf("example-a status = %+v, want its last tick (running, pid 5000)", a)
		}
		recs := records(t, f.path("events.jsonl"))
		names := eventNames(recs)
		complete := slices.Index(names, "shutdown_complete")
		flush := slices.IndexFunc(recs, func(r map[string]any) bool {
			return r["event"] == "shutdown_phase" && r["phase"] == "flush_status"
		})
		if complete < 0 || flush < complete {
			t.Errorf("events %v: want the flush_status phase after shutdown_complete (the loops have returned)", names)
		}
	})
}

func TestDaemonShutdownOverrunExitsWithShutdownOverrun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDaemonFixture(t, newFakeTmux("example-a"), "")
		release := make(chan struct{})
		f.hook = func(ctx context.Context, c cmdrun.Call) (cmdrun.Result, bool) {
			if c.Name != controlBin || c.Args[0] != "usage" {
				return cmdrun.Result{}, false
			}
			<-release // a call that ignores its context
			return cmdrun.Result{Code: -1, CtxErr: context.Canceled}, true
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := f.start(ctx, "--shutdown-deadline", "2s")
		synctest.Wait()
		cancel()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if exits := f.hardExits(); !slices.Equal(exits, []int{10}) {
			t.Errorf("hard exits = %v, want [10] (exitcode.ShutdownOverrun) after the 2s deadline", exits)
		}
		close(release)
		<-done
		recs := records(t, f.path("events.jsonl"))
		r, ok := findEvent(recs, "shutdown_overrun", "phase", "stop_loops", "deadline_ms", "2000")
		if !ok || !strings.Contains(r["in_flight"].(string), "assistant:example-a") {
			t.Errorf("shutdown_overrun = %v (found %v), want phase stop_loops naming assistant:example-a", r, ok)
		}
	})
}

func TestNewUUIDIsVersion4(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 100 {
		id := newUUID()
		if !re.MatchString(id) {
			t.Fatalf("newUUID() = %q, not a version 4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("newUUID() repeated %q", id)
		}
		seen[id] = true
	}
}
