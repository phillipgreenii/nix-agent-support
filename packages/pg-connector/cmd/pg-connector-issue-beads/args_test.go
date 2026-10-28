package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal/budget"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string // substring; "" = no error
		help    bool
	}{
		{name: "none", args: nil, want: ""},
		{name: "separate value", args: []string{"--beads-dir", "/a"}, want: "/a"},
		{name: "equals value", args: []string{"--beads-dir=/a"}, want: "/a"},
		{name: "single dash", args: []string{"-beads-dir", "/a"}, want: "/a"},
		{name: "twice last wins", args: []string{"--beads-dir", "/a", "--beads-dir", "/b"}, want: "/b"},
		{name: "empty value", args: []string{"--beads-dir="}, wantErr: "--beads-dir must not be empty"},
		{name: "blank value", args: []string{"--beads-dir", "  "}, wantErr: "--beads-dir must not be empty"},
		{name: "relative value", args: []string{"--beads-dir", "rel/dir"}, wantErr: "must be an absolute path"},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: "flag provided but not defined"},
		{name: "positional", args: []string{"extra"}, wantErr: `unexpected argument "extra"; the options are --beads-dir DIR`},
		{name: "missing value", args: []string{"--beads-dir"}, wantErr: "needs an argument"},
		{name: "help", args: []string{"-h"}, help: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			opts, err := parseArgs(tc.args, &stderr)
			got := opts.beadsDir
			switch {
			case tc.help:
				if !errors.Is(err, flag.ErrHelp) {
					t.Fatalf("err = %v, want flag.ErrHelp", err)
				}
				if !strings.Contains(stderr.String(), "usage: pg-connector-issue-beads") {
					t.Errorf("stderr = %q, want usage", stderr.String())
				}
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				if !errors.Is(err, errUsage) {
					t.Errorf("err %v should wrap errUsage", err)
				}
			default:
				if err != nil || got != tc.want {
					t.Fatalf("got (%q, %v), want (%q, nil)", got, err, tc.want)
				}
			}
		})
	}
}

func TestNewRunner_ExitCodes(t *testing.T) {
	var stderr bytes.Buffer
	if r, code := newRunner([]string{"-h"}, &stderr); r != nil || code != 0 {
		t.Errorf("-h: got (%v, %d), want (nil, 0)", r, code)
	}
	stderr.Reset()
	r, code := newRunner([]string{"--beads-dir", "relative"}, &stderr)
	if r != nil || code != 2 {
		t.Errorf("bad flag: got (%v, %d), want (nil, 2)", r, code)
	}
	if !strings.Contains(stderr.String(), "pg-connector-issue-beads: --beads-dir") {
		t.Errorf("stderr = %q", stderr.String())
	}
	r, code = newRunner([]string{"--beads-dir", "/example/zr"}, &stderr)
	if r == nil || code != 0 || r.Dir != "/example/zr" {
		t.Errorf("flag: got (%+v, %d)", r, code)
	}
}

// TestNewRunner_FlagBeatsEnv: with both env vars pointing at another
// tracker, the flag-built runner still reports the flag dir (flag >
// PG_CONNECTOR_ISSUE_BEADS_DIR > BEADS_DIR), and the no-flag runner falls
// back to env exactly as before.
func TestNewRunner_FlagBeatsEnv(t *testing.T) {
	t.Setenv(internal.EnvWorkspaceDir, "/env/pg-connector")
	t.Setenv("BEADS_DIR", "/env/bd")
	var stderr bytes.Buffer

	flagRunner, _ := newRunner([]string{"--beads-dir", "/example/zr"}, &stderr)
	if dir, err := flagRunner.Workspace(); err != nil || dir != "/example/zr" {
		t.Fatalf("flag Workspace = %q, %v; want /example/zr", dir, err)
	}
	envRunner, _ := newRunner(nil, &stderr)
	if dir, err := envRunner.Workspace(); err != nil || dir != "/env/pg-connector" {
		t.Fatalf("env Workspace = %q, %v; want /env/pg-connector", dir, err)
	}
}

