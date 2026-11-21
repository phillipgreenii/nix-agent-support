package main

import (
	"strings"
	"testing"
)

// A bd budget denial from a pinned beads backend is a skipped poll: exit 0,
// an empty array, no failure.
func TestBudgetDenied_IsASkippedPollOnEveryVerb(t *testing.T) {
	cases := map[string][]string{
		"changes": {"changes", "issue", "q", "--consumer", "c1", "--backend", "b1"},
		"list":    {"list", "issue", "q", "--backend", "b1"},
		"sweep":   {"sweep", "issue", "q", "--backend", "b1"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			withFactory(t, "budget_denied")
			stdout, stderr, code := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
			}
			if items := mustUnmarshalItems(t, stdout); len(items) != 0 || strings.TrimSpace(stdout) != "[]" {
				t.Fatalf("stdout = %q, want an empty array", stdout)
			}
			if !strings.Contains(stderr, "poll skipped") {
				t.Fatalf("stderr = %q, want a skipped-poll note", stderr)
			}
		})
	}
}

// A genuine bd failure, and a denial mixed with another failure, stay failures.
func TestBudgetDenied_OtherFailuresStayFailures(t *testing.T) {
	for _, behavior := range []string{"bd_failure", "budget_denied_mixed", "total_failure"} {
		t.Run(behavior, func(t *testing.T) {
			withFactory(t, behavior)
			stdout, stderr, code := runCLI(t, "changes", "issue", "q", "--consumer", "c1", "--backend", "b1")
			if code == 0 {
				t.Fatalf("exit code = 0, want non-zero (stdout=%q stderr=%q)", stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if strings.Contains(stderr, "poll skipped") {
				t.Fatalf("stderr = %q, must not be reported as a skipped poll", stderr)
			}
		})
	}
}
