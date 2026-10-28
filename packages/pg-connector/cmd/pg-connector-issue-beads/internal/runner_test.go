package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal/budget"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal/eventlog"
	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeEnv backs ResolveWorkspaceDir/CLIRunner.Getenv in tests, mirroring
// cmd/pg-connector's own registry_test.go fakeEnv pattern — so resolution
// never depends on this test process's real environment.
func fakeEnv(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// ----------------------------------------------------------------------
// ResolveWorkspaceDir — bead pg2-1q9c0 (design finding A9), AC1
// ----------------------------------------------------------------------

func TestResolveWorkspaceDir_NeitherSet_ReturnsErrWorkspaceNotConfigured(t *testing.T) {
	_, err := ResolveWorkspaceDir(fakeEnv(nil))
	if !errors.Is(err, ErrWorkspaceNotConfigured) {
		t.Fatalf("err = %v, want ErrWorkspaceNotConfigured", err)
	}
}

func TestResolveWorkspaceDir_PrefersEnvWorkspaceDir(t *testing.T) {
	dir, err := ResolveWorkspaceDir(fakeEnv(map[string]string{
		EnvWorkspaceDir: "/explicit/workspace",
		"BEADS_DIR":     "/beads/native",
	}))
	if err != nil {
		t.Fatalf("ResolveWorkspaceDir: %v", err)
	}
	if dir != "/explicit/workspace" {
		t.Fatalf("dir = %q, want /explicit/workspace (EnvWorkspaceDir must win over bd's own BEADS_DIR)", dir)
	}
}

func TestResolveWorkspaceDir_FallsBackToBeadsDir(t *testing.T) {
	dir, err := ResolveWorkspaceDir(fakeEnv(map[string]string{
		"BEADS_DIR": "/beads/native",
	}))
	if err != nil {
		t.Fatalf("ResolveWorkspaceDir: %v", err)
	}
	if dir != "/beads/native" {
		t.Fatalf("dir = %q, want /beads/native", dir)
	}
}

func TestResolveWorkspaceDir_BlankValuesTreatedAsUnset(t *testing.T) {
	_, err := ResolveWorkspaceDir(fakeEnv(map[string]string{
		EnvWorkspaceDir: "   ",
		"BEADS_DIR":     "",
	}))
	if !errors.Is(err, ErrWorkspaceNotConfigured) {
		t.Fatalf("err = %v, want ErrWorkspaceNotConfigured for blank/empty values", err)
	}
}

// ----------------------------------------------------------------------
// CLIRunner — resolution wiring
// ----------------------------------------------------------------------

// TestCLIRunner_Run_UnconfiguredWorkspace_NeverInvokesBD proves Run returns
// ErrWorkspaceNotConfigured WITHOUT spawning `bd` at all when no workspace
// resolves — this is a hermetic unit test specifically because resolution
// failure short-circuits before exec.CommandContext is ever constructed;
// if it did reach exec, the returned error would instead be an
// *exec.ExitError or "executable file not found" wrapping, not this
// sentinel.
func TestCLIRunner_Run_UnconfiguredWorkspace_NeverInvokesBD(t *testing.T) {
	r := &CLIRunner{Getenv: fakeEnv(nil)}
	_, err := r.Run(context.Background(), "show", "tp-1", "--json")
	if !errors.Is(err, ErrWorkspaceNotConfigured) {
		t.Fatalf("err = %v, want ErrWorkspaceNotConfigured", err)
	}
}

func TestCLIRunner_Workspace_MatchesRunsResolution(t *testing.T) {
	r := &CLIRunner{Getenv: fakeEnv(map[string]string{EnvWorkspaceDir: "/pinned"})}
	dir, err := r.Workspace()
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if dir != "/pinned" {
		t.Fatalf("dir = %q, want /pinned", dir)
	}
}

// TestCLIRunner_ExplicitDir_BypassesEnvResolution locks in that a
// caller-supplied Dir (as the contract tests in realbd_test.go already do)
// short-circuits env-based resolution entirely — the Getenv seam exists
// for production (Dir unset), not to override an explicit test Dir.
func TestCLIRunner_ExplicitDir_BypassesEnvResolution(t *testing.T) {
	r := &CLIRunner{Dir: "/explicit/test/dir", Getenv: fakeEnv(nil)}
	dir, err := r.Workspace()
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if dir != "/explicit/test/dir" {
		t.Fatalf("dir = %q, want /explicit/test/dir", dir)
	}
}

func TestCLIRunner_Workspace_PropagatesResolutionError(t *testing.T) {
	r := &CLIRunner{Getenv: fakeEnv(nil)}
	_, err := r.Workspace()
	if !errors.Is(err, ErrWorkspaceNotConfigured) {
		t.Fatalf("err = %v, want ErrWorkspaceNotConfigured", err)
	}
}

// ----------------------------------------------------------------------
// bead pg2-332z8: timeout/WaitDelay and output-cap regressions
// ----------------------------------------------------------------------

// bdStubExitingWithStderr puts an executable named `bd` on PATH that
// writes stderrMsg to its standard error and exits with exitCode — the
// same shape the gh-based backends' ghStubExitingWithStderr establishes,
// applied to `bd` for this one.
func bdStubExitingWithStderr(t *testing.T, exitCode int, stderrMsg string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'BDSTUBEOF' >&2\n" + stderrMsg + "\nBDSTUBEOF\nexit " + fmt.Sprint(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o700); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCLIRunner_Command_SetsWaitDelay is the regression test for bead
// pg2-332z8 #13's per-Cmd half: command() (Run's own choke point) must set
// WaitDelay, independent of whatever deadline ctx itself carries —
// WaitDelay bounds Cmd.Wait's own residual wait for the stdout/stderr
// pipes to close (e.g. a grandchild inheriting one and holding it open,
// such as a `bd` that shells out to a dolt server process), which a
// context deadline alone does not cover.
func TestCLIRunner_Command_SetsWaitDelay(t *testing.T) {
	r := &CLIRunner{Dir: "/some/workspace"}
	cmd := r.command(context.Background(), "/some/workspace", []string{"show", "tp-1"})
	if cmd.WaitDelay != scriptout.DefaultWaitDelay {
		t.Fatalf("cmd.WaitDelay = %v, want %v", cmd.WaitDelay, scriptout.DefaultWaitDelay)
	}
}

// ----------------------------------------------------------------------
// bead pg2-8o2cg: BD_JSON_ENVELOPE must never depend on the ambient
// environment — see runner.go's withBDJSONEnvelope/envBDJSONEnvelope doc
// comments for the confirmed, reproduced root cause (a LaunchAgent's
// environment never carries it, so this backend's own decodeBDEnvelope,
// which has no bare-shape fallback, silently degraded to
// "backend unavailable").
// ----------------------------------------------------------------------

// TestCLIRunner_Command_DefaultEnv_SetsBDJSONEnvelope is the regression
// test for the reported defect: when r.Env is unset (the production
// default via NewCLIRunner), command() must not leave cmd.Env nil — a nil
// Env makes the exec'd bd child inherit whatever ambient environment this
// process happens to have, which is exactly the bug. BD_JSON_ENVELOPE=1
// must be present exactly once regardless of what the real os.Environ()
// contains.
func TestCLIRunner_Command_DefaultEnv_SetsBDJSONEnvelope(t *testing.T) {
	r := &CLIRunner{Dir: "/some/workspace"}
	cmd := r.command(context.Background(), "/some/workspace", []string{"show", "tp-1"})
	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil: the exec'd bd child would inherit the ambient environment, which is the pg2-8o2cg defect")
	}
	count := 0
	for _, kv := range cmd.Env {
		if kv == "BD_JSON_ENVELOPE=1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("cmd.Env contains BD_JSON_ENVELOPE=1 %d times, want exactly 1 (env=%v)", count, cmd.Env)
	}
}

// TestCLIRunner_Command_DefaultEnv_OverridesConflictingAmbientValue proves
// withBDJSONEnvelope REPLACES a pre-existing entry rather than merely
// appending after it — an append-only fix would leave the effective value
// up to getenv's own first-match-wins scan order if the ambient
// environment ever carried a conflicting BD_JSON_ENVELOPE value.
func TestCLIRunner_Command_DefaultEnv_OverridesConflictingAmbientValue(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "0")
	r := &CLIRunner{Dir: "/some/workspace"}
	cmd := r.command(context.Background(), "/some/workspace", []string{"show", "tp-1"})
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "BD_JSON_ENVELOPE=") && kv != "BD_JSON_ENVELOPE=1" {
			t.Fatalf("cmd.Env carries a non-1 BD_JSON_ENVELOPE entry: %q (env=%v)", kv, cmd.Env)
		}
	}
}

