// pg2-ft2hl: the `declare` / `typeset` spelling of the in-command assignment pg2-wq3ki
// taught the chain to read, stated at the HOOK-OUTPUT boundary.
//
// It lives beside pg2-wq3ki's TestIntegration_PrimaryCommitInCommandVars (in a file of
// its own so that pinned table is not touched) and uses the same nested-worktree fixture,
// because the mechanism spans the same three layers: cmdparse reads the assignment out of
// a `declare` leaf's ARGS, the engine accumulates the environment per leaf, and the rule
// expands the `git -C` argument.
//
// THE ASSERTIONS ARE RELATIONS, not verdicts, wherever the acceptance criterion is a
// comparison between spellings — "the declare spelling is never LESS restrictive than the
// plain one" survives a later retuning of either, while a hardcoded pair of decisions
// records today's tuning and fails for the wrong reason tomorrow. Only the two claims
// that ARE absolute are written as verdicts: the relief (a worktree target no longer
// reaches the unresolved gate) and its limit (a canonical-clone target still hard-denies
// an auto-approving session).
//
// NOTE ON WHY IDENTITY IS NOT ASSERTED: `export` is in safe-commands' allowlist and
// `declare` deliberately is NOT — a `declare -x LD_PRELOAD=…` is an env-assignment vector
// the env-var guard cannot see, since the lowering keeps a decl's assignments in Args
// rather than EnvVars. So a `declare` leaf contributes NoOpinion where a plain assignment
// contributes the neutral Approve, and the two spellings' hook DECISIONS differ by that
// one step even though their RESOLUTION is now identical (which
// cmdparse.TestInCommandVars_AssignmentBuiltinSpellingParity asserts as identity, at the
// layer where it is true). That difference is in the RESTRICTIVE direction, which is why
// the relation below is the right shape for it.
package engine_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// declSpellings are the leading segments that bind WT to the same text, one per
// assignment-builtin spelling. `plain` is the baseline every other row is compared to.
var declSpellings = struct {
	plain    string
	relieved []string
	declined []string
}{
	plain: "WT=%s",
	// Authorized by pg2-ft2hl: these must READ the value.
	relieved: []string{"declare WT=%s", "typeset WT=%s"},
	// DECLINED (`local`, `readonly`, `nameref`) or unreadable by their own semantics (a
	// FLAGGED declare, whose value bash rewrites). Each must stay at least as restrictive
	// as the plain spelling — never more permissive than it, and never more permissive
	// than it was before this bead.
	declined: []string{
		"local WT=%s", "readonly WT=%s", "nameref WT=%s",
		"declare -i WT=%s", "declare -r WT=%s", "declare -n WT=%s",
		"OTHER=/x declare WT=%s",
	},
}

// TestIntegration_DeclareSpellingIsNeverLessRestrictive is the RELATION over spellings.
// For every target value and both `git -C` / `cd` shapes, in both permission postures, no
// assignment-builtin spelling may produce a LESS restrictive hook decision than the plain
// spelling of the same command. That is pg2-ft2hl's "no spelling becomes MORE permissive"
// criterion, and it covers the declined builtins as well as the authorized ones.
func TestIntegration_DeclareSpellingIsNeverLessRestrictive(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)

	targets := []struct{ name, value string }{
		{"a linked worktree named literally", worktree},
		{"the canonical clone named literally", canonical},
		{"a value no seam derives", "$(mktemp -d)"},
	}
	shapes := []struct{ name, tail string }{
		{"-C", ` && git -C "$WT" commit -m x`},
		{"cd", ` && cd "$WT" && git commit -m x`},
	}
	modes := []string{"bypassPermissions", "default"}

	for _, target := range targets {
		for _, shape := range shapes {
			for _, mode := range modes {
				decide := func(assignment string) hookio.RuleResult {
					eng := buildFullEngine(canonical, canonical)
					return eng.EvaluateHook(&hookio.HookInput{
						ToolName: "Bash", CWD: canonical,
						ToolInput:      makeBashJSON(strings.Replace(assignment, "%s", target.value, 1) + shape.tail),
						PermissionMode: mode,
					})
				}
				plain := decide(declSpellings.plain)
				for _, spelling := range append(append([]string{}, declSpellings.relieved...), declSpellings.declined...) {
					t.Run(target.name+"/"+shape.name+"/"+mode+"/"+spelling, func(t *testing.T) {
						got := decide(spelling)
						if got.Decision < plain.Decision {
							t.Errorf("%q reached %s (%s: %s) where the plain spelling reached %s — that spelling became MORE permissive",
								spelling, got.Decision, got.Module, got.Reason, plain.Decision)
						}
					})
				}
			}
		}
	}
}

