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
// (`-nb`) and short-glued values (`-n5`), per-flag arity, the unknown-flag
// policy, positional roles, the stdin token, and cmdparse's live-expansion
// signal (a dynamic path effect). It never inspects schema.Name.
type GenericInterpreter struct{}

// Interpret implements Interpreter.
func (GenericInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, _ Context) Interpretation {
	st := &interpState{schema: schema, leaf: leaf}
	args := leaf.Args
	positional := 0
	optionsEnded := false
	for i := 0; i < len(args); i++ {
		tok := args[i]
		isFlag := !optionsEnded && strings.HasPrefix(tok, "-") && tok != "-" && tok != schema.Positionals.StdinToken
		if !isFlag {
			st.operand(schema.Positionals.roleAt(positional), tok, i)
			positional++
			continue
		}
		if tok == "--" && schema.EndOfOptions {
			optionsEnded = true
			continue
		}
		consumed, ok := st.flag(tok, i)
		if !ok {
			return st.result()
		}
		i += consumed
	}
	st.stdio()
	return st.result()
}

// interpState accumulates one interpretation. Insufficiency is recorded at
// the FIRST problem; later tokens are still not trusted.
type interpState struct {
	schema     CommandSchema
	leaf       cmdparse.ParsedCommand
	effects    []Effect
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

// operand emits the effect for a positional or flag-valued operand of the given
// role at arg index i.
func (st *interpState) operand(role OperandRole, tok string, i int) {
	source := fmt.Sprintf("arg %d", i)
	switch {
	case role.IsPath():
		if st.schema.Positionals.StdinToken != "" && tok == st.schema.Positionals.StdinToken && !st.leaf.ArgIsLiveExpansion(i) {
			st.stdinToken = true
			return
		}
		st.pathOps++
		st.effects = append(st.effects, Effect{
			Kind:    EffectPath,
			Path:    tok,
			Access:  role.pathAccess(),
			Dynamic: st.leaf.ArgIsLiveExpansion(i),
			Source:  source,
		})
	case role.Kind == KindProgram:
		if st.leaf.ArgIsLiveExpansion(i) {
			st.fail("program operand at %s is a runtime expansion", source)
			return
		}
		st.effects = append(st.effects, Effect{Kind: EffectProgram, Program: tok, Dialect: role.Dialect, Source: source})
	case role.Kind == KindLiteral, role.Kind == KindMessage:
		// Inert: no effect. A live expansion in a literal slot is still inert —
		// its value cannot change what the command touches.
	default:
		st.fail("unmodeled operand role %d at %s", role.Kind, source)
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
			if spec, ok := st.schema.Flags[name]; ok && spec.Arity == 1 {
				return st.applyFlag(name, spec, i, val, true)
			}
		}
		return st.unknownFlag(tok)
	}
	// Short bundle: -abc, or -n5 (arity-1 short flag with glued value).
	rest := tok[1:]
	for pos := 0; pos < len(rest); pos++ {
		name := "-" + string(rest[pos])
		spec, ok := st.schema.Flags[name]
		if !ok {
			return st.unknownFlag(tok)
		}
		if spec.Arity == 0 {
			st.transforms = append(st.transforms, spec.Transform)
			continue
		}
		glued := rest[pos+1:]
		if glued == "" {
			return st.applyFlag(name, spec, i, "", false)
		}
		return st.applyFlag(name, spec, i, glued, true)
	}
	return 0, true
}

// applyFlag records a modeled flag's transform and, for arity 1, its operand —
// either the glued value or the next arg.
func (st *interpState) applyFlag(name string, spec FlagSpec, i int, glued string, hasGlued bool) (int, bool) {
	st.transforms = append(st.transforms, spec.Transform)
	switch spec.Arity {
	case 0:
		if hasGlued {
			st.fail("flag %s takes no value", name)
			return 0, false
		}
		return 0, true
	case 1:
		if hasGlued {
			st.operand(spec.Operand, glued, i)
			return 0, true
		}
		if i+1 >= len(st.leaf.Args) {
			st.fail("flag %s is missing its value", name)
			return 0, false
		}
		st.operand(spec.Operand, st.leaf.Args[i+1], i+1)
		return 1, true
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

// result applies the collected transforms generically and packages the
// interpretation.
func (st *interpState) result() Interpretation {
	effects := st.effects
	for _, t := range st.transforms {
		var ok bool
		effects, ok = applyTransform(t, effects)
		if !ok {
			st.fail("unrecognised effect transform %d", t)
			break
		}
	}
	return Interpretation{
		Effects:       effects,
		Sufficient:    st.insuff == "",
		Insufficiency: st.insuff,
	}
}

// applyTransform rewrites effects under t. Only TransformNone is defined in
// this slice; any other value reports false so the caller fails closed.
func applyTransform(t EffectTransform, effects []Effect) ([]Effect, bool) {
	switch t {
	case TransformNone:
		return effects, true
	default:
		return effects, false
	}
}
