package main

import (
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/corecall"
	"github.com/phillipgreenii/pg-router-handlers/internal/originguard"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router/conformance"
)

// ambientEnvInputs are the process-environment variables this module reads as
// fallback defaults for its own flags and for the ccpool pool it targets. On a
// deployed machine (or inside a pn-managed shell) they point at the LIVE launch
// config, whose originProbe watches a real repo, so a test that does not pass
// its own flag silently picks the live config up: dispatch then declines with
// "origin-unavailable" instead of the branch the test targets (bead pg2-sljo4).
var ambientEnvInputs = []string{
	rolefile.EnvConfig,
	envQueryConfig,
	corecall.EnvSocket,
	corecall.EnvToken,
	"CCPOOL_POOL",
}

// isolateAmbientEnv unsets every ambientEnvInputs variable for the whole test
// process so no test can inherit a developer's or a deployed host's live
// configuration. A test that needs one sets it explicitly (t.Setenv).
func isolateAmbientEnv() {
	for _, k := range ambientEnvInputs {
		_ = os.Unsetenv(k)
	}
}

// adapterRunEnv makes the test binary stand in for the adapter: with it set to
// "1" TestMain runs the real run(os.Args[1:]) (the real subcommand switch, flag
// parsing and signal handling) instead of the tests.
const adapterRunEnv = "PG_ROUTER_HANDLER_CCPOOL_TEST_RUN"

// TestMain points XDG_STATE_HOME at a throwaway directory so that any test that
// builds production deps (dispatchlib.BuildDeps opens <stateDir>/events.jsonl)
// never writes into the developer's real handler state dir, and drops the
// ambient configuration inputs (see isolateAmbientEnv).
func TestMain(m *testing.M) {
	if os.Getenv(adapterRunEnv) == "1" {
		os.Exit(run(os.Args[1:]))
	}
	if os.Getenv(brokenPipeBareEnv) == "1" {
		os.Exit(runBareWrite())
	}
	isolateAmbientEnv()
	// No test depends on a pg-origin-guard installed on the machine running it.
	newGuardCheck = func(config.Config) originguard.Check { return nil }
	// No test depends on lsof: the closed-bead teardown sees no live process
	// unless a test sets its own probe.
	teardownProbe = func(context.Context) ([]string, error) { return nil, nil }
	dir, err := os.MkdirTemp("", "ccpool-adapter-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestIsolateAmbientEnv is the regression guard for pg2-sljo4.
func TestIsolateAmbientEnv(t *testing.T) {
	for _, k := range ambientEnvInputs {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s = %q leaked into the test process; TestMain must call isolateAmbientEnv", k, v)
		}
	}
	for _, k := range ambientEnvInputs {
		t.Setenv(k, "/hostile/ambient/value")
	}
	isolateAmbientEnv()
	for _, k := range ambientEnvInputs {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("isolateAmbientEnv left %s set to %q", k, v)
		}
	}
}

func TestRun_SubcommandSet(t *testing.T) {
	if got := run(nil); got != conformance.ExitUsage {
		t.Errorf("no subcommand: exit %d, want %d", got, conformance.ExitUsage)
	}
	for _, ok := range []string{"help", "version"} {
		if got := run([]string{ok}); got != conformance.ExitOK {
			t.Errorf("%s: exit %d, want 0", ok, got)
		}
	}
	// An unknown subcommand is a usage error, never a stub.
	if got := run([]string{"bogus"}); got != conformance.ExitUsage {
		t.Errorf("bogus: exit %d, want %d", got, conformance.ExitUsage)
	}
	// The ccpool-specific subcommands are real: with no arguments, split and
	// pool-capacity are usage errors from their own parsers (not "unknown
	// subcommand"), and pool-capacity -h prints help with exit 0.
	for _, sub := range []string{"pool-capacity", "janitor", "remove-worktree", "split"} {
		if got := run([]string{sub}); got != conformance.ExitUsage {
			t.Errorf("%s with no arguments: exit %d, want %d", sub, got, conformance.ExitUsage)
		}
	}
	if got := run([]string{"pool-capacity", "-h"}); got != conformance.ExitOK {
		t.Errorf("pool-capacity -h: exit %d, want 0", got)
	}
	if got := run([]string{"split", "--help"}); got != conformance.ExitOK {
		t.Errorf("split --help: exit %d, want 0", got)
	}
	if got := run([]string{"query", "stray"}); got != conformance.ExitUsage {
		t.Errorf("query with an argument: exit %d, want %d", got, conformance.ExitUsage)
	}
	// Requests are JSON on stdin, never arguments.
	for _, sub := range []string{"dispatch", "postStartup", "preShutdown", "validate"} {
		if got := run([]string{sub, "stray"}); got != conformance.ExitUsage {
			t.Errorf("%s with an argument: exit %d, want %d", sub, got, conformance.ExitUsage)
		}
	}
	// origin keeps its old subcommands: the verb is required.
	if got := run([]string{"origin"}); got != conformance.ExitUsage {
		t.Errorf("origin without a verb: exit %d, want %d", got, conformance.ExitUsage)
	}
}

