package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// interpretSubcommand implements the subcommand-dispatch SHAPE described on
// CommandSchema.Subcommands: schema.Flags is scanned as the GLOBAL-option
// table until the first positional, that positional is the subcommand key,
// and the remainder of argv is interpreted under the subcommand's own
// CommandSchema — recursively through LookupInterpreter, so a Subcommands
// value that itself has Subcommands works the same way, one level or many.
// The two schemas' effects are concatenated (global effects first) and the
// PARENT's own flag transforms are applied over the combined list, in
// argument order, exactly like a flat schema's transforms — a global flag
// that transformed something would see the subcommand's effects too, though
// no schema in this slice has one.
func interpretSubcommand(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st, subIdx := scanGlobal(leaf, schema, ctx)
	if !st.scanned {
		return st.result()
	}
	if subIdx < 0 {
		st.fail("no subcommand given")
		return st.result()
	}
	if leaf.ArgIsLiveExpansion(subIdx) {
		st.fail("subcommand token at arg %d is a runtime expansion", subIdx)
		return st.result()
	}
	name := leaf.Args[subIdx]
	start := subIdx + 1 // first arg handed to the subcommand
	subSchema, ok := schema.Subcommands[name]
	if !ok && schema.DefaultSubcommand != "" {
		// The first positional is not a subcommand key: it is the default
		// subcommand's own first positional (yq's bare `yq '.a' f.yaml` is
		// `yq eval '.a' f.yaml`), so it is handed down, not consumed.
		name = schema.DefaultSubcommand
		subSchema, ok = schema.Subcommands[name]
		start = subIdx
	}
	if !ok {
		st.fail("unmodeled subcommand %s", name)
		return st.result()
	}
	subIn, ok := LookupInterpreter(subSchema.Interpreter)
	if !ok {
		st.fail("unknown interpreter %s for subcommand %s", subSchema.Interpreter, name)
		return st.result()
	}

	childArgs := append([]string(nil), leaf.Args[start:]...)
	childLive := make([]bool, len(childArgs))
	for i := range childArgs {
		childLive[i] = leaf.ArgIsLiveExpansion(start + i)
	}
	childLeaf := cmdparse.ParsedCommand{
		Executable:       name,
		Args:             childArgs,
		ArgLiveExpansion: childLive,
	}
	sub := subIn.Interpret(childLeaf, subSchema, ctx)

	effects := append(append([]Effect(nil), st.effects...), sub.Effects...)
	for _, t := range st.transforms {
		var ok bool
		effects, ok = applyTransform(t, effects)
		if !ok {
			st.fail("unrecognised effect transform %d", t.Kind)
			break
		}
	}

	insufficiency := st.insuff
	if insufficiency == "" {
		insufficiency = sub.Insufficiency
	}
	return Interpretation{
		Effects:       effects,
		Children:      sub.Children,
		Sufficient:    st.insuff == "" && sub.Sufficient,
		Insufficiency: insufficiency,
	}
}

