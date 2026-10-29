package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/collector"
	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
)

func TestKillLimit(t *testing.T) {
	cases := []struct {
		name string
		flag int
		cap  int
		want int
	}{
		{"no flag and no recorded cap is the 1,500 default", 0, 0, 1500},
		{"no flag follows the manifest cap", 0, 3000, 3000},
		{"no flag follows a lowered manifest cap", 0, 800, 800},
		{"an explicit flag wins over a raised cap", 2000, 3000, 2000},
		{"an explicit flag wins over the default", 700, 0, 700},
	}
	for _, c := range cases {
		m := scratch.Manifest{DaemonGraphQLPointsPerHour: c.cap}
		if got := killLimit(c.flag, m); got != c.want {
			t.Errorf("%s: killLimit(%d, cap %d) = %d, want %d", c.name, c.flag, c.cap, got, c.want)
		}
	}
}

// oneTickSpending runs one collector tick whose stand-in client makes the
// daemon log a kind=origin row of `cost` points, under the kill ceiling that
// cmdRun would derive from flagValue and the manifest cap.
func oneTickSpending(t *testing.T, cost, flagValue, manifestCap int) error {
	t.Helper()
	root := t.TempDir()
	l := scratch.Layout{Root: root}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	live := t.TempDir()
	m := scratch.Manifest{
		Phase: "B", CreatedAt: "2026-01-05T09:00:00Z", BDMode: "hermetic", Consumer: "shadow-compare", Queries: []string{"mine"}, MaxPerPoll: 50,
		DaemonGraphQLPointsPerHour: manifestCap,
		Live: scratch.LiveSources{
			RouterEvents: filepath.Join(live, "events.jsonl"), RouterQueue: filepath.Join(live, "queue.jsonl"),
			RunRecord: filepath.Join(live, "run-record.log"), ConnectorEvents: filepath.Join(live, "conn.jsonl"),
		},
	}
	cfg := collector.Config{
		Layout: l, Manifest: m, Period: time.Minute, TickTimeout: time.Minute, MaxFailedTicks: 10, FloorMargin: 200,
		KillPointsPerHour: killLimit(flagValue, m), Log: io.Discard,
		ClientBin: filepath.Join(l.BinDir(), scratch.DaemonBinary), ClientConfig: l.DaemonConfig(),
		Exec: execFunc(func() (runner.Result, error) {
			row := fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"list","bucket":"graphql","graphql_cost":%d}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), cost)
			if err := os.WriteFile(l.ConnectorLog(), []byte(row), 0o600); err != nil {
				t.Fatal(err)
			}
			return runner.Result{Stdout: `{"protocolVersion":1,"schemaVersion":12,"result":{"sources":[{"backend":"b","status":"succeeded","version":0,"truncated":false,"next_seq":0}],"changes":[],"sources_freshness":[]}}`}, nil
		}),
	}
	c, err := collector.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	return err
}

// execFunc answers every client call with f's result.
type execFunc func() (runner.Result, error)

func (f execFunc) Run(context.Context, time.Duration, ...string) (runner.Result, error) { return f() }

func (f execFunc) RunStdin(context.Context, time.Duration, string, []string, ...string) (runner.Result, error) {
	return f()
}

func TestARaisedManifestCapIsNotKilledAt1500ButTheDefaultIs(t *testing.T) {
	if err := oneTickSpending(t, 1600, 0, 3000); err != nil {
		t.Fatalf("1,600 points under a raised cap of 3,000 must not kill: %v", err)
	}
	err := oneTickSpending(t, 1600, 0, 0)
	if !errors.Is(err, collector.ErrKill) || !strings.Contains(err.Error(), "exceeds 1500") {
		t.Fatalf("1,600 points with no recorded cap must kill at the 1,500 default, got %v", err)
	}
	err = oneTickSpending(t, 1600, 1000, 3000)
	if !errors.Is(err, collector.ErrKill) || !strings.Contains(err.Error(), "exceeds 1000") {
		t.Fatalf("an explicit --kill-points-per-hour still wins over the cap, got %v", err)
	}
}

