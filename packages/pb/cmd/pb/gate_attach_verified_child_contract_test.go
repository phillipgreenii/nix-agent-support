//go:build contract

package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestContract_AttachVerifiedChild_earlyFailurePrintsNoResult drives the REAL pb
// binary through failures that happen before any child bead exists: stdout MUST
// stay empty (the reason is on stderr) and the exit code is 1. Before pg2-0jt3s
// `--json` printed {"child":"","gates":null} here, a value that looks like the
// success shape. The partial-failure (exit 3 / 4) output is pinned by
// TestWriteAttachResult and internal/gate's TestAttach_* cases.
func TestContract_AttachVerifiedChild_earlyFailurePrintsNoResult(t *testing.T) {
	bin := pbBin(t)
	isolate(t)
	missingBody := filepath.Join(t.TempDir(), "missing.md")
	base := []string{"gate", "attach-verified-child", "--impl", "pg2-x", "--actor", "t", "--gate", "repo=abc123"}
	for name, args := range map[string][]string{
		"title over 500 runes": {"--title", strings.Repeat("t", 501)},
		"unreadable body file": {"--title", "t", "--body-file", missingBody},
		"malformed --gate":     {"--title", "t", "--gate", "no-equals-sign"},
	} {
		for _, asJSON := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s json=%v", name, asJSON), func(t *testing.T) {
				full := append(slices.Clone(base), args...)
				if asJSON {
					full = append(full, "--json")
				}
				stdout, stderr, code := runPB(t, bin, full...)
				if code != 1 {
					t.Errorf("exit = %d, want 1\nstderr: %s", code, stderr)
				}
				if stdout != "" {
					t.Errorf("stdout = %q, want empty: nothing was created, so there is no result to print", stdout)
				}
				if strings.TrimSpace(stderr) == "" {
					t.Error("stderr is empty: the failure reason must still reach the operator")
				}
			})
		}
	}
}
