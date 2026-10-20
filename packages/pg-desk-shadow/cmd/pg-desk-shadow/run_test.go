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
				if code != exitRefuse {
					t.Fatalf("attempt %d: exit %d, want %d (refused)\nstdout: %s\nstderr: %s", attempt, code, exitRefuse, stdout.String(), stderr.String())
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
