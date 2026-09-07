package cmddesc

import (
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
	subSchema, ok := schema.Subcommands[name]
	if !ok {
		st.fail("unmodeled subcommand %s", name)
		return st.result()
	}
	subIn, ok := LookupInterpreter(subSchema.Interpreter)
	if !ok {
		st.fail("unknown interpreter %s for subcommand %s", subSchema.Interpreter, name)
		return st.result()
	}

	childArgs := append([]string(nil), leaf.Args[subIdx+1:]...)
	childLive := make([]bool, len(childArgs))
	for i := range childArgs {
		childLive[i] = leaf.ArgIsLiveExpansion(subIdx + 1 + i)
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
