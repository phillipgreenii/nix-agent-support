// tc-5h6e: the pg2-yeli3/pg2-wq3ki in-command-literal seam (readPathIssue's
// cmdparse.ExpandInCommand call, threaded via primarycommit.LeafVars) resolved a
// TOP-LEVEL dynamic path argument against earlier sibling leaves of the SAME
// expression — `D=<literal>; sed -n '1,5p' "$D/foo"`, pinned by
// engine_integration_test.go's TestIntegration_InCommandLiteralReadPathResolves — but
// never reached a leaf NESTED inside a command substitution or arithmetic expansion,
// however shallow. `D=<literal>; echo "$(cat $D/foo)"` abstained exactly like an
// ambient, unresolvable variable, even though $D is exactly as literal-in-this-command
// as it is in the already-relieved `sed` idiom.
//
// ROOT CAUSE. internal/engine/engine.go's evaluateParsed computes, per leaf i,
// `cmdparse.InCommandVars(parsed, i)` — the literal bindings established by leaves
// BEFORE i in `parsed`, the caller's own leaf slice. foldSubstitutionScan recurses into
// a substitution BODY through a fresh evaluateParsed call over `sub.Leaves` — the
// substitution's OWN internal leaf slice — which is a DIFFERENT slice than the outer
// expression's `parsed`. That recursive call's own `cmdparse.InCommandVars(sub.Leaves,
// i)` therefore has no way to see a binding like `D=<literal>` that lives in the OUTER
// expression's `parsed`, not in `sub.Leaves` at all — the substitution's own leaf list
// simply does not contain the outer assignment. Before this bead the recursive
// evaluateParsed call was handed no outer-scope parameter whatsoever, so this was true
// at every nesting depth: a substitution inside a substitution inside an arithmetic
// expansion lost the outermost command's bindings just as completely as a single level
// of nesting did (a "$(cat $D/foo)" and a "$(( $(date +%s) - $(cat $D/foo) ))" abstained
// identically before the fix — the fix is validated at ONE level of nesting because
// that is where the loss already starts, not because deeper nesting is a materially
// different case).
//
// THE FIX. evaluateParsed and foldSubstitutionScan both gained an outerVars/
// outerTempDirVars parameter (see their own doc comments). Each leaf's OWN merged
// environment — cmdparse.OverlayVars(outerVars, cmdparse.InCommandVars(parsed, i)),
// the outer scope with this leaf's own nearer siblings shadowing/revoking it exactly as
// InCommandVars' existing revocation rule already governs — is threaded down as the
// outerVars for any substitution nested inside THAT leaf. This is the SAME
// cmdparse.ExpandInCommand/readPathIssue resolution pg2-yeli3 already trusted for the
// top-level case, reaching a leaf it previously could not reach; nothing about WHICH
// values can resolve, or how a resolved value is judged once found, has changed — see
// the "unsafe leaf" and "unresolvable var" cases below, which prove exactly that.
package engine_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestIntegration_NestedSubstitutionInCommandLiteralResolves is the RELIEF this bead
// authorizes: a dynamic read-path argument nested inside one or more command
// substitutions / an arithmetic expansion now resolves against an EARLIER SIBLING
// leaf's literal assignment, exactly as the already-relieved top-level idiom does.
//
// The first two rows are the bug reports' own shapes (tc-ltr4), adapted to this
// project's workspace fixture rather than the reporter's scratchpad path — the
// TEXTUAL SHAPE (arithmetic-wrapped double command substitution; a bare single command
// substitution) is what matters, not the literal path bytes.
func TestIntegration_NestedSubstitutionInCommandLiteralResolves(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	relieved := []struct {
		name    string
		command string
	}{
		{
			"arithmetic expansion wrapping two nested command substitutions (tc-ltr4's `elapsed:` report)",
			`SP=` + projectRoot + `; echo "elapsed: $(( $(date +%s) - $(cat $SP/full.start) ))s"`,
		},
		{
			"one level of command-substitution nesting (the minimal shape the loss already reproduces at)",
			`D=` + projectRoot + `; echo "$(cat $D/README.md)"`,
		},
		{
			"nesting inside a second, sibling command substitution in the SAME leaf",
			`D=` + projectRoot + `; echo "$(cat $D/README.md) / $(cat $D/go.mod)"`,
		},
		{
			// Genuinely THREE levels of substitution nesting down to the leaf that
			// reads $D — an arithmetic expansion nested inside another arithmetic
			// expansion nested inside the outer echo's argument. Arithmetic bodies are
			// governed by recursion alone (ClassifySubstitutionBody's static switch
			// only fires for sub.IsCommandSubstitution()), so this shape isolates the
			// propagation depth from the ADR 0048 "both gates" floor entirely — unlike
			// `echo "$(echo "$(cat …)")"`, which LOOKS deeper but actually fails for an
			// unrelated, pre-existing reason: `echo` is not itself a member of either
			// static substitution-body allowlist (classifySubstitutionCommand), so ANY
			// substitution body whose sole leaf is `echo` is SubstitutionRefused and
			// unconditionally floored to Ask regardless of what it wraps — the same
			// mechanism (not variable resolution) that TestIntegration_
			// NestedSubstitutionOrCombinatorInnerLeafResolves documents for `||`.
			"three levels: arithmetic expansion nested inside arithmetic expansion nested inside the outer argument",
			`D=` + projectRoot + `; echo "$(( 1 + $(( $(cat $D/README.md) )) ))"`,
		},
	}
	for _, tt := range relieved {
		t.Run(tt.name, func(t *testing.T) {
			got := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(tt.command)})
			if got.Decision != hookio.Approve {
				t.Errorf("%q got %v (%s: %s); want Approve — the nested leaf's own $D should now resolve against the earlier sibling assignment",
					tt.command, got.Decision, got.Module, got.Reason)
			}
		})
	}
}

