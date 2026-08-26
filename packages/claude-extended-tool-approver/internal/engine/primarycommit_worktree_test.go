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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// hermeticEnviron removes git env vars inherited from a parent `git commit`'s hook
// environment (GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE, GIT_PREFIX,
// GIT_OBJECT_DIRECTORY, GIT_COMMON_DIR). These variables repoint tempdir git calls at
// the real repo, breaking test hermeticity when tests are run from a git commit hook —
// same pattern and same fix as pg2-f6cgn / commit 98f8c95d in packages/pb, ported here
// for pg2-rrhw2.
//
// `-C <dir>` only changes the working directory before git runs; it does NOT override
// these variables, which git's own repo discovery consults FIRST and which `-C` cannot
// override. A value leaked into the environment of whatever shell/session launched
// `go test` (a git hook context, a forgotten `export GIT_DIR=...`) silently redirects
// every "isolated" `-C canonical` call here onto whatever repository that variable
// names instead — this is exactly how this fixture corrupted the AMBIENT repo's shared
// .git/config in pg2-rrhw2 (confirmed by reproduction: `GIT_DIR=<ambient>/.git git -C
// <canonical> config user.email …` silently writes into <ambient>, not <canonical>,
// and <canonical>/.git is never even created).
//
// t.Setenv cannot fix this: GIT_DIR="" is not "unset" to git — it is a fatal "the
// empty string is not a valid path" — so the only reliable fix is to omit these
// variables from the subprocess's OWN environment entirely.
func hermeticEnviron() []string {
	skipVars := map[string]bool{
		"GIT_DIR": true, "GIT_INDEX_FILE": true, "GIT_WORK_TREE": true,
		"GIT_PREFIX": true, "GIT_OBJECT_DIRECTORY": true, "GIT_COMMON_DIR": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; !skipVars[k] {
			env = append(env, kv)
		}
	}
	return env
}

// nestedWorktreeFixture builds a canonical clone on branch "main" with a linked worktree
// on branch "feat" at <canonical>/.worktrees/feat, and returns both paths.
func nestedWorktreeFixture(t *testing.T) (canonical, worktree string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	canonical = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", canonical}, args...)...)
		cmd.Env = hermeticEnviron()
		if out, err := cmd.CombinedOutput(); err != nil {
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

// TestIntegration_PrimaryCommitMissingDirNeverApproves is pg2-5adzj's whole-chain guard,
// mirroring TestIntegration_PrimaryCommitUnresolvedNeverApproves for a DIFFERENT cause:
// the directory resolves to a literal path — correctly reflecting a preceding
// `cd`/`pushd`/`-C`, exactly as pg2-wq3ki intends — but that literal path does not exist
// on disk. Reproduced against the REAL production shape (asks.db row 326758, 2026-07-30):
// a worktree named by an earlier `W=<path>` assignment in the SAME command, `cd`ed into,
// then committed to with NO `-C` on the commit itself — except that by replay time the
// worktree had already been removed (landed and cleaned up), which is exactly what
// worktree.remove below reproduces. Before pg2-5adzj's fix this fell through to
// FileResolver's gitRoot walking UP past the missing directory, landing on the
// ENCLOSING canonical clone, and misreporting "primary" — the false positive this test
// pins shut.
func TestIntegration_PrimaryCommitMissingDirNeverApproves(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", canonical}, args...)...)
		cmd.Env = hermeticEnviron()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", canonical, args, err, out)
		}
	}
	// Mirror production exactly: the worktree existed, and has since been cleaned up.
	git("worktree", "remove", "--force", worktree)

	commands := []string{
		"cd " + worktree + " && git commit -m x",
		"pushd " + worktree + " && git commit -m x",
		"W=" + worktree + "; cd \"$W\" && git commit -q --amend -F - <<'EOF'\nmsg\nEOF",
		"git -C " + worktree + " commit -m x",
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
					t.Fatalf("%q in %q mode: got %s (%s: %s); a target directory that does not exist MUST NOT reach Approve or an empty verdict", cmd, mode, got.Decision, got.Module, got.Reason)
				}
				// The MISDIAGNOSIS pg2-5adzj is about: the agent must not be told it is on
				// primary/canonical when the real problem is the missing directory.
				if strings.Contains(got.Reason, "CANONICAL clone") || strings.Contains(got.Reason, "refusing this commit") {
					t.Errorf("%q in %q mode: reason %q wrongly reads as the primary-branch finding", cmd, mode, got.Reason)
				}
			})
		}
	}
}
