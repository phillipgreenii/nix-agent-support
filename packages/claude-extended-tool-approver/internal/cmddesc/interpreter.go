package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// Context is the environment an interpretation runs in. The generic
// interpreter in this slice reads neither field; they exist so a schema-driven
// interpreter for a cwd- or env-sensitive command has somewhere to look.
type Context struct {
	CWD string
	Env map[string]string
}

// ChildInvocation is a nested command an interpreter found inside an operand
// (a `sh -c` program, an `xargs` tail) that the graph builder should recurse
// into. Unused by cat/head; the type exists so the interface is complete.
type ChildInvocation struct {
	Dialect string
	Program string
	Source  string
}

// Interpretation is what an Interpreter produces for one leaf: its effects,
// any children to recurse into, and whether the model was SUFFICIENT — i.e.
// every token was understood. An insufficient interpretation still carries
// the effects it did understand, but can never be approved.
type Interpretation struct {
	Effects       []Effect
	Children      []ChildInvocation
	Sufficient    bool
	Insufficiency string
}

// Interpreter maps a parsed leaf plus its schema to an Interpretation. An
// implementation reads only the schema and the leaf; it MUST NOT branch on
// schema.Name.
type Interpreter interface {
	Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation
}

// interpreters is the lookup table for CommandSchema.Interpreter. The empty
// name is the generic, flag-table-driven interpreter.
var interpreters = map[string]Interpreter{
	"": GenericInterpreter{},
}

// LookupInterpreter resolves a schema's Interpreter name. The empty name
// resolves to GenericInterpreter; an unknown name reports false so the caller
// can mark the node insufficient rather than guess.
func LookupInterpreter(name string) (Interpreter, bool) {
	in, ok := interpreters[name]
	return in, ok
}

// GenericInterpreter is the flag-table-driven interpreter. It honours `--`
// (when the schema says so), `--flag=value`, POSIX short-flag bundling
// (`-nb`), short-glued values (`-n5`), the three arities, per-flag
// transforms (applied in flag order), the unknown-flag policy, the
// Leading/Rest/Trailing positional layout with its skipped-by-flags
// switches, the stdin token, program operands via the dialect seam, and
// cmdparse's live-expansion signal (a dynamic path effect). It never
// inspects schema.Name.
//
// It runs in two passes: the scan collects flags (recording which spellings
// appeared) and defers every operand, and the resolve pass assigns positional
// roles once the whole argv is known — so Trailing can see the end — and
// emits effects in argument order.
type GenericInterpreter struct{}

// Interpret implements Interpreter.
func (GenericInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := &interpState{schema: schema, leaf: leaf, ctx: ctx, flagsSeen: map[string]bool{}}
	args := leaf.Args
	optionsEnded := false
	scanned := true
	for i := 0; i < len(args); i++ {
		tok := args[i]
		isFlag := !optionsEnded && strings.HasPrefix(tok, "-") && tok != "-" && tok != schema.Positionals.StdinToken
		if !isFlag {
			st.ops = append(st.ops, pendingOp{tok: tok, idx: i, positional: true})
			continue
		}
		if tok == "--" && schema.EndOfOptions {
			optionsEnded = true
			continue
		}
		consumed, ok := st.flag(tok, i)
		if !ok {
			scanned = false
			break
		}
		i += consumed
	}
	st.resolve()
	if scanned {
		st.stdio()
	}
	return st.result()
}

// pendingOp is one deferred operand: a positional (role assigned at resolve
// time) or a flag value (role known at scan time).
type pendingOp struct {
	tok        string
	idx        int
	positional bool
	role       OperandRole
}

// interpState accumulates one interpretation. Insufficiency is recorded at
// the FIRST problem; later tokens are still not trusted.
type interpState struct {
	schema     CommandSchema
	leaf       cmdparse.ParsedCommand
	ctx        Context
	ops        []pendingOp
	flagsSeen  map[string]bool
	effects    []Effect
	children   []ChildInvocation
	transforms []EffectTransform
	pathOps    int
	stdinToken bool
	insuff     string
}

func (st *interpState) fail(format string, a ...any) {
	if st.insuff == "" {
		st.insuff = fmt.Sprintf(format, a...)
	}
}

// resolve assigns roles to the deferred positionals and emits every operand's
// effects in argument order.
func (st *interpState) resolve() {
	n := 0
	for _, op := range st.ops {
		if op.positional {
			n++
		}
	}
	roles, reason, ok := st.schema.Positionals.resolveRoles(n, st.flagsSeen)
	if !ok {
		st.fail("%s", reason)
	}
	pos := 0
	for _, op := range st.ops {
		role := op.role
		if op.positional {
			if !ok {
				continue
			}
			role = roles[pos]
			pos++
		}
		st.operand(role, op.tok, op.idx, op.positional)
	}
}

// operand emits the effect for one operand of the given role at arg index i.
func (st *interpState) operand(role OperandRole, tok string, i int, positional bool) {
	source := fmt.Sprintf("arg %d", i)
	switch {
	case role.IsPath():
		if positional && st.schema.Positionals.StdinToken != "" && tok == st.schema.Positionals.StdinToken && !st.leaf.ArgIsLiveExpansion(i) {
			st.stdinToken = true
			return
		}
		st.pathOps++
		st.effects = append(st.effects, Effect{
			Kind:           EffectPath,
			Path:           tok,
			Access:         role.pathAccess(),
			Dynamic:        st.leaf.ArgIsLiveExpansion(i),
			Source:         source,
			FromPositional: positional,
		})
	case role.Kind == KindProgram:
		st.program(role.Dialect, tok, i, source)
	case role.Kind == KindLiteral, role.Kind == KindMessage:
		// Inert: no effect. A live expansion in a literal slot is still inert —
		// its value cannot change what the command touches.
	default:
		st.fail("unmodeled operand role %d at %s", role.Kind, source)
	}
}

