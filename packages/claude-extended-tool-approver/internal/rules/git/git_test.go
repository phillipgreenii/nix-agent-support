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