// TestIntegration_NestedSubstitutionOrCombinatorInnerLeafResolves is tc-ltr4's SECOND
// report (`S=<path>; echo "done: $(cat $S/full2.done 2>/dev/null || echo running)"`),
// and it is asserted differently from the shapes above ON PURPOSE.
//
// The substitution BODY here is `cat ... || echo running` — TWO leaves joined by `||`,
// not a sole simple command. internal/cmdparse/parser.go's ClassifySubstitutionBody
// (via soleSimpleCommandLeaf) classifies ANY non-sole-simple-command body
// SubstitutionRefused, and engine.go's foldSubstitutionScan floors a Refused body's
// contribution to commandSubstitutionFloor's Ask UNCONDITIONALLY (ADR 0048 / operator
// ruling pg2-gwp57's "both gates" requirement: recursion approving a Refused body must
// never leak an Approve through). That floor is INDEPENDENT of variable resolution —
// it fires for `cat /literal/path || echo running` exactly as it fires for
// `cat $S/full2.done || echo running` — so this bead's fix does not and MUST NOT move
// this command's outer verdict to Approve; doing so would require loosening the
// static-allowlist admission criteria for `||`-bearing substitution bodies, a DIFFERENT,
// unruled widening this bead does not authorize.
//
// What tc-5h6e's fix DOES change, and what this test actually pins, is the INNER
// leaf's own judgment: cat's own reason for not clearing must no longer be the
// unresolved-variable refusal ("has a dynamically-expanded path arg") — that leaf now
// resolves $S and is judged safe on its own — so the ONLY thing left holding the outer
// verdict at Ask is the ADR 0048 floor's own reason text, not a residual variable-
// resolution failure. A regression that reintroduced the propagation gap would still
// show "ask" here (masked by the ADR 0048 floor), which is exactly why the reason text,
// not just the Decision, is asserted.
func TestIntegration_NestedSubstitutionOrCombinatorInnerLeafResolves(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	cmd := `S=` + projectRoot + `; echo "done: $(cat $S/full2.done 2>/dev/null || echo running)"`
	got := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(cmd)})
	if got.Decision != hookio.Ask {
		t.Fatalf("%q got %v (%s: %s); want Ask — the ADR 0048 \"both gates\" floor for a non-sole-simple-command substitution body is unrelated to this bead and must still apply",
			cmd, got.Decision, got.Module, got.Reason)
	}
	if strings.Contains(got.Reason, "dynamically-expanded") {
		t.Errorf("%q reason %q still cites an unresolved variable; tc-5h6e's propagation fix should have resolved $S for the inner `cat` leaf, leaving only the ADR 0048 floor's own reason",
			cmd, got.Reason)
	}
	if !strings.Contains(got.Reason, "not positively cleared by both gates") {
		t.Errorf("%q reason %q does not name the ADR 0048 floor; want confirmation THAT mechanism (not variable resolution) is what still holds this at Ask",
			cmd, got.Reason)
	}

	// CONTROL: the identical body WITHOUT the `||` combinator — a sole simple command —
	// is not subject to the ADR 0048 floor at all, and DOES reach Approve once $S
	// resolves. This is what proves the Ask above is caused by the combinator shape,
	// not by some OTHER thing this bead's fix failed to reach.
	controlCmd := `S=` + projectRoot + `; echo "done: $(cat $S/README.md)"`
	controlGot := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(controlCmd)})
	if controlGot.Decision != hookio.Approve {
		t.Errorf("CONTROL %q got %v (%s: %s); want Approve — removing the `||` combinator should let the identical resolution reach a decisive verdict",
			controlCmd, controlGot.Decision, controlGot.Module, controlGot.Reason)
	}
}

