package git

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE PERSISTENT `export` ROUTE (pg2-xjt1s).
//
// All three of this file's env screens read ONE LEAF's own prefix, so the inline spelling
// was screened and the spelling that OUTLIVES the command was not. Measured through the
// real binary on main @ a064a73e, 2026-08-14 (scripts/probe-pg2-xjt1s.sh):
//
//	GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status  -> {}
//	export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil; git status -> allow
//	GIT_PAGER=/tmp/evil git log     -> {}     export GIT_PAGER=/tmp/evil; git log     -> allow
//	GIT_DIR=/other git commit -m x  -> ask    export GIT_DIR=/other; git commit -m x  -> allow
//
// The export form is the MORE COMMON in-corpus spelling — 111 of the 183
// GIT_CONFIG-assignment-bearing Bash rows in the 154-day ask log.
//
// EVERY ASSERTION IS A RELATION BETWEEN THE TWO SPELLINGS, which is what the acceptance
// criteria ask for and what makes these rows survive a retune of either route: nothing here
// names Approve, NoOpinion, Ask or Reject for the hazard itself.
//
// TWO TEST HELPERS, AND THE SECOND ONE IS THE LOAD-BEARING ONE. `evalCmd` hands the WHOLE
// expression to the rule, which is the DIRECT-caller path: the rule's own Parse sees every
// leaf, so a cross-leaf screen would appear to work even if the engine seam were broken.
// Under the engine a rule receives ONE leaf plus RootExpression, and `evalLeaf` below
// reproduces exactly that. A change that reads only its own leaves passes the first and
// fails the second, which is the whole reason both exist.

// evalLeaf asks the rule about ONE leaf of an expression, the way
// engine.EvaluateExpression does: the leaf's own text as the command, and the whole
// expression as RootExpression.
func evalLeaf(t *testing.T, expr, leafRaw string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New(nil).Evaluate(&hookio.HookInput{
		ToolName:       "Bash",
		ToolInput:      mustJSON(map[string]string{"command": leafRaw}),
		RootExpression: expr,
	}))
}

// exportSeparators are the ways an export and a git call are joined in one expression. The
// bead names `;`, `&&` and a newline explicitly.
var exportSeparators = []string{"; ", " && ", "\n"}

// crossLeafHazards pairs an env prefix with the sibling screen it must trip. One per
// screen, so a change that widens two of the three and forgets the last fails here.
var crossLeafHazards = []struct {
	name   string
	prefix string // the assignment text, without `export`
	sub    string // the git invocation
}{
	// pg2-a12rl's config-SOURCE family (hasGitConfigEnvInjection).
	{"GIT_CONFIG triple", "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil", "status"},
	{"GIT_CONFIG_GLOBAL file", "GIT_CONFIG_GLOBAL=/tmp/evil.cfg", "status"},
	// pg2-6c85x's program-NAMING family (hasGitProgramEnvVar).
	{"GIT_PAGER", "GIT_PAGER=/tmp/evil", "log"},
	{"GIT_EXTERNAL_DIFF", "GIT_EXTERNAL_DIFF=/tmp/evil", "diff"},
	{"GIT_SSH_COMMAND", "GIT_SSH_COMMAND=/tmp/evil", "fetch origin"},
	{"GIT_ASKPASS", "GIT_ASKPASS=/tmp/evil", "fetch origin"},
	// The redirect pair (hasRedirectEnvVar) — the twin the bead requires be treated
	// alike rather than left silently open.
	{"GIT_DIR", "GIT_DIR=/other", "commit -m msg"},
	{"GIT_WORK_TREE", "GIT_WORK_TREE=/other", "commit -m msg"},
}

// TestGit_CrossLeafEnv_ExportMatchesTheInlinePrefix is the bead's headline relation: the
// persistent spelling reaches the SAME verdict as the inline-prefix spelling of the same
// assignment, in every separator.
func TestGit_CrossLeafEnv_ExportMatchesTheInlinePrefix(t *testing.T) {
	for _, h := range crossLeafHazards {
		inlineCmd := fmt.Sprintf("%s git %s", h.prefix, h.sub)
		inline := evalCmd(t, inlineCmd)
		for _, sep := range exportSeparators {
			gitLeaf := "git " + h.sub
			expr := fmt.Sprintf("export %s%s%s", h.prefix, sep, gitLeaf)
			got := evalLeaf(t, expr, gitLeaf)
			if got.Decision != inline.Decision {
				t.Errorf("%s, separator %q: the export spelling got %s (%s) but the inline prefix %q got %s (%s) — one hazard, two spellings, and they MUST reach the same verdict (pg2-xjt1s)",
					h.name, strings.TrimSpace(sep), got.Decision, got.Reason, inlineCmd, inline.Decision, inline.Reason)
			}
		}
	}
}