// TestCLIRunner_Command_ExplicitEnv_NotAugmented locks in that an
// explicit r.Env (the test-isolation seam every other CLIRunner test in
// this file relies on, and the one production round-trip test in
// realbd_test.go) is used exactly as given — command() injects the
// default envelope pin only when r.Env is nil.
func TestCLIRunner_Command_ExplicitEnv_NotAugmented(t *testing.T) {
	r := &CLIRunner{Dir: "/some/workspace", Env: []string{"FOO=bar"}}
	cmd := r.command(context.Background(), "/some/workspace", []string{"show", "tp-1"})
	if len(cmd.Env) != 1 || cmd.Env[0] != "FOO=bar" {
		t.Fatalf("cmd.Env = %v, want exactly the explicit override [FOO=bar]", cmd.Env)
	}
}

// TestCLIRunner_Run_CapsStderr is the regression test for bead pg2-332z8
// #26: before TruncateForFold, Run folded bd's ENTIRE captured stderr into
// the returned error with no bound — a runaway or unexpectedly verbose bd
// failure could produce an unbounded error string.
func TestCLIRunner_Run_CapsStderr(t *testing.T) {
	huge := strings.Repeat("e", scriptout.MaxFoldedOutputBytes*3)
	bdStubExitingWithStderr(t, 1, huge)

	r := &CLIRunner{Dir: t.TempDir()}
	_, err := r.Run(context.Background(), "show", "tp-1")
	if err == nil {
		t.Fatal("expected error from the failing bd stub")
	}
	if got := len(err.Error()); got > scriptout.MaxFoldedOutputBytes+256 {
		t.Fatalf("error message is %d bytes; stderr fold was not capped", got)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncation marker in the error, got a %d-byte message", len(err.Error()))
	}
}

