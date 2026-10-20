package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
)

// standIn is a stand-in for pg-connector-github: serve stays up (exit 0 on
// SIGTERM) and records its pid; status --json exits with the number in the
// file status-exit next to the script, sleeps when status-hang exists, and
// serve exits at once with the number in serve-exit when that file exists.
const standIn = `dir=$(dirname "$0")
mode=""
for a in "$@"; do case "$a" in serve|status) mode=$a ;; esac; done
case "$mode" in
serve)
  if [ -f "$dir/serve-exit" ]; then exit "$(cat "$dir/serve-exit")"; fi
  echo $$ >> "$dir/serve.pids"
  trap 'exit 0' TERM
  sleep 300 &
  # Not ready until the grandchild has exec'd: a SIGTERM between the shell's
  # fork and that exec is consumed by the forked shell's trap, leaving it behind.
  until [ "$(ps -o comm= -p $!)" = sleep ]; do :; done
  while :; do wait; done
  ;;
status)
  if [ -f "$dir/status-hang" ]; then exec sleep 30; fi
  echo '{}'
  exit "$(cat "$dir/status-exit")"
  ;;
esac
`

type fixture struct {
	dir string
	sup *Supervisor
}

func newFixture(t *testing.T, statusExit int) *fixture {
	t.Helper()
	dir := t.TempDir()
	bin := testutil.WriteScript(t, dir, "pg-connector-github", standIn)
	f := &fixture{dir: dir, sup: &Supervisor{
		Runner: testutil.NewRunner(t), Binary: bin, Config: filepath.Join(dir, "config.json"),
		ReadyTimeout: 5 * time.Second, ReadyInterval: 20 * time.Millisecond, StatusTimeout: 5 * time.Second,
		StopGrace: 5 * time.Second, RestartBackoff: time.Nanosecond,
	}}
	f.setStatus(t, statusExit)
	t.Cleanup(func() { _ = f.sup.Stop() })
	return f
}

func (f *fixture) setStatus(t *testing.T, code int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "status-exit"), []byte(strconv.Itoa(code)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) pids(t *testing.T) []int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "serve.pids"))
	if err != nil {
		return nil
	}
	var out []int
	for _, w := range strings.Fields(string(b)) {
		n, _ := strconv.Atoi(w)
		out = append(out, n)
	}
	return out
}

func (f *fixture) waitPids(t *testing.T, n int) []int {
	t.Helper()
	for i := 0; i < 250; i++ {
		if p := f.pids(t); len(p) >= n {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fewer than %d serve process(es) recorded", n)
	return nil
}

func TestStartWaitsUntilServingAndStopLeavesNoProcess(t *testing.T) {
	f := newFixture(t, 0)
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pids := f.waitPids(t, 1)
	if err := f.sup.Start(context.Background()); err == nil {
		t.Error("a second Start must be refused while the child is up")
	}
	if err := f.sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !testutil.ProcessGone(pids[0]) {
		t.Errorf("serve process %d left behind", pids[0])
	}
	if err := f.sup.Stop(); err != nil {
		t.Errorf("a second Stop must be a no-op, got %v", err)
	}
}

func TestStartAcceptsADegradedDaemonAsServing(t *testing.T) {
	f := newFixture(t, 2)
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatalf("status exit 2 (degraded) is serving: %v", err)
	}
}

func TestStartRefusesWhenTheDaemonNeverBecomesReadyAndLeavesNoChild(t *testing.T) {
	f := newFixture(t, 3)
	f.sup.ReadyTimeout = 300 * time.Millisecond
	err := f.sup.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not serving") || !strings.Contains(err.Error(), "status exit 3") {
		t.Fatalf("Start = %v, want a clear not-serving error naming the last status", err)
	}
	pids := f.waitPids(t, 1)
	if !testutil.ProcessGone(pids[0]) {
		t.Errorf("the never-ready child %d must be stopped", pids[0])
	}
	if got := f.sup.Sample(context.Background()); got.Alive || got.State != schema.DaemonDown {
		t.Errorf("after a failed Start the sample is down: %+v", got)
	}
}