// TestNewRunner_NoTrackerAtAll_StaysWorkspaceNotConfigured: no flag and no
// env is still the existing refusal, not a silent cwd fallback.
func TestNewRunner_NoTrackerAtAll_StaysWorkspaceNotConfigured(t *testing.T) {
	t.Setenv(internal.EnvWorkspaceDir, "")
	t.Setenv("BEADS_DIR", "")
	r, code := newRunner(nil, &bytes.Buffer{})
	if r == nil || code != 0 {
		t.Fatalf("got (%v, %d)", r, code)
	}
	if _, err := r.Workspace(); !errors.Is(err, internal.ErrWorkspaceNotConfigured) {
		t.Fatalf("err = %v, want ErrWorkspaceNotConfigured", err)
	}
}

// TestCapabilitiesAdvertisesFlagDir: capabilities for a flag-pinned runner
// echo the flag's tracker regardless of env, which is how config validate
// shows which tracker each instance serves.
func TestCapabilitiesAdvertisesFlagDir(t *testing.T) {
	t.Setenv(internal.EnvWorkspaceDir, "/env/pg-connector")
	r, _ := newRunner([]string{"--beads-dir", "/example/zr"}, &bytes.Buffer{})
	table := newDispatchTable(internal.New(r))
	result, err := table[scriptout.OpCapabilities].Handle(t.Context(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := result.(scriptout.CapabilitiesResponse)
	if got := resp.Vocabulary["workspace_dir"]; got != "/example/zr" {
		t.Fatalf("vocabulary.workspace_dir = %#v, want /example/zr", got)
	}
}

func TestParseArgs_BudgetDefaults(t *testing.T) {
	got, err := parseArgs(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want := options{maxConcurrent: 9, maxPerMinute: 92, waitBound: 5 * time.Second, writeReserve: 2}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	// A tracker flag alone leaves the budget defaults alone.
	got, err = parseArgs([]string{"--beads-dir", "/a"}, &bytes.Buffer{})
	if err != nil || got.beadsDir != "/a" || got.maxConcurrent != 9 || got.maxPerMinute != 92 || got.waitBound != 5*time.Second || got.writeReserve != 2 || got.budgetGroup != "" {
		t.Fatalf("beads-dir only = %+v, %v", got, err)
	}
}

func TestParseArgs_BudgetFlags(t *testing.T) {
	want := options{beadsDir: "/a", maxConcurrent: 4, maxPerMinute: 30, waitBound: 750 * time.Millisecond, writeReserve: 1, budgetGroup: "shared"}
	for name, args := range map[string][]string{
		"separate": {"--bd-max-concurrent", "4", "--bd-max-per-minute", "30", "--bd-wait-bound", "750ms", "--bd-write-reserve", "1", "--budget-group", "shared", "--beads-dir", "/a"},
		"equals":   {"--bd-max-concurrent=4", "--bd-max-per-minute=30", "--bd-wait-bound=750ms", "--bd-write-reserve=1", "--budget-group=shared", "--beads-dir=/a"},
		"mixed":    {"--beads-dir", "/a", "--bd-max-concurrent=4", "--bd-max-per-minute", "30", "--bd-wait-bound=750ms", "--bd-write-reserve", "1", "--budget-group=shared"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseArgs(args, &bytes.Buffer{})
			if err != nil || got != want {
				t.Fatalf("got (%+v, %v), want %+v", got, err, want)
			}
		})
	}
}

func TestParseArgs_BudgetFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"zero concurrent", []string{"--bd-max-concurrent", "0"}, "--bd-max-concurrent 0 must be at least 1"},
		{"negative concurrent", []string{"--bd-max-concurrent=-3"}, "must be at least 1"},
		{"non-numeric concurrent", []string{"--bd-max-concurrent", "many"}, "is not a whole number"},
		{"zero per minute", []string{"--bd-max-per-minute=0"}, "--bd-max-per-minute 0 must be at least 1"},
		{"negative per minute", []string{"--bd-max-per-minute", "-1"}, "must be at least 1"},
		{"negative reserve", []string{"--bd-write-reserve", "-1"}, "--bd-write-reserve -1 must be at least 0"},
		{"reserve equals concurrent", []string{"--bd-max-concurrent", "3", "--bd-write-reserve", "3"}, "--bd-write-reserve 3 must be below --bd-max-concurrent 3"},
		{"reserve above concurrent", []string{"--bd-max-concurrent=3", "--bd-write-reserve=7"}, "must be below --bd-max-concurrent"},
		{"default reserve vs small concurrent", []string{"--bd-max-concurrent", "2"}, "--bd-write-reserve 2 must be below --bd-max-concurrent 2"},
		{"unparsable duration", []string{"--bd-wait-bound", "soon"}, `--bd-wait-bound "soon" is not a duration`},
		{"bare number duration", []string{"--bd-wait-bound=5"}, "is not a duration"},
		{"negative duration", []string{"--bd-wait-bound", "-1s"}, "must not be negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseArgs(tc.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
			if !errors.Is(err, errUsage) {
				t.Errorf("err %v should wrap errUsage (exit 2)", err)
			}
		})
	}
}