func TestNewDaemonRefusesAScratchDirectoryPreparedWithoutOne(t *testing.T) {
	l := scratch.Layout{Root: t.TempDir()}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	_, _, err := newDaemon(l, scratch.Manifest{}, &runner.Runner{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "prepared without the daemon") {
		t.Fatalf("newDaemon = %v, want a clear refusal", err)
	}
}

func TestNewDaemonWiresTheDaemonLogAndTheScratchBinary(t *testing.T) {
	l := scratch.Layout{Root: t.TempDir()}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	bin := testutil.WriteScript(t, l.BinDir(), scratch.DaemonBinary, "exit 0\n")
	if err := os.WriteFile(l.DaemonConfig(), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &runner.Runner{}
	s, closeLog, err := newDaemon(l, scratch.Manifest{DaemonGraphQLPointsPerHour: 1500}, r, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	if s.Binary != bin || s.Config != l.DaemonConfig() || r.ChildOut == nil {
		t.Errorf("supervisor = %+v, ChildOut %v", s, r.ChildOut)
	}
	if _, err := os.Stat(l.DaemonLog()); err != nil {
		t.Errorf("the daemon log must be created: %v", err)
	}
}

// The whole `run` command, not only newDaemon: a scratch directory prepared
// without the daemon is refused with the exit code and the message an operator
// sees, before the collector or the daemon starts, and the refusal leaves the
// directory runnable (the lock is released, no daemon log was opened).
func TestCmdRunRefusesAScratchDirectoryPreparedWithoutTheDaemon(t *testing.T) {
	cases := map[string]struct {
		cap    int
		binary bool
		config bool
	}{
		"no recorded cap (prepared before the daemon existed)": {cap: 0, binary: true, config: true},
		"daemon binary link missing":                           {cap: 1500, binary: false, config: true},
		"daemon config missing":                                {cap: 1500, binary: true, config: false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			l := scratch.Layout{Root: safety.Resolve(t.TempDir())}
			if err := l.MkdirAll(); err != nil {
				t.Fatal(err)
			}
			// What prepare writes for every scratch directory: the pg-pr config
			// the safety check reads for --beads-dir words.
			if err := os.WriteFile(l.PRConfig(), []byte("connector: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.binary {
				testutil.WriteScript(t, l.BinDir(), scratch.DaemonBinary, "exit 0\n")
			}
			if c.config {
				if err := os.WriteFile(l.DaemonConfig(), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			home := t.TempDir()
			if err := l.Save(scratch.Manifest{
				Phase: "B", CreatedAt: "2026-01-05T09:00:00Z", BDMode: "hermetic", Consumer: "shadow-compare", Queries: []string{"mine"},
				Home: home, DaemonGraphQLPointsPerHour: c.cap,
			}); err != nil {
				t.Fatal(err)
			}
			for attempt := 1; attempt <= 2; attempt++ {
				var stdout, stderr strings.Builder
				code := run([]string{"run", "--scratch", l.Root, "--max-ticks", "1"}, &stdout, &stderr)
				if code != exitRefused {
					t.Fatalf("attempt %d: exit %d, want %d (refused)\nstdout: %s\nstderr: %s", attempt, code, exitRefused, stdout.String(), stderr.String())
				}
				for _, want := range []string{"pg-desk-shadow run: refusing to start: daemon:", "prepared without the daemon", "run prepare again"} {
					if !strings.Contains(stderr.String(), want) {
						t.Errorf("attempt %d: stderr %q lacks %q", attempt, stderr.String(), want)
					}
				}
			}
			if _, err := os.Stat(l.DaemonLog()); err == nil {
				t.Error("the daemon log was opened although the run was refused")
			}
			if _, err := os.Stat(l.TicksFile()); err == nil {
				t.Error("a tick was recorded although the run was refused")
			}
		})
	}
}

// The exit-code vocabulary is pinned: 1 is the generic, unexpected error and
// carries no branchable meaning; 2 is usage only; the specific outcomes sit at
// 3 and up (bead pg2-rfxl3y, docs/runbooks/pg-desk-shadow-compare.md).
func TestExitCodeVocabularyIsPinned(t *testing.T) {
	for name, c := range map[string]struct{ got, want int }{
		"exitOK":      {exitOK, 0},
		"exitError":   {exitError, 1},
		"exitUsage":   {exitUsage, 2},
		"exitRefused": {exitRefused, 3},
		"exitKill":    {exitKill, 4},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", name, c.got, c.want)
		}
	}
}

// A usage error (no command, an unknown command, a bad or missing flag, exec
// without a command) exits 2 and never touches a scratch directory.
func TestUsageErrorsExit2(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-prepared")
	for name, args := range map[string][]string{
		"no command":                 nil,
		"unknown command":            {"bogus"},
		"prepare without --scratch":  {"prepare"},
		"prepare with a bad points":  {"prepare", "--scratch", missing, "--daemon-points-per-hour", "0"},
		"run without --scratch":      {"run"},
		"run with an unknown flag":   {"run", "--nope"},
		"probe with an unknown flag": {"probe", "--nope"},
		"report without --scratch":   {"report"},
		"exec without a command":     {"exec", "--scratch", missing},
		"adapter-exercise bad flag":  {"adapter-exercise", "--nope"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := run(args, &stdout, &stderr); code != exitUsage {
				t.Errorf("run(%q) = %d, want %d (usage)\nstderr: %s", args, code, exitUsage, stderr.String())
			}
			if _, err := os.Stat(missing); err == nil {
				t.Errorf("run(%q) created the scratch directory on a usage error", args)
			}
		})
	}
}

// runExit is the collector-result mapping of `run`: a real KILL verdict from
// the collector exits 4 with a KILL line, any other Run error exits 1 (it is
// not a kill verdict), and a clean stop exits 0.
func TestRunExitMapsOnlyAKillVerdictToExitKill(t *testing.T) {
	// A real kill verdict: one tick spends 1,600 points under the 1,500 default.
	kill := oneTickSpending(t, 1600, 0, 0)
	if !errors.Is(kill, collector.ErrKill) {
		t.Fatalf("setup: the over-spend tick must trip a kill criterion, got %v", kill)
	}
	for name, c := range map[string]struct {
		err      error
		want     int
		wantLine string
		noLine   string
	}{
		"clean stop":          {err: nil, want: exitOK},
		"a real kill verdict": {err: kill, want: exitKill, wantLine: "pg-desk-shadow run: KILL: "},
		"a wrapped kill":      {err: fmt.Errorf("slot 3: %w", kill), want: exitKill, wantLine: "pg-desk-shadow run: KILL: "},
		"a non-kill error":    {err: errors.New("record the tick: no space left on device"), want: exitError, wantLine: "pg-desk-shadow run: record the tick", noLine: "KILL"},
		"a cancelled context": {err: context.Canceled, want: exitError, wantLine: "pg-desk-shadow run: ", noLine: "KILL"},
		"a wrapped deadline":  {err: fmt.Errorf("tick: %w", context.DeadlineExceeded), want: exitError, noLine: "KILL"},
		// The same text as collector.ErrKill is not the kill verdict: only the
		// sentinel itself (errors.Is) is.
		"a lookalike text":        {err: errors.New("kill criterion"), want: exitError, noLine: "KILL:"},
		"a scrubbed non-kill one": {err: errors.New("poll example-login failed"), want: exitError, noLine: "example-login"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr strings.Builder
			if got := runExit(c.err, &stderr, []string{"example-login"}); got != c.want {
				t.Errorf("runExit(%v) = %d, want %d", c.err, got, c.want)
			}
			if c.wantLine != "" && !strings.Contains(stderr.String(), c.wantLine) {
				t.Errorf("stderr %q lacks %q", stderr.String(), c.wantLine)
			}
			if c.noLine != "" && strings.Contains(stderr.String(), c.noLine) {
				t.Errorf("stderr %q must not contain %q", stderr.String(), c.noLine)
			}
			if c.err == nil && stderr.Len() != 0 {
				t.Errorf("a clean stop prints nothing, got %q", stderr.String())
			}
		})
	}
}
