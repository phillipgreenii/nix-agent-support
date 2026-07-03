package main

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
)

// TestNewClaudeSpawner_DefaultBin verifies that when claude_bin is unset in
// config, newClaudeSpawner leaves bin empty so Produce falls back to "claude"
// (back-compat: existing deployments that have claude on PATH keep working).
func TestNewClaudeSpawner_DefaultBin(t *testing.T) {
	cfg := &config.Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp",
		Repos:        []config.RepoConfig{{Remote: "owner/repo"}},
		// ClaudeBin intentionally unset
	}
	s := newClaudeSpawner(cfg)
	if s.bin != "" {
		t.Fatalf("bin: got %q want empty (unset → falls back to \"claude\" at spawn time)", s.bin)
	}
}

// TestNewClaudeSpawner_ConfiguredBin verifies that a configured claude_bin path
// flows through to claudeSpawner.bin so the daemon uses the absolute path.
func TestNewClaudeSpawner_ConfiguredBin(t *testing.T) {
	const wantBin = "/run/current-system/sw/bin/claude"
	cfg := &config.Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp",
		Repos:        []config.RepoConfig{{Remote: "owner/repo"}},
		ClaudeBin:    wantBin,
	}
	s := newClaudeSpawner(cfg)
	if s.bin != wantBin {
		t.Fatalf("bin: got %q want %q", s.bin, wantBin)
	}
}

func TestParseHeadSHAFromOutput_JSONLine(t *testing.T) {
	out := "some log\nrunning orchestrator\n{\"head_sha\":\"abc123def\"}\ndone\n"
	sha := parseHeadSHAFromOutput(out)
	if sha != "abc123def" {
		t.Fatalf("head sha = %q, want abc123def", sha)
	}
}

func TestParseHeadSHAFromOutput_LastWins(t *testing.T) {
	out := `{"head_sha":"first"}` + "\n" + `{"head_sha":"second"}` + "\n"
	sha := parseHeadSHAFromOutput(out)
	if sha != "second" {
		t.Fatalf("head sha = %q, want second (last wins)", sha)
	}
}

func TestParseHeadSHAFromOutput_None(t *testing.T) {
	if sha := parseHeadSHAFromOutput("no json here\n"); sha != "" {
		t.Fatalf("head sha = %q, want empty", sha)
	}
}