// TestIntegration_NestedSubstitutionUnresolvableVarNeverApproves is the MANDATORY
// negative control: a variable that is NOT a literal in-command binding must keep
// abstaining from inside a nested substitution exactly as it always did — proving the
// fix is compositional (it resolves a value ExpandInCommand ALREADY proves literal, at
// a leaf position it previously could not reach) rather than shape-based (rubber-
// stamping anything textually shaped like "$VAR/path" once it sees the VAR name
// syntactically assigned somewhere).
func TestIntegration_NestedSubstitutionUnresolvableVarNeverApproves(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	cases := []struct {
		name    string
		command string
	}{
		{
			"ambient variable, no in-command assignment at all",
			`echo "$(cat $D/README.md)"`,
		},
		{
			"assigned from a command substitution, not a literal",
			`D=$(pwd); echo "$(cat $D/README.md)"`,
		},
		{
			"a later non-literal reassignment REVOKES the earlier literal binding",
			`D=` + projectRoot + `; D=$(pwd); echo "$(cat $D/README.md)"`,
		},
		{
			"a DIFFERENT name is not a binding for this one",
			`OTHER=` + projectRoot + `; echo "$(cat $D/README.md)"`,
		},
		{
			"prefix assignment on the leaf that OWNS the substitution is not established (expanded before the prefix applies)",
			`D=` + projectRoot + ` echo "$(cat $D/README.md)"`,
		},
		{
			"assignment inside a pipeline stage never reaches the shell",
			`D=` + projectRoot + ` | cat; echo "$(cat $D/README.md)"`,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(tt.command)})
			if got.Decision == hookio.Approve {
				t.Errorf("%q got Approve (%s: %s); an unresolvable/revoked/unrelated variable MUST NOT resolve just because it sits inside a substitution",
					tt.command, got.Module, got.Reason)
			}
			if !strings.Contains(got.Reason, "dynamically-expanded") {
				t.Errorf("%q got %v (%s: %s); want the SAME unresolved-dynamic-path refusal the top-level idiom already gives (pg2-yeli3), not some other reason",
					tt.command, got.Decision, got.Module, got.Reason)
			}
		})
	}
}

// TestIntegration_NestedSubstitutionResolvedButUnsafeStillRefuses is the fix's OTHER
// mandatory negative control: an ACTUALLY-UNSAFE leaf, nested at the identical position,
// must reach the SAME refusal a literal spelling of that unsafe path already gets — the
// resolution widens WHICH values reach safe-commands' zone check, never WHAT that check
// decides once a value reaches it (the identical parity engine_integration_test.go's
// TestIntegration_InCommandLiteralReadPathResolves already pins for the TOP-LEVEL case).
func TestIntegration_NestedSubstitutionResolvedButUnsafeStillRefuses(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	// Sanity: the literal spelling, nested at the identical substitution position,
	// abstains (safe-commands' own zone check, unrelated to variable resolution).
	literalCmd := `echo "$(cat /etc/shadow)"`
	literalGot := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(literalCmd)})
	if literalGot.Decision != hookio.NoOpinion {
		t.Fatalf("sanity: %q got %v, want abstain", literalCmd, literalGot.Decision)
	}

	resolvedCmd := `V=/etc; echo "$(cat $V/shadow)"`
	resolvedGot := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(resolvedCmd)})
	if resolvedGot.Decision != literalGot.Decision || resolvedGot.Module != literalGot.Module {
		t.Errorf("%q got %v (%s: %s); want the SAME verdict as the literal nested spelling (%v, %s) — resolution must route the dangerous literal through the identical zone check, not skip it",
			resolvedCmd, resolvedGot.Decision, resolvedGot.Module, resolvedGot.Reason, literalGot.Decision, literalGot.Module)
	}
	if !strings.Contains(resolvedGot.Reason, "/etc/shadow") {
		t.Errorf("%q reason %q does not name the resolved dangerous path", resolvedCmd, resolvedGot.Reason)
	}

	// The write path is explicitly out of scope for readPathIssue's vars threading
	// (pg2-2ke04/pg2-yeli3's own "MUST NOT change" list) — a write nested inside a
	// substitution must keep abstaining even when its own variable is fully resolvable,
	// exactly as the top-level write guard already does.
	writeCmd := `D=` + projectRoot + `; echo "$(rm $D/build)"`
	writeGot := eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(writeCmd)})
	if writeGot.Decision == hookio.Approve {
		t.Errorf("%q got Approve (%s: %s); the write guard must stay unaffected by this bead, nested or not", writeCmd, writeGot.Module, writeGot.Reason)
	}
}
