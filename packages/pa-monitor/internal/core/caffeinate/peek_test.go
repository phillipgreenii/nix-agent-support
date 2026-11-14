package caffeinate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate/keepertest"
)

// countingBin wraps the real pg-keep-awake in a script that appends one line to
// a log per launch, and returns the script path and a launch counter.
func countingBin(t *testing.T) (string, func() int) {
	t.Helper()
	real := keepertest.Bin(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "launches")
	script := filepath.Join(dir, "pg-keep-awake")
	body := "#!/bin/sh\necho x >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, func() int {
		b, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "x")
	}
}

// rewriteStatus edits the keeper's status.json after SimulateKeeper wrote it.
func rewriteStatus(t *testing.T, stateDir string, edit func(m map[string]any)) {
	t.Helper()
	path := filepath.Join(stateDir, "status.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An idle daemon over 12 ticks with the toggle off launches nothing; the
// pre-change path (ObserveViaBinary) launches once per tick, which proves the
// counter counts.
func TestIdleUpKeeperLaunchesNothingOver12Ticks(t *testing.T) {
	bin, launches := countingBin(t)
	stateDir := keepertest.UseStateDir(t)
	keepertest.SimulateKeeper(t, stateDir, keepertest.KeeperState{})

	now := time.Unix(1_000_000, 0)
	m := &Manager{Grace: time.Second, Keeper: &CLI{Configured: bin}, Now: func() time.Time { return now }, PID: os.Getpid()}
	for i := 0; i < 12; i++ {
		m.Tick(false)
		now = now.Add(5 * time.Second)
	}
	if n := launches(); n != 0 {
		t.Errorf("idle up keeper, toggle off: %d launches over 12 ticks, want 0", n)
	}
	if !m.KeeperUp() || m.Held() {
		t.Errorf("KeeperUp=%v Held=%v, want up and not held", m.KeeperUp(), m.Held())
	}

	cli := &CLI{Configured: bin}
	for i := 0; i < 12; i++ {
		cli.ObserveViaBinary()
	}
	if n := launches(); n != 12 {
		t.Errorf("negative control (pre-change path): %d launches, want 12", n)
	}
}

// Every status file the in-process reader cannot judge alone still reaches the
// binary.
func TestUnjudgeableStatusStillLaunchesBinary(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"missing status, heartbeat present": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "status.json")); err != nil {
				t.Fatal(err)
			}
		},
		"malformed": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable": func(t *testing.T, dir string) {
			p := filepath.Join(dir, "status.json")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			if f, err := os.Open(p); err == nil {
				_ = f.Close()
				t.Skip("file is readable despite mode 0 (running as root)")
			}
		},
		"newer schema": func(t *testing.T, dir string) {
			rewriteStatus(t, dir, func(m map[string]any) { m["schema_version"] = 99 })
		},
		"held": func(t *testing.T, dir string) {
			rewriteStatus(t, dir, func(m map[string]any) { m["assertion"] = map[string]any{"held": true} })
		},
		"live lease": func(t *testing.T, dir string) {
			rewriteStatus(t, dir, func(m map[string]any) {
				m["leases"] = []any{map[string]any{"id": "a", "live": true}}
			})
		},
		"paused": func(t *testing.T, dir string) {
			rewriteStatus(t, dir, func(m map[string]any) { m["paused"] = map[string]any{"until": nil} })
		},
		"lingering": func(t *testing.T, dir string) {
			rewriteStatus(t, dir, func(m map[string]any) { m["linger_until"] = "2099-01-01T00:00:00Z" })
		},
		"unexplained ticks": func(t *testing.T, dir string) {
			rewriteStatus(t, dir, func(m map[string]any) { m["unexplained_ticks"] = 1 })
		},
		"heartbeat missing, status present": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "keeper.heartbeat")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			bin, launches := countingBin(t)
			stateDir := keepertest.UseStateDir(t)
			keepertest.SimulateKeeper(t, stateDir, keepertest.KeeperState{})
			setup(t, stateDir)
			(&CLI{Configured: bin}).Observe()
			if n := launches(); n != 1 {
				t.Errorf("%d launches, want 1", n)
			}
		})
	}
}

// The in-process reading agrees with the real binary wherever it answers
// without launching it.
func TestInProcessObservationMatchesRealBinary(t *testing.T) {
	type fixture struct {
		name       string
		setup      func(t *testing.T, bin, dir string)
		wantLaunch bool
		wantHealth Health
	}
	simulate := func(k keepertest.KeeperState) func(t *testing.T, bin, dir string) {
		return func(t *testing.T, _, dir string) { keepertest.SimulateKeeper(t, dir, k) }
	}
	fixtures := []fixture{
		{name: "never started", setup: func(*testing.T, string, string) {}, wantHealth: HealthDown},
		{name: "down, files removed", setup: func(t *testing.T, _, dir string) {
			keepertest.SimulateKeeper(t, dir, keepertest.KeeperState{})
			keepertest.StopKeeper(t, dir)
		}, wantHealth: HealthDown},
		{name: "down, stale heartbeat", setup: func(t *testing.T, _, dir string) {
			keepertest.SimulateKeeper(t, dir, keepertest.KeeperState{OnBattery: true, Percent: 42})
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(filepath.Join(dir, "keeper.heartbeat"), old, old); err != nil {
				t.Fatal(err)
			}
		}, wantHealth: HealthDown},
		{name: "up idle", setup: simulate(keepertest.KeeperState{OnBattery: true, Percent: 77, Gate: "no_spawn"}), wantHealth: HealthOK},
		{name: "held", setup: simulate(keepertest.KeeperState{Held: true}), wantLaunch: true, wantHealth: HealthOK},
		{name: "degraded", setup: func(t *testing.T, bin, dir string) {
			keepertest.SimulateKeeper(t, dir, keepertest.KeeperState{UnexplainedTicks: 2})
			rewriteStatus(t, dir, func(m map[string]any) {
				m["leases"] = []any{map[string]any{"id": "x", "live": true}}
			})
			out, err := exec.Command(bin, "acquire", "--owner", "diff", "--pid", itoa(os.Getpid())).CombinedOutput()
			if err != nil {
				t.Fatalf("acquire: %v: %s", err, out)
			}
		}, wantLaunch: true, wantHealth: HealthDegraded},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			bin, launches := countingBin(t)
			stateDir := keepertest.UseStateDir(t)
			f.setup(t, bin, stateDir)
			base := launches()

			cli := &CLI{Configured: bin}
			got := cli.Observe()
			launched := launches() - base
			if f.wantLaunch != (launched > 0) {
				t.Errorf("launches = %d, wantLaunch = %v", launched, f.wantLaunch)
			}
			want := cli.ObserveViaBinary()
			if got.Health != f.wantHealth {
				t.Errorf("Health = %v, want %v", got.Health, f.wantHealth)
			}
			if got.Health != want.Health || got.StatusKnown != want.StatusKnown || got.Held != want.Held ||
				got.Gate != want.Gate || got.OnBattery != want.OnBattery || got.Percent != want.Percent ||
				got.BatteryReadOK != want.BatteryReadOK {
				t.Errorf("in-process %+v differs from binary %+v", got, want)
			}
		})
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
