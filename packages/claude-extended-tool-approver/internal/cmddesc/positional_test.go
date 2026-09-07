package cmddesc

import (
	"reflect"
	"testing"
)

// TestResolveRoles: Leading/Rest/Trailing assignment, the skipped-by-flags
// switches, MinRest, and the too-few fail-closed path, all on a synthetic
// schema so no command name is involved.
func TestResolveRoles(t *testing.T) {
	prog := Program("d")
	spec := PositionalSpec{
		Leading:                []OperandRole{prog},
		LeadingSkippedByFlags:  []string{"-e"},
		Rest:                   PathRead,
		MinRest:                1,
		Trailing:               []OperandRole{PathTruncate},
		TrailingSkippedByFlags: []string{"-t"},
	}
	seen := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	cases := []struct {
		name  string
		n     int
		flags map[string]bool
		want  []OperandRole
		ok    bool
	}{
		{"leading, rest, trailing", 4, seen(), []OperandRole{prog, PathRead, PathRead, PathTruncate}, true},
		{"exactly leading+min+trailing", 3, seen(), []OperandRole{prog, PathRead, PathTruncate}, true},
		{"too few for leading+min+trailing", 2, seen(), nil, false},
		{"leading skipped by flag", 2, seen("-e"), []OperandRole{PathRead, PathTruncate}, true},
		{"trailing skipped by flag", 2, seen("-t"), []OperandRole{prog, PathRead}, true},
		{"both skipped", 1, seen("-e", "-t"), []OperandRole{PathRead}, true},
		{"both skipped but under MinRest", 0, seen("-e", "-t"), nil, false},
		{"unrelated flag does not skip", 3, seen("-n"), []OperandRole{prog, PathRead, PathTruncate}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason, ok := spec.resolveRoles(tc.n, tc.flags)
			if ok != tc.ok {
				t.Fatalf("ok = %v (%s), want %v", ok, reason, tc.ok)
			}
			if !ok && reason == "" {
				t.Error("failed without a reason")
			}
			if ok && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("roles = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRestCount: restCount counts only positionals left for the Rest role
// after the Leading/Trailing slots (honouring the same skip-by-flag rules
// resolveRoles applies), and never goes negative.
func TestRestCount(t *testing.T) {
	spec := PositionalSpec{
		Leading:                []OperandRole{Literal},
		LeadingSkippedByFlags:  []string{"-e"},
		Rest:                   PathRead,
		Trailing:               []OperandRole{PathTruncate},
		TrailingSkippedByFlags: []string{"-t"},
	}
	seen := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	cases := []struct {
		name  string
		n     int
		flags map[string]bool
		want  int
	}{
		{"zero positionals", 0, seen(), 0},
		{"only the leading slot filled", 1, seen(), 0},
		{"leading and trailing, no rest", 2, seen(), 0},
		{"one rest", 3, seen(), 1},
		{"leading skipped: one positional is rest", 1, seen("-e"), 0}, // it fills the trailing slot
		{"leading and trailing skipped: all rest", 2, seen("-e", "-t"), 2},
		{"never negative", 0, seen("-e"), 0},
	}
	for _, tc := range cases {
		if got := spec.restCount(tc.n, tc.flags); got != tc.want {
			t.Errorf("%s: restCount(%d) = %d, want %d", tc.name, tc.n, got, tc.want)
		}
	}
}

// TestImplicitWhenNoRestPositionals: a WhenNoRestPositionals implicit read
// fires when the only positional filled a Leading slot, is suppressed by a
// Rest positional, and (unlike WhenNoPositionals) counts as a path operand
// for the stdin rule — on a synthetic grep-shaped schema.
func TestImplicitWhenNoRestPositionals(t *testing.T) {
	schema := CommandSchema{
		Name: "g",
		Flags: map[string]FlagSpec{
			"-r": inert,
			"-e": literal1,
		},
		Positionals: PositionalSpec{
			Leading:               []OperandRole{Literal},
			LeadingSkippedByFlags: []string{"-e"},
			Rest:                  PathRead,
		},
		ImplicitEffects: []ImplicitEffect{
			{Role: PathRead, Target: ".", WhenNoRestPositionals: true, WhenFlags: []string{"-r"}},
		},
		Stdin:  StdinWhenNoPathOperands,
		Stdout: StdoutContent,
	}
	run := func(command string) Interpretation {
		return GenericInterpreter{}.Interpret(leaf(t, command), schema, Context{})
	}
	has := func(in Interpretation, want string) bool {
		for _, e := range in.Effects {
			if e.String() == want {
				return true
			}
		}
		return false
	}
	implicitRead := "path:read . [implicit]"
	stdinRead := "stdio:stdin content"

	if in := run("g -r TODO"); !has(in, implicitRead) || has(in, stdinRead) {
		t.Errorf("bare positional pattern with -r: effects %v — want the implicit read and no stdin read", in.Effects)
	}
	if in := run("g -r -e TODO"); !has(in, implicitRead) || has(in, stdinRead) {
		t.Errorf("-e pattern with -r: effects %v — want the implicit read and no stdin read", in.Effects)
	}
	if in := run("g -r TODO f.txt"); has(in, implicitRead) || has(in, stdinRead) {
		t.Errorf("rest positional given: effects %v — want neither the implicit read nor a stdin read", in.Effects)
	}
	if in := run("g TODO"); has(in, implicitRead) || !has(in, stdinRead) {
		t.Errorf("no -r: effects %v — want no implicit read and a stdin read", in.Effects)
	}
}

// TestResolveRolesRestOverride: RestOverride swaps Rest to a different role
// when one of its flags was seen, and leaves Leading/Trailing untouched —
// the mechanism gitBranchSchema/gitConfigSchema use (registry.go) to
// disambiguate a safe listing/filter pattern from a write.
func TestResolveRolesRestOverride(t *testing.T) {
	spec := PositionalSpec{
		Rest:         Unmodeled,
		RestOverride: RestOverride{Flags: []string{"-l", "--list"}, Role: Literal},
	}
	seen := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	cases := []struct {
		name  string
		n     int
		flags map[string]bool
		want  []OperandRole
	}{
		{"no override flag: Unmodeled", 1, seen(), []OperandRole{Unmodeled}},
		{"override flag (short spelling): Literal", 1, seen("-l"), []OperandRole{Literal}},
		{"override flag (long spelling): Literal", 2, seen("--list"), []OperandRole{Literal, Literal}},
		{"unrelated flag does not trigger override", 1, seen("-a"), []OperandRole{Unmodeled}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason, ok := spec.resolveRoles(tc.n, tc.flags)
			if !ok {
				t.Fatalf("resolveRoles failed: %s", reason)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("roles = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPositionalLayoutThroughInterpreter: the same layout end to end,
// including that Trailing is resolved from the END of argv and that a
// positional-derived effect carries FromPositional.
func TestPositionalLayoutThroughInterpreter(t *testing.T) {
	s := CommandSchema{
		Name: "x",
		Flags: map[string]FlagSpec{
			"-t": {Arity: ArityOne, Operand: PathModify},
			"-o": {Arity: ArityOptionalGlued, Operand: Literal},
			"-n": inert,
		},
		Positionals: PositionalSpec{
			Rest:                   PathRead,
			MinRest:                1,
			Trailing:               []OperandRole{PathTruncate},
			TrailingSkippedByFlags: []string{"-t"},
		},
		EndOfOptions: true,
	}
	in := GenericInterpreter{}
	cases := []struct {
		name       string
		command    string
		sufficient bool
		effects    []Effect
	}{
		{
			"last positional is trailing", "x a b c", true,
			[]Effect{
				{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "b", Access: AccessRead, Source: "arg 1", FromPositional: true},
				{Kind: EffectPath, Path: "c", Access: AccessTruncate, Source: "arg 2", FromPositional: true},
			},
		},
		{
			"flag after positionals still skips trailing", "x a -t d", true,
			[]Effect{
				{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 0", FromPositional: true},
				{Kind: EffectPath, Path: "d", Access: AccessModify, Source: "arg 2"},
			},
		},
		{"too few positionals", "x a", false, nil},
		{
			"optional-glued short with value", "x -o.bak a b", true,
			[]Effect{
				{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 1", FromPositional: true},
				{Kind: EffectPath, Path: "b", Access: AccessTruncate, Source: "arg 2", FromPositional: true},
			},
		},
		{
			"optional-glued short without value does not eat the next token", "x -o a b", true,
			[]Effect{
				{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 1", FromPositional: true},
				{Kind: EffectPath, Path: "b", Access: AccessTruncate, Source: "arg 2", FromPositional: true},
			},
		},
		{
			"optional-glued in a bundle takes the rest of the bundle", "x -noe a b", true,
			[]Effect{
				{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 1", FromPositional: true},
				{Kind: EffectPath, Path: "b", Access: AccessTruncate, Source: "arg 2", FromPositional: true},
			},
		},
		{
			"end of options then dash-named positionals", "x -- -a -b", true,
			[]Effect{
				{Kind: EffectPath, Path: "-a", Access: AccessRead, Source: "arg 1", FromPositional: true},
				{Kind: EffectPath, Path: "-b", Access: AccessTruncate, Source: "arg 2", FromPositional: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := in.Interpret(leaf(t, tc.command), s, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("sufficient = %v (%s), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if tc.sufficient && !reflect.DeepEqual(got.Effects, tc.effects) {
				t.Errorf("effects = %+v\nwant     %+v", got.Effects, tc.effects)
			}
		})
	}
}