// TestCLIRunner_Run_FailureNamesArgvAndElapsed is bead pg2-5dyz2's runner
// half: a bd failure must carry the argv and how long bd ran, so a call
// killed at its deadline is attributable to a command and a duration instead
// of a bare "signal: killed".
func TestCLIRunner_Run_FailureNamesArgvAndElapsed(t *testing.T) {
	bdStubExitingWithStderr(t, 1, "boom")
	r := &CLIRunner{Dir: t.TempDir()}
	_, err := r.Run(context.Background(), "list", "--json")
	if err == nil {
		t.Fatal("expected error from the failing bd stub")
	}
	for _, want := range []string{"bd -C ", "list --json", "(after ", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// TestCLIRunner_Run_RecordsEveryBDCallOnTheCallsEvent proves Run notes each bd
// execution (argv + elapsed) on the in-flight call's recorder, success or
// failure, and is a no-op when the context carries none.
func TestCLIRunner_Run_RecordsEveryBDCallOnTheCallsEvent(t *testing.T) {
	bdStubExitingWithStderr(t, 0, "")
	r := &CLIRunner{Dir: t.TempDir()}
	if _, err := r.Run(context.Background(), "list", "--json"); err != nil {
		t.Fatalf("Run without a recorder: %v", err)
	}

	var got []string
	tb := eventlog.Instrument(scriptout.DispatchTable{"list": {Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		if _, err := r.Run(ctx, "list", "--json"); err != nil {
			return nil, err
		}
		_, err := r.Run(ctx, "show", "tp-1")
		return nil, err
	}}}, sinkFunc(func(ev eventlog.Event) {
		got = append(got, fmt.Sprintf("%d|%s", ev.BDCalls, ev.BDSlowestArgv))
	}), "", time.Now)
	if _, err := tb["list"].Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "2|bd -C ") {
		t.Fatalf("event = %v, want 2 bd calls and a slowest argv starting with bd -C", got)
	}
}

type sinkFunc func(eventlog.Event)

func (f sinkFunc) Write(ev eventlog.Event)           { f(ev) }
func (f sinkFunc) WriteProgress(evlog.ProgressEvent) {}

