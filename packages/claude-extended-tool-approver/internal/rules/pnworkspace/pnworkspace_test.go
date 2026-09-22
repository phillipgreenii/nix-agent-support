package pnworkspace

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// TestPnWorkspace_ApprovedSubcommands covers every routine subcommand the
// 2026-09-17 operator ruling names (pg2-4zyqf/pg2-vlpe1's acceptance
// criteria): build/status/push/doctor/update, plus workforest add/list —
// and the two pg2-tvdh3 additions, rebase and workforest prune.
func TestPnWorkspace_ApprovedSubcommands(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"build", "pn workspace build"},
		{"status", "pn workspace status"},
		{"push", "pn workspace push"},
		{"doctor", "pn workspace doctor"},
		{"update", "pn workspace update"},
		{"workforest add with set name", "pn workspace workforest add pn-workspace-sync"},
		{"workforest list", "pn workspace workforest list"},
		// pg2-tvdh3: rebase is local-only (fetch + pull --rebase --autostash,
		// or --autostash rebase onto an explicit branch) — no push, and
		// --autostash makes it as recoverable as a plain `git stash`.
		{"rebase, no branch", "pn workspace rebase"},
		{"rebase onto explicit branch", "pn workspace rebase main"},
		// pg2-tvdh3: workforest prune is `git worktree prune` in every
		// canonical repo — pure administrative bookkeeping, no arguments,
		// no effect on any live worktree/branch.
		{"workforest prune", "pn workspace workforest prune"},
		// A leading compound leaf ("export && pn workspace push", from the bead's
		// own reproduce sample) still approves: LeavesOf's typical production
		// caller (the engine) has already split the expression and hands each
		// leaf to Evaluate separately, but this rule's own for-loop over every
		// parsed leaf must independently reach the pn leaf too.
		{"compound with export prefix", "export FOO=bar && pn workspace push"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.Approve {
				t.Errorf("%q => %v, want Approve", tt.command, got)
			}
		})
	}
}

// TestPnWorkspace_ExcludedCommands pins the two commands the operator ruling
// explicitly carves OUT of the relief (pg2-vlpe1's Caution section): they must
// NOT be swept into approval by a broad "pn workspace"/"pn workspace
// workforest" prefix match, and must retain their prior (non-relaxed)
// scrutiny — i.e. THIS rule must answer NoOpinion/not-applicable, deferring
// to whatever the rest of the chain already does with them.
func TestPnWorkspace_ExcludedCommands(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		// Excluded case 1: workforest remove is a different, destructive risk
		// class (deletes a pre-existing set's worktrees/branches) and must keep
		// its current scrutiny.
		{"workforest remove", "pn workspace workforest remove pg2-5yq5-nix-hooks"},
		// Excluded case 2: a direct write to pn-workspace.toml. This is not even
		// a `pn` invocation, so it was never going to match the basename check —
		// pinned here so a future refactor that widens the basename match cannot
		// silently start covering it.
		{"direct write to pn-workspace.toml", "cp pn-workspace.toml.new pn-workspace.toml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.NoOpinion {
				t.Errorf("%q => %v, want NoOpinion (deferred, unchanged scrutiny)", tt.command, got)
			}
		})
	}
}

// TestPnWorkspace_UnrelatedCommandsAbstain covers a handful of shapes this
// rule must NOT claim: a bare "pn" invocation, a non-"workspace" pn
// subcommand, an unlisted "pn workspace" subcommand, a non-Bash tool, and a
// non-pn command entirely.
func TestPnWorkspace_UnrelatedCommandsAbstain(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"bare pn", "pn"},
		{"pn non-workspace subcommand", "pn --version"},
		{"unlisted workspace subcommand", "pn workspace frobnicate"},
		{"bare workforest, no leaf verb", "pn workspace workforest"},
		{"unrelated command", "kubectl get pods"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.NoOpinion {
				t.Errorf("%q => %v, want NoOpinion", tt.command, got)
			}
		})
	}

	t.Run("non-bash tool", func(t *testing.T) {
		input := &hookio.HookInput{ToolName: "Read", ToolInput: mustJSON("")}
		if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.NoOpinion {
			t.Errorf("non-bash tool => %v, want NoOpinion", got)
		}
	})
}
