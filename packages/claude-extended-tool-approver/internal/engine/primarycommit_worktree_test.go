// pg2-h2npt: the WHOLE-CHAIN guard for which directory a `git commit` is judged
// against. It lives in the engine suite rather than in internal/rules/primarycommit
// because the mechanism spans both: the rule reads `git -C`, but the `cd`/`pushd`
// re-root is the ENGINE's (EvaluateExpression, pg2-opclh), and the rule's unresolved-
// directory test only works because the engine joins an unexpandable target VERBATIM
// into the running cwd. A rule-level test cannot observe either half.
//
// Every case runs against a REAL on-disk fixture — a canonical clone checked out on its
// primary branch with a NESTED linked worktree under it, which is the layout that
// produced the reported false denies. The nesting is the point: FileResolver's gitRoot
// walks UP from the directory it is given, so a mis-resolved worktree path lands on the
// enclosing canonical clone and looks like a commit on primary.
package engine_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// nestedWorktreeFixture builds a canonical clone on branch "main" with a linked worktree
// on branch "feat" at <canonical>/.worktrees/feat, and returns both paths.
func nestedWorktreeFixture(t *testing.T) (canonical, worktree string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	canonical = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", canonical}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", canonical, args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	git("commit", "--allow-empty", "-q", "-m", "init")
	worktree = filepath.Join(canonical, ".worktrees", "feat")
	git("worktree", "add", "-q", "-b", "feat", worktree)
	return canonical, worktree
}

// TestIntegration_PrimaryCommitDirectoryResolution covers the four fixture shapes
// pg2-h2npt names, in both permission postures, with the session cwd pinned to the
// CANONICAL clone (on primary) throughout — the situation an agent working in a nested
// worktree is actually in.
func TestIntegration_PrimaryCommitDirectoryResolution(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)

	cases := []struct {
		name    string
		command string
		// wantAuto is the verdict in an auto-approving session (bypassPermissions),
		// wantInteractive the verdict in the default session.
		wantAuto, wantInteractive hookio.Decision
		// reasonHas, when non-empty, must appear in the auto-session reason.
		reasonHas string
	}{
		// The commit really is on the canonical primary: unchanged verdicts, with a
		// reason that now names the directory it judged and how it picked it.
		{
			name: "plain commit in canonical clone on primary", command: "git commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Approve,
			reasonHas: "Directory evaluated: " + canonical,
		},
		// FIXTURE 1 — literal `git -C /abs/path`: the only form that already worked.
		{
			name: "literal -C into the nested worktree", command: "git -C " + worktree + " commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// FIXTURE 2 — `cd /abs/path && git commit`: judged against the cd TARGET. This
		// is the engine's re-root; the assertion here is that the rule sees the advanced
		// cwd, so a future engine change that stops advancing fails HERE.
		{
			name: "cd into the nested worktree then commit", command: "cd " + worktree + " && git commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// `pushd` changes directory exactly as `cd` does and was NOT modelled, so this
		// spelling hard-denied a feature-branch commit. It settles at NoOpinion rather
		// than Approve because nothing approves a `pushd` leaf — which is also why
		// modelling it cannot widen anything.
		{
			name: "pushd into the nested worktree then commit", command: "pushd " + worktree + " && git commit -m x",
			wantAuto: hookio.NoOpinion, wantInteractive: hookio.NoOpinion,
		},
		// The subshell spelling, for completeness — it already worked and must keep working.
		{
			name: "subshell cd into the nested worktree", command: "(cd " + worktree + " && git commit -m x)",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// FIXTURE 3 — `git -C $VAR commit`: the target is unknowable from the command
		// text. Never Approve; the auto-approving session keeps a hard deny and the
		// interactive one Asks rather than letting the git rule approve it.
		{
			name: "unresolved -C variable", command: "git -C $WT commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		// The same, reached through the CWD instead of `-C`. This is the pinned coupling:
		// the engine joins the unexpanded `$WT` into the running cwd, and that surviving
		// token is the ONLY thing that tells the rule the directory is unknown.
		{
			name: "unresolved cd target", command: "cd $WT && git commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			name: "unresolved pushd target", command: "pushd $WT && git commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			name: "unresolved -C command substitution", command: "git -C $(git rev-parse --show-toplevel) commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		// Scope check: an unresolved directory on a NON-commit git subcommand is none of
		// this rule's business and must not acquire a gate.
		{
			name: "unresolved -C on git status is untouched", command: "git -C $WT status",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, m := range []struct {
				mode string
				want hookio.Decision
			}{{"bypassPermissions", tc.wantAuto}, {"default", tc.wantInteractive}} {
				// The engine is rebuilt per case so the running cwd starts at the
				// canonical clone every time, exactly as a fresh hook invocation would.
				eng := buildFullEngine(canonical, canonical)
				in := &hookio.HookInput{
					ToolName: "Bash", CWD: canonical,
					ToolInput: makeBashJSON(tc.command), PermissionMode: m.mode,
				}
				got := eng.EvaluateHook(in)
				if got.Decision != m.want {
					t.Errorf("[%s] %q: got %s (%s: %s), want %s", m.mode, tc.command, got.Decision, got.Module, got.Reason, m.want)
				}
				if m.mode == "bypassPermissions" && tc.reasonHas != "" && !strings.Contains(got.Reason, tc.reasonHas) {
					t.Errorf("[%s] %q: reason %q does not mention %q", m.mode, tc.command, got.Reason, tc.reasonHas)
				}
			}
		})
	}
}

// TestIntegration_PrimaryCommitUnresolvedNeverApproves is the fail-closed direction
// stated as its own invariant over the whole chain: for an unresolvable target, NO
// permission mode may yield Approve, and none may yield the empty NoOpinion verdict
// either — `{}` is auto-accepted in an auto-approving session, so it is an approval by
// another route. It is deliberately independent of WHICH non-approving verdict comes
// out, so tuning Ask-vs-Reject later cannot silently retire the guarantee.
func TestIntegration_PrimaryCommitUnresolvedNeverApproves(t *testing.T) {
	canonical, _ := nestedWorktreeFixture(t)
	commands := []string{
		"git -C $WT commit -m x",
		"git -C ${WT} commit -m x",
		"git -C \"$WT\" commit -m x",
		"git -C $(pwd) commit -m x",
		"git -C ~/repo commit -m x",
		"cd $WT && git commit -m x",
		"cd ${WT}/nested && git commit -m x",
		"git commit -m x && git -C $WT commit -m y",
	}
	for _, mode := range []string{"bypassPermissions", "auto", "dontAsk", "default", "plan", "acceptEdits", ""} {
		for _, cmd := range commands {
			t.Run(mode+" "+cmd, func(t *testing.T) {
				eng := buildFullEngine(canonical, canonical)
				got := eng.EvaluateHook(&hookio.HookInput{
					ToolName: "Bash", CWD: canonical,
					ToolInput: makeBashJSON(cmd), PermissionMode: mode,
				})
				if got.Decision == hookio.Approve || got.Decision == hookio.NoOpinion {
					t.Fatalf("%q in %q mode: got %s (%s: %s); an unresolvable commit target MUST NOT reach Approve or an empty verdict", cmd, mode, got.Decision, got.Module, got.Reason)
				}
			})
		}
	}
}