// TestGit_CrossLeafEnv_ExportIsNeverLessRestrictive is the weaker relation that must hold
// even where the strict equality above would be the wrong claim — a later ruling could
// legitimately make the persistent spelling STRICTER (it applies to every later command,
// not just this one), but never weaker.
//
// It runs over the decisive subcommands as well, so it also pins that the widening did not
// turn any screen into a pre-classify short-circuit (the pg2-6f4q9 defect).
func TestGit_CrossLeafEnv_ExportIsNeverLessRestrictive(t *testing.T) {
	subs := append([]string{
		"tag v1",
		"push --force origin main",
		"remote add upstream https://example.invalid/x.git",
		"config remote.origin.url https://evil.invalid/x.git",
		"config core.hooksPath /tmp/h",
		"clean -fdx",
		"reset --hard HEAD~1",
	}, approveClassSubcommands...)
	for _, h := range crossLeafHazards {
		for _, sub := range subs {
			inline := evalCmd(t, fmt.Sprintf("%s git %s", h.prefix, sub))
			gitLeaf := "git " + sub
			got := evalLeaf(t, fmt.Sprintf("export %s; %s", h.prefix, gitLeaf), gitLeaf)
			if got.Decision < inline.Decision {
				t.Errorf("%s, `git %s`: the export spelling got %s (%s), which is LESS restrictive than the inline prefix's %s (%s) — the persistent spelling must never be the cheaper way around the same guard (pg2-xjt1s)",
					h.name, sub, got.Decision, got.Reason, inline.Decision, inline.Reason)
			}
		}
	}
}

// TestGit_CrossLeafEnv_FailsClosed is the acceptance criterion that an export whose EFFECT
// cannot be determined MUST NOT reach Approve.
//
// Each row is a spelling from which the value, or the export state, or both cannot be read:
// a dynamic value, a bare `export NAME` whose value an earlier BASH CALL set (invisible to
// CETA entirely), the `declare`/`typeset` family whose attributes change what the value IS,
// and `set -a` turning a later plain assignment into an export. A screen that answered these
// individually would fail open on whichever it did not anticipate; taking the NAME and
// treating the value as unknown answers all of them by construction.
func TestGit_CrossLeafEnv_FailsClosed(t *testing.T) {
	rows := []struct{ expr, leaf string }{
		// The value is not statically readable.
		{"export GIT_PAGER=$X; git log", "git log"},
		{"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=$K GIT_CONFIG_VALUE_0=$V; git status", "git status"},
		{"export GIT_EXTERNAL_DIFF=$(command -v difft); git diff", "git diff"},
		{`export GIT_SSH_COMMAND="ssh -i /tmp/k"; git fetch origin`, "git fetch origin"},
		// A BARE export: the value came from an earlier Bash call and is invisible here.
		{"export GIT_PAGER; git log", "git log"},
		{"GIT_PAGER=/tmp/evil; export GIT_PAGER; git log", "git log"},
		{"export GIT_DIR; git commit -m msg", "git commit -m msg"},
		// The declare family — read whatever the flags say, because the net export state
		// per name is not modelled.
		{"declare -x GIT_PAGER=/tmp/evil; git log", "git log"},
		{"typeset -x GIT_PAGER=/tmp/evil; git log", "git log"},
		{"declare -gx GIT_EXTERNAL_DIFF=/tmp/evil; git diff", "git diff"},
		{"declare GIT_PAGER=/tmp/evil; git log", "git log"},
		// allexport: a LATER plain assignment really does reach the child (measured).
		{"set -a; GIT_PAGER=/tmp/evil; git log", "git log"},
		{"set -o allexport; GIT_CONFIG_GLOBAL=/tmp/evil.cfg; git status", "git status"},
		// A benign-looking value is screened anyway — value-blindness is unchanged.
		{"export GIT_PAGER=cat; git log", "git log"},
		// An inert EDITOR literal arriving through the declare family must NOT reach the
		// carve-out: its value is unknown by construction, which is the fail-closed half.
		{"declare -x GIT_EDITOR=true; git commit --amend", "git commit --amend"},
		// Several leaves between the export and the git call.
		{"export GIT_PAGER=/tmp/evil; echo one; echo two; git log", "git log"},
		// A duplicate leaf text resolves to its LAST occurrence, so the export is seen.
		{"git log; export GIT_PAGER=/tmp/evil; git log", "git log"},
	}
	for _, row := range rows {
		if got := evalLeaf(t, row.expr, row.leaf); got.Decision == hookio.Approve {
			t.Errorf("expr %q (leaf %q): got APPROVE (%s) — an export whose effect cannot be determined MUST NOT reach Approve (pg2-xjt1s)", row.expr, row.leaf, got.Reason)
		}
	}
}

