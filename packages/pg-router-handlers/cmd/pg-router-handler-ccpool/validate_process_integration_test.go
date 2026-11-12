//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// runAdapterProcess runs the test binary as the adapter (adapterRunEnv) with
// args and a request on stdin, and returns the exit code and stdout.
func runAdapterProcess(t *testing.T, args []string, stdin string, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), append([]string{adapterRunEnv + "=1"}, env...)...)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String()
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, out.String()
}

// The CLI surface through the real process: --skip-pool-dirs omits only the pool
// check, an unknown flag is a usage error, and the help documents the flag.
func TestProcess_validateFlags(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-pool")
	req := request("", "r", ccpoolConfig(t, missing, "", ""))

	code, out := runAdapterProcess(t, []string{"validate"}, req)
	var r validateReplyBody
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 || len(r.Problems) != 1 {
		t.Fatalf("without the flag: exit=%d problems=%q err=%v, want 0 and the missing-pool problem", code, r.Problems, err)
	}
	code, out = runAdapterProcess(t, []string{"validate", "--skip-pool-dirs"}, req)
	r = validateReplyBody{}
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 || len(r.Problems) != 0 {
		t.Fatalf("with --skip-pool-dirs: exit=%d problems=%q err=%v, want 0 and none", code, r.Problems, err)
	}
	if code, _ = runAdapterProcess(t, []string{"validate", "--bogus"}, req); code != conformance.ExitUsage {
		t.Fatalf("unknown flag: exit = %d, want %d", code, conformance.ExitUsage)
	}
	if !strings.Contains(helpText, "--skip-pool-dirs") || !strings.Contains(usageLine, "validate") {
		t.Errorf("--help must document validate and --skip-pool-dirs")
	}
}