// program emits the Program effect for an operand and folds in the dialect
// interpreter's classification. The Program effect always stays (the graph
// records that a program ran); sufficiency comes from the dialect: a live
// expansion or an unknown dialect is insufficient, and a known dialect's own
// insufficiency propagates.
func (st *interpState) program(dialect, tok string, i int, source string) {
	st.effects = append(st.effects, Effect{Kind: EffectProgram, Program: tok, Dialect: dialect, Source: source})
	if st.leaf.ArgIsLiveExpansion(i) {
		st.fail("program operand at %s is a runtime expansion", source)
		return
	}
	d, ok := LookupDialect(dialect)
	if !ok {
		st.fail("no interpreter for dialect %s", dialect)
		return
	}
	// A path the program names (sed's `r x`) is NOT a path operand: the
	// command's own stdin rule still keys on operands only, so pathOps is left
	// alone here.
	res := d.InterpretProgram(tok, st.ctx)
	st.effects = append(st.effects, res.Effects...)
	st.children = append(st.children, res.Children...)
	if !res.Sufficient {
		st.fail("%s program at %s: %s", dialect, source, res.Insufficiency)
	}
}

// flag handles one `-`-prefixed token at index i. It returns how many FOLLOWING
// args it consumed and false when the token made the interpretation
// insufficient.
func (st *interpState) flag(tok string, i int) (int, bool) {
	if spec, ok := st.schema.Flags[tok]; ok {
		return st.applyFlag(tok, spec, i, "", false)
	}
	// --flag=value
	if strings.HasPrefix(tok, "--") {
		if eq := strings.IndexByte(tok, '='); eq > 0 {
			name, val := tok[:eq], tok[eq+1:]
			if spec, ok := st.schema.Flags[name]; ok && spec.Arity != ArityNone {
				return st.applyFlag(name, spec, i, val, true)
			}
		}
		return st.unknownFlag(tok)
	}
	// Short bundle: -abc, -n5 (arity-1 short flag with glued value), or -i.bak
	// (optional-glued short flag: the rest of the bundle is its value, even
	// when empty).
	rest := tok[1:]
	for pos := 0; pos < len(rest); pos++ {
		name := "-" + string(rest[pos])
		spec, ok := st.schema.Flags[name]
		if !ok {
			return st.unknownFlag(tok)
		}
		if spec.Arity == ArityNone {
			st.applyFlag(name, spec, i, "", false)
			continue
		}
		glued := rest[pos+1:]
		if glued == "" && spec.Arity == ArityOne {
			return st.applyFlag(name, spec, i, "", false)
		}
		return st.applyFlag(name, spec, i, glued, true)
	}
	return 0, true
}

// applyFlag records a modeled flag (spelling seen, transform) and its operand
// per arity — the glued value, the next arg, or none.
func (st *interpState) applyFlag(name string, spec FlagSpec, i int, glued string, hasGlued bool) (int, bool) {
	st.flagsSeen[name] = true
	st.transforms = append(st.transforms, spec.Transform)
	switch spec.Arity {
	case ArityNone:
		if hasGlued {
			st.fail("flag %s takes no value", name)
			return 0, false
		}
		return 0, true
	case ArityOne:
		if hasGlued {
			st.ops = append(st.ops, pendingOp{tok: glued, idx: i, role: spec.Operand})
			return 0, true
		}
		if i+1 >= len(st.leaf.Args) {
			st.fail("flag %s is missing its value", name)
			return 0, false
		}
		st.ops = append(st.ops, pendingOp{tok: st.leaf.Args[i+1], idx: i + 1, role: spec.Operand})
		return 1, true
	case ArityOptionalGlued:
		if hasGlued && glued != "" {
			st.ops = append(st.ops, pendingOp{tok: glued, idx: i, role: spec.Operand})
		}
		return 0, true
	default:
		st.fail("flag %s has unsupported arity %d", name, spec.Arity)
		return 0, false
	}
}

func (st *interpState) unknownFlag(tok string) (int, bool) {
	switch st.schema.UnknownFlag {
	case UnknownFlagInert:
		return 0, true
	default:
		st.fail("unknown flag %s", tok)
		return 0, false
	}
}

// stdio appends the stream effects the schema declares.
func (st *interpState) stdio() {
	readsStdin := st.stdinToken
	switch st.schema.Stdin {
	case StdinAlways:
		readsStdin = true
	case StdinWhenNoPathOperands:
		if st.pathOps == 0 {
			readsStdin = true
		}
	}
	if readsStdin {
		st.effects = append(st.effects, Effect{Kind: EffectStdio, Stream: StreamStdin, Source: "stdin"})
	}
	switch st.schema.Stdout {
	case StdoutContent:
		st.effects = append(st.effects, Effect{Kind: EffectStdio, Stream: StreamStdout})
	case StdoutMetadata:
		st.effects = append(st.effects, Effect{Kind: EffectStdio, Stream: StreamStdout, Metadata: true})
	}
}

// result applies the collected transforms generically, in flag order, and
// packages the interpretation.
func (st *interpState) result() Interpretation {
	effects := st.effects
	for _, t := range st.transforms {
		var ok bool
		effects, ok = applyTransform(t, effects)
		if !ok {
			st.fail("unrecognised effect transform %d", t.Kind)
			break
		}
	}
	return Interpretation{
		Effects:       effects,
		Children:      st.children,
		Sufficient:    st.insuff == "",
		Insufficiency: st.insuff,
	}
}
