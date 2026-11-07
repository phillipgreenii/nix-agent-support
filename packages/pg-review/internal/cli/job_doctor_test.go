package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
)

// doctorStubs installs executable stubs on a private PATH: the three built-in
// providers, the recorder, the approval hook and ccpool. The real ledger runs
// under a throwaway XDG_STATE_HOME. omit names stubs to leave out.
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
	scripts := map[string]string{
		"pg-review-provider-ccaudit":          "#!/bin/sh\nexit 0\n",
		"pg-review-provider-hook-decisions":   "#!/bin/sh\nexit 0\n",
		"pg-review-provider-config-inventory": "#!/bin/sh\nexit 0\n",
		"pg-ccaudit":                          "#!/bin/sh\necho '{\"files_indexed\": 3}'\n",
		"claude-extended-tool-approver":       "#!/bin/sh\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"permissionDecision\":\"allow\"}}'\n",
		"ccpool":                              "#!/bin/sh\nexit 0\n",
	}
	for _, o := range omit {
		delete(scripts, o)
	}
	for n, s := range scripts {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return root
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
	if code != exitcode.Provider {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "FAIL providers: hook-decisions:") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDoctorCcpoolAbsentIsProviderCode(t *testing.T) {
	doctorStubs(t, "ccpool")
	code, out, _ := run(t, "job", "doctor", "--skip-session-check")
	if code != exitcode.Provider || !strings.Contains(out, "FAIL ccpool:") {
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
	if code != exitcode.ValidationRejected || !strings.Contains(out, "FAIL config:") {
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
	if code != exitcode.Ledger || !strings.Contains(out, "FAIL ledger:") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
