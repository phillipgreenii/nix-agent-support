package cmddesc

import (
	"fmt"
	"path"
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
// that the graph builder recurses into. It carries EITHER program TEXT in a
// shell dialect (Dialect "shell", Program) OR an already-split argument vector
// (Dialect "argv", Argv, with ArgvDynamic marking the operands whose value is
// only known at runtime — an xargs item, a replaced token). Source names the
// flag or operand that produced it (`bash -c`, `xargs`) and becomes the label
// of the scope the child is judged in.
type ChildInvocation struct {
	Dialect     string
	Program     string
	Argv        []string
	ArgvDynamic []bool
	Source      string
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
// name is the generic, flag-table-driven interpreter; the other keys are the
// only place outside registry data where a command's own argument semantics
// are named — each entry exists because the generic table cannot express how
// that command gives its operands meaning.
var interpreters = map[string]Interpreter{
	"":      GenericInterpreter{},
	"xargs": xargsInterpreter{},
	"curl":  curlInterpreter{},
	"find":  findInterpreter{},
}

// LookupInterpreter resolves a schema's Interpreter name. The empty name
// resolves to GenericInterpreter; an unknown name reports false so the caller
// can mark the node insufficient rather than guess.
func LookupInterpreter(name string) (Interpreter, bool) {
	in, ok := interpreters[name]
	return in, ok
}

// RegisterInterpreter adds (or replaces) a command-level interpreter under
// name. It exists for tests and extension; it is not safe to call
// concurrently with interpretation.
func RegisterInterpreter(name string, in Interpreter) {
	interpreters[name] = in
}

// GenericInterpreter is the flag-table-driven interpreter. It honours `--`
// (when the schema says so), `--flag=value`, POSIX short-flag bundling
// (`-nb`), short-glued values (`-n5`), the three arities, per-flag
// transforms (applied in flag order), the unknown-flag policy, the
// Leading/Rest/Trailing positional layout with its skipped-by-flags
// switches, the stdin token, the data-or-@file convention, program operands
// via the dialect seam, and cmdparse's live-expansion signal (a dynamic path
// effect). It never inspects schema.Name.
//
// It runs in two passes: the scan collects flags (recording which spellings
// appeared) and defers every operand, and the resolve pass assigns positional
// roles once the whole argv is known — so Trailing can see the end — and
// emits effects in argument order. Both passes are shared with the
// command-level interpreters, which run them and then add what the table
// cannot say.
type GenericInterpreter struct{}

// Interpret implements Interpreter.
func (GenericInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	if len(schema.Subcommands) > 0 {
		return interpretSubcommand(leaf, schema, ctx)
	}
	st := scan(leaf, schema, ctx)
	st.finish()
	return st.result()
}

// pendingOp is one deferred operand: a positional (role assigned at resolve
// time) or a flag value (role known at scan time, flag naming the spelling
// that carried it).
type pendingOp struct {
	tok        string
	idx        int
	positional bool
	flag       string
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
	scanned    bool
	insuff     string
}

// scan is the flag pass: it walks the argv, records every modeled flag and
// defers every operand. scanned is false when a token stopped the scan (an
// unknown flag, a missing value), in which case the operands seen so far are
// still resolved but no stdio effects are added and the state is already
// insufficient.
func scan(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) *interpState {
	st := &interpState{schema: schema, leaf: leaf, ctx: ctx, flagsSeen: map[string]bool{}, scanned: true}
	args := leaf.Args
	optionsEnded := false
	for i := 0; i < len(args); i++ {
		tok := args[i]
		isFlag := !optionsEnded && strings.HasPrefix(tok, "-") && tok != "-" && tok != schema.Positionals.StdinToken
		if !isFlag {
			st.ops = append(st.ops, pendingOp{tok: tok, idx: i, positional: true})
			if schema.PositionalsEndOptions {
				optionsEnded = true
			}
			continue
		}
		if tok == "--" && schema.EndOfOptions {
			optionsEnded = true
			continue
		}
		consumed, ok := st.flag(tok, i)
		if !ok {
			st.scanned = false
			break
		}
		i += consumed
	}
	return st
}

// finish is the resolve pass: positional roles, operand effects, and — when
// the scan completed — the schema's implicit and stdio effects.
func (st *interpState) finish() {
	st.resolve()
	if st.scanned {
		st.implicit()
		st.stdio()
	}
}

func (st *interpState) fail(format string, a ...any) {
	if st.insuff == "" {
		st.insuff = fmt.Sprintf(format, a...)
	}
}

// positionals returns the deferred positional operands in argument order.
func (st *interpState) positionals() []pendingOp {
	var out []pendingOp
	for _, op := range st.ops {
		if op.positional {
			out = append(out, op)
		}
	}
	return out
}

// flagValues returns the values carried by any of the named flag spellings,
// in argument order.
func (st *interpState) flagValues(names ...string) []pendingOp {
	var out []pendingOp
	for _, op := range st.ops {
		if op.positional {
			continue
		}
		for _, n := range names {
			if op.flag == n {
				out = append(out, op)
				break
			}
		}
	}
	return out
}

// anyFlagSeen reports whether any of the named spellings appeared.
func (st *interpState) anyFlagSeen(names ...string) bool { return anySeen(names, st.flagsSeen) }

// baseName is the leaf's executable basename, used only to LABEL child
// invocations (`bash -c`); nothing branches on it.
func (st *interpState) baseName() string { return path.Base(st.leaf.Executable) }

// childSource labels a child invocation by the leaf's basename and the flag
// (or positional index) that carried it: `bash -c`, `sh arg 0`.
func (st *interpState) childSource(op pendingOp) string {
	if op.flag != "" {
		return st.baseName() + " " + op.flag
	}
	return fmt.Sprintf("%s arg %d", st.baseName(), op.idx)
}

// resolve assigns roles to the deferred positionals and emits every operand's
// effects in argument order.
func (st *interpState) resolve() {
	n := len(st.positionals())
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
		st.operand(op, role)
	}
}

// operand emits the effect for one operand under the given role.
func (st *interpState) operand(op pendingOp, role OperandRole) {
	source := fmt.Sprintf("arg %d", op.idx)
	live := st.leaf.ArgIsLiveExpansion(op.idx)
	switch {
	case role.IsPath():
		if op.positional && st.schema.Positionals.StdinToken != "" && op.tok == st.schema.Positionals.StdinToken && !live {
			st.stdinToken = true
			return
		}
		st.pathOps++
		st.effects = append(st.effects, Effect{
			Kind:           EffectPath,
			Path:           op.tok,
			Access:         role.pathAccess(),
			Dynamic:        live,
			Source:         source,
			FromPositional: op.positional,
		})
	case role.Kind == KindProgram:
		st.program(role.Dialect, op, source)
	case role.Kind == KindDataOrAtFile:
		st.dataOrAtFile(op, source, live)
	case role.Kind == KindRemote:
		st.effects = append(st.effects, Effect{Kind: EffectRemote, Resource: op.tok, Operation: role.Operation, Dynamic: live, Source: source})
	case role.Kind == KindEnvAssign:
		st.envAssign(op, source, live)
	case role.Kind == KindChdir:
		st.chdir(op.tok, live, source)
	case role.Kind == KindLiteral, role.Kind == KindMessage:
		// Inert: no effect. A live expansion in a literal slot is still inert —
		// its value cannot change what the command touches.
	default:
		st.fail("unmodeled operand role %d at %s", role.Kind, source)
	}
}

// chdir emits the two effects a KindChdir operand stands for: a metadata read
// of the target directory (judged like any read) and the EffectChdir the
// graph builder consumes. `-` is the shell's previous directory — a value
// only the runtime knows — so it is Dynamic with a Detail saying why; a live
// expansion is Dynamic the ordinary way.
func (st *interpState) chdir(target string, live bool, source string) {
	dynamic := live
	detail := ""
	if !live && target == "-" {
		dynamic, detail = true, "previous directory"
	}
	st.effects = append(
		st.effects,
		Effect{Kind: EffectPath, Path: target, Access: AccessRead, Dynamic: dynamic, Source: source, Detail: detail},
		Effect{Kind: EffectChdir, Path: target, Dynamic: dynamic, Source: source, Detail: detail},
	)
}

// dataOrAtFile applies the `@` convention: `@-` consumes stdin, `@path` reads
// path, anything else is inert data. A live expansion that does not visibly
// start with `@` MIGHT still expand to one, so it is recorded as a dynamic
// read (fail-closed) rather than as inert data. A `@path` read is not a path
// OPERAND for the stdin rule (pathOps is left alone): the convention rides on
// data flags, not on the positional layout.
func (st *interpState) dataOrAtFile(op pendingOp, source string, live bool) {
	switch {
	case !live && op.tok == "@-":
		st.stdinToken = true
	case strings.HasPrefix(op.tok, "@"):
		st.effects = append(st.effects, Effect{Kind: EffectPath, Path: op.tok[1:], Access: AccessRead, Dynamic: live, Source: source, FromPositional: op.positional})
	case live:
		st.effects = append(st.effects, Effect{Kind: EffectPath, Path: op.tok, Access: AccessRead, Dynamic: true, Source: source, FromPositional: op.positional, Detail: "may expand to @file"})
	}
}

// envAssign emits an EffectEnv SET for an operand under KindEnvAssign: the
// NAME left of the first `=` (or the whole token, for a bare NAME that only
// marks an existing variable exported). A live expansion anywhere in the
// token might change WHICH variable gets set — there is no per-substring
// liveness signal to isolate a static NAME prefix from a dynamic VALUE
// suffix — so it fails closed rather than guessing.
func (st *interpState) envAssign(op pendingOp, source string, live bool) {
	if live {
		st.fail("env assignment at %s is a runtime expansion", source)
		return
	}
	name := op.tok
	if eq := strings.IndexByte(op.tok, '='); eq >= 0 {
		name = op.tok[:eq]
	}
	st.effects = append(st.effects, Effect{Kind: EffectEnv, EnvName: name, EnvSet: true, Source: source})
}

// program emits the Program effect for an operand and folds in the dialect
// interpreter's classification. The Program effect always stays (the graph
// records that a program ran); sufficiency comes from the dialect: a live
// expansion or an unknown dialect is insufficient, and a known dialect's own
// insufficiency propagates. A child the dialect returns without a Source is
// labelled by the flag or operand that carried the program.
func (st *interpState) program(dialect string, op pendingOp, source string) {
	tok := op.tok
	st.effects = append(st.effects, Effect{Kind: EffectProgram, Program: tok, Dialect: dialect, Source: source})
	if st.leaf.ArgIsLiveExpansion(op.idx) {
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
	for _, c := range res.Children {
		if c.Source == "" {
			c.Source = st.childSource(op)
		}
		st.children = append(st.children, c)
	}
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
		if spec, name, val, ok := st.gluedFlag(tok); ok {
			return st.applyFlag(name, spec, i, val, true)
		}
		return st.unknownFlag(tok)
	}
	// -flag=value (slice 3x): the SAME glued convention as --flag=value, but
	// under a SINGLE leading dash — go's own CLI style (`go test -count=1`,
	// `go build -cpuprofile=prof.out`; `go help testflag`'s own worked
	// example uses exactly this spelling: "-cpuprofile=prof.out"), distinct
	// from getopt's single-dash SHORT-flag bundling just below (whose glued
	// values never contain "="). Checked only when the text before "=" is a
	// REGISTERED, value-taking flag NAME (gluedFlag requires an exact match
	// on the whole pre-"=" substring), so an ordinary short bundle can never
	// be misread as this form: no existing schema's bundled glued value (a
	// sed -i backup suffix, a pgrep -d delimiter, ...) is itself a
	// registered multi-character flag NAME followed by "=".
	if spec, name, val, ok := st.gluedFlag(tok); ok {
		return st.applyFlag(name, spec, i, val, true)
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

// gluedFlag splits tok at its first "=" and reports the flag spec, its name
// and the glued value when the text BEFORE "=" is an EXACT, registered flag
// name whose arity takes a value — the one check shared by both
// "--flag=value" (GNU long options) and "-flag=value" (go's own single-dash
// convention, slice 3x). ok is false when tok has no "=", or the pre-"="
// text is not a registered flag, or that flag takes no value (ArityNone) —
// every one of those falls through to the caller's existing handling
// unchanged.
func (st *interpState) gluedFlag(tok string) (FlagSpec, string, string, bool) {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return FlagSpec{}, "", "", false
	}
	name, val := tok[:eq], tok[eq+1:]
	spec, ok := st.schema.Flags[name]
	if !ok || spec.Arity == ArityNone {
		return FlagSpec{}, "", "", false
	}
	return spec, name, val, true
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
			st.ops = append(st.ops, pendingOp{tok: glued, idx: i, flag: name, role: spec.Operand})
			return 0, true
		}
		if i+1 >= len(st.leaf.Args) {
			st.fail("flag %s is missing its value", name)
			return 0, false
		}
		st.ops = append(st.ops, pendingOp{tok: st.leaf.Args[i+1], idx: i + 1, flag: name, role: spec.Operand})
		return 1, true
	case ArityOptionalGlued:
		if hasGlued && glued != "" {
			st.ops = append(st.ops, pendingOp{tok: glued, idx: i, flag: name, role: spec.Operand})
		}
		return 0, true
	case ArityN:
		if hasGlued {
			st.fail("flag %s takes %d separate values, not a glued one", name, len(spec.Operands))
			return 0, false
		}
		n := len(spec.Operands)
		if i+n >= len(st.leaf.Args) {
			st.fail("flag %s is missing its values (needs %d)", name, n)
			return 0, false
		}
		for k, role := range spec.Operands {
			st.ops = append(st.ops, pendingOp{tok: st.leaf.Args[i+1+k], idx: i + 1 + k, flag: name, role: role})
		}
		return n, true
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

// implicit appends the effects the schema declares WITHOUT an operand (see
// ImplicitEffect): each fires either always, or only when the invocation
// resolved zero positionals (WhenNoPositionals) or zero REST positionals
// (WhenNoRestPositionals). It runs before stdio() and, like every other
// effect collected here, is subject to the flag transforms applied in
// result() — a dry-run flag strips an implicit delete exactly like an
// explicit one.
func (st *interpState) implicit() {
	n := len(st.positionals())
	for _, ie := range st.schema.ImplicitEffects {
		if ie.WhenNoPositionals && n > 0 {
			continue
		}
		if ie.WhenNoRestPositionals && st.schema.Positionals.restCount(n, st.flagsSeen) > 0 {
			continue
		}
		if len(ie.WhenFlags) > 0 && !st.anyFlagSeen(ie.WhenFlags...) {
			continue
		}
		st.emitImplicit(ie)
	}
}

// emitImplicit builds the one effect an ImplicitEffect describes. An
// unmodeled role kind fails closed rather than silently doing nothing.
//
// An implicit path READ that stands in for the missing file operands
// (WhenNoRestPositionals — grep's recursive search of ".") counts as a
// path operand for the stdin rule: grep -r with no FILE searches the tree
// and does not read stdin, so pathOps is bumped exactly as an explicit
// operand would bump it. Every other implicit path effect (git status's
// always-on read of ".", git clean's delete of ".") leaves pathOps alone,
// as before — those schemas are StdinNever anyway.
func (st *interpState) emitImplicit(ie ImplicitEffect) {
	switch {
	case ie.Role.IsPath():
		st.effects = append(st.effects, Effect{
			Kind:    EffectPath,
			Path:    ie.Target,
			Access:  ie.Role.pathAccess(),
			Dynamic: ie.Dynamic,
			Source:  "implicit",
		})
		if ie.WhenNoRestPositionals && ie.Role.pathAccess() == AccessRead {
			st.pathOps++
		}
	case ie.Role.Kind == KindRemote:
		st.effects = append(st.effects, Effect{
			Kind:      EffectRemote,
			Resource:  ie.Target,
			Operation: ie.Role.Operation,
			Dynamic:   ie.Dynamic,
			Source:    "implicit",
		})
	case ie.Role.Kind == KindChdir:
		st.chdir(ie.Target, ie.Dynamic, "implicit")
	case ie.Role.Kind == KindExec:
		st.effects = append(st.effects, Effect{
			Kind:   EffectExec,
			Detail: "trusted checkout code",
			Source: ie.Target,
		})
	default:
		st.fail("unmodeled implicit effect role %d", ie.Role.Kind)
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
