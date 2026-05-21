package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
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
//
// It also stubs loadConfigForCLI to return ErrNoConfig so the changes
// command takes its single-runner fallback path (the per-repo fan-out
// requires a populated config which these unit tests don't supply).
func withStubRunner(t *testing.T, stub beads.Runner) func() {
	t.Helper()
	prev := newChangesRunner
	newChangesRunner = func() beads.Runner { return stub }
	prevFlags := chFlags
	chFlags = changesFlags{}
	prevCfg := loadConfigForCLI
	loadConfigForCLI = func(_ context.Context) (*config.Config, error) {
		return nil, config.ErrNoConfig
	}
	return func() {
		newChangesRunner = prev
		chFlags = prevFlags
		loadConfigForCLI = prevCfg
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

// TestChangesCommand_FansOutPerRepo verifies that when config defines
// multiple repos with distinct paths, the changes command issues one bd
// query per repo (each via newChangesRunnerForRepo with the matching dir)
// and merges the results.
func TestChangesCommand_FansOutPerRepo(t *testing.T) {
	// Reset flags between invocations.
	prevFlags := chFlags
	chFlags = changesFlags{}
	defer func() { chFlags = prevFlags }()

	prevCfg := loadConfigForCLI
	defer func() { loadConfigForCLI = prevCfg }()
	loadConfigForCLI = func(_ context.Context) (*config.Config, error) {
		return &config.Config{
			Repos: []config.RepoConfig{
				{Remote: "mono/a", Path: "/repos/mono/a"},
				{Remote: "mono/b", Path: "/repos/mono/b"},
			},
		}, nil
	}

	// Record which dir each runner was constructed for, and supply a
	// distinct canned bd output per repo.
	prevFactory := newChangesRunnerForRepo
	defer func() { newChangesRunnerForRepo = prevFactory }()
	calls := map[string]*stubChangesRunner{}
	newChangesRunnerForRepo = func(dir string) beads.Runner {
		stub := &stubChangesRunner{}
		switch dir {
		case "/repos/mono/a":
			stub.stdout = `[{
                "id": "a-1",
                "issue_type": "merge-request",
                "title": "PR A",
                "status": "open",
                "created_at": "2026-05-20T12:00:00Z",
                "updated_at": "2026-05-20T12:00:00Z"
            }]`
		case "/repos/mono/b":
			stub.stdout = `[{
                "id": "b-1",
                "issue_type": "feedback",
                "title": "PR B feedback",
                "status": "open",
                "created_at": "2026-05-20T13:00:00Z",
                "updated_at": "2026-05-20T13:00:00Z"
            }]`
		default:
			stub.stdout = "[]"
		}
		calls[dir] = stub
		return stub
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"changes", "--since", "2026-05-20T00:00:00Z", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v (stderr=%s)", err, stderr.String())
	}

	// Both repos must have been queried.
	if _, ok := calls["/repos/mono/a"]; !ok {
		t.Fatalf("expected bd query against /repos/mono/a, got %v", keysOf(calls))
	}
	if _, ok := calls["/repos/mono/b"]; !ok {
		t.Fatalf("expected bd query against /repos/mono/b, got %v", keysOf(calls))
	}

	var out struct {
		Created []struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse output: %v\nraw: %s", err, stdout.String())
	}
	gotIDs := map[string]bool{}
	for _, b := range out.Created {
		gotIDs[b.ID] = true
	}
	if !gotIDs["a-1"] || !gotIDs["b-1"] {
		t.Fatalf("expected merged Created set {a-1, b-1}, got %v (raw=%s)", gotIDs, stdout.String())
	}
}

func keysOf(m map[string]*stubChangesRunner) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