// ----------------------------------------------------------------------
// bead pg2-lhi3b: every bd CLAIM carries an explicit actor
// ----------------------------------------------------------------------

// bdArgvRecorder puts a `bd` on PATH that records its argv (one arg per
// line) into the returned file, so a test asserts what actually reached bd.
func bdArgvRecorder(t *testing.T) (argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argvFile + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o700); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvFile
}

func recordedArgv(t *testing.T, argvFile string) string {
	t.Helper()
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("bd stub never ran: %v", err)
	}
	return strings.Join(strings.Split(strings.TrimRight(string(b), "\n"), "\n"), " ")
}

// The actor from $PG_CONNECTOR_ISSUE_BEADS_ACTOR reaches the bd invocation
// as --actor, here via the real claim-shaped Backend.Transition(in_progress).
func TestBackend_Transition_InProgress_ActorReachesBD(t *testing.T) {
	argvFile := bdArgvRecorder(t)
	r := &CLIRunner{Dir: t.TempDir(), Getenv: fakeEnv(map[string]string{EnvActor: "sess-1-sync"})}
	b := New(r)
	if err := b.Transition(context.Background(), "pg2-x", "in_progress"); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got := recordedArgv(t, argvFile)
	if !strings.Contains(got, "--actor sess-1-sync update --status in_progress --json -- pg2-x") {
		t.Fatalf("bd argv = %q, want it to carry --actor sess-1-sync before the update subcommand", got)
	}
}

// $BEADS_ACTOR is honored when the backend-scoped var is unset, and the
// backend-scoped var wins when both are set.
func TestCLIRunner_resolveActor_Precedence(t *testing.T) {
	cases := []struct {
		name string
		r    *CLIRunner
		want string
	}{
		{"none", &CLIRunner{Getenv: fakeEnv(nil)}, ""},
		{"beads actor", &CLIRunner{Getenv: fakeEnv(map[string]string{"BEADS_ACTOR": "b"})}, "b"},
		{"backend var wins", &CLIRunner{Getenv: fakeEnv(map[string]string{EnvActor: "a", "BEADS_ACTOR": "b"})}, "a"},
		{"field wins", &CLIRunner{Actor: "f", Getenv: fakeEnv(map[string]string{EnvActor: "a"})}, "f"},
		{"blank is unset", &CLIRunner{Getenv: fakeEnv(map[string]string{EnvActor: "  "})}, ""},
	}
	for _, c := range cases {
		if got := c.r.resolveActor(); got != c.want {
			t.Errorf("%s: resolveActor = %q, want %q", c.name, got, c.want)
		}
	}
}

// A claim with no actor configured never reaches bd, whichever claim shape.
func TestCLIRunner_Run_ClaimWithoutActor_Refused(t *testing.T) {
	argvFile := bdArgvRecorder(t)
	r := &CLIRunner{Dir: t.TempDir(), Getenv: fakeEnv(nil)}
	for _, args := range [][]string{
		{"update", "--claim", "--json", "--", "x"},
		{"ready", "--claim", "--json"},
		{"update", "--status", "in_progress", "--json", "--", "x"},
		{"update", "--status=in_progress", "--", "x"},
		{"update", "--assignee", "someone", "--", "x"},
		{"update", "--assignee=someone", "--", "x"},
	} {
		_, err := r.Run(context.Background(), args...)
		if !errors.Is(err, ErrActorNotConfigured) {
			t.Errorf("%v: err = %v, want ErrActorNotConfigured", args, err)
		}
	}
	if _, err := os.Stat(argvFile); err == nil {
		t.Error("bd was spawned for a refused claim")
	}
}

// Through the Backend, the refusal surfaces as an ErrUnavailable-classified
// error naming the fix.
func TestBackend_Transition_InProgress_NoActor_Refused(t *testing.T) {
	bdArgvRecorder(t)
	b := New(&CLIRunner{Dir: t.TempDir(), Getenv: fakeEnv(nil)})
	err := b.Transition(context.Background(), "pg2-x", "in_progress")
	if err == nil || !strings.Contains(err.Error(), "refusing to claim") {
		t.Fatalf("err = %v, want a refusal naming the missing actor", err)
	}
}