// TestGit_CrossLeafEnv_DoesNotOverreach pins the boundary, and it is the half that keeps the
// change from being a blanket "any GIT_* text earlier in the command" screen.
//
// The plain-assignment rows are the measured ones. bash 5.3.9, 2026-08-14, reading the
// variable in a real child process: `NAME=v; child` and `NAME=v && child` leave it UNSET —
// a shell variable is not an environment variable — so git never sees it and screening it
// would be a false prompt on ordinary traffic. Likewise a pipeline stage's export
// (`export NAME=v | cat`), which runs in a subshell.
func TestGit_CrossLeafEnv_DoesNotOverreach(t *testing.T) {
	rows := []struct {
		expr, leaf string
		want       hookio.Decision
		why        string
	}{
		// A PLAIN assignment is a shell variable and reaches no child.
		{"GIT_CONFIG_COUNT=1; git status", "git status", hookio.Approve, "measured: a plain assignment is not in the child's environment"},
		{"GIT_PAGER=/tmp/evil; git log", "git log", hookio.Approve, "same"},
		{"GIT_PAGER=/tmp/evil && git log", "git log", hookio.Approve, "same, with &&"},
		{"GIT_DIR=/other; git commit -m msg", "git commit -m msg", hookio.Approve, "same; the redirect Ask needs the variable to actually reach git"},
		// A pipeline stage runs in a subshell — measured UNSET in a later child.
		{"export GIT_PAGER=/tmp/evil | cat; git log", "git log", hookio.Approve, "a pipeline stage's export does not reach the shell"},
		// An export AFTER the git call cannot affect it.
		{"git log; export GIT_PAGER=/tmp/evil", "git log", hookio.Approve, "the export is a LATER leaf"},
		// Names that are not screened, exported or not.
		{"export FOO=bar; git status", "git status", hookio.Approve, "an unrelated variable"},
		{"export GIT_PAGERX=/tmp/evil; git log", "git log", hookio.Approve, "a longer name is a different variable"},
		{"export git_pager=/tmp/evil; git log", "git log", hookio.Approve, "lowercase is not the variable git reads"},
		{"export GIT_SSH_VARIANT=ssh; git fetch origin", "git fetch origin", hookio.Approve, "selects a dialect; names no program"},
		// The editor carve-out still clears through the export route, with a STATIC inert
		// literal — the relief pg2-6qh3p bought must not be undone by this widening.
		{"export GIT_EDITOR=true; git commit --amend", "git commit --amend", hookio.Approve, "the inert-value editor carve-out (pg2-6qh3p) applies to the export spelling too"},
		{"export GIT_EDITOR=:; git rebase --skip", "git rebase --skip", hookio.Approve, "same, the null-command editor"},
		// TEXT IS NOT AN OPERATION — the pg2-5b901 class, over the new EXPRESSION scope.
		// This one matters more than usual: the seam re-parses RootExpression, so a text
		// match over it would be a much wider false-positive surface than a leaf-local one.
		{`git commit -m "screen the export GIT_CONFIG_COUNT=1 route (pg2-xjt1s)"`, `git commit -m "screen the export GIT_CONFIG_COUNT=1 route (pg2-xjt1s)"`, hookio.Approve, "a mention in a commit message is text"},
		{`echo "export GIT_PAGER=/tmp/evil" > notes.txt; git status`, "git status", hookio.Approve, "an export written INTO A FILE is data, not an assignment"},
	}
	for _, row := range rows {
		if got := evalLeaf(t, row.expr, row.leaf); got.Decision != row.want {
			t.Errorf("expr %q (leaf %q): got %s (%s), want %s — %s", row.expr, row.leaf, got.Decision, got.Reason, row.want, row.why)
		}
	}
}

