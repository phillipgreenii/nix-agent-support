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
		{"no-args-is-usage-error", []string{"pg-router"}, routeUsageErr},
		{"drain-subcommand-is-retired", []string{"pg-router", "drain"}, routeUsageErr},
		{"run-subcommand", []string{"pg-router", "run"}, routeRun},
		{"run-until-idle-subcommand", []string{"pg-router", "run-until-idle"}, routeRunUntilIdle},
		{"version-subcommand", []string{"pg-router", "version"}, routeVersion},
		{"version-long-flag", []string{"pg-router", "--version"}, routeVersion},
		{"version-short-flag", []string{"pg-router", "-v"}, routeVersion},
		{"help-word", []string{"pg-router", "help"}, routeHelp},
		{"help-long-flag", []string{"pg-router", "--help"}, routeHelp},
		{"help-short-flag", []string{"pg-router", "-h"}, routeHelp},
		{"unknown-flag-is-usage-error", []string{"pg-router", "--bogus"}, routeUsageErr},
		{"unknown-subcommand-is-usage-error", []string{"pg-router", "bogus"}, routeUsageErr},
		{"sessions-subcommand", []string{"pg-router", "sessions"}, routeSessions},
		{"sessions-with-arg-is-usage-error", []string{"pg-router", "sessions", "x"}, routeUsageErr},
		{"reconcile-subcommand", []string{"pg-router", "reconcile"}, routeReconcile},
		{"reconcile-with-arg-is-usage-error", []string{"pg-router", "reconcile", "x"}, routeUsageErr},
		{"pause-subcommand", []string{"pg-router", "pause"}, routePause},
		{"resume-subcommand", []string{"pg-router", "resume"}, routeResume},
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
		{[]string{"pg-router", "--version", "run"}, routeVersion},
		{[]string{"pg-router", "-v", "anything", "else"}, routeVersion},
		{[]string{"pg-router", "version", "run"}, routeVersion},
		{[]string{"pg-router", "--help", "run"}, routeHelp},
		{[]string{"pg-router", "-h", "whatever"}, routeHelp},
		{[]string{"pg-router", "help", "run"}, routeHelp},
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
		{"run-role-ok", []string{"pg-router", "run-role", "feedback", "zr-1"}, routeRunRole},
		{"run-role-missing-bead", []string{"pg-router", "run-role", "feedback"}, routeUsageErr},
		// An unknown role NAME now parses OK — it is validated in the handler after
		// config load (arg parse stays config-free, pg2-52rn). A flag-like token is
		// still a missing role.
		{"run-role-unknown-name-parses", []string{"pg-router", "run-role", "bogus", "zr-1"}, routeRunRole},
		{"run-role-flag-as-role", []string{"pg-router", "run-role", "--x", "zr-1"}, routeUsageErr},
		{"run-role-extra-arg", []string{"pg-router", "run-role", "feedback", "zr-1", "x"}, routeUsageErr},
		{"run-query-ok", []string{"pg-router", "run-query", "query:worker"}, routeRunQuery},
		{"run-query-missing-query", []string{"pg-router", "run-query"}, routeUsageErr},
		{"run-query-bare-role-form-is-usage-error", []string{"pg-router", "run-query", "worker"}, routeUsageErr},
		{"run-query-extra-arg", []string{"pg-router", "run-query", "query:worker", "extra"}, routeUsageErr},
		{"config-print-defaults", []string{"pg-router", "config", "--print-defaults"}, routeConfig},
		{"config-show", []string{"pg-router", "config", "--show"}, routeConfig},
		{"config-no-flag", []string{"pg-router", "config"}, routeUsageErr},
		{"config-bad-flag", []string{"pg-router", "config", "--nope"}, routeUsageErr},
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

func TestParseRunQueryArgs_carriesQuery(t *testing.T) {
	r := parseRunQueryArgs([]string{"query:feedback"})
	if r.kind != routeRunQuery || r.query != "feedback" || r.role != "" {
		t.Errorf("parseRunQueryArgs = %+v, want routeRunQuery query=feedback role empty", r)
	}
}

// Operator ruling (2026-09-02): the pre-Task-1.5c bare-role form is retired
// outright, with no mapping-diagnostic special case — a token with no
// "query:" prefix is an ordinary usage error like any other.
func TestParseRunQueryArgs_bareRoleFormIsUsageError(t *testing.T) {
	r := parseRunQueryArgs([]string{"feedback"})
	if r.kind != routeUsageErr {
		t.Errorf("parseRunQueryArgs(feedback) = %+v, want routeUsageErr", r)
	}
}