// TestHelp_ListsExactlyItsOwnSubcommands pins the help and usage line to the
// twelve subcommands this binary dispatches, and none of the old binary's
// retired spellings.
func TestHelp_ListsExactlyItsOwnSubcommands(t *testing.T) {
	want := []string{"dispatch", "validate", "origin", "postStartup", "preShutdown", "register", "self-status", "pool-capacity", "janitor", "remove-worktree", "split", "query", "version", "help"}
	for _, sub := range want {
		if !strings.Contains(usageLine, sub) {
			t.Errorf("usageLine lacks %q", sub)
		}
		if !strings.Contains(helpText, "\n  "+sub+" ") {
			t.Errorf("helpText has no entry for %q", sub)
		}
	}
	// Every two-space-indented entry in the help is one of the thirteen.
	entry := regexp.MustCompile(`(?m)^  ([A-Za-z-]+) {2,}\S`)
	known := map[string]bool{}
	for _, w := range want {
		known[w] = true
	}
	for _, m := range entry.FindAllStringSubmatch(helpText, -1) {
		if !known[m[1]] {
			t.Errorf("helpText lists %q, which is not a subcommand of this binary", m[1])
		}
	}
}

// runCapturingStderr runs run(args) with os.Stderr redirected to a pipe and
// returns the exit code and everything written to stderr.
func runCapturingStderr(t *testing.T, args []string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	code := run(args)
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatalf("close stderr pipe writer: %v", err)
	}
	return code, <-done
}

// TestRun_DispatchesEachSubcommandToItsOwnHandler distinguishes a dispatched
// subcommand from the unknown-subcommand fallback: both exit 2, so the exit
// code alone cannot tell them apart. The stderr message does.
func TestRun_DispatchesEachSubcommandToItsOwnHandler(t *testing.T) {
	code, stderr := runCapturingStderr(t, []string{"bogus"})
	if code != conformance.ExitUsage || !strings.Contains(stderr, "unknown subcommand") {
		t.Fatalf("bogus: exit %d, stderr %q; want exit %d and an unknown-subcommand message", code, stderr, conformance.ExitUsage)
	}
	code, stderr = runCapturingStderr(t, []string{"remove-worktree"})
	if code != conformance.ExitUsage {
		t.Errorf("remove-worktree with no path: exit %d, want %d", code, conformance.ExitUsage)
	}
	if strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("remove-worktree fell through to the unknown-subcommand branch: %q", stderr)
	}
	if !strings.Contains(stderr, "remove-worktree: exactly one argument is required") {
		t.Errorf("remove-worktree did not reach its own handler: stderr %q", stderr)
	}
	// With a path that is not a worktree the handler runs and refuses (exit 1),
	// which the unknown-subcommand branch (exit 2) can never produce.
	code, _ = runCapturingStderr(t, []string{"remove-worktree", t.TempDir()})
	if code != 1 {
		t.Errorf("remove-worktree on a non-worktree: exit %d, want 1", code)
	}
}

// TestRun_NoSubcommandFallsThroughToUnknown runs every subcommand the switch
// dispatches with a minimal invocation that stops in its own parser or usage
// path, and asserts none of them reaches the unknown-subcommand fallback.
func TestRun_NoSubcommandFallsThroughToUnknown(t *testing.T) {
	invocations := [][]string{
		{"pool-capacity"},
		{"janitor"},
		{"split"},
		{"remove-worktree"},
		{"origin"},
		{"query", "stray"},
		{"dispatch", "stray"},
		{"postStartup", "stray"},
		{"preShutdown", "stray"},
		{"validate", "stray"},
	}
	for _, args := range invocations {
		_, stderr := runCapturingStderr(t, args)
		if strings.Contains(stderr, "unknown subcommand") {
			t.Errorf("%v reached the unknown-subcommand fallback: %q", args, stderr)
		}
	}
}
