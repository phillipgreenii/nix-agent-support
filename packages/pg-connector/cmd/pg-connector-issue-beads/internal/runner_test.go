package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