// TestGit_CrossLeafEnv_LeafScopeFallbackIsUnchanged pins that the seam DEGRADES to exactly
// the pre-pg2-xjt1s behaviour when the expression is not available, which is what makes the
// widening incapable of regressing a verdict.
//
// Two fallbacks, and both are real: RootExpression is EMPTY for a non-engine caller
// (hookio.HookInput documents it), and it may be present but contain no leaf whose Raw
// matches — a shape this rule must survive rather than guess about.
func TestGit_CrossLeafEnv_LeafScopeFallbackIsUnchanged(t *testing.T) {
	for _, cmd := range []string{"git status", "git log", "git commit -m msg", "GIT_PAGER=/tmp/evil git log", "GIT_DIR=/other git commit -m msg"} {
		bare := evalCmd(t, cmd) // no RootExpression at all
		mismatched := evalLeaf(t, "some totally unrelated expression", cmd)
		if bare.Decision != mismatched.Decision {
			t.Errorf("cmd %q: leaf-only got %s (%s) but an unmatched RootExpression got %s (%s) — when the expression cannot be located the seam MUST fall back to leaf scope, which is the property that stops it regressing any verdict",
				cmd, bare.Decision, bare.Reason, mismatched.Decision, mismatched.Reason)
		}
	}
}

// TestGit_CrossLeafEnv_SequenceEditorRequirementStaysLeafLocal pins the ONE predicate
// deliberately NOT widened, and pins it as the CURRENT verdict rather than as a good idea.
//
// The three screens are demotions, so widening them only ever restricts. The rebase arm's
// editor test is a REQUIREMENT, so widening it would APPROVE something that abstains today —
// a relaxation, which needs its own ruling and its own replay. This row is where that ruling
// will show up: it fails the moment someone widens it, forcing the relaxation to be
// deliberate.
func TestGit_CrossLeafEnv_SequenceEditorRequirementStaysLeafLocal(t *testing.T) {
	leafLocal := evalCmd(t, "GIT_SEQUENCE_EDITOR=: git rebase -i main")
	if leafLocal.Decision != hookio.Approve {
		t.Fatalf("`GIT_SEQUENCE_EDITOR=: git rebase -i main`: got %s (%s), want APPROVE — pg2-a12rl's configenv_test.go pins this and pg2-xjt1s must not disturb it", leafLocal.Decision, leafLocal.Reason)
	}
	exported := evalLeaf(t, "export GIT_SEQUENCE_EDITOR=: ; git rebase -i main", "git rebase -i main")
	if exported.Decision == hookio.Approve {
		t.Errorf("`export GIT_SEQUENCE_EDITOR=: ; git rebase -i main`: got APPROVE (%s) — the rebase arm's editor REQUIREMENT is deliberately still leaf-local, because satisfying it from an earlier leaf is a RELAXATION and needs its own ruling; if that ruling has been made, move this row and record the replay", exported.Reason)
	}
}

// TestGit_CrossLeafEnv_EmitsEmptyHookOutput is the BOUNDARY assertion for the demotion arms:
// asserting the internal Decision cannot show what Claude Code RECEIVES, and `{}` versus
// `allow` is the entire difference this bead moves.
//
// The chain-level twin — proving no LATER rule re-approves these leaves — is the
// orchestrator's to add in the engine suite; the row edits are reported with this bead.
func TestGit_CrossLeafEnv_EmitsEmptyHookOutput(t *testing.T) {
	for _, row := range []struct{ expr, leaf string }{
		{"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil; git status", "git status"},
		{"export GIT_PAGER=/tmp/evil; git log", "git log"},
		{"export GIT_EXTERNAL_DIFF=/tmp/evil && git diff", "git diff"},
		{"export GIT_ASKPASS=/tmp/evil\ngit fetch origin", "git fetch origin"},
	} {
		got := evalLeaf(t, row.expr, row.leaf)
		out := string(hookio.FormatOutput(got, nil))
		if out != "{}" {
			t.Errorf("expr %q: emitted %s, want {} — `permissionDecision: \"allow\"` auto-approves a command whose environment names a program git will execute", row.expr, out)
		}
		if strings.Contains(out, "allow") {
			t.Errorf("expr %q: emitted %s, which contains an allow decision", row.expr, out)
		}
	}
}