// interpretVerbDispatch implements the BUILD-TOOL VERB-DISPATCH shape
// (CommandSchema.VerbFamily; tc-8og1 item 3 sub-slice 4; tc-vn5z Q4, ruled
// 2026-09-08): a WRAPPER whose child is spelled as a single ARGV-SHAPED verb
// positional — `just <verb> [args...]`, `npm run <verb> [-- args...]`,
// `devbox run <verb> [-- args...]` (the three targets slice 3ag's workspace
// verb-discovery facet covers). It reuses scanGlobal UNCHANGED — the SAME
// "scan the global-option table, stop at the first positional" shape
// interpretSubcommand already uses — per Q4's ruling ("reuse interpreter_
// subcommand.go's recursion... for argv-shaped children"). Where
// interpretSubcommand looks the positional up in a STATIC
// map[string]CommandSchema (Subcommands) and recurses into a further
// schema, this dispatch does not: the verb name is OPEN, project-defined
// data (there is no fixed per-verb schema to recurse into — the verb's
// BODY is opaque, defined by a justfile recipe/package.json script/
// devbox.json script this package cannot see the contents of), so
// interpretation stops at CAPTURING the verb itself as a single EffectExec
// naming (Family, Operation) — the two fields effectpolicy.
// judgeBuildToolVerb (slice 3ai) already reads, and the SAME two fields
// deletable.DiscoveredVerbs (slice 3ag) / evalcontract.VerbScopedApproval
// (slice 3ai) compare it against.
//
// EVERYTHING after the verb positional — further tokens, even ones shaped
// like flags (`just deploy --prod`, `npm run test -- --grep=x`) — is INERT
// to this model: it is an argument to the verb's own opaque body, which
// this package has no visibility into, exactly like KindLiteral. No further
// flag/path/program interpretation is attempted on it, which is WHY
// scanGlobal (not the ordinary interleaved scan()) is the right tool to
// reuse: scanGlobal stops scanning at the first positional instead of
// continuing to look for MORE flags afterward, so a recipe's own
// `--prod`-shaped argument is never mistaken for an unmodeled flag of the
// WRAPPER and never makes the whole invocation Insufficient.
//
// A live-expansion verb token is captured as a DYNAMIC EffectExec (Family
// set, Operation empty) rather than failing the interpretation closed — the
// SAME "well-understood shape, unknown value" treatment every other Dynamic
// effect in this package gets (a dynamic path, a dynamic chdir target):
// judgeBuildToolVerb's own first check (e.Dynamic -> Unknown) exists
// specifically to consume this. The interpretation stays Sufficient either
// way: what changes is the EFFECT's own Dynamic/Operation fields, not
// whether the model understood the shape — mirrors how a dynamic path
// operand stays Sufficient elsewhere in this package.
//
// No verb positional at all (a bare `just`, or `npm run` alone) does NOT
// dispatch: the schema's OWN top-level Positionals/ImplicitEffects/Stdin/
// Stdout describe that case instead (those fields are otherwise UNUSED by a
// VerbFamily-shaped schema, exactly like Subcommands' own documented
// convention of repurposing them to the subcommand — see VerbFamily's own
// doc comment, schema.go) — see justSchema/npmRunSchema's own doc comments
// (registry_breadth.go) for the worked bare-invocation cases.
func interpretVerbDispatch(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st, verbIdx := scanGlobal(leaf, schema, ctx)
	if !st.scanned {
		return st.result()
	}
	if verbIdx < 0 {
		st.finish()
		return st.result()
	}
	source := fmt.Sprintf("arg %d", verbIdx)
	if leaf.ArgIsLiveExpansion(verbIdx) {
		st.effects = append(st.effects, Effect{Kind: EffectExec, Family: schema.VerbFamily, Dynamic: true, Source: source})
		return st.result()
	}
	st.effects = append(st.effects, Effect{Kind: EffectExec, Family: schema.VerbFamily, Operation: leaf.Args[verbIdx], Source: source})
	return st.result()
}

// scanGlobal scans schema.Flags exactly like scan() does — bundling, glued
// values, arities, transforms, EndOfOptions, the unknown-flag policy — but
// STOPS at the first positional token instead of deferring it: the caller
// treats that index as the subcommand key and hands everything after it to
// the subcommand's own schema, so a later `-x` belongs to the SUBCOMMAND, not
// to this table. It returns -1 for the positional index when the scan never
// found one (every token was consumed as a global flag, or the scan failed).
func scanGlobal(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) (*interpState, int) {
	st := &interpState{schema: schema, leaf: leaf, ctx: ctx, flagsSeen: map[string]bool{}, scanned: true}
	args := leaf.Args
	optionsEnded := false
	for i := 0; i < len(args); i++ {
		tok := args[i]
		isFlag := !optionsEnded && strings.HasPrefix(tok, "-") && tok != "-"
		if !isFlag {
			return st, i
		}
		if tok == "--" && schema.EndOfOptions {
			optionsEnded = true
			continue
		}
		consumed, ok := st.flag(tok, i)
		if !ok {
			st.scanned = false
			return st, -1
		}
		i += consumed
	}
	return st, -1
}
