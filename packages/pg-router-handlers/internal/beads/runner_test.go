package beads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestScrubbedEnv_removesBeadsTaint(t *testing.T) {
	t.Setenv("BEADS_DIR", "/wrong/.beads")
	t.Setenv("WORKSPACE_ROOT", "/wrong")
	t.Setenv("PATH", "/usr/bin")
	r := NewCLIRunnerForRepo("/repo", "")
	if r.Dir != "/repo" {
		t.Errorf("Dir = %q, want /repo", r.Dir)
	}
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "BEADS_DIR=") || strings.HasPrefix(kv, "WORKSPACE_ROOT=") {
			t.Errorf("scrubbed env still contains %q", kv)
		}
	}
	var sawPath bool
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "PATH=") {
			sawPath = true
		}
	}
	if !sawPath {
		t.Error("scrubbed env dropped PATH; should only remove BEADS_DIR/WORKSPACE_ROOT")
	}
}

func TestScrubEnv_pure(t *testing.T) {
	in := []string{"A=1", "BEADS_DIR=/x", "B=2", "WORKSPACE_ROOT=/y", "C=3"}
	got := scrubEnv(in)
	want := []string{"A=1", "B=2", "C=3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scrubEnv = %v, want %v", got, want)
	}
	_ = os.Environ // referenced to keep import if test is trimmed
}

