package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal"
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
		{name: "positional", args: []string{"extra"}, wantErr: `unexpected argument "extra"; the only option is --beads-dir DIR`},
		{name: "missing value", args: []string{"--beads-dir"}, wantErr: "needs an argument"},
		{name: "help", args: []string{"-h"}, help: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseArgs(tc.args, &stderr)
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
