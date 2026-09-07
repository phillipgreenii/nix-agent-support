package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

// TestSubcommandDispatchIsRegistryOnly: proves subcommand dispatch is a
// schema SHAPE, not code. A throwaway parent command "tool" (a name that
// appears nowhere in production code) with one subcommand "look" modeled
// exactly like git's `status` (an always-on implicit PathRead of ".", a
// pathspec positional, metadata stdout) produces the SAME effects as git's
// real `status` subcommand, through the same GenericInterpreter — the
// mechanism owes nothing to the names "git" or "status".
func TestSubcommandDispatchIsRegistryOnly(t *testing.T) {
	look := CommandSchema{
		Positionals:     PositionalSpec{Rest: PathRead},
		ImplicitEffects: []ImplicitEffect{{Role: PathRead, Target: "."}},
		Stdout:          StdoutMetadata,
		EndOfOptions:    true,
	}
	toolSchema := CommandSchema{
		Name:        "tool",
		Subcommands: map[string]CommandSchema{"look": look},
	}
	in := GenericInterpreter{}
	want := []Effect{
		{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "implicit"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}

	got := in.Interpret(leaf(t, "tool look"), toolSchema, Context{})
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Fatalf("tool look: got %+v, want effects %+v", got, want)
	}

	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	got2 := in.Interpret(leaf(t, "git status"), gitSchema, Context{})
	if !got2.Sufficient || !reflect.DeepEqual(got2.Effects, want) {
		t.Errorf("git status: got %+v, want the SAME effects %+v as tool look", got2, want)
	}
}

