package git

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestGit_ReadOnly_Approve(t *testing.T) {
	readOnly := []string{
		// Porcelain inspection
		"git log", "git diff", "git status", "git show", "git blame", "git describe",
		"git shortlog", "git reflog", "git grep foo",
		"git show-branch", "git whatchanged", "git range-diff main..feat main..other",
		// Plumbing: ref/object inspection
		"git for-each-ref", "git ls-files", "git ls-remote", "git ls-tree",
		"git merge-base", "git rev-list", "git rev-parse", "git show-ref",
		"git name-rev HEAD", "git cat-file -p HEAD", "git count-objects",
		// Plumbing: diff variants
		"git diff-tree --no-commit-id -r HEAD", "git diff-index HEAD", "git diff-files",
		// Plumbing: verification/integrity
		"git verify-commit HEAD", "git verify-tag v1.0", "git fsck",
		// Plumbing: gitignore/gitattributes checks
		"git check-ignore foo.log", "git check-attr diff -- file.txt",
		"git check-mailmap user@example.com", "git check-ref-format refs/heads/main",
	}
	r := New()
	for _, cmd := range readOnly {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_Modifying_Approve(t *testing.T) {
	modifying := []string{
		"git add .", "git commit -m msg", "git branch feat", "git fetch",
		"git push", "git stash", "git config x y", "git mu",
	}
	r := New()
	for _, cmd := range modifying {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_ResetSoft_Approve(t *testing.T) {
	approve := []string{
		"git reset HEAD~1",
		"git reset --soft HEAD~1",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_ResetHard_Ask(t *testing.T) {
	// Ensure reset --hard still asks
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git reset --hard HEAD~1"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("git reset --hard: got %s, want ask", got.Decision)
	}
}

func TestGit_Destructive_Ask(t *testing.T) {
	destructive := []string{
		"git reset --hard HEAD",
		"git clean -fd",
		"git push --force",
		"git push -f",
		"git branch -D feat",
	}
	r := New()
	for _, cmd := range destructive {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}

func TestGit_NonGit_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("ls: got %s, want abstain", got.Decision)
	}
}

func TestGit_NonBash_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Read: got %s, want abstain", got.Decision)
	}
}

func TestGit_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "git" {
		t.Errorf("Name() = %q, want git", got)
	}
}

func TestGit_GitDirReadOnly_Approve(t *testing.T) {
	r := New()
	commands := []string{
		"GIT_DIR=/other git log",
		"GIT_DIR=/other git diff",
		"GIT_DIR=/other git status",
		"GIT_WORK_TREE=/other git show HEAD",
		"GIT_DIR=/other GIT_WORK_TREE=/other git blame file.go",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (read-only with redirected context)", cmd, got.Decision)
		}
	}
}

func TestGit_GitDirModifying_Ask(t *testing.T) {
	r := New()
	commands := []string{
		"GIT_DIR=/other git push",
		"GIT_DIR=/other git commit -m msg",
		"GIT_WORK_TREE=/other git add .",
		"GIT_DIR=/other git rebase main",
		"GIT_DIR=/other git reset HEAD~1",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask (modifying with redirected context)", cmd, got.Decision)
		}
	}
}

func TestGit_CosmeticEnvVars_Unchanged(t *testing.T) {
	r := New()
	commands := []string{
		"GIT_AUTHOR_NAME=foo git commit -m msg",
		"GIT_AUTHOR_EMAIL=foo@bar git commit -m msg",
		"GIT_COMMITTER_NAME=foo git commit -m msg",
		"GIT_AUTHOR_DATE=2024-01-01 git commit -m msg",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (cosmetic env var shouldn't change decision)", cmd, got.Decision)
		}
	}
}

func TestGit_RebaseNonInteractive_Approve(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git rebase main"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("git rebase main: got %s, want approve", got.Decision)
	}
}

func TestGit_Checkout_Approve(t *testing.T) {
	approve := []string{
		"git checkout feature-branch",
		"git checkout -- src/main.go",
		"git checkout -b new-branch",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_CheckoutDot_Approve(t *testing.T) {
	approve := []string{
		"git checkout .",
		"git checkout -- .",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_MvRm_Approve(t *testing.T) {
	approve := []string{
		"git mv old.go new.go",
		"git rm stale.go",
		"git rm --cached file.go",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_Worktree_Approve(t *testing.T) {
	approve := []string{
		"git worktree add ../feature feature-branch",
		"git worktree remove ../feature",
		"git worktree list",
		"git worktree prune",
		"git worktree move ../old ../new",
		"git worktree repair",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_CherryPick_Approve(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git cherry-pick abc123"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("git cherry-pick: got %s, want approve", got.Decision)
	}
}

func TestGit_RebaseInteractive_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git rebase -i HEAD~3"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("git rebase -i: got %s, want abstain (interactive)", got.Decision)
	}
}

func TestGit_RebaseInteractiveWithSequenceEditor_Approve(t *testing.T) {
	r := New()
	approve := []string{
		`GIT_SEQUENCE_EDITOR="sed -i.bak 's/^pick /reword /'" git rebase -i HEAD~3`,
		`GIT_SEQUENCE_EDITOR="sed -i 's/^pick /fixup /'" git rebase --interactive ae21327~1`,
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (automated interactive rebase)", cmd, got.Decision)
		}
	}
}

func TestGit_FilterBranch_Approve(t *testing.T) {
	r := New()
	approve := []string{
		`FILTER_BRANCH_SQUELCH_WARNING=1 git filter-branch -f --msg-filter 'sed "/^Refs: NO-JIRA$/d"' HEAD~4..HEAD`,
		`git filter-branch --msg-filter 'sed "s/old/new/"' HEAD~2..HEAD`,
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_FilterBranchWithGitDir_Ask(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": `GIT_DIR=/other git filter-branch --msg-filter 'cat' HEAD~1..HEAD`}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("git filter-branch with GIT_DIR: got %s, want ask", got.Decision)
	}
}

func TestGit_Tag_Reject(t *testing.T) {
	reject := []string{
		"git tag v1.0",
		"git tag -d v1.0",
		"git tag -a v1.0 -m \"msg\"",
	}
	r := New()
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s, want reject", cmd, got.Decision)
		}
	}
}

func TestGit_PushForceWithLease_Approve(t *testing.T) {
	approve := []string{
		"git push --force-with-lease",
		"git push origin main --force-with-lease",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGit_PushForceWithLease_CrossBranch_Ask(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git push origin local:different --force-with-lease"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("git push origin local:different --force-with-lease: got %s, want ask", got.Decision)
	}
}

func TestGit_PushForce_Ask(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git push --force"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("git push --force: got %s, want ask", got.Decision)
	}
}