// fakeRunner returns canned stdout/err without spawning bd. Reused by issue_test.go.
type fakeRunner struct {
	out  string
	err  error
	args [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.args = append(f.args, args)
	return f.out, f.err
}

// compile-time check: fakeRunner satisfies Runner
var _ Runner = (*fakeRunner)(nil)

// stubBD puts a `bd` on PATH that records its argv (one arg per line) to the
// returned file, so a test can assert what actually reached the bd binary.
func stubBD(t *testing.T) (argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argvFile + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvFile
}

func readArgv(t *testing.T, f string) []string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("bd stub never ran: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// A runner with an Actor passes it to the bd invocation as --actor (pg2-lhi3b).
func TestCLIRunner_Run_ActorReachesBD(t *testing.T) {
	argvFile := stubBD(t)
	r := NewCLIRunnerForRepo(t.TempDir(), "pgii-pool__worker")
	if _, err := r.Run(context.Background(), "update", "pg2-x", "--claim"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.Join(readArgv(t, argvFile), " ")
	if want := "--actor pgii-pool__worker update pg2-x --claim"; got != want {
		t.Errorf("bd argv = %q, want %q", got, want)
	}
}

// Every write (not only claims) carries the runner's actor, and a call that
// already names its own --actor keeps it (no duplicate).
func TestCLIRunner_bdArgs(t *testing.T) {
	r := &CLIRunner{Actor: "me"}
	got, err := r.bdArgs([]string{"update", "x", "--add-label", "human"})
	if err != nil || strings.Join(got, " ") != "--actor me update x --add-label human" {
		t.Errorf("got %v, %v", got, err)
	}
	got, err = r.bdArgs([]string{"update", "x", "--claim", "--actor", "other"})
	if err != nil || strings.Join(got, " ") != "update x --claim --actor other" {
		t.Errorf("explicit --actor must win and not be duplicated; got %v, %v", got, err)
	}
}

// A claim with no identity anywhere never reaches bd.
func TestCLIRunner_Run_ClaimWithoutActorRefused(t *testing.T) {
	argvFile := stubBD(t)
	t.Setenv("BEADS_ACTOR", "")
	for _, args := range [][]string{
		{"update", "x", "--claim"},
		{"ready", "--claim", "--json"},
		{"update", "x", "--status", "in_progress"},
		{"update", "x", "--status=in_progress"},
		{"update", "x", "--assignee", "someone"},
		{"update", "x", "--assignee=someone"},
	} {
		r := NewCLIRunnerForRepo(t.TempDir(), "")
		_, err := r.Run(context.Background(), args...)
		if !errors.Is(err, ErrClaimWithoutActor) {
			t.Errorf("%v: err = %v, want ErrClaimWithoutActor", args, err)
		}
	}
	if _, err := os.Stat(argvFile); err == nil {
		t.Error("bd was spawned for a refused claim")
	}
}

// Releases, reads and non-claiming writes still run with no actor, and a
// claim is allowed when BEADS_ACTOR (the dispatched-session case) supplies one.
func TestCLIRunner_Run_NonClaimsAndEnvActorAllowed(t *testing.T) {
	stubBD(t)
	t.Setenv("BEADS_ACTOR", "")
	r := NewCLIRunnerForRepo(t.TempDir(), "")
	for _, args := range [][]string{
		{"update", "x", "--status=open", "--assignee="},
		{"update", "x", "--status", "open", "--assignee", ""},
		{"list", "--status", "open", "--json"},
		{"comment", "x", "--", "--claim"},
	} {
		if _, err := r.Run(context.Background(), args...); err != nil {
			t.Errorf("%v: unexpected error %v", args, err)
		}
	}
	t.Setenv("BEADS_ACTOR", "sess-1-worker")
	r = NewCLIRunnerForRepo(t.TempDir(), "")
	if _, err := r.Run(context.Background(), "update", "x", "--claim"); err != nil {
		t.Errorf("claim with BEADS_ACTOR set must be allowed: %v", err)
	}
}

func TestCLIRunnerRun_boundsAHungBdWithTheTimeout(t *testing.T) {
	var seen context.Context
	fake := cmdruntest.New(t)
	fake.On(func(cmdrun.Call) bool { return true }).Respond(func(cmdrun.Call) cmdrun.Result {
		<-seen.Done() // a wedged bd: returns only when its context ends
		return cmdrun.Result{Code: -1, Signal: "killed", CtxErr: seen.Err()}
	})
	inner := fake.Runner()
	r := &CLIRunner{Timeout: 50 * time.Millisecond, Exec: func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		seen = ctx
		return inner(ctx, c)
	}}
	start := time.Now()
	_, err := r.Run(context.Background(), "show", "x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("Run took %s, want ~50ms", el)
	}
}

func TestCLIRunnerRun_mapsExitAndStartFailures(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(func(c cmdrun.Call) bool { return c.Args[0] == "fail" }).
		Return(cmdrun.Result{Code: 3, Stderr: []byte("boom\n")})
	fake.On(func(c cmdrun.Call) bool { return c.Args[0] == "ok" }).
		Return(cmdrun.Result{Stdout: []byte("out")})
	fake.On(func(c cmdrun.Call) bool { return c.Args[0] == "nobd" }).
		Return(cmdrun.Result{Code: -1, StartErr: errors.New("not found")})
	r := &CLIRunner{Exec: fake.Runner(), Env: []string{"A=1"}, Dir: "/d"}
	if out, err := r.Run(t.Context(), "ok"); err != nil || out != "out" {
		t.Errorf("ok: out=%q err=%v", out, err)
	}
	if _, err := r.Run(t.Context(), "fail"); err == nil || !strings.Contains(err.Error(), "exit status 3: boom") {
		t.Errorf("fail: err=%v", err)
	}
	if _, err := r.Run(t.Context(), "nobd"); err == nil || !strings.Contains(err.Error(), "is bd on PATH?") {
		t.Errorf("nobd: err=%v", err)
	}
	c := fake.Calls()[0]
	if c.Name != Command || c.Dir != "/d" || c.EnvMode != cmdrun.EnvExact {
		t.Errorf("call = %+v", c)
	}
}

// A real #!/bin/sh bd stub on PATH that sleeps proves the real Exec path is
// bounded, even for a caller on context.Background().
func TestCLIRunnerRun_realProcessStubIsBounded(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "bd")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	r := &CLIRunner{Timeout: 200 * time.Millisecond, WaitDelay: 200 * time.Millisecond}
	start := time.Now()
	_, err := r.Run(context.Background(), "ready")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("Run took %s, want under 3s", el)
	}
}

func TestCLIRunnerRun_nonZeroExitKeepsTheCodeForIsGuardStale(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(func(cmdrun.Call) bool { return true }).Return(cmdrun.Result{Code: 13, Stderr: []byte("assignee mismatch")})
	r := &CLIRunner{Exec: fake.Runner()}
	_, err := r.Run(t.Context(), "update", "x")
	if !IsGuardStale(err) {
		t.Fatalf("IsGuardStale(%v) = false, want true for exit 13", err)
	}
}
