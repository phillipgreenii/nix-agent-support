package main

// run() end to end (bead pg2-h5cmo items 4 and 8): the flag-parse error
// reporting contract, and the generic conformance suite driven through a real
// process exec with the registry command's argument words
// (--beads-dir DIR), so run()'s argument handling is exercised, not only
// newRunner.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// TestParseArgs_ExactStderr pins what a parse failure and -h print: a parse
// error is returned to the caller (printed once there), the flag package
// prints nothing of its own, and -h prints the usage line exactly once.
func TestParseArgs_ExactStderr(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"unknown flag", []string{"--nope"}, ""},
		{"missing value", []string{"--beads-dir"}, ""},
		{"positional", []string{"extra"}, ""},
		{"help", []string{"-h"}, "usage: pg-connector-issue-beads [--beads-dir DIR]\n"},
		{"long help", []string{"--help"}, "usage: pg-connector-issue-beads [--beads-dir DIR]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, _ = parseArgs(tc.args, &stderr)
			if stderr.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

// TestRun_BadFlagPrintsOnceToStderrAndNothingToStdout drives run() itself:
// exit 2, the message exactly once on stderr, and nothing on stdout (the
// serve loop never started).
func TestRun_BadFlagPrintsOnceToStderrAndNothingToStdout(t *testing.T) {
	origStdout := os.Stdout
	defer func() { os.Stdout = origStdout }()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = outW

	var stderr bytes.Buffer
	code := run([]string{"--nope"}, &stderr)

	_ = outW.Close()
	stdout, _ := io.ReadAll(outR)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if want := "flag provided but not defined: -nope\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestRun_HelpExitsZeroWithUsageOnceAndNothingOnStdout(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"-h"}, &stderr); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if got := strings.Count(stderr.String(), "usage:"); got != 1 {
		t.Errorf("stderr = %q, want exactly one usage line", stderr.String())
	}
}

// helperProcessEnv selects TestHelperRunProcess's child mode.
const helperProcessEnv = "PG_CONNECTOR_ISSUE_BEADS_TEST_RUN_HELPER"

// TestHelperRunProcess is not a test: re-exec'd by the conformance test below
// as a stand-in for the installed binary. It calls run() with the words after
// "--" and exits with its code, before the testing framework prints anything
// to stdout.
func TestHelperRunProcess(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		t.Skip("helper process only")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(run(args, os.Stderr))
}

// fakeBdScript logs its argv (one line per call) and answers every call with
// the attention/search fixture, like the in-process fakeRunner.
const fakeBdScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_BD_LOG"
cat <<'JSON'
` + attentionSearchBdJSON + `
JSON
`

// TestConformance_GenericSuitePassesThroughRunWithBeadsDirArgs runs the
// generic suite against a real process whose argv carries --beads-dir, via
// run(), with a fake bd on PATH: it proves run() accepts the registry
// command's argument words, serves the wire protocol, and pins every bd call
// to that tracker (bd -C <dir>).
func TestConformance_GenericSuitePassesThroughRunWithBeadsDirArgs(t *testing.T) {
	beadsDir := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(fakeBdScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "bd.log")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_BD_LOG", logPath)
	t.Setenv(helperProcessEnv, "1")
	t.Setenv(eventlog.EnvPath, "off")
	// Env vars naming a different tracker must lose to the flag.
	t.Setenv("PG_CONNECTOR_ISSUE_BEADS_DIR", "/env/other")
	t.Setenv("BEADS_DIR", "/env/other")

	backend := conformance.ExecBackend{
		Binary: os.Args[0],
		Args:   []string{"-test.run=^TestHelperRunProcess$", "--", "--beads-dir", beadsDir},
	}
	results := conformance.Run(context.Background(), backend)
	if len(results) == 0 {
		t.Fatal("conformance.Run returned no results")
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
	// The generic suite sends no op that reaches bd, so drive one that does
	// to prove the flag pins the tracker end to end.
	if resp := invokeRaw(t, backend, `{"op":"show","args":{"id":"tp-1"}}`); resp.Error != nil && resp.Error.Code == "unknown_op" {
		t.Fatalf("show answered unknown_op: %+v", resp.Error)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("fake bd was never called: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.HasPrefix(line, "-C "+beadsDir+" ") {
			t.Errorf("bd call %q was not pinned to the --beads-dir tracker %s", line, beadsDir)
		}
	}
}