// parsePauseArgs carries a TYPED gate field (Task 1.2b), never re-parsed from
// .rest: an omitted gate defaults to operator-paused, an explicit known gate
// name is carried verbatim, and an unknown gate name or a flag-like token is
// a usage error (pg2-52rn's fail-fast-on-bad-input contract).
func TestParsePauseArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantKind routeKind
		wantGate string
	}{
		{"no-args-defaults-operator-paused", nil, routePause, gateOperatorPaused},
		{"explicit-operator-paused", []string{"operator-paused"}, routePause, gateOperatorPaused},
		{"explicit-cicd-down", []string{"cicd-down"}, routePause, gateCICDDown},
		{"unknown-gate-is-usage-error", []string{"bogus"}, routeUsageErr, ""},
		{"flag-like-token-is-usage-error", []string{"--bogus"}, routeUsageErr, ""},
		{"extra-arg-is-usage-error", []string{"operator-paused", "extra"}, routeUsageErr, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := parsePauseArgs(tc.args)
			if r.kind != tc.wantKind {
				t.Fatalf("parsePauseArgs(%v).kind = %v, want %v", tc.args, r.kind, tc.wantKind)
			}
			if tc.wantKind == routePause && r.gate != tc.wantGate {
				t.Errorf("parsePauseArgs(%v).gate = %q, want %q", tc.args, r.gate, tc.wantGate)
			}
		})
	}
}

// parseResumeArgs carries TYPED gate/allGates fields (Task 1.2b). "resume
// --all <gate>" (both at once) is a usage error — interfaces.md draws no
// meaning for the combination.
func TestParseResumeArgs(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantKind     routeKind
		wantGate     string
		wantAllGates bool
	}{
		{"no-args-defaults-operator-paused", nil, routeResume, gateOperatorPaused, false},
		{"explicit-cicd-down", []string{"cicd-down"}, routeResume, gateCICDDown, false},
		{"all-flag", []string{"--all"}, routeResume, "", true},
		{"all-and-gate-is-usage-error", []string{"--all", "operator-paused"}, routeUsageErr, "", false},
		{"gate-and-all-is-usage-error", []string{"operator-paused", "--all"}, routeUsageErr, "", false},
		{"unknown-gate-is-usage-error", []string{"bogus"}, routeUsageErr, "", false},
		{"extra-arg-is-usage-error", []string{"operator-paused", "extra"}, routeUsageErr, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := parseResumeArgs(tc.args)
			if r.kind != tc.wantKind {
				t.Fatalf("parseResumeArgs(%v).kind = %v, want %v", tc.args, r.kind, tc.wantKind)
			}
			if tc.wantKind == routeResume {
				if r.gate != tc.wantGate {
					t.Errorf("parseResumeArgs(%v).gate = %q, want %q", tc.args, r.gate, tc.wantGate)
				}
				if r.allGates != tc.wantAllGates {
					t.Errorf("parseResumeArgs(%v).allGates = %v, want %v", tc.args, r.allGates, tc.wantAllGates)
				}
			}
		})
	}
}

// parseRunLikeArgs collects repeated --only/--disable occurrences into
// routeResult.only/disable (STORY-OP-3, DEC-CLI-1); it does NOT fold in
// PG_ROUTER_ONLY/PG_ROUTER_DISABLE (that happens later, in resolveSelectors).
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
		{"run", []string{"pg-router", "run", "--only", "role:a", "--disable", "query:b"}, routeRun},
		{"run-until-idle", []string{"pg-router", "run-until-idle", "--only", "role:a"}, routeRunUntilIdle},
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
// push_inject_test.go's TestRoute_pushInject). PG_ROUTER_ACTIVITY_RING
// (internal/activity's ring buffer capacity, Task 3.4) is new operator-
// facing surface — helpText MUST advertise it.
func TestHelpText_MentionsActivityRingEnvVar(t *testing.T) {
	if !strings.Contains(helpText, "PG_ROUTER_ACTIVITY_RING") {
		t.Fatal("helpText does not mention PG_ROUTER_ACTIVITY_RING")
	}
}

// Same operator-command-surface rule as above: PG_ROUTER_NO_CONFIG_WARN
// (internal/config's missing-config WARN opt-out, pg2-xl659) is new
// operator-facing surface — helpText MUST advertise it.
func TestHelpText_MentionsNoConfigWarnEnvVar(t *testing.T) {
	if !strings.Contains(helpText, "PG_ROUTER_NO_CONFIG_WARN") {
		t.Fatal("helpText does not mention PG_ROUTER_NO_CONFIG_WARN")
	}
}