// TestIntegration_DeclareSpellingRelievesTheResolvedTarget is the RELIEF this bead
// authorizes, and the one place a verdict is asserted outright: with the worktree written
// down in the same command, the `declare`/`typeset` spelling must no longer reach the
// unresolved-directory gate (Ask interactively, Reject in an auto-approving session) in
// EITHER posture. The reason text is checked too, because "not Ask" could be reached by
// some unrelated rule and the claim here is specifically that the DIRECTORY resolved.
func TestIntegration_DeclareSpellingRelievesTheResolvedTarget(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)

	for _, cmd := range []string{
		"declare WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"typeset WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"declare WT=" + worktree + ` && cd "$WT" && git commit -m x`,
		"typeset WT=" + worktree + ` && cd "$WT" && git commit -m x`,
	} {
		for _, mode := range []string{"bypassPermissions", "default"} {
			t.Run(mode+" "+cmd, func(t *testing.T) {
				eng := buildFullEngine(canonical, canonical)
				got := eng.EvaluateHook(&hookio.HookInput{
					ToolName: "Bash", CWD: canonical,
					ToolInput: makeBashJSON(cmd), PermissionMode: mode,
				})
				if got.Decision >= hookio.Ask {
					t.Errorf("got %s (%s: %s); the target is written down in the same command, so the unresolved gate MUST NOT fire",
						got.Decision, got.Module, got.Reason)
				}
				if strings.Contains(got.Reason, "cannot determine which repository or branch") {
					t.Errorf("reason %q is still the unresolved-directory reason", got.Reason)
				}
			})
		}
	}
}

// TestIntegration_DeclareSpellingIsNotALaunderingRoute is the LIMIT. Reading a `declare`
// value must not become a way to commit on the canonical primary: with the CANONICAL
// clone written down, the rule now KNOWS the commit is on primary and must hard-deny the
// auto-approving session, naming the directory it judged.
func TestIntegration_DeclareSpellingIsNotALaunderingRoute(t *testing.T) {
	canonical, _ := nestedWorktreeFixture(t)

	for _, cmd := range []string{
		"declare WT=" + canonical + ` && git -C "$WT" commit -m x`,
		"typeset WT=" + canonical + ` && git -C "$WT" commit -m x`,
		"declare WT=" + canonical + ` && cd "$WT" && git commit -m x`,
	} {
		t.Run(cmd, func(t *testing.T) {
			eng := buildFullEngine(canonical, canonical)
			got := eng.EvaluateHook(&hookio.HookInput{
				ToolName: "Bash", CWD: canonical,
				ToolInput: makeBashJSON(cmd), PermissionMode: "bypassPermissions",
			})
			if got.Decision != hookio.Reject {
				t.Errorf("got %s (%s: %s), want Reject — a resolved canonical target is a commit on primary",
					got.Decision, got.Module, got.Reason)
			}
			if !strings.Contains(got.Reason, "Directory evaluated: "+canonical) {
				t.Errorf("reason %q does not name the directory it judged", got.Reason)
			}
		})
	}
}

// TestIntegration_DeclareSpellingNeverApprovesTheUnestablished is the fail-closed
// direction, in the shape pg2-wq3ki's sibling test gives it: for a target the command
// does not ESTABLISH — because the value is not derivable, or because the builtin's own
// semantics mean the value written down is not the one bash keeps — NO permission mode
// may reach Approve or the empty NoOpinion verdict, since `{}` is auto-accepted in an
// auto-approving session and is therefore an approval by another route.
//
// Every command here CONTAINS an assignment naming the worktree or a derivable-looking
// value, which is the point: the environment must not become "there was a declare, so
// resolve something".
func TestIntegration_DeclareSpellingNeverApprovesTheUnestablished(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)
	commands := []string{
		// DECLINED builtins: the value is written down and still must not be read.
		"local WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"readonly WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"nameref WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"local WT=" + worktree + ` && cd "$WT" && git commit -m x`,
		// FLAGGED declare: bash rewrites the value, so the text is not what it holds.
		"declare -i WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"declare -l WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"declare -n WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"declare -r WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"declare -- WT=" + worktree + ` && git -C "$WT" commit -m x`,
		// A PREFIX assignment makes a declare's own write ephemeral (bash 5.3.9:
		// `WT=/first; WT=/x declare WT=/y; echo "$WT"` prints `/first`).
		"WT=/x declare WT=" + worktree + ` && git -C "$WT" commit -m x`,
		"OTHER=/x declare WT=" + worktree + ` && git -C "$WT" commit -m x`,
		// A declare in a PIPELINE STAGE assigns in a subshell that dies with it.
		"declare WT=" + worktree + ` | cat && git -C "$WT" commit -m x`,
		// The value itself is not derivable, whatever the spelling.
		"declare WT=$(mktemp -d)" + ` && git -C "$WT" commit -m x`,
		"typeset WT=$(git rev-parse --show-toplevel)" + ` && git -C "$WT" commit -m x`,
		// A REVOKED binding: the literal is established and then reassigned unreadably
		// through a declare, so the earlier value must not survive.
		"WT=" + worktree + " && declare -i WT=5+5" + ` && git -C "$WT" commit -m x`,
		"WT=" + worktree + " && readonly WT=/elsewhere" + ` && git -C "$WT" commit -m x`,
		// bash's ARRAY form: `$WT` is the FIRST ELEMENT, never the parenthesised text.
		"WT=(" + worktree + " /other)" + ` && git -C "$WT" commit -m x`,
		"declare -a WT=(" + worktree + " /other)" + ` && git -C "$WT" commit -m x`,
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
					t.Fatalf("%q in %q mode: got %s (%s: %s); a target the command does not ESTABLISH MUST NOT reach Approve or an empty verdict",
						cmd, mode, got.Decision, got.Module, got.Reason)
				}
			})
		}
	}
}