// TestGitLogIsRegistryOnlySubcommand: the same proof, for slice 3e's new git
// subcommand `log` — nested under gitSchema.Subcommands, resolved through the
// SAME interpretSubcommand code path as status/clean/push, with no branch on
// the name "log" anywhere. Renaming the parent schema does not change the
// result, exactly like TestHeadIsRegistryOnly/TestRmIsRegistryOnly prove for
// a flat (non-subcommand) schema.
func TestGitLogIsRegistryOnlySubcommand(t *testing.T) {
	reg := DefaultRegistry()
	gitSchema, ok := reg.Lookup("git")
	if !ok {
		t.Fatal("git not registered")
	}
	logSchema, ok := gitSchema.Subcommands["log"]
	if !ok {
		t.Fatal("git log not registered as a subcommand")
	}
	if logSchema.Interpreter != "" {
		t.Fatalf("git log names interpreter %q; must be generic", logSchema.Interpreter)
	}

	in := GenericInterpreter{}
	l := leaf(t, "git log")
	got := in.Interpret(l, gitSchema, Context{})
	renamed := gitSchema
	renamed.Name = "not-git"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
	want := []Effect{
		{Kind: EffectPath, Path: ".git", Access: AccessRead, Source: "implicit"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
}

// TestSubcommandDispatch: known/unknown/missing/dynamic subcommand tokens,
// a global flag preceding the subcommand, and one level of nesting.
func TestSubcommandDispatch(t *testing.T) {
	sub := CommandSchema{Positionals: PositionalSpec{Rest: PathRead}, EndOfOptions: true}
	schema := CommandSchema{
		Name:        "tool",
		Flags:       map[string]FlagSpec{"-g": inert},
		Subcommands: map[string]CommandSchema{"go": sub},
	}
	in := GenericInterpreter{}

	t.Run("known subcommand", func(t *testing.T) {
		got := in.Interpret(leaf(t, "tool go a"), schema, Context{})
		want := []Effect{{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 0", FromPositional: true}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		got := in.Interpret(leaf(t, "tool frobnicate a"), schema, Context{})
		if got.Sufficient || !strings.Contains(got.Insufficiency, "unmodeled subcommand frobnicate") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("missing subcommand", func(t *testing.T) {
		got := in.Interpret(leaf(t, "tool"), schema, Context{})
		if got.Sufficient || !strings.Contains(got.Insufficiency, "no subcommand given") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("dynamic subcommand", func(t *testing.T) {
		got := in.Interpret(leaf(t, `tool "$SUB"`), schema, Context{})
		if got.Sufficient || !strings.Contains(got.Insufficiency, "runtime expansion") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("unknown global flag before subcommand", func(t *testing.T) {
		got := in.Interpret(leaf(t, "tool --weird go a"), schema, Context{})
		if got.Sufficient || !strings.Contains(got.Insufficiency, "unknown flag --weird") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("inert global flag before subcommand does not block dispatch", func(t *testing.T) {
		got := in.Interpret(leaf(t, "tool -g go a"), schema, Context{})
		// The subcommand's own argv is reindexed from 0 (it is a fresh synthetic
		// leaf), so "a" is arg 0 of the SUBCOMMAND, not arg 2 of the parent.
		want := []Effect{{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 0", FromPositional: true}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

// TestSubcommandDispatchNested: a Subcommands value that itself has
// Subcommands recurses through the SAME code path, one level deep.
func TestSubcommandDispatchNested(t *testing.T) {
	inner := CommandSchema{Positionals: PositionalSpec{Rest: PathRead}, EndOfOptions: true}
	outer := CommandSchema{Subcommands: map[string]CommandSchema{"inner": inner}}
	top := CommandSchema{Name: "tool", Subcommands: map[string]CommandSchema{"outer": outer}}

	got := GenericInterpreter{}.Interpret(leaf(t, "tool outer inner a"), top, Context{})
	want := []Effect{{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 0", FromPositional: true}}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// A dynamic token at the INNER level is caught by the same dispatch code
	// running one level down, not just at the top.
	dyn := GenericInterpreter{}.Interpret(leaf(t, `tool outer "$X"`), top, Context{})
	if dyn.Sufficient || !strings.Contains(dyn.Insufficiency, "runtime expansion") {
		t.Errorf("dynamic inner subcommand should fail closed too: %+v", dyn)
	}

	// A static but UNMODELED token at the INNER level is likewise caught one
	// level down.
	unmodeled := GenericInterpreter{}.Interpret(leaf(t, "tool outer frobnicate"), top, Context{})
	if unmodeled.Sufficient || !strings.Contains(unmodeled.Insufficiency, "unmodeled subcommand frobnicate") {
		t.Errorf("unmodeled inner subcommand should fail closed too: %+v", unmodeled)
	}
}

// TestImplicitEffects: an ImplicitEffect fires ALWAYS (WhenNoPositionals
// false) regardless of what positionals were also given, or ONLY when the
// invocation resolved zero positionals; either way it is subject to the same
// transforms as an operand effect.
func TestImplicitEffects(t *testing.T) {
	always := CommandSchema{
		Name:            "x",
		Positionals:     PositionalSpec{Rest: PathRead},
		ImplicitEffects: []ImplicitEffect{{Role: PathRead, Target: "."}},
		EndOfOptions:    true,
	}
	withArg := GenericInterpreter{}.Interpret(leaf(t, "x a"), always, Context{})
	want := []Effect{
		{Kind: EffectPath, Path: "a", Access: AccessRead, Source: "arg 0", FromPositional: true},
		{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "implicit"},
	}
	if !withArg.Sufficient || !reflect.DeepEqual(withArg.Effects, want) {
		t.Errorf("always, with a positional: got %+v, want %+v", withArg, want)
	}
	bare := GenericInterpreter{}.Interpret(leaf(t, "x"), always, Context{})
	wantBare := []Effect{{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "implicit"}}
	if !bare.Sufficient || !reflect.DeepEqual(bare.Effects, wantBare) {
		t.Errorf("always, with no positional: got %+v, want %+v", bare, wantBare)
	}

	whenNone := CommandSchema{
		Name:            "y",
		Positionals:     PositionalSpec{Rest: PathDelete},
		ImplicitEffects: []ImplicitEffect{{Role: PathDelete, Target: ".", WhenNoPositionals: true}},
		EndOfOptions:    true,
	}
	withPos := GenericInterpreter{}.Interpret(leaf(t, "y a"), whenNone, Context{})
	wantWithPos := []Effect{{Kind: EffectPath, Path: "a", Access: AccessDelete, Source: "arg 0", FromPositional: true}}
	if !withPos.Sufficient || !reflect.DeepEqual(withPos.Effects, wantWithPos) {
		t.Errorf("when-no-positionals, WITH a positional: got %+v, want %+v (implicit must not also fire)", withPos, wantWithPos)
	}
	noPos := GenericInterpreter{}.Interpret(leaf(t, "y"), whenNone, Context{})
	wantNoPos := []Effect{{Kind: EffectPath, Path: ".", Access: AccessDelete, Source: "implicit"}}
	if !noPos.Sufficient || !reflect.DeepEqual(noPos.Effects, wantNoPos) {
		t.Errorf("when-no-positionals, with NO positional: got %+v, want %+v", noPos, wantNoPos)
	}

	dry := CommandSchema{
		Name:            "z",
		Flags:           map[string]FlagSpec{"-n": {Transform: EffectTransform{Kind: TransformDryRun}}},
		Positionals:     PositionalSpec{Rest: PathDelete},
		ImplicitEffects: []ImplicitEffect{{Role: PathDelete, Target: ".", WhenNoPositionals: true}},
		EndOfOptions:    true,
	}
	stripped := GenericInterpreter{}.Interpret(leaf(t, "z -n"), dry, Context{})
	if !stripped.Sufficient || len(stripped.Effects) != 0 {
		t.Errorf("dry-run must strip the implicit delete: got %+v", stripped)
	}

	dynRemote := CommandSchema{
		Name:            "r",
		Positionals:     PositionalSpec{Leading: []OperandRole{Remote("push")}, LeadingOptional: true, Rest: Literal},
		ImplicitEffects: []ImplicitEffect{{Role: Remote("push"), Target: "<default>", Dynamic: true, WhenNoPositionals: true}},
		EndOfOptions:    true,
	}
	remoteGot := GenericInterpreter{}.Interpret(leaf(t, "r"), dynRemote, Context{})
	wantRemote := []Effect{{Kind: EffectRemote, Resource: "<default>", Operation: "push", Dynamic: true, Source: "implicit"}}
	if !remoteGot.Sufficient || !reflect.DeepEqual(remoteGot.Effects, wantRemote) {
		t.Errorf("dynamic remote implicit effect: got %+v, want %+v", remoteGot, wantRemote)
	}
}

// TestImplicitEffectUnmodeledRoleFailsClosed: an ImplicitEffect naming a role
// kind emitImplicit does not know how to render fails the interpretation
// closed instead of silently doing nothing.
func TestImplicitEffectUnmodeledRoleFailsClosed(t *testing.T) {
	s := CommandSchema{
		Name:            "x",
		ImplicitEffects: []ImplicitEffect{{Role: Program("sed")}},
		EndOfOptions:    true,
	}
	got := GenericInterpreter{}.Interpret(leaf(t, "x"), s, Context{})
	if got.Sufficient {
		t.Errorf("expected insufficient, got %+v", got)
	}
}
