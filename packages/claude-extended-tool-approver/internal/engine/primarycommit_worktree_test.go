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

// hermeticEnviron builds a MINIMAL, EXPLICITLY ALLOWLISTED environment for the git
// subprocesses this fixture shells out to, so that no code path here can touch a real
// git repo/path BY CONSTRUCTION — not merely because today's known-leaky var names
// happen to be scrubbed.
//
// pg2-rrhw2's original fix (this function, before pg2-8wnhc) scrubbed a DENYLIST of six
// named vars (GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE, GIT_PREFIX, GIT_OBJECT_DIRECTORY,
// GIT_COMMON_DIR) out of the inherited os.Environ() — same pattern and same fix as
// pg2-f6cgn / commit 98f8c95d in packages/pb. That denylist already missed
// GIT_CEILING_DIRECTORIES — the SAME variable pg2-jqwrr's original bug report named as a
// leak vector — which is not hypothetical: it is the exact failure mode ("a new
// inheritable git-location var the list doesn't yet know about") the operator design
// guidance behind pg2-8wnhc warned could recur, demonstrated by the very fix meant to
// prevent it.
//
// `-C <dir>` only changes the working directory before git runs; it does NOT override
// GIT_DIR and friends, which git's own repo discovery consults FIRST and which `-C`
// cannot override. A value leaked into the environment of whatever shell/session
// launched `go test` (a git hook context, a forgotten `export GIT_DIR=...`) silently
// redirects every "isolated" `-C canonical` call here onto whatever repository that
// variable names instead — this is exactly how this fixture corrupted the AMBIENT
// repo's shared .git/config in pg2-rrhw2 (confirmed by reproduction: `GIT_DIR=<ambient>
// /.git git -C <canonical> config user.email …` silently writes into <ambient>, not
// <canonical>, and <canonical>/.git is never even created).
//
// Fixed structurally here by inverting denylist to allowlist: the subprocess
// environment is built by ADDING only the handful of vars git demonstrably needs for
// these local, no-network operations (init/config/commit/worktree/checkout), rather
// than by SUBTRACTING vars known to be dangerous. Any git env var this list does not
// name — known today, forgotten today (GIT_CEILING_DIRECTORIES), or invented by a
// future git release — is excluded automatically, because inclusion requires an
// explicit entry rather than someone remembering to add it to a ban list before it can
// leak.
//
// HOME is a second, independent confinement layer: instead of forwarding the ambient
// value, it is pointed at its own fresh t.TempDir(). Even a git code path this allowlist
// has not anticipated that falls back to $HOME/<something> lands in a directory created
// empty for this test and torn down with it — never the real user's home.
// GIT_CONFIG_NOSYSTEM=1 is set unconditionally for the same reason: system config is
// skipped outright, not merely redirected by GIT_CONFIG_SYSTEM (still forwarded below,
// since callers rely on t.Setenv-ing it to "/dev/null" explicitly).
//
// t.Setenv alone cannot fix any of this: GIT_DIR="" is not "unset" to git — it is a
// fatal "the empty string is not a valid path" — so the only reliable fix is to omit
// these variables from the subprocess's OWN environment entirely.
func hermeticEnviron(t *testing.T) []string {
	t.Helper()
	ambient := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			ambient[k] = v
		}
	}
	env := []string{"HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	// PATH: to locate the git binary and anything it execs. TMPDIR: git's own scratch
	// files. GIT_CONFIG_GLOBAL/_SYSTEM: forwarded so a caller's t.Setenv override
	// (every caller here points them at /dev/null) actually reaches the subprocess.
	// None of these four names a git repository location.
	for _, k := range []string{"PATH", "TMPDIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if v, ok := ambient[k]; ok {
			env = append(env, k+"="+v)
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
		cmd.Env = hermeticEnviron(t)
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
		cmd.Env = hermeticEnviron(t)
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

// TestIntegration_PrimaryCommitSelfCreatedDir is pg2-70g51's whole-chain guard for the
// literal-but-missing-dir shape (pg2-69i0d's row 426677): the SAME "removed worktree"
// fixture TestIntegration_PrimaryCommitMissingDirNeverApproves uses, but the command
// ITSELF recreates that exact path (`mkdir`/`git init`) before committing to it, rather
// than assuming it is still there. Every POSITIVE row is "&&"-chained end to end, so
// mkdir's success is REQUIRED for the commit to ever run — the directory it creates
// cannot be the pre-existing canonical clone, whatever path it is. Every NEGATIVE row
// swaps in a ";"/newline, reproducing row 426677's OWN shape exactly: a failed mkdir
// there falls through to a `cd` that ALSO fails and leaves the shell wherever it
// already was (the canonical clone, in this fixture) — those rows MUST keep the
// pre-existing fail-safe verdict, never Approve or the empty NoOpinion (an approval by
// another route in an auto-accepting session).
func TestIntegration_PrimaryCommitSelfCreatedDir(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", canonical}, args...)...)
		cmd.Env = hermeticEnviron(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", canonical, args, err, out)
		}
	}
	// Mirror TestIntegration_PrimaryCommitMissingDirNeverApproves: the path existed as
	// a linked worktree and has since been cleaned up, so it is genuinely absent —
	// exactly what pg2-70g51's own live-repro recipe uses `rm -rf`+`mktemp -d` for.
	git("worktree", "remove", "--force", worktree)

	positive := []string{
		"mkdir -p " + worktree + " && cd " + worktree + " && git init -q && git commit -q -m seed --allow-empty",
		"mkdir -p " + worktree + " && git -C " + worktree + " init -q && git -C " + worktree + " commit -q -m seed --allow-empty",
		"git init -q " + worktree + " && cd " + worktree + " && git commit -q -m seed --allow-empty",
	}
	for _, mode := range []string{"bypassPermissions", "auto", "dontAsk", "default", "plan", "acceptEdits", ""} {
		for _, cmd := range positive {
			t.Run("positive "+mode+" "+cmd, func(t *testing.T) {
				eng := buildFullEngine(canonical, canonical)
				got := eng.EvaluateHook(&hookio.HookInput{
					ToolName: "Bash", CWD: canonical,
					ToolInput: makeBashJSON(cmd), PermissionMode: mode,
				})
				// NoOpinion (Abstain), not Approve: primarycommit defers entirely
				// (findingNone -> NotApplicable) and nothing else in the chain has an
				// opinion on a bare mkdir/git-init/git-commit sequence either — the
				// same "allow" outcome TestPrimaryCommit_MissingDir's own "bare CWD
				// reported missing stays fail-open" case already asserts. What MUST
				// NOT happen is Ask or Reject.
				if got.Decision != hookio.NoOpinion {
					t.Errorf("%q in %q mode: got %s (%s: %s), want NoOpinion (allow)", cmd, mode, got.Decision, got.Module, got.Reason)
				}
			})
		}
	}

	negative := []string{
		// row 426677's OWN shape: ";"-separated create-then-cd on one logical unit,
		// the commit reached only via a LATER, independently-connected leaf.
		"mkdir -p " + worktree + "; cd " + worktree + " && git init -q && git commit -q -m seed --allow-empty",
		"mkdir -p " + worktree + "\ncd " + worktree + " && git init -q && git commit -q -m seed --allow-empty",
		// No creating leaf at all — an absent directory the command merely NAMES.
		"cd " + worktree + " && git commit -q -m seed --allow-empty",
	}
	for _, mode := range []string{"bypassPermissions", "auto", "dontAsk", "default", "plan", "acceptEdits", ""} {
		for _, cmd := range negative {
			t.Run("negative "+mode+" "+cmd, func(t *testing.T) {
				eng := buildFullEngine(canonical, canonical)
				got := eng.EvaluateHook(&hookio.HookInput{
					ToolName: "Bash", CWD: canonical,
					ToolInput: makeBashJSON(cmd), PermissionMode: mode,
				})
				if got.Decision == hookio.Approve || got.Decision == hookio.NoOpinion {
					t.Errorf("%q in %q mode: got %s (%s: %s); a ';'-broken (or absent) creator MUST NOT reach Approve or an empty verdict", cmd, mode, got.Decision, got.Module, got.Reason)
				}
			})
		}
	}
}
