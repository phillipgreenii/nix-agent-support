package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/appcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/doctor"
	"github.com/phillipgreenii/x/exitcode"
)

// doctorStubs lays out a private PATH holding empty executable files for the
// three built-in providers, the recorder, the approval hook and ccpool, so the
// doctor's LookPath sees them, and replaces the doctor's command execution with
// an in-process fake that answers as each tool would (no process is spawned).
// The real config loader and the real ledger run under a throwaway
// XDG_STATE_HOME. omit names tools to leave out. The genuine spawn path
// (doctor.OSExec against a real provider binary) is covered by the doctor
// package's own real-tool tests.
func doctorStubs(t *testing.T, omit ...string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root+"/cfg")
	t.Setenv("XDG_STATE_HOME", root+"/state")
	t.Setenv("XDG_CACHE_HOME", root+"/cache")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := map[string]bool{
		"pg-review-provider-ccaudit":          true,
		"pg-review-provider-hook-decisions":   true,
		"pg-review-provider-config-inventory": true,
		"pg-ccaudit":                          true,
		"claude-extended-tool-approver":       true,
		"ccpool":                              true,
	}
	for _, o := range omit {
		delete(tools, o)
	}
	for n := range tools {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	prev := doctorDeps
	doctorDeps = func(ctx context.Context, skip bool) *doctor.Deps {
		d := prev(ctx, skip)
		d.Exec = fakeDoctorExec
		return d
	}
	t.Cleanup(func() { doctorDeps = prev })
	return root
}

// fakeDoctorExec answers the doctor's probes the way the real tools do:
// providers exit 0 on --help, pg-ccaudit reports three indexed files and the
// approval hook allows the sample command. Anything else fails the test via a
// non-zero exit, so an unexpected command cannot pass silently.
func fakeDoctorExec(_ context.Context, name string, args []string, stdin string, _ []string, _ string) (string, int, error) {
	switch filepath.Base(name) {
	case "pg-review-provider-ccaudit", "pg-review-provider-hook-decisions", "pg-review-provider-config-inventory":
		if len(args) > 0 && args[len(args)-1] == "--help" {
			return "", 0, nil
		}
	case "pg-ccaudit":
		if len(args) == 2 && args[0] == "status" && args[1] == "--json" {
			return `{"files_indexed": 3}`, 0, nil
		}
	case "claude-extended-tool-approver":
		if strings.Contains(stdin, doctor.HookSample) {
			return `{"hookSpecificOutput":{"permissionDecision":"allow"}}`, 0, nil
		}
	}
	return "", 99, nil
}

func TestDoctorAllPassSkippingSession(t *testing.T) {
	doctorStubs(t)
	code, out, _ := run(t, "job", "doctor", "--skip-session-check")
	if code != exitcode.OK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"PASS config", "PASS ledger", "PASS providers", "PASS recorder", "PASS ccpool", "PASS hook", "SKIP session"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDoctorBuiltInProviderAbsentFailsWithoutConfigToml(t *testing.T) {
	doctorStubs(t, "pg-review-provider-hook-decisions")
	code, out, _ := run(t, "job", "doctor", "--skip-session-check")
	if code != appcode.Provider {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "FAIL providers: hook-decisions:") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDoctorCcpoolAbsentIsProviderCode(t *testing.T) {
	doctorStubs(t, "ccpool")
	code, out, _ := run(t, "job", "doctor", "--skip-session-check")
	if code != appcode.Provider || !strings.Contains(out, "FAIL ccpool:") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestDoctorBadConfigIsCode4(t *testing.T) {
	root := doctorStubs(t)
	if err := os.MkdirAll(root+"/cfg/pg-review", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[[lens]]\nid=\"agent-workflow\"\nproviders=[\"nope\"]\n"
	if err := os.WriteFile(root+"/cfg/pg-review/config.toml", []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, "job", "doctor", "--skip-session-check")
	if code != exitcode.Config || !strings.Contains(out, "FAIL config:") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestDoctorCorruptLedgerIsCode6(t *testing.T) {
	root := doctorStubs(t)
	dir := root + "/state/pg-review"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/ledger.db", []byte(strings.Repeat("not a database ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, "job", "doctor", "--skip-session-check")
	if code != appcode.Ledger || !strings.Contains(out, "FAIL ledger:") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