// A list is a read: filtering by `--status in_progress` selects beads, it does
// not claim one, so it reaches bd with no actor configured and carries no
// --actor.
func TestCLIRunner_Run_ListInProgress_NoActorNeeded(t *testing.T) {
	argvFile := bdArgvRecorder(t)
	r := &CLIRunner{Dir: t.TempDir(), Getenv: fakeEnv(nil)}
	for _, args := range [][]string{
		{"list", "--status", "in_progress", "--json", "-n", "0"},
		{"list", "--status=in_progress", "--json"},
		{"list", "--assignee", "someone", "--json"},
	} {
		if _, err := r.Run(context.Background(), args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		got := recordedArgv(t, argvFile)
		if strings.Contains(got, "--actor") || !strings.Contains(got, strings.Join(args, " ")) {
			t.Errorf("%v: argv = %q", args, got)
		}
	}
}

// Non-claiming calls (reads, close/open transitions, a release's empty
// assignee, claim-looking text behind "--") still run with no actor, and
// carry no --actor.
func TestCLIRunner_Run_NonClaim_NoActorNeeded(t *testing.T) {
	argvFile := bdArgvRecorder(t)
	r := &CLIRunner{Dir: t.TempDir(), Getenv: fakeEnv(nil)}
	for _, args := range [][]string{
		{"show", "--readonly", "--json", "--", "x"},
		{"update", "--status", "closed", "--json", "--", "x"},
		{"update", "--status", "open", "--assignee", "", "--", "x"},
		{"comment", "--json", "--", "x", "--claim"},
	} {
		if _, err := r.Run(context.Background(), args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if got := recordedArgv(t, argvFile); strings.Contains(got, "--actor") {
			t.Errorf("%v: unexpected --actor in %q", args, got)
		}
	}
}

// An explicit --actor in the call is kept, not duplicated or overridden.
func TestCLIRunner_withActor_ExplicitActorKept(t *testing.T) {
	r := &CLIRunner{Getenv: fakeEnv(map[string]string{EnvActor: "env-actor"})}
	got, err := r.withActor([]string{"update", "--claim", "--actor", "mine", "--", "x"})
	if err != nil || strings.Join(got, " ") != "update --claim --actor mine -- x" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// TestCLIRunner_FlagDirBeatsBothEnvVars is the --beads-dir precedence proof
// (bead pg2-91y12): the flag lands in CLIRunner.Dir, which must win over
// $PG_CONNECTOR_ISSUE_BEADS_DIR, which wins over $BEADS_DIR.
func TestCLIRunner_FlagDirBeatsBothEnvVars(t *testing.T) {
	env := fakeEnv(map[string]string{EnvWorkspaceDir: "/env/pg-connector", envBeadsDir: "/env/bd"})
	flagRunner := &CLIRunner{Dir: "/flag/dir", Getenv: env}
	if dir, err := flagRunner.Workspace(); err != nil || dir != "/flag/dir" {
		t.Fatalf("flag runner Workspace = %q, %v; want /flag/dir", dir, err)
	}
	envRunner := &CLIRunner{Getenv: env}
	if dir, err := envRunner.Workspace(); err != nil || dir != "/env/pg-connector" {
		t.Fatalf("env runner Workspace = %q, %v; want /env/pg-connector", dir, err)
	}
}

// TestErrWorkspaceNotConfigured_MentionsFlagKeepsOldText keeps the existing
// substring consumers match (pg-rescue) while advertising the flag.
func TestErrWorkspaceNotConfigured_MentionsFlagKeepsOldText(t *testing.T) {
	msg := ErrWorkspaceNotConfigured.Error()
	for _, want := range []string{"bd workspace not configured", EnvWorkspaceDir, "--beads-dir"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

// ----------------------------------------------------------------------
// bd budget wiring (design 4.4): a slot and a window entry before every bd
// ----------------------------------------------------------------------

// fakeBDScript records one marker file per start (so a test can wait until a
// call is in flight), records whether the slot's descriptor reached it as fd 3,
// sleeps FAKE_BD_SLEEP seconds and prints an empty JSON object.
const fakeBDScript = `#!/bin/sh
: > "$FAKE_BD_STARTED/start-$$"
if ( : >&3 ) 2>/dev/null; then : > "$FAKE_BD_STARTED/fd3-$$"; fi
sleep "${FAKE_BD_SLEEP:-0}"
echo '{}'
`

// fakeBD puts the fake bd first on PATH and returns the directory it records
// its starts in.
func fakeBD(t *testing.T) (startedDir string) {
	t.Helper()
	binDir := t.TempDir()
	startedDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(fakeBDScript), 0o700); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_BD_STARTED", startedDir)
	t.Setenv("FAKE_BD_SLEEP", "0")
	return startedDir
}

// countStarted counts the fake bd's marker files with the given prefix.
func countStarted(t *testing.T, dir, prefix string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			n++
		}
	}
	return n
}

// waitStarted waits (test code, bounded) until n fake bd processes started.
func waitStarted(t *testing.T, dir string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for countStarted(t, dir, "start-") < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d fake bd processes started", countStarted(t, dir, "start-"), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func budgetRunner(t *testing.T, cfg budget.Config) *CLIRunner {
	t.Helper()
	if cfg.StateDir == "" {
		cfg.StateDir = t.TempDir()
	}
	b, err := budget.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &CLIRunner{Dir: t.TempDir(), Budget: b}
}

func TestIsWriteVerb(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"create", "--json"}, true},
		{[]string{"update", "--status", "closed"}, true},
		{[]string{"close", "x"}, true},
		{[]string{"comment", "--json", "--", "id", "body"}, true},
		{[]string{"show", "--readonly"}, false},
		{[]string{"list", "--json"}, false},
		{[]string{"search", "--query=update"}, false},
		{[]string{"dep", "list", "update"}, false},
		{[]string{"--json", "create"}, true},
		{[]string{"--json"}, false},
		{nil, false},
	} {
		if got := isWriteVerb(tc.args); got != tc.want {
			t.Errorf("isWriteVerb(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// Case 1: over bd_max_per_minute a call waits the bound and the backend then
// answers unavailable. The window is a full minute so the cap cannot slide
// open under host load.
func TestCLIRunner_Budget_PerMinuteCapDenies(t *testing.T) {
	started := fakeBD(t)
	r := budgetRunner(t, budget.Config{MaxConcurrent: 5, MaxPerMinute: 2, WaitBound: 100 * time.Millisecond})
	for i := 0; i < 2; i++ {
		if _, err := r.Run(context.Background(), "show", "--json"); err != nil {
			t.Fatalf("call %d within the cap: %v", i, err)
		}
	}
	begin := time.Now()
	err := New(r).Comment(context.Background(), "x-1", "hello")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("over the cap: err = %v, want ErrUnavailable", err)
	}
	if want := "bd budget denied (retryable): "; !strings.HasPrefix(strings.TrimPrefix(err.Error(), scriptout.ErrUnavailable.Error()+": "), want) {
		t.Errorf("message = %q, want it to start with %q", err.Error(), want)
	}
	if time.Since(begin) < 90*time.Millisecond {
		t.Errorf("denied after %v, want a wait of about the 100ms bound", time.Since(begin))
	}
	if got := countStarted(t, started, "start-"); got != 2 {
		t.Fatalf("%d bd processes started, want 2: a denied call must not start bd", got)
	}
	if got := countStarted(t, started, "fd3-"); got != 2 {
		t.Errorf("slot descriptor reached %d of 2 bd children, want all", got)
	}
}

// Case 1, second half: once the window slides a call is admitted. The call
// waits for the slide inside the wait bound, so no host load can race it.
func TestCLIRunner_Budget_PerMinuteWindowSlides(t *testing.T) {
	started := fakeBD(t)
	r := budgetRunner(t, budget.Config{MaxConcurrent: 5, MaxPerMinute: 1, Window: 300 * time.Millisecond, WaitBound: 30 * time.Second})
	for i := 0; i < 2; i++ {
		if _, err := r.Run(context.Background(), "show", "--json"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := countStarted(t, started, "start-"); got != 2 {
		t.Errorf("%d bd processes started, want 2", got)
	}
}

// Case 2: over bd_max_concurrent the same.
func TestCLIRunner_Budget_ConcurrencyCap(t *testing.T) {
	started := fakeBD(t)
	t.Setenv("FAKE_BD_SLEEP", "0.6")
	r := budgetRunner(t, budget.Config{MaxConcurrent: 2, WaitBound: 100 * time.Millisecond})
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := r.Run(context.Background(), "show", "--json")
			done <- err
		}()
	}
	waitStarted(t, started, 2)
	t.Setenv("FAKE_BD_SLEEP", "0")
	err := New(r).Comment(context.Background(), "x-1", "hello")
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "bd budget denied (retryable): ") {
		t.Fatalf("over the cap: err = %v, want the retryable budget answer", err)
	}
	if got := countStarted(t, started, "start-"); got != 2 {
		t.Fatalf("%d bd processes started, want 2", got)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("admitted call: %v", err)
		}
	}
	// Both children exited, so the slots are free again.
	if _, err := r.Run(context.Background(), "show", "--json"); err != nil {
		t.Fatalf("after the slots freed: %v", err)
	}
}

