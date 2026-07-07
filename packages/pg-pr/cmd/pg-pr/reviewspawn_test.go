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

// TestResolveBin_emptyBinFallsBackToClaude verifies that resolveBin("") returns
// "claude", so callers that receive an empty bin still exec the right binary.
func TestResolveBin_emptyBinFallsBackToClaude(t *testing.T) {
	got := resolveBin("")
	if got != "claude" {
		t.Fatalf("resolveBin(%q) = %q; want %q", "", got, "claude")
	}
}

// TestResolveBin_absolutePathPassedThrough verifies that a configured absolute
// path is returned unchanged so deployments pinned to an absolute binary work.
func TestResolveBin_absolutePathPassedThrough(t *testing.T) {
	const want = "/run/current-system/sw/bin/claude"
	got := resolveBin(want)
	if got != want {
		t.Fatalf("resolveBin(%q) = %q; want %q", want, got, want)
	}
}

// TestClaudeArgs_HeadlessWorkerFlags verifies the daemon spawns claude with the
// headless-worker flags: the prompt runs via -p, and --permission-mode
// bypassPermissions is present so the orchestrator agent can use its tools
// (Bash/Edit/`pg-pr review draft`) without an interactive permission prompt. A
// plain `claude -p` (no permission mode) leaves the headless orchestrator unable
// to act and stages no Draft ("no Draft staged"). pg2-jpfw.2.
func TestClaudeArgs_HeadlessWorkerFlags(t *testing.T) {
	const prompt = "Run the pg-pr-review-orchestrator for owner/repo#1"
	args := claudeArgs(prompt)

	if len(args) < 2 || args[0] != "-p" || args[1] != prompt {
		t.Fatalf("args must start with -p <prompt>; got %v", args)
	}

	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--permission-mode" && args[i+1] == "bypassPermissions" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("args must include --permission-mode bypassPermissions; got %v", args)
	}
}

// TestClaudeArgs_ModelSonnet verifies the daemon pins the spawned orchestration
// to Sonnet. Without --model, a headless `claude -p` runs on the default model
// (Opus 4.8), ~3x slower, even though every review agent def declares
// `model: sonnet`. The orchestrator only delegates, so Sonnet is sufficient.
func TestClaudeArgs_ModelSonnet(t *testing.T) {
	args := claudeArgs("Run the pg-pr-review-orchestrator for owner/repo#1")
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--model" && args[i+1] == "sonnet" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("args must include --model sonnet; got %v", args)
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
