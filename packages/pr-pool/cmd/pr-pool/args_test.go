package main

import (
	"errors"
	"flag"
	"reflect"
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
		{"no-args-drains", []string{"pr-pool"}, routeDrain},
		{"drain-subcommand", []string{"pr-pool", "drain"}, routeDrain},
		{"version-subcommand", []string{"pr-pool", "version"}, routeVersion},
		{"version-long-flag", []string{"pr-pool", "--version"}, routeVersion},
		{"version-short-flag", []string{"pr-pool", "-v"}, routeVersion},
		{"help-word", []string{"pr-pool", "help"}, routeHelp},
		{"help-long-flag", []string{"pr-pool", "--help"}, routeHelp},
		{"help-short-flag", []string{"pr-pool", "-h"}, routeHelp},
		{"unknown-flag-is-usage-error", []string{"pr-pool", "--bogus"}, routeUsageErr},
		{"unknown-subcommand-is-usage-error", []string{"pr-pool", "bogus"}, routeUsageErr},
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
		{[]string{"pr-pool", "--version", "drain"}, routeVersion},
		{[]string{"pr-pool", "-v", "anything", "else"}, routeVersion},
		{[]string{"pr-pool", "version", "drain"}, routeVersion},
		{[]string{"pr-pool", "--help", "drain"}, routeHelp},
		{[]string{"pr-pool", "-h", "whatever"}, routeHelp},
		{[]string{"pr-pool", "help", "drain"}, routeHelp},
	}
	for _, tc := range cases {
		if got := route(tc.argv).kind; got != tc.want {
			t.Errorf("route(%v).kind = %v, want %v (must short-circuit, not error on trailing args)", tc.argv, got, tc.want)
		}
	}
}

// pg2-h6i2: --version/--help are GLOBAL, not drain flags. `drain --version` is an
// unknown flag for the drain subcommand (exit 2), matching cobra/docker; only
// `drain --help`/`-h` is honoured (help is conventionally available per command).
func TestParseDrainArgs_versionIsUnknownButHelpWorks(t *testing.T) {
	if got := parseDrainArgs([]string{"--version"}).kind; got != routeUsageErr {
		t.Errorf("drain --version should be routeUsageErr (unknown flag), got %v", got)
	}
	if got := parseDrainArgs([]string{"-h"}).kind; got != routeHelp {
		t.Errorf("drain -h should be routeHelp, got %v", got)
	}
}

func TestRoute_drainPassesRemainingArgs(t *testing.T) {
	r := route([]string{"pr-pool", "drain", "--cwd", "/p"})
	if r.kind != routeDrain {
		t.Fatalf("kind = %v, want routeDrain", r.kind)
	}
	if !reflect.DeepEqual(r.rest, []string{"--cwd", "/p"}) {
		t.Errorf("rest = %v, want [--cwd /p]", r.rest)
	}
}

// parseDrainArgs must short-circuit (proceed=false) on a help request or any
// parse error, so runDrain never reaches config.Load/precheck/DrainOnce — i.e.
// no Claude session dispatch and no tmux teardown on a parse error (pg2-52rn).
func TestParseDrainArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want routeKind
	}{
		{"no-args-proceeds", nil, routeDrain},
		{"help-flag", []string{"-h"}, routeHelp},
		{"help-long-flag", []string{"--help"}, routeHelp},
		{"unknown-flag", []string{"--bogus"}, routeUsageErr},
		{"unexpected-positional", []string{"extra"}, routeUsageErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDrainArgs(tc.args).kind; got != tc.want {
				t.Errorf("parseDrainArgs(%v).kind = %v, want %v", tc.args, got, tc.want)
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
		{"run-role-unknown-role", []string{"pr-pool", "run-role", "bogus", "zr-1"}, routeUsageErr},
		{"run-role-extra-arg", []string{"pr-pool", "run-role", "feedback", "zr-1", "x"}, routeUsageErr},
		{"run-query-ok", []string{"pr-pool", "run-query", "worker"}, routeRunQuery},
		{"run-query-missing-role", []string{"pr-pool", "run-query"}, routeUsageErr},
		{"run-query-unknown-role", []string{"pr-pool", "run-query", "bogus"}, routeUsageErr},
		{"run-query-extra-arg", []string{"pr-pool", "run-query", "worker", "extra"}, routeUsageErr},
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
