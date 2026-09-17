// pg2-zsv1c AC3: hookio.Evaluator.EvaluateStructure was widened to accept
// outerVars/outerTempDirVars, and nix.go's three structural-delegation call
// sites (nix develop -c, nix shell --command, nix-shell --run) now forward
// input.InCommandVars/input.InCommandTempDirVars — the in-command environment
// the engine already computed for nix.go's OWN leaf position — instead of
// nil,nil. This is the END-TO-END proof that the widening actually reaches a
// verdict: a PLAIN outer assignment (an earlier sibling leaf of the SAME
// top-level command, never exported) now resolves inside the nested script
// nix's inner-command unwrap delegates to, exactly as envvars.go's OWN nested
// bash -c recursion (this bead's AC1/AC2) already does for a leaf that is
// itself the bash -c wrapper rather than one reached through nix's unwrap.
//
// # THE REAL-CORPUS ROWS THIS DOES NOT RELIEVE, AND WHY THAT IS A SEPARATE GAP
//
// This bead's own motivating real-corpus rows (asks.db ids 341824/341956) have
// the identical PLAIN-outer-assignment shape, but construct the PATH value via
// an outer-shell QUOTE-SPLICE: `bash -c '\n  export PATH="'"$SP"'/bin-fbxdw:
// $PATH"\n  ...'` — the outer shell substitutes $SP into the single-quoted
// script text before nix's --command ever sees it (so that a value nix's own
// -c/--command argv could not otherwise carry a live substitution into is
// delivered as literal text instead). TestNixOuterVars_RealCorpusQuoteSplice_
// StillAsks below pins that this SPECIFIC shape is UNCHANGED by this bead: it
// still asks, and the reason (unlike the relieved cases) still comes from the
// "sensitive env var requires confirmation" default branch, never from
// preservesCallerValue's relief.
//
// ROOT CAUSE of that residual gap (verified against this tree, not assumed):
// cmdparse's own word-decoding (unquote, internal/cmdparse/parser.go) and
// cmdparse.LiteralAssignmentValueText (internal/cmdparse/shellparse.go) both
// deliberately refuse a value whose text carries a SURVIVING quote character —
// i.e. a value built from more than one quoted region ("mixed quoting",
// unquote's own doc: "so `a"b"c` KEEPS its quotes") — as an I4 safety fence:
// LiteralAssignmentValueText's `default:` case explicitly refuses "a
// *syntax.DblQuoted that does not span the WHOLE value (mixed quoting)". The
// real-corpus rows' PATH value is exactly that shape once nix.go's own
// innerCommandStructure re-quotes/reparses the extracted inner script, so
// preservesCallerValue's `LiteralAssignmentValueText(ev.Value)` call returns
// ok=false and the assignment falls through to the decisive "Ask" default
// REGARDLESS of what outerVars/outerTempDirVars carry — the outer-scope
// threading this bead adds is never even reached for this value shape. That
// fence is orthogonal to this bead's authorized scope (widening
// EvaluateStructure's signature) and to AC1/AC2's own nested-bash-c recursion,
// which hits the identical fence for the same reason if a caller ever spells
// the idiom this way beside a real command in a leaf envvars.go reaches
// directly. Relieving it would mean relaxing LiteralAssignmentValueText's I4
// fence for a specific class of mixed-quoted-but-otherwise-resolvable values —
// a separate, larger decision this bead's operator authorization ("widen
// EvaluateStructure... Update every mock...") does not cover and this test
// file does not attempt.
package engine_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestNixOuterVars_PlainOuterAssignment_Approve is the RELIEF this bead
// authorizes, for all three of nix.go's structural-delegation call sites.
func TestNixOuterVars_PlainOuterAssignment_Approve(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	relieved := []struct {
		name    string
		command string
	}{
		{
			"nix shell --command bash -c",
			"SP=/tmp/scratch-fbxdw\n" + `nix shell nixpkgs#hello --command bash -c 'export PATH="$SP/bin:$PATH"'`,
		},
		{
			"nix develop -c bash -c",
			"SP=/tmp/scratch-fbxdw\n" + `nix develop -c bash -c 'export PATH="$SP/bin:$PATH"'`,
		},
		{
			"nix-shell --run",
			"SP=/tmp/scratch-fbxdw\n" + `nix-shell --run 'export PATH="$SP/bin:$PATH"'`,
		},
	}
	for _, tt := range relieved {
		t.Run(tt.name, func(t *testing.T) {
			got := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(tt.command)})
			if got.Decision != hookio.Approve {
				t.Errorf("%q got %v (%s: %s); want Approve — the outer plain SP=... assignment should now resolve inside the delegated inner script",
					tt.command, got.Decision, got.Module, got.Reason)
			}
		})
	}
}

// TestNixOuterVars_AmbientVar_StillAsks is the MANDATORY negative control: a
// variable never assigned anywhere in the outer command (ambient/unresolvable)
// must keep asking exactly as before this bead — the widening resolves a name
// the command's OWN text binds, never an inherited/ambient one.
func TestNixOuterVars_AmbientVar_StillAsks(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	cmd := `nix shell nixpkgs#hello --command bash -c 'export PATH="$AMBIENT/bin:$PATH"; true'`
	got := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(cmd)})
	if got.Decision == hookio.Approve {
		t.Errorf("%q got Approve (%s: %s); want a non-approving decision — $AMBIENT is never assigned anywhere in the outer command",
			cmd, got.Module, got.Reason)
	}
}

// TestNixOuterVars_RealCorpusQuoteSplice_StillAsks pins this bead's own
// motivating real-corpus shape (asks.db ids 341824/341956, re-derived against
// the live production database, 2026-09-17) as UNRELIEVED by this bead — see
// this file's package doc for the root cause (cmdparse's I4 mixed-quoting
// fence, orthogonal to EvaluateStructure's outerVars widening). This is a
// DELIBERATE negative pin, not a bug report: it documents, in an executable
// form the next reader cannot miss, that AC3's "evaluate --baseline diff
// confirms rows 341824/341956 move ask -> allow" criterion is NOT met by this
// bead's change, and why.
func TestNixOuterVars_RealCorpusQuoteSplice_StillAsks(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	cmd := "SP=/tmp/scratch-fbxdw\n" +
		`nix shell nixpkgs#hello --command bash -c '` + "\n" +
		`  export PATH="'"$SP"'/bin-fbxdw:$PATH"` + "\n" +
		`  hello` + "\n" +
		`'`
	got := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(cmd)})
	if got.Decision == hookio.Approve {
		t.Fatalf("%q got Approve (%s: %s); want a non-approving decision — this test exists to CATCH the day cmdparse's I4 mixed-quoting fence is relaxed, so whoever relaxes it updates this pin deliberately instead of an unnoticed side effect flipping the real-corpus rows",
			cmd, got.Module, got.Reason)
	}
	if !strings.Contains(got.Reason, "PATH") {
		t.Errorf("%q reason %q does not mention PATH; want the env-vars module's decisive Ask for the PATH assignment", cmd, got.Reason)
	}
}
