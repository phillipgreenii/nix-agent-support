package cmddesc

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// xargsReplaceFlags are the spellings that set a replacement token; their
// value (or `{}` when none) is the token. This is the one piece of xargs's
// argument semantics the generic table cannot express: the positionals are
// not operands of xargs at all but the argv of the command it runs, and the
// token inside them is replaced by input items at runtime.
var xargsReplaceFlags = []string{"-I", "-i", "--replace"}

// xargsInterpreter runs the generic scan for xargs's own flags and then
// reconstructs the ONE command xargs runs as an `argv` child: every
// positional is an argv element; a positional containing the replacement
// token, or a live expansion, is dynamic; without a replacement token one
// trailing `<stdin-item>` operand stands for the items xargs appends. It does
// not know what command the child is — the builder recurses into it.
type xargsInterpreter struct{}

// Interpret implements Interpreter.
func (xargsInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	st.finish()
	if !st.scanned {
		return st.result()
	}
	pos := st.positionals()
	if len(pos) == 0 {
		st.fail("no command operand: xargs would run its default command, which is not modeled")
		return st.result()
	}
	token := ""
	if st.anyFlagSeen(xargsReplaceFlags...) {
		token = "{}"
		if vals := st.flagValues(xargsReplaceFlags...); len(vals) > 0 {
			token = vals[len(vals)-1].tok
		}
	}
	argv := make([]string, 0, len(pos)+1)
	dynamic := make([]bool, 0, len(pos)+1)
	for _, op := range pos {
		argv = append(argv, op.tok)
		dynamic = append(dynamic, st.leaf.ArgIsLiveExpansion(op.idx) || (token != "" && strings.Contains(op.tok, token)))
	}
	if token == "" {
		argv = append(argv, "<stdin-item>")
		dynamic = append(dynamic, true)
	}
	st.children = append(st.children, ChildInvocation{Dialect: "argv", Argv: argv, ArgvDynamic: dynamic, Source: st.baseName()})
	return st.result()
}