func TestParseArgs_ZeroWaitBoundAccepted(t *testing.T) {
	got, err := parseArgs([]string{"--bd-wait-bound", "0s"}, &bytes.Buffer{})
	if err != nil || got.waitBound != 0 {
		t.Fatalf("got (%+v, %v), want waitBound 0 and no error", got, err)
	}
	// The zero reserve is accepted too: every slot is open to reads.
	if got, err = parseArgs([]string{"--bd-write-reserve=0"}, &bytes.Buffer{}); err != nil || got.writeReserve != 0 {
		t.Fatalf("got (%+v, %v), want writeReserve 0", got, err)
	}
}

func TestNewRunner_BadBudgetFlagExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	r, code := newRunner([]string{"--bd-max-per-minute", "0"}, &stderr)
	if r != nil || code != 2 {
		t.Fatalf("got (%v, %d), want (nil, 2)", r, code)
	}
	if !strings.Contains(stderr.String(), "--bd-max-per-minute 0 must be at least 1") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestNewRunner_BuildsBudgetFromFlags(t *testing.T) {
	t.Setenv(EnvBudgetDir, "")
	r, code := newRunner([]string{"--beads-dir", "/example/zr"}, &bytes.Buffer{})
	if r == nil || code != 0 || r.Budget == nil {
		t.Fatalf("got (%+v, %d), want a runner with a budget", r, code)
	}
	// Default group: first 12 hex characters of SHA-256("/example/zr").
	sum := sha256.Sum256([]byte("/example/zr"))
	wantDir := budget.DefaultStateDir(hex.EncodeToString(sum[:])[:12])
	if got := r.Budget.Dir(); got != wantDir {
		t.Errorf("default group dir = %q, want %q", got, wantDir)
	}
}

func TestNewRunner_NamedGroupIsSharedAndVerbatim(t *testing.T) {
	t.Setenv(EnvBudgetDir, "")
	a, _ := newRunner([]string{"--beads-dir", "/example/a", "--budget-group", "shared-dolt"}, &bytes.Buffer{})
	b, _ := newRunner([]string{"--beads-dir=/example/b", "--budget-group=shared-dolt"}, &bytes.Buffer{})
	if a.Budget.Dir() != b.Budget.Dir() {
		t.Fatalf("same group, different dirs: %q vs %q", a.Budget.Dir(), b.Budget.Dir())
	}
	if want := budget.DefaultStateDir("shared-dolt"); a.Budget.Dir() != want {
		t.Errorf("dir = %q, want the group used verbatim: %q", a.Budget.Dir(), want)
	}
	c, _ := newRunner([]string{"--beads-dir", "/example/c"}, &bytes.Buffer{})
	if c.Budget.Dir() == a.Budget.Dir() {
		t.Errorf("different trackers without a group share %q", c.Budget.Dir())
	}
}

func TestNewRunner_BudgetDirEnvOverridesStateDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvBudgetDir, dir)
	r, _ := newRunner([]string{"--beads-dir", "/example/zr"}, &bytes.Buffer{})
	if got := r.Budget.Dir(); got != dir {
		t.Fatalf("budget dir = %q, want %q", got, dir)
	}
}

func TestNewRunner_NoTrackerStillBuildsBudgetInDefaultGroup(t *testing.T) {
	t.Setenv(internal.EnvWorkspaceDir, "")
	t.Setenv("BEADS_DIR", "")
	t.Setenv(EnvBudgetDir, "")
	r, code := newRunner(nil, &bytes.Buffer{})
	if r == nil || code != 0 || r.Budget == nil {
		t.Fatalf("got (%+v, %d)", r, code)
	}
	if got, want := r.Budget.Dir(), budget.DefaultStateDir(""); got != want {
		t.Errorf("dir = %q, want %q", got, want)
	}
}
