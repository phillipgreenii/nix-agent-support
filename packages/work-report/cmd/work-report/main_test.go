package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootDeclaresGlobalFlags(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"config", "output", "store"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("root command lacks persistent flag --%s", name)
		}
	}
	if root.Use != "work-report" {
		t.Errorf("root Use = %q, want work-report", root.Use)
	}
}

func TestHelpListsGlobalFlags(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"--config", "--output", "--store"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("help output lacks %s:\n%s", want, out.String())
		}
	}
}

func TestRegisterCommandAndGlobalFlags(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })

	type seen struct{ config, output, store string }
	var got seen
	registerCommand(func() *cobra.Command {
		return &cobra.Command{
			Use: "probe",
			RunE: func(cmd *cobra.Command, _ []string) error {
				got.config, got.output, got.store = globalFlags(cmd)
				return nil
			},
		}
	})

	root := newRootCmd()
	root.SetArgs([]string{"probe", "--config", "/c.yaml", "--output", "json", "--store", "/s.db"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := (seen{"/c.yaml", "json", "/s.db"}); got != want {
		t.Errorf("globalFlags = %+v, want %+v", got, want)
	}

	got = seen{"x", "x", "x"}
	root = newRootCmd()
	root.SetArgs([]string{"probe"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := (seen{}); got != want {
		t.Errorf("unset globalFlags = %+v, want all empty", got)
	}
}

// TestMain turns the test binary into a fake generator (WR_FAKE_GEN) or into
// the work-report command itself (WR_AS_MAIN), so no generator and no binary
// has to be built.
func TestMain(m *testing.M) {
	if os.Getenv(envFakeGen) != "" {
		os.Exit(fakeGenMain())
	}
	if os.Getenv(envAsMain) == "1" {
		main()
		os.Exit(0)
	}
	if !invokedAsTest(os.Args) {
		// Neither `go test` nor a re-executed role: a child that lost WR_FAKE_GEN
		// or WR_AS_MAIN. Running the suite here would spawn the same child
		// again, without end (each exec puts its child in its own process
		// group, so killing the top test process does not reap the descendants).
		fmt.Fprintln(os.Stderr, "work-report.test: refusing to run the suite as a child without", envFakeGen, "or", envAsMain)
		os.Exit(exitLostRole)
	}
	os.Exit(m.Run())
}

// exitLostRole is the exit status of a re-executed test binary that has no
// role (see TestMain).
const exitLostRole = 93

// invokedAsTest reports whether args carry a test flag. `go test` and a
// directly run test binary always pass -test.* flags; a re-executed child is
// started with its registered command and arguments only.
func invokedAsTest(args []string) bool {
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-test.") {
			return true
		}
	}
	return false
}

func TestExitCodeSeam(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain error", errors.New("boom"), 1},
		{"exit error", &exitError{code: 3}, 3},
		{"wrapped exit error", fmt.Errorf("ctx: %w", &exitError{code: 2}), 2},
		{"usage error", usageErrorf("bad flag"), 2},
		{"wrapped usage error", fmt.Errorf("ctx: %w", usageErrorf("bad")), 2},
	}
	for _, tc := range cases {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("%s: exitCode = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Flag errors, argument errors, unknown subcommands and bad --range or
// --output values exit 2 for report and config only; pull and the root keep 1.
func TestUsageErrorsExitTwoOnlyForReportAndConfig(t *testing.T) {
	isolate(t)
	storePath := seedQueryStore(t)
	cfg := writeCfg(t, "timezone: UTC\n")
	base := []string{"--config", cfg, "--store", storePath}
	for _, args := range [][]string{
		{"report", "--bogus"},
		{"report", "a", "b"},
		{"report", "--range", "nonsense"},
		{"report", "--output", "yaml"},
		{"report", "--generator", "x", "somepreset"},
		{"report", "--print", "--output", "json"},
		{"report", "nosuchpreset"},
		{"config", "bogus"},
		{"config", "show", "--bogus"},
		{"config", "show", "extra"},
		{"config", "validate", "--output", "yaml"},
		{"config", "pg-router-query", "--bogus"},
	} {
		_, err := runCLI(t, append(append([]string{}, base...), args...)...)
		if err == nil {
			t.Errorf("%v: want an error", args)
			continue
		}
		if got := exitCode(err); got != 2 {
			t.Errorf("%v: exit %d (%v), want 2", args, got, err)
		}
	}
	for _, args := range [][]string{
		{"pull", "--bogus"},
		{"pull", "--range", "next-fortnight"},
		{"status", "--bogus"},
		{"query", "--bogus"},
		{"nosuchverb"},
	} {
		_, err := runCLI(t, append(append([]string{}, base...), args...)...)
		if err == nil {
			t.Errorf("%v: want an error", args)
			continue
		}
		if got := exitCode(err); got != 1 {
			t.Errorf("%v: exit %d (%v), want 1", args, got, err)
		}
	}
}

// runMain runs the real command line in a child process (this test binary
// with WR_AS_MAIN=1) and returns its stdout, stderr and exit status.
func runMain(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), envAsMain+"=1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errOut.String(), code
}

// The process exit status, as the shell sees it: 0 valid, 2 usage error, 3 a
// problem or an outcome.
func TestProcessExitStatuses(t *testing.T) {
	isolate(t)
	fakeConnector(t, "fine", 0)
	good := writeCfg(t, "timezone: UTC\n")
	bad := writeCfg(t, "timezone: UTC\ngenerators:\n  ghost:\n    command: [\"/nonexistent/ghost\"]\nreports:\n  r:\n    generator: ghost\n")
	storePath := seedQueryStore(t)

	cases := []struct {
		name string
		args []string
		want int
		in   string // a substring of stderr
	}{
		{"valid config", []string{"--config", good, "config", "validate"}, 0, ""},
		{"report baseline", []string{"--config", good, "--store", storePath, "report", "--range", "2026-03-01", "--output-dir", t.TempDir()}, 0, ""},
		{"usage: bad flag", []string{"--config", good, "report", "--bogus"}, 2, "unknown flag"},
		{"usage: hidden --kind", []string{"--config", good, "report", "--kind", "baseline"}, 2, "--kind was removed"},
		{"usage: hidden --out", []string{"--config", good, "report", "--out", "x"}, 2, "--out was removed"},
		{"usage: unknown preset", []string{"--config", good, "report", "nosuch"}, 2, "work-report config show"},
		{"problem: bad generator", []string{"--config", bad, "config", "validate"}, 3, ""},
		{"outcome: no such generator", []string{"--config", good, "--store", storePath, "report", "--generator", "nosuch"}, 3, "nosuch"},
		{"pull keeps 1 for flag errors", []string{"--config", good, "pull", "--bogus"}, 1, "unknown flag"},
		{"unexpected: config does not load", []string{"--config", filepath.Join(t.TempDir(), "absent.yaml"), "report"}, 1, "absent.yaml"},
	}
	for _, tc := range cases {
		_, stderr, code := runMain(t, tc.args...)
		if code != tc.want {
			t.Errorf("%s: exit %d, want %d\nstderr: %s", tc.name, code, tc.want, stderr)
		}
		if tc.in != "" && !strings.Contains(stderr, tc.in) {
			t.Errorf("%s: stderr lacks %q:\n%s", tc.name, tc.in, stderr)
		}
	}
}

// A re-executed test binary that lost its role must refuse at once instead of
// running the suite, which would re-execute itself without end.
func TestChildWithoutARoleRefusesToRunTheSuite(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "status")
	cmd.Env = []string{} // non-nil and empty: neither WR_FAKE_GEN nor WR_AS_MAIN
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != exitLostRole {
		t.Fatalf("exit = %v, want status %d\n%s", err, exitLostRole, out)
	}
}

func TestInvokedAsTestRecognizesOnlyTestFlags(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"x", "status"}, false},
		{[]string{"x", "-test.v"}, true},
		{[]string{"x", "-test"}, false},
		{[]string{"-test.v"}, false},
	} {
		if got := invokedAsTest(c.args); got != c.want {
			t.Errorf("invokedAsTest(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