func TestStartRefusesWhenTheChildExitsBeforeServing(t *testing.T) {
	f := newFixture(t, 3)
	if err := os.WriteFile(filepath.Join(f.dir, "serve-exit"), []byte("9"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.sup.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited (code 9)") {
		t.Fatalf("Start = %v, want the child's exit code", err)
	}
}

func TestStartRefusesWhenTheBinaryCannotBeStarted(t *testing.T) {
	f := newFixture(t, 0)
	f.sup.Binary = filepath.Join(f.dir, "missing")
	if err := f.sup.Start(context.Background()); err == nil {
		t.Fatal("a binary that cannot start must refuse the run")
	}
}

func TestStartCancelledWhileWaitingStopsTheChild(t *testing.T) {
	f := newFixture(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	f.sup.Sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	if err := f.sup.Start(ctx); err == nil {
		t.Fatal("Start must fail when its context ends")
	}
	pids := f.waitPids(t, 1)
	if !testutil.ProcessGone(pids[0]) {
		t.Errorf("child %d left behind after a cancelled Start", pids[0])
	}
}

func TestSampleMapsTheStatusExitCode(t *testing.T) {
	f := newFixture(t, 0)
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		exit  int
		state string
	}{
		{0, schema.DaemonHealthy}, {2, schema.DaemonDegraded}, {3, schema.DaemonDown}, {1, schema.DaemonDown}, {4, schema.DaemonDown},
	} {
		f.setStatus(t, c.exit)
		got := f.sup.Sample(context.Background())
		want := schema.DaemonSample{State: c.state, StatusExit: c.exit, Alive: true, Restarts: 0}
		if got != want {
			t.Errorf("status exit %d: sample = %+v, want %+v", c.exit, got, want)
		}
	}
	// A degraded or down status never restarts a live process.
	if n := len(f.pids(t)); n != 1 {
		t.Errorf("%d serve processes started; a bad status must not restart a live child", n)
	}
}

func TestSampleOfAHungStatusIsDownNotHealthy(t *testing.T) {
	f := newFixture(t, 0)
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "status-hang"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.sup.StatusTimeout = 200 * time.Millisecond
	got := f.sup.Sample(context.Background())
	if got.State != schema.DaemonDown || got.StatusExit != -1 || !got.Alive {
		t.Errorf("sample = %+v, want down, status_exit -1, alive", got)
	}
}

func TestSampleBeforeStartIsDownNeverHealthy(t *testing.T) {
	f := newFixture(t, 0)
	got := f.sup.Sample(context.Background())
	want := schema.DaemonSample{State: schema.DaemonDown, StatusExit: -1}
	if got != want {
		t.Errorf("sample = %+v, want %+v", got, want)
	}
}

func killChild(t *testing.T, s *Supervisor) {
	t.Helper()
	s.mu.Lock()
	c := s.child
	s.mu.Unlock()
	if err := syscall.Kill(-c.Pid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the killed child never reported Done")
	}
}

func TestADeadChildIsRestartedAndTheRestartCounted(t *testing.T) {
	f := newFixture(t, 0)
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := f.waitPids(t, 1)[0]
	killChild(t, f.sup)

	got := f.sup.Sample(context.Background())
	if want := (schema.DaemonSample{State: schema.DaemonHealthy, StatusExit: 0, Alive: true, Restarts: 1}); got != want {
		t.Fatalf("sample after the restart = %+v, want %+v", got, want)
	}
	pids := f.waitPids(t, 2)
	if pids[1] == first {
		t.Errorf("the restart reused pid %d", first)
	}
	if f.sup.Restarts() != 1 {
		t.Errorf("Restarts = %d, want 1", f.sup.Restarts())
	}
	killChild(t, f.sup)
	if got := f.sup.Sample(context.Background()); got.Restarts != 2 || !got.Alive {
		t.Errorf("second restart sample = %+v", got)
	}
	if err := f.sup.Stop(); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.pids(t) {
		if !testutil.ProcessGone(p) {
			t.Errorf("serve process %d left behind", p)
		}
	}
}

func TestADeadChildInsideTheRestartBackoffIsSampledDownWithoutRunningStatus(t *testing.T) {
	f := newFixture(t, 0)
	f.sup.RestartBackoff = time.Hour
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.waitPids(t, 1)
	killChild(t, f.sup)
	// Status would say healthy; the dead process must win.
	got := f.sup.Sample(context.Background())
	if want := (schema.DaemonSample{State: schema.DaemonDown, StatusExit: -1, Alive: false, Restarts: 0}); got != want {
		t.Errorf("sample = %+v, want %+v", got, want)
	}
	if n := len(f.pids(t)); n != 1 {
		t.Errorf("%d serve processes: no restart is allowed inside the backoff", n)
	}
}

func TestAStoppedSupervisorIsNeverRestartedBySample(t *testing.T) {
	f := newFixture(t, 0)
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.waitPids(t, 1)
	if err := f.sup.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := f.sup.Sample(context.Background()); got.Alive || got.State != schema.DaemonDown {
		t.Errorf("sample after Stop = %+v", got)
	}
	if n := len(f.pids(t)); n != 1 {
		t.Errorf("%d serve processes: Sample must not restart after Stop", n)
	}
}

// Compile-time proof that the real runner satisfies the supervisor's seam.
var _ Process = (*runner.Runner)(nil)

func TestSampleIsNotMadeDownByAShutdownSignalArrivingMidSlot(t *testing.T) {
	f := newFixture(t, 0)
	if err := f.sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := f.sup.Sample(ctx)
	if want := (schema.DaemonSample{State: schema.DaemonHealthy, StatusExit: 0, Alive: true}); got != want {
		t.Errorf("sample with a cancelled context = %+v, want %+v (the child is up)", got, want)
	}
}
