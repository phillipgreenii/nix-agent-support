package cmddesc

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

func leaf(t *testing.T, command string) cmdparse.ParsedCommand {
	t.Helper()
	sp := cmdparse.ParseShell(command)
	if sp.Unparseable || len(sp.Leaves) != 1 {
		t.Fatalf("expected one leaf for %q: %+v", command, sp)
	}
	return sp.Leaves[0]
}

// TestHeadIsRegistryOnly: head is a plain schema VALUE with no interpreter
// name, so the generic interpreter is the one resolved for it, and that
// interpreter produces the same result whatever the schema is CALLED.
func TestHeadIsRegistryOnly(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("head")
	if !ok {
		t.Fatal("head not registered")
	}
	if schema.Interpreter != "" {
		t.Fatalf("head names interpreter %q; must be generic", schema.Interpreter)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter for the empty name")
	}
	if _, isGeneric := in.(GenericInterpreter); !isGeneric {
		t.Fatalf("resolved %T, want GenericInterpreter", in)
	}
	if schema.Provenance == "" || len(schema.Flags) == 0 || schema.Positionals.Rest != PathRead {
		t.Errorf("head schema is not a complete value: %+v", schema)
	}

	l := leaf(t, "head -n 5 README.md")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-head"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
	want := []Effect{
		{Kind: EffectPath, Path: "README.md", Access: AccessRead, Source: "arg 2", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdout},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
}

// TestRmIsRegistryOnly: the same proof for rm — a plain schema value, the
// generic interpreter, and a result that does not depend on schema.Name.
func TestRmIsRegistryOnly(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("rm")
	if !ok {
		t.Fatal("rm not registered")
	}
	if schema.Interpreter != "" {
		t.Fatalf("rm names interpreter %q; must be generic", schema.Interpreter)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter for the empty name")
	}
	if _, isGeneric := in.(GenericInterpreter); !isGeneric {
		t.Fatalf("resolved %T, want GenericInterpreter", in)
	}
	if schema.Provenance == "" || len(schema.Flags) == 0 || schema.Positionals.Rest != PathDelete {
		t.Errorf("rm schema is not a complete value: %+v", schema)
	}

	l := leaf(t, "rm -rf a -- -b")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-rm"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
	want := []Effect{
		{Kind: EffectPath, Path: "a", Access: AccessDelete, Source: "arg 1", FromPositional: true},
		{Kind: EffectPath, Path: "-b", Access: AccessDelete, Source: "arg 3", FromPositional: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
}

// TestMkdirIsRegistryOnly: the same proof as TestHeadIsRegistryOnly/
// TestRmIsRegistryOnly for slice 3e's new top-level command mkdir — a plain
// schema value, the generic interpreter, and a result that does not depend
// on schema.Name.
func TestMkdirIsRegistryOnly(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("mkdir")
	if !ok {
		t.Fatal("mkdir not registered")
	}
	if schema.Interpreter != "" {
		t.Fatalf("mkdir names interpreter %q; must be generic", schema.Interpreter)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter for the empty name")
	}
	if _, isGeneric := in.(GenericInterpreter); !isGeneric {
		t.Fatalf("resolved %T, want GenericInterpreter", in)
	}
	if schema.Provenance == "" || len(schema.Flags) == 0 || schema.Positionals.Rest != PathCreate {
		t.Errorf("mkdir schema is not a complete value: %+v", schema)
	}

	l := leaf(t, "mkdir -p a")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-mkdir"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
	want := []Effect{
		{Kind: EffectPath, Path: "a", Access: AccessCreate, Source: "arg 1", FromPositional: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
}

func TestGenericInterpreter(t *testing.T) {
	reg := DefaultRegistry()
	cat, _ := reg.Lookup("cat")
	head, _ := reg.Lookup("head")
	in := GenericInterpreter{}

	cases := []struct {
		name       string
		schema     CommandSchema
		command    string
		sufficient bool
		effects    []Effect
	}{
		{
			"stdin when no path operands", cat, "cat", true,
			[]Effect{{Kind: EffectStdio, Stream: StreamStdin, Source: "stdin"}, {Kind: EffectStdio, Stream: StreamStdout}},
		},
		{
			"stdin token is stdin not a path", cat, "cat -", true,
			[]Effect{{Kind: EffectStdio, Stream: StreamStdin, Source: "stdin"}, {Kind: EffectStdio, Stream: StreamStdout}},
		},
		{
			"bundled short flags", cat, "cat -nb a b", true,
			[]Effect{
				{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 1", FromPositional: true},
				{Kind: EffectPath, Path: "b", Access: AccessRead, Source: "arg 2", FromPositional: true},
				{Kind: EffectStdio, Stream: StreamStdout},
			},
		},
		{
			"end of options", cat, "cat -- --number", true,
			[]Effect{{Kind: EffectPath, Path: "--number", Access: AccessRead, Source: "arg 1", FromPositional: true}, {Kind: EffectStdio, Stream: StreamStdout}},
		},
		{"unknown long flag", cat, "cat --weird a", false, nil},
		{"unknown short in bundle", cat, "cat -nz a", false, nil},
		{
			"live expansion is dynamic", cat, `cat "$F"`, true,
			[]Effect{{Kind: EffectPath, Path: "$F", Access: AccessRead, Dynamic: true, Source: "arg 0", FromPositional: true}, {Kind: EffectStdio, Stream: StreamStdout}},
		},
		{
			"glued long value", head, "head --lines=3 a", true,
			[]Effect{{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 1", FromPositional: true}, {Kind: EffectStdio, Stream: StreamStdout}},
		},
		{
			"glued short value", head, "head -n5 a", true,
			[]Effect{{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 1", FromPositional: true}, {Kind: EffectStdio, Stream: StreamStdout}},
		},
		{
			"separate value is not a path", head, "head -n README.md", true,
			[]Effect{{Kind: EffectStdio, Stream: StreamStdin, Source: "stdin"}, {Kind: EffectStdio, Stream: StreamStdout}},
		},
		{"missing value", head, "head -n", false, nil},
		{"arity-0 flag given a value", cat, "cat --number=3 a", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := in.Interpret(leaf(t, tc.command), tc.schema, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("sufficient = %v (%s), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if tc.sufficient && !reflect.DeepEqual(got.Effects, tc.effects) {
				t.Errorf("effects = %+v\nwant     %+v", got.Effects, tc.effects)
			}
			if !tc.sufficient && got.Insufficiency == "" {
				t.Error("insufficient without a reason")
			}
		})
	}
}

// TestUnknownFlagInert: the schema-level policy, not a command, decides that
// unmodeled flags are inert.
func TestUnknownFlagInert(t *testing.T) {
	s := CommandSchema{Name: "x", Positionals: PositionalSpec{Rest: PathRead}, UnknownFlag: UnknownFlagInert}
	got := GenericInterpreter{}.Interpret(leaf(t, "x --anything a"), s, Context{})
	if !got.Sufficient || len(got.Effects) != 1 || got.Effects[0].Path != "a" {
		t.Errorf("got %+v", got)
	}
}

// TestUnknownTransformFailsClosed: a transform value the interpreter does not
// know makes the interpretation insufficient rather than passing effects on.
func TestUnknownTransformFailsClosed(t *testing.T) {
	s := CommandSchema{
		Name:        "x",
		Flags:       map[string]FlagSpec{"-z": {Transform: EffectTransform{Kind: TransformKind(99)}}},
		Positionals: PositionalSpec{Rest: PathRead},
	}
	got := GenericInterpreter{}.Interpret(leaf(t, "x -z a"), s, Context{})
	if got.Sufficient {
		t.Errorf("expected insufficient, got %+v", got)
	}
}

// TestExportNUnexportUnknownFlag: slice 3g — `-n` (unexport) is removed
// from exportSchema's Flags entirely, so it is an unknown flag: the whole
// interpretation is insufficient and FOO's positional is never routed
// through EnvAssign at all. This replaces the old (incorrect) modeling that
// treated `export -n FOO` as if `-n` were inert and FOO were a plain SET of
// FOO, which would have wrongly reached the EnvAssignment policy as
// EffectEnv{EnvSet: true} for a name that is actually being un-exported.
func TestExportNUnexportUnknownFlag(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("export")
	if !ok {
		t.Fatal("export not registered")
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter for export")
	}
	got := in.Interpret(leaf(t, "export -n FOO"), schema, Context{})
	if got.Sufficient {
		t.Errorf("export -n FOO: sufficient = true, want false (unknown flag)")
	}
	if got.Insufficiency == "" {
		t.Error("insufficient without a reason")
	}
	for _, e := range got.Effects {
		if e.Kind == EffectEnv {
			t.Errorf("export -n FOO: unexpected EffectEnv %+v — -n must not route FOO through EnvAssign", e)
		}
	}
}

func TestRegistryNames(t *testing.T) {
	want := []string{
		"[", "awk", "bash", "bd", "cat", "cd", "cp", "curl", "echo", "export", "false", "find", "gawk", "git", "go", "gofmt", "grep",
		"head", "jq", "kubectl", "ls", "mkdir", "pgrep", "printf", "ps", "rm", "scp", "sed", "sh", "sleep", "sort", "ssh", "tail", "tee",
		"test", "treefmt", "true", "wc", "which", "xargs", "yq",
	}
	if got := DefaultRegistry().Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	if _, ok := DefaultRegistry().Lookup("frobnicate"); ok {
		t.Error("frobnicate must not be registered")
	}
}

func TestEffectString(t *testing.T) {
	cases := map[Effect]string{
		{Kind: EffectPath, Path: "a", Access: AccessTruncate, Source: "redirect >"}:        "path:truncate a [redirect >]",
		{Kind: EffectPath, Path: "$F", Access: AccessRead, Dynamic: true, Source: "arg 0"}: "path:read $F (dynamic) [arg 0]",
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true}:                          "stdio:stdout metadata",
		{Kind: EffectEnv, EnvName: "X", EnvSet: true}:                                      "env:set X",
		{Kind: EffectOpaque, Detail: "no schema for z"}:                                    "opaque (no schema for z)",
		{Kind: EffectProgram, Program: "ls", Dialect: "bash"}:                              `program:bash "ls"`,
		{Kind: EffectNet, Host: "h", Direction: NetInbound}:                                "net:inbound h",
	}
	for e, want := range cases {
		if got := e.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", e, got, want)
		}
	}
}