// Case 4: a write is admitted while reads have used every non-reserved slot.
func TestCLIRunner_Budget_WriteReserve(t *testing.T) {
	started := fakeBD(t)
	t.Setenv("FAKE_BD_SLEEP", "0.6")
	r := budgetRunner(t, budget.Config{MaxConcurrent: 3, WriteReserve: 1, WaitBound: 100 * time.Millisecond})
	r.Actor = "budget-test" // withActor prefixes --actor; the verb is still found
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := r.Run(context.Background(), "list", "--json")
			done <- err
		}()
	}
	waitStarted(t, started, 2)
	t.Setenv("FAKE_BD_SLEEP", "0")
	if _, err := r.Run(context.Background(), "list", "--json"); !errors.Is(err, budget.ErrDenied) {
		t.Fatalf("third read: err = %v, want ErrDenied (the last slot is reserved)", err)
	}
	if _, err := r.Run(context.Background(), "update", "--status", "closed", "--json"); err != nil {
		t.Fatalf("a write must use the reserved slot: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("read: %v", err)
		}
	}
}

// Case 6: an unwritable state directory makes bd run unthrottled.
func TestCLIRunner_Budget_UnwritableStateDirFailsOpen(t *testing.T) {
	started := fakeBD(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := budgetRunner(t, budget.Config{
		MaxConcurrent: 1, MaxPerMinute: 1, WaitBound: 0,
		StateDir: filepath.Join(blocker, "state"), // a path below a regular file
	})
	for i := 0; i < 3; i++ {
		if _, err := r.Run(context.Background(), "show", "--json"); err != nil {
			t.Fatalf("call %d: %v (an unusable state directory must fail open)", i, err)
		}
	}
	if got := countStarted(t, started, "start-"); got != 3 {
		t.Errorf("%d bd processes started, want 3", got)
	}
	if got := countStarted(t, started, "fd3-"); got != 0 {
		t.Errorf("a fail-open call passed a slot descriptor to %d children", got)
	}
}

// With no Budget (every existing test) bd gets no extra file and nothing is
// throttled.
func TestCLIRunner_NilBudget_PassesNoExtraFile(t *testing.T) {
	started := fakeBD(t)
	r := &CLIRunner{Dir: t.TempDir()}
	for i := 0; i < 5; i++ {
		if _, err := r.Run(context.Background(), "show", "--json"); err != nil {
			t.Fatal(err)
		}
	}
	if got := countStarted(t, started, "start-"); got != 5 {
		t.Errorf("%d started, want 5", got)
	}
	if got := countStarted(t, started, "fd3-"); got != 0 {
		t.Errorf("%d children saw an extra descriptor, want 0", got)
	}
}

// A refused workspace or actor never takes a slot: the budget is consulted only
// when bd is about to run.
func TestCLIRunner_Budget_NotConsultedWhenBDWouldNotRun(t *testing.T) {
	dir := t.TempDir()
	b, err := budget.New(budget.Config{MaxConcurrent: 1, MaxPerMinute: 1, StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	r := &CLIRunner{Getenv: fakeEnv(nil), Budget: b}
	if _, err := r.Run(context.Background(), "show"); !errors.Is(err, ErrWorkspaceNotConfigured) {
		t.Fatalf("err = %v, want ErrWorkspaceNotConfigured", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("state dir has %d entries after a refused call, want 0", len(entries))
	}
}
