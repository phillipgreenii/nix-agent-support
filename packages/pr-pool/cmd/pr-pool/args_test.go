package main

import (
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantPos []string
		wantCwd string
	}{
		{"flags-first", []string{"--cwd", "/p", "alpha"}, []string{"alpha"}, "/p"},
		{"flag-after-positional", []string{"alpha", "--cwd", "/p"}, []string{"alpha"}, "/p"},
		{"no-flags", []string{"alpha"}, []string{"alpha"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			cwd := fs.String("cwd", "", "")
			pos, err := parseInterspersed(fs, tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(pos, tc.wantPos) {
				t.Errorf("positionals = %v, want %v", pos, tc.wantPos)
			}
			if *cwd != tc.wantCwd {
				t.Errorf("--cwd = %q, want %q", *cwd, tc.wantCwd)
			}
		})
	}
}

// parseInterspersed must surface parse failures rather than swallowing them; a
// dropped error is what let -h and unknown flags fall through to a real drain
// (pg2-52rn).
func TestParseInterspersed_propagatesErrors(t *testing.T) {
	t.Run("help-flag", func(t *testing.T) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(discardWriter{})
		if _, err := parseInterspersed(fs, []string{"-h"}); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("want flag.ErrHelp, got %v", err)
		}
	})
	t.Run("unknown-flag", func(t *testing.T) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(discardWriter{})
		if _, err := parseInterspersed(fs, []string{"--bogus"}); err == nil || errors.Is(err, flag.ErrHelp) {
			t.Errorf("want a non-help parse error, got %v", err)
		}
	})
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestRoute(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want routeKind
	}{
		{"no-args-is-usage-error", []string{"pr-pool"}, routeUsageErr},
		{"drain-subcommand-is-deprecated-alias", []string{"pr-pool", "drain"}, routeRunUntilIdle},
		{"run-subcommand", []string{"pr-pool", "run"}, routeRun},
		{"run-until-idle-subcommand", []string{"pr-pool", "run-until-idle"}, routeRunUntilIdle},
		{"version-subcommand", []string{"pr-pool", "version"}, routeVersion},
		{"version-long-flag", []string{"pr-pool", "--version"}, routeVersion},
		{"version-short-flag", []string{"pr-pool", "-v"}, routeVersion},
		{"help-word", []string{"pr-pool", "help"}, routeHelp},
		{"help-long-flag", []string{"pr-pool", "--help"}, routeHelp},
		{"help-short-flag", []string{"pr-pool", "-h"}, routeHelp},
		{"unknown-flag-is-usage-error", []string{"pr-pool", "--bogus"}, routeUsageErr},
		{"unknown-subcommand-is-usage-error", []string{"pr-pool", "bogus"}, routeUsageErr},
		{"sessions-subcommand", []string{"pr-pool", "sessions"}, routeSessions},
		{"sessions-with-arg-is-usage-error", []string{"pr-pool", "sessions", "x"}, routeUsageErr},
		{"reconcile-subcommand", []string{"pr-pool", "reconcile"}, routeReconcile},
		{"reconcile-with-arg-is-usage-error", []string{"pr-pool", "reconcile", "x"}, routeUsageErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := route(tc.argv).kind; got != tc.want {
				t.Errorf("route(%v).kind = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

// pg2-h6i2: a leading --version/--help short-circuits and exits 0, ignoring any
// trailing args (GNU/clap/cobra convention) — it must NOT become a usage error.
func TestRoute_versionHelpShortCircuitTrailingArgs(t *testing.T) {
	cases := []struct {
		argv []string
		want routeKind
	}{
		{[]string{"pr-pool", "--version", "run"}, routeVersion},
		{[]string{"pr-pool", "-v", "anything", "else"}, routeVersion},
		{[]string{"pr-pool", "version", "run"}, routeVersion},
		{[]string{"pr-pool", "--help", "run"}, routeHelp},
		{[]string{"pr-pool", "-h", "whatever"}, routeHelp},
		{[]string{"pr-pool", "help", "run"}, routeHelp},
	}
	for _, tc := range cases {
		if got := route(tc.argv).kind; got != tc.want {
			t.Errorf("route(%v).kind = %v, want %v (must short-circuit, not error on trailing args)", tc.argv, got, tc.want)
		}
	}
}

// pg2-h6i2: --version/--help are GLOBAL, not run/run-until-idle flags.
// `run --version` is an unknown flag for the run subcommand (exit 2), matching
// cobra/docker; only `run --help`/`-h` is honoured (help is conventionally
// available per command).
func TestParseRunLikeArgs_versionIsUnknownButHelpWorks(t *testing.T) {
	if got := parseRunLikeArgs(routeRun, []string{"--version"}).kind; got != routeUsageErr {
		t.Errorf("run --version should be routeUsageErr (unknown flag), got %v", got)
	}
	if got := parseRunLikeArgs(routeRun, []string{"-h"}).kind; got != routeHelp {
		t.Errorf("run -h should be routeHelp, got %v", got)
	}
}

// parseRunLikeArgs must short-circuit (proceed=false) on a help request or any
// parse error, so runRun/runRunUntilIdle never reach config.Load/precheck/the
// queue — i.e. no Claude session dispatch and no core boot on a parse error
// (pg2-52rn, carried over from the retired parseDrainArgs).
func TestParseRunLikeArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want routeKind
	}{
		{"no-args-proceeds", nil, routeRunUntilIdle},
		{"help-flag", []string{"-h"}, routeHelp},
		{"help-long-flag", []string{"--help"}, routeHelp},
		{"unknown-flag", []string{"--bogus"}, routeUsageErr},
		{"unexpected-positional", []string{"extra"}, routeUsageErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRunLikeArgs(routeRunUntilIdle, tc.args).kind; got != tc.want {
				t.Errorf("parseRunLikeArgs(routeRunUntilIdle, %v).kind = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestRoute_runSubcommands(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want routeKind
	}{
		{"run-role-ok", []string{"pr-pool", "run-role", "feedback", "zr-1"}, routeRunRole},
		{"run-role-missing-bead", []string{"pr-pool", "run-role", "feedback"}, routeUsageErr},
		// An unknown role NAME now parses OK — it is validated in the handler after
		// config load (arg parse stays config-free, pg2-52rn). A flag-like token is
		// still a missing role.
		{"run-role-unknown-name-parses", []string{"pr-pool", "run-role", "bogus", "zr-1"}, routeRunRole},
		{"run-role-flag-as-role", []string{"pr-pool", "run-role", "--x", "zr-1"}, routeUsageErr},
		{"run-role-extra-arg", []string{"pr-pool", "run-role", "feedback", "zr-1", "x"}, routeUsageErr},
		{"run-query-ok", []string{"pr-pool", "run-query", "worker"}, routeRunQuery},
		{"run-query-missing-role", []string{"pr-pool", "run-query"}, routeUsageErr},
		{"run-query-unknown-name-parses", []string{"pr-pool", "run-query", "bogus"}, routeRunQuery},
		{"run-query-extra-arg", []string{"pr-pool", "run-query", "worker", "extra"}, routeUsageErr},
		{"config-print-defaults", []string{"pr-pool", "config", "--print-defaults"}, routeConfig},
		{"config-show", []string{"pr-pool", "config", "--show"}, routeConfig},
		{"config-no-flag", []string{"pr-pool", "config"}, routeUsageErr},
		{"config-bad-flag", []string{"pr-pool", "config", "--nope"}, routeUsageErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := route(tc.argv).kind; got != tc.want {
				t.Errorf("route(%v).kind = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

func TestParseRunRoleArgs_carriesRoleAndBead(t *testing.T) {
	r := parseRunRoleArgs([]string{"worker", "zr-9"})
	if r.kind != routeRunRole || r.role != "worker" || r.bead != "zr-9" {
		t.Errorf("parseRunRoleArgs = %+v, want routeRunRole role=worker bead=zr-9", r)
	}
}

func TestParseRunQueryArgs_carriesRole(t *testing.T) {
	r := parseRunQueryArgs([]string{"feedback"})
	if r.kind != routeRunQuery || r.role != "feedback" || r.bead != "" {
		t.Errorf("parseRunQueryArgs = %+v, want routeRunQuery role=feedback bead empty", r)
	}
}

// parseRunLikeArgs collects repeated --only/--disable occurrences into
// routeResult.only/disable (STORY-OP-3, DEC-CLI-1); it does NOT fold in
// PR_POOL_ONLY/PR_POOL_DISABLE (that happens later, in resolveSelectors).
func TestParseRunLikeArgs_collectsRepeatedSelectorFlags(t *testing.T) {
	r := parseRunLikeArgs(routeRun, []string{"--only", "role:a", "--only", "query:b", "--disable", "role:c"})
	if r.kind != routeRun {
		t.Fatalf("kind = %v, want routeRun", r.kind)
	}
	if want := []string{"role:a", "query:b"}; !reflect.DeepEqual(r.only, want) {
		t.Errorf("only = %v, want %v", r.only, want)
	}
	if want := []string{"role:c"}; !reflect.DeepEqual(r.disable, want) {
		t.Errorf("disable = %v, want %v", r.disable, want)
	}
}

// route() itself reaches the same selector flags through the full dispatch
// for both run and run-until-idle.
func TestRoute_runAndRunUntilIdleAcceptSelectorFlags(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want routeKind
	}{
		{"run", []string{"pr-pool", "run", "--only", "role:a", "--disable", "query:b"}, routeRun},
		{"run-until-idle", []string{"pr-pool", "run-until-idle", "--only", "role:a"}, routeRunUntilIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := route(tc.argv)
			if r.kind != tc.want {
				t.Fatalf("route(%v).kind = %v, want %v", tc.argv, r.kind, tc.want)
			}
			if len(r.only) == 0 {
				t.Errorf("route(%v).only is empty, want it to carry --only", tc.argv)
			}
		})
	}
}

// helpText-mentions test (operator-command-surface rule; pattern:
// push_inject_test.go's TestRoute_pushInject). PR_POOL_ACTIVITY_RING
// (internal/activity's ring buffer capacity, Task 3.4) is new operator-
// facing surface — helpText MUST advertise it.
func TestHelpText_MentionsActivityRingEnvVar(t *testing.T) {
	if !strings.Contains(helpText, "PR_POOL_ACTIVITY_RING") {
		t.Fatal("helpText does not mention PR_POOL_ACTIVITY_RING")
	}
}
