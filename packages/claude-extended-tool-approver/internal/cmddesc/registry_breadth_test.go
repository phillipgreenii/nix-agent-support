package cmddesc

import (
	"reflect"
	"testing"
)

// TestBreadthSchemasAreRegistryOnly is the data-only proof for slice 3n:
// every schema it added is a plain value with a Provenance and no named
// interpreter, so the generic interpreter (or the generic subcommand
// dispatch) is what runs it — no command name reaches any code path.
func TestBreadthSchemasAreRegistryOnly(t *testing.T) {
	reg := DefaultRegistry()
	for _, name := range []string{"bd", "sleep", "which", "pgrep", "ps", "jq", "yq", "gofmt"} {
		s, ok := reg.Lookup(name)
		if !ok {
			t.Errorf("%s not registered", name)
			continue
		}
		if s.Interpreter != "" {
			t.Errorf("%s names interpreter %q; must be generic", name, s.Interpreter)
		}
		if s.Provenance == "" {
			t.Errorf("%s has no provenance", name)
		}
	}
	// Every bd verb, including the nested ones, is likewise data.
	var walk func(prefix string, s CommandSchema)
	walk = func(prefix string, s CommandSchema) {
		for k, sub := range s.Subcommands {
			if sub.Interpreter != "" {
				t.Errorf("%s %s names interpreter %q", prefix, k, sub.Interpreter)
			}
			if len(sub.Subcommands) == 0 && len(sub.ImplicitEffects) != 1 {
				t.Errorf("%s %s: want exactly one implicit remote effect, got %d", prefix, k, len(sub.ImplicitEffects))
			}
			walk(prefix+" "+k, sub)
		}
	}
	bd, _ := reg.Lookup("bd")
	walk("bd", bd)
}

// TestArityN: an ArityN flag consumes exactly len(Operands) separate tokens,
// each with its own role, and fails closed on a glued value or on running
// out of argv.
func TestArityN(t *testing.T) {
	schema := CommandSchema{
		Name: "x",
		Flags: map[string]FlagSpec{
			"--pair": {Arity: ArityN, Operands: []OperandRole{Literal, PathRead}},
		},
		Positionals:  PositionalSpec{Rest: PathRead},
		Stdin:        StdinNever,
		Stdout:       StdoutNone,
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
	}
	run := func(cmd string) Interpretation {
		return GenericInterpreter{}.Interpret(leaf(t, cmd), schema, Context{})
	}
	in := run("x --pair name f.txt g.txt")
	if !in.Sufficient {
		t.Fatalf("insufficient: %s", in.Insufficiency)
	}
	want := []Effect{
		{Kind: EffectPath, Path: "f.txt", Access: AccessRead, Source: "arg 2"},
		{Kind: EffectPath, Path: "g.txt", Access: AccessRead, Source: "arg 3", FromPositional: true},
	}
	if !reflect.DeepEqual(in.Effects, want) {
		t.Errorf("effects = %+v, want %+v", in.Effects, want)
	}
	if in := run("x --pair name"); in.Sufficient {
		t.Error("missing second value must be insufficient")
	}
	if in := run("x --pair=name f.txt"); in.Sufficient {
		t.Error("glued value must be insufficient")
	}
}

// TestDefaultSubcommand: with DefaultSubcommand set, a first positional
// that is not a key is handed to the default subcommand unconsumed; a real
// key still dispatches normally; without DefaultSubcommand the same token
// is an unmodeled subcommand.
func TestDefaultSubcommand(t *testing.T) {
	eval := CommandSchema{
		Name:         "eval",
		Flags:        map[string]FlagSpec{"-i": {Transform: EffectTransform{Kind: TransformInPlace}}},
		Positionals:  PositionalSpec{Leading: []OperandRole{Literal}, Rest: PathRead},
		Stdin:        StdinNever,
		Stdout:       StdoutContent,
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
	}
	parent := CommandSchema{
		Name:              "y",
		Flags:             eval.Flags,
		UnknownFlag:       UnknownFlagInsufficient,
		EndOfOptions:      true,
		DefaultSubcommand: "eval",
		Subcommands:       map[string]CommandSchema{"eval": eval, "e": renamed(eval, "e")},
	}
	run := func(cmd string, s CommandSchema) Interpretation {
		return interpretSubcommand(leaf(t, cmd), s, Context{})
	}
	read := Effect{Kind: EffectPath, Path: "f.yaml", Access: AccessRead, Source: "arg 1", FromPositional: true}
	if in := run("y .a f.yaml", parent); !in.Sufficient || !reflect.DeepEqual(in.Effects[:1], []Effect{read}) {
		t.Errorf("bare form: sufficient=%v effects=%+v (%s)", in.Sufficient, in.Effects, in.Insufficiency)
	}
	if in := run("y e .a f.yaml", parent); !in.Sufficient || in.Effects[0].Path != "f.yaml" || in.Effects[0].Access != AccessRead {
		t.Errorf("keyed form: sufficient=%v effects=%+v (%s)", in.Sufficient, in.Effects, in.Insufficiency)
	}
	// A parent-level -i transforms the default subcommand's effects too.
	if in := run("y -i .a=1 f.yaml", parent); !in.Sufficient || in.Effects[0].Access != AccessModify {
		t.Errorf("parent -i over default subcommand: sufficient=%v effects=%+v (%s)", in.Sufficient, in.Effects, in.Insufficiency)
	}
	noDefault := parent
	noDefault.DefaultSubcommand = ""
	if in := run("y .a f.yaml", noDefault); in.Sufficient {
		t.Error("without DefaultSubcommand, a non-key first positional must be an unmodeled subcommand")
	}
}
