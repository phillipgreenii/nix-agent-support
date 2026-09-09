package hookio

import "github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"

// LeavesOf and RootLeavesOf used to live in internal/cmdparse, taking
// *hookio.HookInput as their one parameter (ADR 0039 step 3's rule-facing
// accessors for HookInput.ParsedLeaf/ParsedRoot). They moved here in slice 3ap
// of the effect-graph spike (tc-8og1): that parameter was cmdparse's ONLY
// reason to import internal/hookio at all, and hookio importing cmdparse back
// (needed so ParsedLeaf/ParsedRoot and Evaluator.EvaluateStructure's `leaves`
// parameter could carry the concrete []cmdparse.ParsedCommand type instead of
// `any`) would have cycled through it. Relocating the two functions — rather
// than the *HookInput type they close over — removes cmdparse's hookio import
// entirely, so this package can import cmdparse freely. Every call site in
// internal/engine and internal/rules/* now reads hookio.LeavesOf /
// hookio.RootLeavesOf where it used to read cmdparse.LeavesOf /
// cmdparse.RootLeavesOf; nothing about their behaviour changed in the move.

// LeavesOf returns a Bash HookInput's already-parsed leaf structure — ADR 0039
// step 3's replacement for the pattern `cmdStr, err := input.BashCommand();
// parsed := cmdparse.Parse(cmdStr)` that every rule module used to spell for
// itself. That pattern re-derived structure the engine had already computed:
// engine.EvaluateExpression parses the whole expression once, then used to
// re-serialise ONE leaf's `Raw` into a synthetic `ToolInput` JSON string
// (`mustBashJSON`) purely so each rule could unmarshal it back out and re-parse
// it — root cause 3 of ADR 0039's Context, restated at the rule boundary
// instead of the engine-to-seam one.
//
// When the engine threaded the leaf's structure onto input.ParsedLeaf (see that
// field's doc), this returns it directly — no parse call at all. When it did
// not — a direct unit-test call, or the real top-level Bash input at
// EvaluateHook's entry point before EvaluateExpression has split it into
// leaves — this falls back to parsing BashCommand(), i.e. EXACTLY what every
// call site did before this function existed, so no existing caller's
// behaviour changes.
//
// The error is BashCommand()'s: a genuine failure (wrong tool, malformed
// ToolInput), never cmdparse's own "unparseable" — cmdparse.Parse already
// discards that distinction at this same boundary (see its own doc), and this
// function makes the identical choice for the threaded path.
func LeavesOf(input *HookInput) ([]cmdparse.ParsedCommand, error) {
	if input.ParsedLeaf != nil {
		return input.ParsedLeaf, nil
	}
	cmd, err := input.BashCommand()
	if err != nil {
		return nil, err
	}
	return cmdparse.Parse(cmd), nil
}

// RootLeavesOf is LeavesOf's sibling for HookInput.RootExpression: the full
// expression's already-parsed leaf set, threaded onto input.ParsedRoot by
// engine.EvaluateExpression alongside RootExpression itself (see that field's
// doc). A rule needing the SIBLING leaves — the same scope RootExpression
// exists to recover — reads them through here instead of re-parsing
// RootExpression itself, which is what git's `expressionScope` and gitdir's
// `pipeScope` did before this function existed.
//
// Falls back to parsing RootExpression when the engine did not thread it (a
// direct unit-test call), and returns nil when RootExpression is itself empty
// — the same "no expression, no leaves" answer every existing caller's own
// `if input.RootExpression == ""` guard already gave, restated here so callers
// that used to guard for it themselves no longer have to.
func RootLeavesOf(input *HookInput) []cmdparse.ParsedCommand {
	if input == nil {
		return nil
	}
	if input.ParsedRoot != nil {
		return input.ParsedRoot
	}
	if input.RootExpression == "" {
		return nil
	}
	return cmdparse.Parse(input.RootExpression)
}
