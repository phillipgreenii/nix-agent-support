package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// stubChangesRunner implements beads.Runner for the changes CLI tests.
type stubChangesRunner struct {
	stdout string
	err    error
	args   []string
}

func (s *stubChangesRunner) Run(_ context.Context, args ...string) (string, error) {
	s.args = args
	return s.stdout, s.err
}

// withStubRunner swaps newChangesRunner for the duration of the test and
// resets the package-level flags so prior tests don't leak --json.
func withStubRunner(t *testing.T, stub beads.Runner) func() {
	t.Helper()
	prev := newChangesRunner
	newChangesRunner = func() beads.Runner { return stub }
	prevFlags := chFlags
	chFlags = changesFlags{}
	return func() {
		newChangesRunner = prev
		chFlags = prevFlags
	}
}

func TestChangesCommand_RequiresSince(t *testing.T) {
	defer withStubRunner(t, &stubChangesRunner{stdout: "[]"})()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"changes"})

	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--since") {
		t.Fatalf("expected --since required error, got %v", err)
	}
}

func TestChangesCommand_RejectsBadTimestamp(t *testing.T) {
	defer withStubRunner(t, &stubChangesRunner{stdout: "[]"})()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"changes", "--since", "yesterday"})

	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("expected RFC3339 parse error, got %v", err)
	}
}

func TestChangesCommand_JSONOutput(t *testing.T) {
	stub := &stubChangesRunner{stdout: `[{
        "id": "t-1",
        "issue_type": "merge-request",
        "title": "PR one",
        "status": "open",
        "created_at": "2026-05-20T12:00:00Z",
        "updated_at": "2026-05-20T12:00:00Z"
    }]`}
	defer withStubRunner(t, stub)()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"changes", "--since", "2026-05-20T00:00:00Z", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var out struct {
		Since   string `json:"since"`
		Created []struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse JSON output: %v\nraw: %s", err, stdout.String())
	}
	if len(out.Created) != 1 || out.Created[0].ID != "t-1" {
		t.Fatalf("unexpected created list: %+v", out.Created)
	}
	if out.Since != "2026-05-20T00:00:00Z" {
		t.Fatalf("Since round-trip: got %q", out.Since)
	}
}

func TestChangesCommand_HumanOutput(t *testing.T) {
	stub := &stubChangesRunner{stdout: `[{
        "id": "t-5",
        "issue_type": "feedback",
        "title": "review thread",
        "status": "open",
        "created_at": "2026-05-19T00:00:00Z",
        "updated_at": "2026-05-20T01:00:00Z"
    }]`}
	defer withStubRunner(t, stub)()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"changes", "--since", "2026-05-20T00:00:00Z", "--json=false"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "Changes since 2026-05-20T00:00:00Z") {
		t.Fatalf("missing header: %q", got)
	}
	if !strings.Contains(got, "t-5") || !strings.Contains(got, "review thread") {
		t.Fatalf("missing bead row: %q", got)
	}
	if !strings.Contains(got, "updated") {
		t.Fatalf("expected 'updated' state label in row: %q", got)
	}
}

func TestChangesCommand_PGPROutputEnv(t *testing.T) {
	stub := &stubChangesRunner{stdout: "[]"}
	defer withStubRunner(t, stub)()
	t.Setenv("PGPR_OUTPUT", "json")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"changes", "--since", "2026-05-20T00:00:00Z"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// JSON output must parse as a JSON object.
	var obj map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &obj); err != nil {
		t.Fatalf("PGPR_OUTPUT=json did not yield JSON output: %v\n%s", err, stdout.String())
	}
}
