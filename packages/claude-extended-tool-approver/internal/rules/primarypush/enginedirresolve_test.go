// pg2-eqacu: the WHOLE-CHAIN guard for which directory a `git push` is judged against.
//
// It is an EXTERNAL test package (primarypush_test) because it drives the real engine and
// the real rule chain, which import this rule — the fixtures the bead names cannot be
// observed from inside the package:
//
//   - the `cd`/`pushd` re-root is the ENGINE's (EvaluateExpression, pg2-opclh), not this
//     rule's, and this rule MUST NOT grow a `cd` model of its own. `cd /abs/wt && git push`
//     is only judged against the worktree because the engine advanced the cwd first;
//   - the fail-safe direction is a claim about what the CHAIN does, not about this rule's
//     verdict. Behind primary-push the generic git rule approves a non-force push, so
//     "MUST NOT reach Approve" is only meaningful with that rule present;
//   - the directory bias that caused the bug is a property of the real FileResolver walking
//     a real filesystem: gitRoot walks UP from `<canonical>/$WT` and lands on the enclosing
//     canonical clone. A stub cannot reproduce it, so every case here runs against a REAL
//     canonical clone on its primary branch with a NESTED linked worktree under it — the
//     layout that produced the reported false Rejects.
//
// This is the push-side twin of internal/engine/primarycommit_worktree_test.go.
package primarypush_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

// hermeticEnviron removes git env vars inherited from a parent `git commit`'s hook
// environment (GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE, GIT_PREFIX,
// GIT_OBJECT_DIRECTORY, GIT_COMMON_DIR). These variables repoint tempdir git calls at
// the real repo, breaking test hermeticity when tests are run from a git commit hook —
// same pattern and same fix as pg2-f6cgn / commit 98f8c95d in packages/pb, ported here
// for pg2-rrhw2 (the primarycommit-side twin of this fixture, in internal/engine, hit
// this live).
//
// `-C <dir>` only changes the working directory before git runs; it does NOT override
// these variables, which git's own repo discovery consults FIRST and which `-C` cannot
// override. A value leaked into the environment of whatever shell/session launched
// `go test` silently redirects every "isolated" `-C canonical` call here onto whatever
// repository that variable names instead — confirmed by reproduction:
// `GIT_DIR=<ambient>/.git git -C <canonical> config user.email …` silently writes into
// <ambient>, not <canonical>, and <canonical>/.git is never even created.
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

// chainEngine is the REAL rule chain (setup.RuleChain) over an empty consumer config, so
// the rules behind primary-push — above all the generic git rule that approves a non-force
// push — are present and an APPROVE is observable rather than inferred from NoOpinion.
// shells is nil: no shell-ownership store offline, the same posture as replay.
func chainEngine(projectRoot, cwd string) *engine.Engine {
	pe := patheval.NewWithCWD(projectRoot, cwd)
	eng := engine.New()
	eng.SetPathEvaluator(pe)
	eng.RegisterRules(setup.RuleChain(eng, pe, configrules.Load(""), nil)...)
	return eng
}

func bashJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// verdict evaluates cmd through the whole chain, with the session cwd at `cwd` and the
// engine rebuilt per call so the running cwd starts fresh exactly as a hook invocation does.
func verdict(canonical, cwd, mode, cmd string) hookio.RuleResult {
	return chainEngine(canonical, cwd).EvaluateHook(&hookio.HookInput{
		ToolName: "Bash", CWD: cwd, ToolInput: bashJSON(cmd), PermissionMode: mode,
	})
}

