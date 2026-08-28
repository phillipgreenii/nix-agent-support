// pg2-wq3ki: the WHOLE-CHAIN guard for a commit whose directory the command ITSELF
// writes down — `WT=/abs/worktree && git -C "$WT" commit`, and the `cd "$WT"` spelling
// of the same thing.
//
// It lives beside pg2-h2npt's TestIntegration_PrimaryCommitDirectoryResolution (in a
// file of its own so that pinned sweep is not touched) and for the same reason: the
// mechanism spans three layers and no single-layer test can observe it. cmdparse reads
// the assignment, the ENGINE accumulates the environment per leaf and expands a
// `cd`/`pushd` target before its verbatim join, and the RULE expands a `git -C`
// argument. The fixture is the same nested layout — a canonical clone on its primary
// branch with a linked worktree UNDER it — because that nesting is what turns a
// mis-resolved directory into a confident "commit on primary".
//
// THE DIRECTION OF THIS BEAD IS THE OPPOSITE OF ITS SIBLINGS' and the tests are written
// to keep it honest: exactly one shape gets LESS restrictive (a target the command
// establishes literally), and every neighbouring shape — an inherited variable, a
// non-derivable `$(…)`, a PREFIX assignment bash would not have expanded, a pipeline
// stage whose assignment dies with it, and a genuine commit on the canonical primary —
// MUST keep the verdict it had.
package engine_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestIntegration_PrimaryCommitInCommandVars is the acceptance table for pg2-wq3ki.
// Every row runs in BOTH permission postures with the session cwd pinned to the
// canonical clone (on primary), which is where an agent working in a nested worktree
// actually is.
func TestIntegration_PrimaryCommitInCommandVars(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)

	cases := []struct {
		name                      string
		command                   string
		wantAuto, wantInteractive hookio.Decision
		reasonHas                 string
	}{
		// ── ACCEPTANCE 1: the `-C` spelling resolves and does NOT gate ──────────────
		{
			name:     "assignment then -C into the worktree",
			command:  "WT=" + worktree + " && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "the same with a ';' separator",
			command:  "WT=" + worktree + "; git -C \"$WT\" commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "braced reference",
			command:  "WT=" + worktree + " && git -C \"${WT}\" commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "unquoted reference",
			command:  "WT=" + worktree + " && git -C $WT commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "reference with a trailing path segment",
			command:  "ROOT=" + canonical + " && git -C \"$ROOT/.worktrees/feat\" commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "export establishes it too",
			command:  "export WT=" + worktree + " && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		// ── ACCEPTANCE 2: the `cd` spelling resolves IDENTICALLY ────────────────────
		{
			name:     "assignment then cd then commit",
			command:  "WT=" + worktree + " && cd \"$WT\" && git commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "assignment then cd with a trailing segment",
			command:  "ROOT=" + canonical + " && cd \"$ROOT/.worktrees/feat\" && git commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			// `pushd` settles at NoOpinion for the same reason the literal spelling does
			// (nothing approves a `pushd` leaf), which is also why re-rooting it can
			// never widen anything. What matters here is that it is no longer the
			// Reject/Ask of an unresolved target.
			name:     "assignment then pushd then commit",
			command:  "WT=" + worktree + " && pushd \"$WT\" && git commit -m x",
			wantAuto: hookio.NoOpinion, wantInteractive: hookio.NoOpinion,
		},
		// ── ACCEPTANCE 3: a target the command does NOT establish keeps its verdict ──
		{
			// The inherited-export case: CETA sees no environment, so this is unknowable.
			name:     "no assignment at all (inherited variable)",
			command:  "git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			// The DECLINED derivation. `$(git rev-parse --show-toplevel)` is not read,
			// deliberately — see dirresolve.go's DECLINED section.
			name:     "value from a git read command substitution",
			command:  "WT=$(git rev-parse --show-toplevel) && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			// pg2-70g51: a `mktemp -d`-bound variable IS a narrow exception to
			// ACCEPTANCE 3's "not established" rule, not an instance of it — see the
			// dedicated ACCEPTANCE 5 block below. This row is the "cd" spelling; the
			// "-C" spelling is covered there too.
			name:     "value from a fresh mktemp -d, cd spelling, is now recognized safe",
			command:  "WT=$(mktemp -d) && cd \"$WT\" && git commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			// A PREFIX assignment is scoped to that one command's environment AND is
			// applied AFTER its words are expanded, so bash would NOT have expanded
			// `$WT` to the worktree here. Resolving it would be a wrong answer, not a
			// permissive one.
			name:     "prefix assignment on the same leaf is not established",
			command:  "WT=" + worktree + " git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			// A pipeline stage runs in a subshell, so WT never reaches the shell.
			name:     "assignment in a pipeline stage is not established",
			command:  "WT=" + worktree + " | cat && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			// A later unreadable, non-mktemp assignment REVOKES the earlier literal
			// one — deliberately reassigned to $(git rev-parse --show-toplevel)
			// rather than $(mktemp -d) so this row keeps testing REVOCATION and does
			// not collide with pg2-70g51's ACCEPTANCE 5 (below), which is what a
			// mktemp -d reassignment would now trigger.
			name:     "reassignment to a non-literal, non-mktemp value revokes the binding",
			command:  "WT=" + worktree + " && WT=$(git rev-parse --show-toplevel) && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			// A DIFFERENT variable is not a binding for this one.
			name:     "a different name is not a binding",
			command:  "OTHER=" + worktree + " && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		// ── ACCEPTANCE 4: a genuine commit on the canonical primary still Rejects ────
		{
			name:     "plain commit in the canonical clone on primary",
			command:  "git commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Approve,
			reasonHas: "Directory evaluated: " + canonical,
		},
		{
			// THE ONE THAT PROVES RESOLUTION IS NOT A LAUNDERING ROUTE: the variable
			// resolves to the CANONICAL clone, so the rule now KNOWS it is a commit on
			// primary and hard-denies the auto-approving session. Before this bead the
			// same row was denied for the wrong reason (unresolvable); it must not
			// become an approval.
			name:     "assignment resolving to the canonical clone still Rejects",
			command:  "WT=" + canonical + " && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Approve,
			reasonHas: "Directory evaluated: " + canonical,
		},
		{
			name:     "assignment then cd into the canonical clone still Rejects",
			command:  "WT=" + canonical + " && cd \"$WT\" && git commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Approve,
			reasonHas: "Directory evaluated: " + canonical,
		},
		// ── ACCEPTANCE 5 (pg2-70g51): an "&&"-chained `mktemp -d` binding is safe ────
		// regardless of the literal value — see selfCreatedTempDir's own doc for the
		// full argument. Every positive row here is paired with a ";"-separated
		// NEGATIVE row proving the safety argument really does hinge on "&&": with a
		// ";" the `mktemp -d` leaf's own success is no longer REQUIRED for the
		// commit leaf to run at all, so the target stays genuinely unresolvable.
		{
			name:     "-C spelling, mktemp -d bound directly",
			command:  "WT=$(mktemp -d) && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "reassignment TO a fresh mktemp -d is also recognized safe",
			command:  "WT=" + worktree + " && WT=$(mktemp -d) && git -C \"$WT\" commit -m x",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
		{
			name:     "NEGATIVE: ';'-separated mktemp -d is still unresolved",
			command:  "WT=$(mktemp -d); git -C \"$WT\" commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		{
			name:     "NEGATIVE: ';'-separated mktemp -d, cd spelling, is still unresolved",
			command:  "WT=$(mktemp -d); cd \"$WT\" && git commit -m x",
			wantAuto: hookio.Reject, wantInteractive: hookio.Ask,
			reasonHas: "cannot determine which repository or branch",
		},
		// ── SCOPE: a non-commit git subcommand is none of this rule's business ──────
		{
			name:     "resolved -C on git status is untouched",
			command:  "WT=" + worktree + " && git -C \"$WT\" status",
			wantAuto: hookio.Approve, wantInteractive: hookio.Approve,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, m := range []struct {
				mode string
				want hookio.Decision
			}{{"bypassPermissions", tc.wantAuto}, {"default", tc.wantInteractive}} {
				eng := buildFullEngine(canonical, canonical)
				got := eng.EvaluateHook(&hookio.HookInput{
					ToolName: "Bash", CWD: canonical,
					ToolInput: makeBashJSON(tc.command), PermissionMode: m.mode,
				})
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

// TestIntegration_PrimaryCommitInCommandVarsReasonNamesTheResolution pins the reason
// text's cause-naming quality (pg2-h2npt's contribution, which this bead must not
// erode): when the directory came from an EXPANDED `-C`, the prompt states the token as
// the author wrote it AND the value it resolved to. An agent that reads only the
// resolved path cannot find the text to fix, and one that reads only `$WT` cannot tell
// which directory was judged.
func TestIntegration_PrimaryCommitInCommandVarsReasonNamesTheResolution(t *testing.T) {
	canonical, _ := nestedWorktreeFixture(t)
	eng := buildFullEngine(canonical, canonical)
	got := eng.EvaluateHook(&hookio.HookInput{
		ToolName: "Bash", CWD: canonical,
		ToolInput:      makeBashJSON("WT=" + canonical + " && git -C \"$WT\" commit -m x"),
		PermissionMode: "bypassPermissions",
	})
	for _, want := range []string{
		"`git -C $WT`",
		"resolved to " + canonical + " from an assignment earlier in the same command",
		"Directory evaluated: " + canonical,
	} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason %q does not mention %q", got.Reason, want)
		}
	}
}

// TestIntegration_PrimaryCommitInCommandVarsNeverApprovesTheUnestablished is the
// fail-closed direction stated as an invariant over the whole chain, in the shape
// pg2-h2npt's TestIntegration_PrimaryCommitUnresolvedNeverApproves gave it: for a target
// the command does not itself establish, NO permission mode may reach Approve or the
// empty NoOpinion verdict — `{}` is auto-accepted in an auto-approving session, so it is
// an approval by another route.
//
// Every command here CONTAINS an assignment, which is the point: the environment must
// not be a blanket "there was an assignment, so resolve something".
func TestIntegration_PrimaryCommitInCommandVarsNeverApprovesTheUnestablished(t *testing.T) {
	canonical, worktree := nestedWorktreeFixture(t)
	commands := []string{
		"WT=$(git rev-parse --show-toplevel) && git -C \"$WT\" commit -m x",
		"WT=$(git rev-parse --show-toplevel); cd \"$WT\" && git commit -m x",
		// NOT here: "WT=$(mktemp -d) && git -C \"$WT\" commit -m x" and
		// "WT=" + worktree + " && WT=$(mktemp -d) && git -C \"$WT\" commit -m x" moved
		// OUT to TestIntegration_PrimaryCommitInCommandVars's ACCEPTANCE 5 (pg2-70g51):
		// an "&&"-chained `mktemp -d` binding IS now established, for reasons that do
		// not apply to $(git rev-parse …)/`pwd` above — a `mktemp -d` directory cannot
		// be the canonical clone REGARDLESS of its literal value, which
		// selfCreatedTempDir proves structurally rather than by resolving it. The
		// ";"-separated variant of the SAME command stays in this "never approves"
		// list below (unchanged, still genuinely unestablished).
		"WT=$(mktemp -d); git -C \"$WT\" commit -m x",
		"WT=`pwd` && git -C \"$WT\" commit -m x",
		"WT=" + worktree + " git -C \"$WT\" commit -m x",
		"WT=" + worktree + " | cat && git -C \"$WT\" commit -m x",
		"OTHER=" + worktree + " && git -C \"$WT\" commit -m x",
		"WT=" + worktree + " && git -C \"${WT:-/tmp}\" commit -m x",
		"WT=" + worktree + " && git -C \"$WTX\" commit -m x",
		"WT=" + worktree + " && git -C \"$WT\"'*' commit -m x",
		"WT='$HOME' && git -C \"$WT\" commit -m x",
		"WT=" + worktree + " && git -C \"$OTHER\" commit -m x",
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
