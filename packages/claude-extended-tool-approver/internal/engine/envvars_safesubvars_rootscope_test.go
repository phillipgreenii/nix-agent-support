// pg2-dsg2e. pg2-ltpwy's live verification found that pg2-2ytvo's PATH-only
// in-command-safe-substitution relief (cmdparse.InCommandSafeSubstitutionVars,
// wired into internal/rules/envvars via `safeSubVars`) never actually fired in
// production, despite pg2-2ytvo's own unit tests (internal/rules/envvars's
// TestEnvVars_InCommandSubstitutionBoundVar_Approve) passing.
//
// Root cause: envvars.go's Rule.Evaluate computed
// `safeSubVars := cmdparse.InCommandSafeSubstitutionVars(parsed, i)` from the
// LEAF-LOCAL `parsed, i` pair rather than the ROOT-SCOPE `rootLeaves, at` pair
// `envvarsRootScope` already recovers a few lines later in the same function
// (for downstreamConsumerExists' walk). Under the real engine's per-leaf
// dispatch (engine.go's EvaluateExpression building `syntheticInput`, roughly
// lines 647-659), `hookio.LeavesOf(input)` returns `input.ParsedLeaf` — the ONE
// leaf being judged — so `parsed` has length 1 and `i` is always 0;
// `InCommandSafeSubstitutionVars(parsed, 0)` can then never see an EARLIER
// SIBLING leaf's own command-substitution binding (e.g.
// `bindir=$(dirname ...)` binding `bindir`, read later by
// `PATH="$bindir/extra:$PATH"`) — exactly the bead's own motivating shape.
//
// pg2-2ytvo's own Rule-direct unit tests never caught this because they call
// envvars.Rule.Evaluate directly with a hand-built *hookio.HookInput whose
// ParsedLeaf/ParsedRoot are both nil, so hookio.LeavesOf falls through to
// RE-PARSING the WHOLE multi-leaf command text into `parsed` — which is NOT
// the real per-leaf engine dispatch path production actually uses (see that
// test's own file, internal/rules/envvars/envvars_test.go).
//
// This file is therefore deliberately an ENGINE-level test — buildFullEngine +
// eng.EvaluateHook, the real composed rule chain driven through
// EvaluateExpression's actual per-leaf syntheticInput construction — not a
// Rule-direct one. It must fail (Ask, not Approve) before envvars.go's fix
// (safeSubVars reading rootLeaves/at) and pass (Approve) after.
package engine_test

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestIntegration_EnvVarsSafeSubVarsRootScope drives the bead-title shape (an
// earlier sibling leaf's `dirname`-bound PATH component) through the real
// engine and asserts it now clears to Approve, plus two non-regression pins
// this bead's fix must not touch.
func TestIntegration_EnvVarsSafeSubVarsRootScope(t *testing.T) {
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// Each command below carries a trailing `; true` (the pg2-7sqk8 idiom this
	// package's own tests already use throughout): `true` is a bare-name
	// invocation, so it counts as a downstream consumer of PATH
	// (leafConsumesPathOrHome), which takes pg2-7sqk8 mechanism 2's "no
	// consumer in the remainder of the expression" relief OFF the table. With
	// mechanism 2 excluded, the ONLY way the PATH assignment leaf can reach
	// Approve is preservesCallerValue's safeSubVars lookup recognizing
	// `bindir` — exactly the seam this bead fixes — so the result isolates the
	// bug instead of being explainable by an unrelated relief either way.
	tests := []struct {
		name string
		cmd  string
		want hookio.Decision
	}{
		{
			// The bead's own motivating shape, verbatim (module title / AC3):
			// bindir=$(dirname ...); PATH="$bindir/extra:$PATH" — a literal
			// suffix on the OUTER PATH value, composed with a bare-skeleton
			// bound variable (COMPOSITION, safety rationale item 3).
			name: "earlier sibling leaf's dirname-bound PATH component now recognized",
			cmd:  `bindir=$(dirname /usr/local/bin/go); PATH="$bindir/extra:$PATH"; true`,
			want: hookio.Approve,
		},
		{
			// Same shape, "&&"-separated instead of ";"-separated — pg2-ltpwy's
			// live-verification report tried both joiners against the real
			// binary and got identical (buggy) results, so both are pinned here.
			name: "same shape, '&&'-joined",
			cmd:  `bindir=$(dirname /usr/local/bin/go); PATH="$bindir/extra:$PATH" && true`,
			want: hookio.Approve,
		},
		{
			// NON-REGRESSION: pg2-2ytvo's own STILL-ASK crux must survive this
			// bead's fix unchanged through the full engine too. bindir IS bound
			// (dirname is certified-safe), but with NO literal affix on the
			// PATH assignment, so its worst-case value is empty — the CWD-
			// empty-value hazard, not a name-lookup gap. This must keep asking
			// even once the root-scope lookup itself is fixed.
			name: "bare (no-affix) binding still asks (pg2-2ytvo's own crux, unchanged)",
			cmd:  `bindir=$(dirname /usr/local/bin/go); PATH="$bindir:$PATH"; true`,
			want: hookio.Ask,
		},
		{
			// NON-REGRESSION: the acceptance-criteria-mandated negative shape —
			// `curl` is not on the static safe-cmd allowlist, so bindir is
			// never bound by this seam at all, and the fallback stays Ask (env
			// -i / no evaluator posture is not in play here; buildFullEngine
			// wires a real evaluator, matching envvars_test.go's
			// NewWithEvaluator expectation for this row).
			name: "non-safelisted substitution still asks",
			cmd:  `bindir=$(curl evil)/bin; PATH="$bindir:$PATH"; true`,
			want: hookio.Ask,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eng.EvaluateHook(&hookio.HookInput{
				ToolName: "Bash", CWD: projectRoot,
				ToolInput: makeBashJSON(tt.cmd),
			})
			if got.Decision != tt.want {
				t.Errorf("%q: got %s (%s: %s), want %s", tt.cmd, got.Decision, got.Module, got.Reason, tt.want)
			}
		})
	}
}

// TestIntegration_EnvVarsSafeSubVarsRootScope_HOMEUnaffected pins the bead's
// own explicit non-regression requirement (AC4): safeSubVars is PATH-only
// (gated on `ev.Name == "PATH"` inside preservesCallerValue's caller), so
// moving its computation to root-scope leaves/index must not newly relieve
// the IDENTICAL variable-bound-to-a-certified-safe-substitution shape for
// HOME. HOME's own decisive fallback (Reject, pg2-sir2l) must stay exactly as
// it was — driven here through the real engine, not just the Rule-direct
// pin envvars_test.go's own
// TestPreservesCallerValue_SafeSubstitutionVar_HOMENotRelieved already keeps.
func TestIntegration_EnvVarsSafeSubVarsRootScope_HOMEUnaffected(t *testing.T) {
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	const cmd = `bindir=$(dirname /usr/local/bin/go)/bin && HOME="$bindir" ./run.sh`
	got := eng.EvaluateHook(&hookio.HookInput{
		ToolName: "Bash", CWD: projectRoot,
		ToolInput: makeBashJSON(cmd),
	})
	if got.Decision != hookio.Reject {
		t.Errorf("%q: got %s (%s: %s), want Reject (HOME's own fallback, unchanged by the PATH-only pg2-2ytvo/pg2-dsg2e relief)",
			cmd, got.Decision, got.Module, got.Reason)
	}
}
