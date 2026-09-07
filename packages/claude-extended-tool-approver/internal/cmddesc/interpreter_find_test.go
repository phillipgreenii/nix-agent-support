package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

// TestFindIsRegistryDispatched: find names the "find" interpreter (not the
// generic one), and the resolved interpreter does not depend on schema.Name
// — the same proof TestHeadIsRegistryOnly/TestRmIsRegistryOnly give for the
// generic interpreter, adapted for a command-level one (mirroring
// TestXargsInterpreter's own use of DefaultRegistry).
func TestFindIsRegistryDispatched(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("find")
	if !ok {
		t.Fatal("find not registered")
	}
	if schema.Interpreter != "find" {
		t.Fatalf("find names interpreter %q, want %q", schema.Interpreter, "find")
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter for find")
	}
	if _, isFind := in.(findInterpreter); !isFind {
		t.Fatalf("resolved %T, want findInterpreter", in)
	}
	if schema.Provenance == "" {
		t.Error("find schema has no Provenance")
	}

	l := leaf(t, "find . -name *.go")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-find"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
}

func TestFindInterpreter(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("find")
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatalf("no interpreter %q", schema.Interpreter)
	}

	cases := []struct {
		name       string
		command    string
		sufficient bool
		effects    []Effect
		children   []ChildInvocation
		reason     string
	}{
		{
			"bare find defaults to .", "find", true,
			[]Effect{{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "implicit"}},
			nil, "",
		},
		{
			"one starting point plus a literal test", "find . -name *.go", true,
			[]Effect{{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true}},
			nil, "",
		},
		{
			"two starting points", "find sub README.md -type f", true,
			[]Effect{
				{Kind: EffectPath, Path: "sub", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "README.md", Access: AccessRead, Source: "arg 1", FromPositional: true},
			},
			nil, "",
		},
		{
			"leading options are inert", "find -H -L -O2 -D tree . -print", true,
			[]Effect{{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 5", FromPositional: true}},
			nil, "",
		},
		{
			"parenthesised expression with -o", `find . \( -name *.go -o -name *.md \)`, true,
			[]Effect{{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true}},
			nil, "",
		},
		{
			"-delete deletes every starting point", "find . -delete", true,
			[]Effect{
				{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: ".", Access: AccessDelete, Source: "arg 0", FromPositional: true, Detail: "find -delete"},
			},
			nil, "",
		},
		{
			"-delete with no starting point deletes the implicit .", "find -delete", true,
			[]Effect{
				{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "implicit"},
				{Kind: EffectPath, Path: ".", Access: AccessDelete, Source: "implicit", Detail: "find -delete"},
			},
			nil, "",
		},
		{
			"-newer reads its FILE", "find . -newer go.mod -print", true,
			[]Effect{
				{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "go.mod", Access: AccessRead, Source: "arg 2"},
			},
			nil, "",
		},
		{
			"-newerXY reads its FILE", "find . -newermt go.mod -print", true,
			[]Effect{
				{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "go.mod", Access: AccessRead, Source: "arg 2"},
			},
			nil, "",
		},
		{
			"-samefile reads its FILE", "find . -samefile go.mod", true,
			[]Effect{
				{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "go.mod", Access: AccessRead, Source: "arg 2"},
			},
			nil, "",
		},
		{
			"-fprint truncates its FILE", "find . -fprint out.txt", true,
			[]Effect{
				{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "out.txt", Access: AccessTruncate, Source: "arg 2"},
			},
			nil, "",
		},
		{
			"-fprintf truncates its FILE, format is a trailing literal", "find . -fprintf out.txt %p", true,
			[]Effect{
				{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "out.txt", Access: AccessTruncate, Source: "arg 2"},
			},
			nil, "",
		},
		{
			"-exec becomes an argv child with {} dynamic", `find . -exec rm {} \;`, true,
			[]Effect{{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true}},
			[]ChildInvocation{{Dialect: "argv", Argv: []string{"rm", "{}"}, ArgvDynamic: []bool{false, true}, Source: "find -exec"}},
			"",
		},
		{
			"-execdir with + terminator", `find . -execdir cat {} +`, true,
			[]Effect{{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "arg 0", FromPositional: true}},
			[]ChildInvocation{{Dialect: "argv", Argv: []string{"cat", "{}"}, ArgvDynamic: []bool{false, true}, Source: "find -execdir"}},
			"",
		},
		{
			"-ok is interactive", `find . -ok rm {} ;`, false,
			nil, nil, "interactive",
		},
		{
			"-exec missing terminator", `find . -exec rm {}`, false,
			nil, nil, "no terminating",
		},
		{
			"unknown primary", "find . -frobnicate", false,
			nil, nil, "unmodeled find primary",
		},
		{
			"unknown literal-arg primary missing value", "find . -name", false,
			nil, nil, "missing its value",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := in.Interpret(leaf(t, tc.command), schema, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("sufficient = %v (%s), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if !tc.sufficient {
				if !strings.Contains(got.Insufficiency, tc.reason) {
					t.Errorf("insufficiency = %q, want substring %q", got.Insufficiency, tc.reason)
				}
				return
			}
			if !reflect.DeepEqual(got.Effects, tc.effects) {
				t.Errorf("effects = %+v\nwant     %+v", got.Effects, tc.effects)
			}
			if !reflect.DeepEqual(got.Children, tc.children) {
				t.Errorf("children = %+v\nwant     %+v", got.Children, tc.children)
			}
		})
	}
}