// TestIntegration_PrimaryPushDirectoryResolution covers the four fixture shapes pg2-eqacu
// names, in both permission postures, with the session cwd pinned to the CANONICAL clone
// (on primary) throughout — the situation an agent working in a nested worktree is in.
func TestIntegration_PrimaryPushDirectoryResolution(t *testing.T) {
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
		// FIXTURE 4 — the push really does advance the canonical primary: unchanged
		// verdicts, with a reason that now names the directory it judged and how it chose it.
		{
			name: "bare push in the canonical clone on primary", command: "git push",
			wantAuto: hookio.Reject, wantInteractive: hookio.Approve,
			reasonHas: "Directory evaluated: " + canonical,
		},
		{
			name: "explicit HEAD:main in the canonical clone", command: "git push origin HEAD:main",
			wantAuto: hookio.Reject, wantInteractive: hookio.Approve,
			reasonHas: "Directory evaluated: " + canonical,
		},
		// FIXTURE 1 — literal `git -C /abs/wt push`: the nested worktree is on a feature
		// branch, so this must not be gated at all.
		{
			name: "literal -C into the nested worktree", command: "git -C " + worktree + " push",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name: "literal -C into the nested worktree, -u origin HEAD", command: "git -C " + worktree + " push -u origin HEAD",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// FIXTURE 2 — `cd /abs/wt && git push`: judged against the cd TARGET. The
		// assertion is that the rule sees the ENGINE's advanced cwd, so a future engine
		// change that stops advancing fails HERE.
		{
			name: "cd into the nested worktree then push", command: "cd " + worktree + " && git push",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// `pushd` changes directory exactly as `cd` does. It settles at NoOpinion rather
		// than Approve because nothing approves a `pushd` leaf — which is also why
		// modelling it cannot widen anything.
		{
			name: "pushd into the nested worktree then push", command: "pushd " + worktree + " && git push",
			wantAuto: hookio.NoOpinion, wantInteractive: hookio.NoOpinion,
		},
		{
			name: "subshell cd into the nested worktree", command: "(cd " + worktree + " && git push)",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// FIXTURE 3 — `git -C $VAR push`: the target is unknowable from the command text.
		// Never Approve. The auto-approving session keeps a hard deny — but for the RIGHT
		// reason now: before pg2-eqacu it denied claiming the push advanced primary, which
		// was a statement about the canonical clone the `$WT` had been resolved onto.
		{
			name: "unresolved -C variable", command: "git -C $WT push",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			name: "unresolved -C variable with an explicit feature refspec", command: "git -C $WT push origin feat:feat",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		// The same, reached through the CWD instead of `-C`. This is the pinned coupling:
		// the engine joins the unexpanded `$WT` into the running cwd, and that surviving
		// token is the ONLY thing that tells the rule the directory is unknown.
		{
			name: "unresolved cd target", command: "cd $WT && git push",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			name: "unresolved pushd target", command: "pushd $WT && git push",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			name: "unresolved -C command substitution", command: "git -C $(git rev-parse --show-toplevel) push",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		// pg2-wq3ki parity, and the OTHER half of this bead: a value the command itself
		// writes down resolves, so the worktree push stops being a hard deny. Before
		// pg2-eqacu the first row here rejected in an auto-approving session while the
		// literal `git -C <worktree> push` above it approved.
		{
			name: "in-command assignment into the nested worktree", command: "WT=" + worktree + " && git -C \"$WT\" push",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name: "in-command assignment then cd", command: "WT=" + worktree + " && cd \"$WT\" && git push",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// NOT LAUNDERED: resolving to the canonical clone on primary still denies — the
		// resolution makes the rule KNOW it is a primary push, which is the opposite of an
		// escape hatch.
		{
			name: "in-command assignment to the canonical clone still denies", command: "WT=" + canonical + " && git -C \"$WT\" push",
			wantAuto: hookio.Reject, wantInteractive: hookio.Approve,
			reasonHas: "Directory evaluated: " + canonical,
		},
		// Scope: an unresolved directory on a NON-push git subcommand is none of this
		// rule's business and must not acquire a gate.
		{
			name: "unresolved -C on git status is untouched", command: "git -C $WT status",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name: "unresolved -C on git fetch is untouched", command: "git -C $WT fetch origin",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, m := range []struct {
				mode string
				want hookio.Decision
			}{{"bypassPermissions", tc.wantAuto}, {"default", tc.wantInteractive}} {
				got := verdict(canonical, canonical, m.mode, tc.command)
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

// TestIntegration_PrimaryPushFromWorktreeCwd is the session-cwd source over the real
// fixture: an agent whose session is INSIDE the linked worktree pushes its feature branch.
// That is the ordinary workforest case and must cost nothing in any mode.
func TestIntegration_PrimaryPushFromWorktreeCwd(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)
	for _, mode := range []string{"bypassPermissions", "auto", "dontAsk", "default", "plan", ""} {
		for _, cmd := range []string{"git push", "git push origin HEAD", "git push -u origin feat"} {
			t.Run(mode+" "+cmd, func(t *testing.T) {
				got := verdict(canonical, worktree, mode, cmd)
				if got.Decision != hookio.Approve {
					t.Errorf("[%s] %q from the worktree: got %s (%s: %s), want approve", mode, cmd, got.Decision, got.Module, got.Reason)
				}
			})
		}
	}
}

// TestIntegration_PrimaryPushUnresolvedNeverApproves states the fail-closed direction as
// its own invariant over the whole chain: for an unresolvable target, NO permission mode
// may yield Approve, and none may yield the empty NoOpinion verdict either — `{}` is
// auto-accepted in an auto-approving session, so it is an approval by another route. It is
// deliberately independent of WHICH non-approving verdict comes out, so tuning
// Ask-vs-Reject later cannot silently retire the guarantee.
func TestIntegration_PrimaryPushUnresolvedNeverApproves(t *testing.T) {
	canonical, _ := nestedWorktreeFixture(t)
	commands := []string{
		"git -C $WT push",
		"git -C ${WT} push",
		"git -C \"$WT\" push",
		"git -C $WT push origin HEAD",
		"git -C $WT push origin feat:feat",
		"git -C $(pwd) push",
		"git -C ~/repo push",
		"cd $WT && git push",
		"cd ${WT}/nested && git push",
		"WT=$(mktemp -d) && git -C \"$WT\" push",
		"git push origin feat:feat && git -C $WT push",
	}
	for _, mode := range []string{"bypassPermissions", "auto", "dontAsk", "default", "plan", "acceptEdits", ""} {
		for _, cmd := range commands {
			t.Run(mode+" "+cmd, func(t *testing.T) {
				got := verdict(canonical, canonical, mode, cmd)
				if got.Decision == hookio.Approve || got.Decision == hookio.NoOpinion {
					t.Fatalf("%q in %q mode: got %s (%s: %s); an unresolvable push target MUST NOT reach Approve or an empty verdict", cmd, mode, got.Decision, got.Module, got.Reason)
				}
			})
		}
	}
}

// TestIntegration_PrimaryPushSpellingRelation is the safety relation for the less-restrictive
// half, over the real fixture and the whole chain: naming a directory through a variable the
// command ITSELF assigns must reach the SAME verdict as naming it literally. Phrased as a
// relation between two spellings rather than as expected decisions, so it keeps holding
// whatever those verdicts are later tuned to.
func TestIntegration_PrimaryPushSpellingRelation(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)
	for _, dir := range []string{canonical, worktree} {
		for _, mode := range []string{"bypassPermissions", "default"} {
			for _, tail := range []string{"push", "push origin HEAD"} {
				t.Run(mode+" "+dir+" "+tail, func(t *testing.T) {
					literal := verdict(canonical, canonical, mode, "git -C "+dir+" "+tail)
					viaVar := verdict(canonical, canonical, mode, "WT="+dir+" && git -C \"$WT\" "+tail)
					if literal.Decision != viaVar.Decision {
						t.Errorf("[%s] `WT=%s && git -C \"$WT\" %s` = %s (%s) but the literal spelling = %s; they must agree",
							mode, dir, tail, viaVar.Decision, viaVar.Reason, literal.Decision)
					}
				})
			}
		}
	}
}
